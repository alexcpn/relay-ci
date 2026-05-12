package store

import (
	"sync"
	"time"

	"github.com/ci-system/ci/pkg/review"
)

// MemStore is a no-op in-memory Store used in tests and when no data
// directory is configured.
type MemStore struct {
	mu       sync.RWMutex
	builds   map[string]*BuildRecord
	audit    []*AuditEntry
	reviews  map[string]*review.ReviewRecord
	sessions map[string]*review.SessionRecord
}

func NewMemStore() *MemStore {
	return &MemStore{builds: make(map[string]*BuildRecord)}
}

func (m *MemStore) SaveBuild(b *BuildRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *b
	m.builds[b.ID] = &cp
	return nil
}

func (m *MemStore) UpdateBuildState(id, state string, finishedAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.builds[id]; ok {
		b.State = state
		b.FinishedAt = finishedAt
	}
	return nil
}

func (m *MemStore) UpdateTaskState(t *TaskRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.builds[t.BuildID]
	if !ok {
		return nil
	}
	for i, existing := range b.Tasks {
		if existing.ID == t.ID {
			b.Tasks[i] = *t
			return nil
		}
	}
	b.Tasks = append(b.Tasks, *t)
	return nil
}

func (m *MemStore) ListBuilds(limit int) ([]*BuildRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*BuildRecord, 0, len(m.builds))
	for _, b := range m.builds {
		cp := *b
		out = append(out, &cp)
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemStore) GetBuild(id string) (*BuildRecord, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.builds[id]
	if !ok {
		return nil, false, nil
	}
	cp := *b
	return &cp, true, nil
}

func (m *MemStore) AppendAudit(e *AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *e
	cp.TS = time.Now()
	m.audit = append(m.audit, &cp)
	return nil
}

func (m *MemStore) ListAudit(limit int) ([]*AuditEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := len(m.audit)
	if limit > 0 && n > limit {
		n = limit
	}
	out := make([]*AuditEntry, n)
	for i := range out {
		cp := *m.audit[len(m.audit)-n+i]
		out[i] = &cp
	}
	return out, nil
}

func (m *MemStore) DeleteBuildsBefore(cutoff time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for id, b := range m.builds {
		if b.CreatedAt.Before(cutoff) {
			delete(m.builds, id)
			n++
		}
	}
	return n, nil
}

func (m *MemStore) Close() error { return nil }

// --- Review methods ---

func (m *MemStore) SaveReview(r *review.ReviewRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reviews == nil {
		m.reviews = make(map[string]*review.ReviewRecord)
	}
	cp := *r
	m.reviews[r.ID] = &cp
	return nil
}

func (m *MemStore) UpdateReviewState(id, state, verdict, summary string, findings []review.Finding, finishedAt time.Time, durationMs int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reviews == nil {
		return nil
	}
	r, ok := m.reviews[id]
	if !ok {
		return nil
	}
	r.State = state
	r.Verdict = verdict
	r.Summary = summary
	r.FinishedAt = finishedAt
	r.DurationMs = durationMs
	r.Findings = append([]review.Finding{}, findings...)
	return nil
}

func (m *MemStore) GetReview(id string) (*review.ReviewRecord, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.reviews == nil {
		return nil, false, nil
	}
	r, ok := m.reviews[id]
	if !ok {
		return nil, false, nil
	}
	cp := *r
	return &cp, true, nil
}

func (m *MemStore) ListReviews(sessionID string, limit int) ([]*review.ReviewRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.reviews == nil {
		return nil, nil
	}
	var out []*review.ReviewRecord
	for _, r := range m.reviews {
		if sessionID != "" && r.SessionID != sessionID {
			continue
		}
		cp := *r
		out = append(out, &cp)
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemStore) GetFindings(reviewID string) ([]review.Finding, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.reviews == nil {
		return nil, nil
	}
	r, ok := m.reviews[reviewID]
	if !ok {
		return nil, nil
	}
	return append([]review.Finding{}, r.Findings...), nil
}

func (m *MemStore) SaveSession(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions == nil {
		m.sessions = make(map[string]*review.SessionRecord)
	}
	if _, ok := m.sessions[id]; !ok {
		m.sessions[id] = &review.SessionRecord{ID: id, CreatedAt: time.Now()}
	}
	return nil
}

func (m *MemStore) UpdateSession(id, lastReviewID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions == nil {
		return nil
	}
	if s, ok := m.sessions[id]; ok {
		s.LastReviewID = lastReviewID
	}
	return nil
}

func (m *MemStore) GetSession(id string) (*review.SessionRecord, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.sessions == nil {
		return nil, false, nil
	}
	s, ok := m.sessions[id]
	if !ok {
		return nil, false, nil
	}
	cp := *s
	return &cp, true, nil
}
