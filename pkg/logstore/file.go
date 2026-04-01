package logstore

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FileBackend persists log lines to disk alongside the in-memory store.
// Each task gets a JSON-lines file at {dir}/{taskID}.jsonl.
type FileBackend struct {
	dir string
}

// NewFileBackend creates a file-backed log store in the given directory.
// The directory is created if it does not exist.
func NewFileBackend(dir string) (*FileBackend, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("logstore: create dir %s: %w", dir, err)
	}
	return &FileBackend{dir: dir}, nil
}

// persistedLine is the on-disk JSON format for a log line.
type persistedLine struct {
	TaskID     string `json:"task_id"`
	LineNumber int64  `json:"line"`
	Timestamp  int64  `json:"ts"`     // unix nanos
	Content    string `json:"content"`
	Stream     int    `json:"stream"` // 0=stdout, 1=stderr, 2=system
}

// Append writes lines to disk for a task (append-only).
func (f *FileBackend) Append(taskID string, lines []Line) error {
	path := f.taskPath(taskID)

	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("logstore: open %s: %w", path, err)
	}
	defer file.Close()

	enc := json.NewEncoder(file)
	for _, l := range lines {
		pl := persistedLine{
			TaskID:     l.TaskID,
			LineNumber: l.LineNumber,
			Timestamp:  l.Timestamp.UnixNano(),
			Content:    l.Content,
			Stream:     int(l.Stream),
		}
		if err := enc.Encode(pl); err != nil {
			return fmt.Errorf("logstore: encode line: %w", err)
		}
	}
	return nil
}

// Load reads all persisted log lines for a task from disk.
func (f *FileBackend) Load(taskID string) ([]Line, error) {
	path := f.taskPath(taskID)

	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("logstore: open %s: %w", path, err)
	}
	defer file.Close()

	var lines []Line
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	for scanner.Scan() {
		var pl persistedLine
		if err := json.Unmarshal(scanner.Bytes(), &pl); err != nil {
			continue // skip malformed lines
		}
		lines = append(lines, Line{
			TaskID:     pl.TaskID,
			LineNumber: pl.LineNumber,
			Timestamp:  time.Unix(0, pl.Timestamp),
			Content:    pl.Content,
			Stream:     Stream(pl.Stream),
		})
	}

	sort.Slice(lines, func(i, j int) bool {
		return lines[i].LineNumber < lines[j].LineNumber
	})

	return lines, scanner.Err()
}

// ListTasks returns all task IDs that have persisted logs.
func (f *FileBackend) ListTasks() ([]string, error) {
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		return nil, fmt.Errorf("logstore: readdir %s: %w", f.dir, err)
	}

	var ids []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(e.Name(), ".jsonl"))
	}
	return ids, nil
}

func (f *FileBackend) taskPath(taskID string) string {
	safe := strings.ReplaceAll(taskID, "/", "_")
	safe = strings.ReplaceAll(safe, "..", "_")
	return filepath.Join(f.dir, safe+".jsonl")
}
