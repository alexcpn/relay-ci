package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// pipelinePinRecord is one row in the pipeline-digests.json file.
// Content is stored alongside the digest so a real unified diff can be
// produced when a digest mismatch is detected.
type pipelinePinRecord struct {
	ProjectID    string    `json:"project_id"`     // root commit SHA
	RepoName     string    `json:"repo_name"`      // human label
	Digest       string    `json:"digest"`         // sha256:<hex>
	PipelinePath string    `json:"pipeline_path"`  // pipeline.yml or pipeline.yaml
	Content      string    `json:"content"`        // last pinned pipeline.yaml content
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
}

// digestStore persists pinned pipeline digests in a JSON file. Concurrent
// access is serialised with a mutex; the file is rewritten on every change.
type digestStore struct {
	mu      sync.Mutex
	path    string
	records map[string]pipelinePinRecord // project_id -> record
}

// newDigestStore loads or creates the digest store at the given path.
func newDigestStore(path string) (*digestStore, error) {
	ds := &digestStore{
		path:    path,
		records: make(map[string]pipelinePinRecord),
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating digest store dir: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ds, nil
		}
		return nil, fmt.Errorf("reading digest store: %w", err)
	}
	if len(data) == 0 {
		return ds, nil
	}
	var recs []pipelinePinRecord
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, fmt.Errorf("parsing digest store: %w", err)
	}
	for _, r := range recs {
		ds.records[r.ProjectID] = r
	}
	return ds, nil
}

// Get returns the pinned record for a project, if any.
func (ds *digestStore) Get(projectID string) (pipelinePinRecord, bool) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	r, ok := ds.records[projectID]
	return r, ok
}

// Pin saves a new digest for a project. If a record already exists, only the
// last_seen timestamp is updated (use Repin to overwrite the digest).
func (ds *digestStore) Pin(projectID, repoName, digest, pipelinePath, content string) error {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	now := time.Now().UTC()
	if existing, ok := ds.records[projectID]; ok && existing.Digest == digest {
		existing.LastSeen = now
		existing.RepoName = repoName
		ds.records[projectID] = existing
	} else {
		ds.records[projectID] = pipelinePinRecord{
			ProjectID:    projectID,
			RepoName:     repoName,
			Digest:       digest,
			PipelinePath: pipelinePath,
			Content:      content,
			FirstSeen:    now,
			LastSeen:     now,
		}
	}
	return ds.saveLocked()
}

// Repin overwrites the pinned digest for a project (used when caller passes
// --accept-pipeline-change).
func (ds *digestStore) Repin(projectID, repoName, digest, pipelinePath, content string) error {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	now := time.Now().UTC()
	rec := ds.records[projectID]
	rec.ProjectID = projectID
	rec.RepoName = repoName
	rec.Digest = digest
	rec.PipelinePath = pipelinePath
	rec.Content = content
	rec.LastSeen = now
	if rec.FirstSeen.IsZero() {
		rec.FirstSeen = now
	}
	ds.records[projectID] = rec
	return ds.saveLocked()
}

// Unpin removes a project's record. Returns true if a record was removed.
func (ds *digestStore) Unpin(projectID string) (bool, error) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	if _, ok := ds.records[projectID]; !ok {
		return false, nil
	}
	delete(ds.records, projectID)
	return true, ds.saveLocked()
}

// List returns a copy of all pinned records.
func (ds *digestStore) List() []pipelinePinRecord {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	out := make([]pipelinePinRecord, 0, len(ds.records))
	for _, r := range ds.records {
		out = append(out, r)
	}
	return out
}

func (ds *digestStore) saveLocked() error {
	recs := make([]pipelinePinRecord, 0, len(ds.records))
	for _, r := range ds.records {
		recs = append(recs, r)
	}
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling digest store: %w", err)
	}
	tmp := ds.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing digest store: %w", err)
	}
	if err := os.Rename(tmp, ds.path); err != nil {
		return fmt.Errorf("renaming digest store: %w", err)
	}
	return nil
}
