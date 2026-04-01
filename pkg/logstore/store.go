package logstore

import (
	"fmt"
	"sync"
	"time"
)

// Stream identifies stdout vs stderr vs system messages.
type Stream int

const (
	StreamStdout Stream = iota
	StreamStderr
	StreamSystem
)

// Line is a single log line from a task.
type Line struct {
	TaskID     string
	LineNumber int64
	Timestamp  time.Time
	Content    string
	Stream     Stream
}

// Subscriber receives log lines in real time.
type Subscriber struct {
	C      chan Line
	closed bool
	mu     sync.Mutex
}

func newSubscriber(bufSize int) *Subscriber {
	return &Subscriber{C: make(chan Line, bufSize)}
}

func (s *Subscriber) send(line Line) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.C <- line:
	default:
		// Drop line if subscriber is too slow (backpressure).
	}
}

// Close stops the subscriber from receiving further lines.
func (s *Subscriber) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.C)
	}
}

// Store holds log lines for all tasks in memory, with support for
// real-time streaming via subscribers and optional file persistence.
type Store struct {
	mu          sync.RWMutex
	logs        map[string][]Line           // taskID -> lines
	subscribers map[string][]*Subscriber    // taskID -> active subscribers
	completed   map[string]bool             // taskID -> true if task is done
	file        *FileBackend                // optional disk persistence
}

// New creates an empty in-memory log store.
func New() *Store {
	return &Store{
		logs:        make(map[string][]Line),
		subscribers: make(map[string][]*Subscriber),
		completed:   make(map[string]bool),
	}
}

// NewWithFile creates a log store backed by files in the given directory.
// Logs are written to both memory and disk. Existing logs are loaded on startup.
func NewWithFile(dir string) (*Store, error) {
	fb, err := NewFileBackend(dir)
	if err != nil {
		return nil, err
	}

	s := &Store{
		logs:        make(map[string][]Line),
		subscribers: make(map[string][]*Subscriber),
		completed:   make(map[string]bool),
		file:        fb,
	}

	// Load existing logs from disk.
	tasks, err := fb.ListTasks()
	if err != nil {
		return nil, fmt.Errorf("logstore: list tasks: %w", err)
	}
	for _, taskID := range tasks {
		lines, err := fb.Load(taskID)
		if err != nil {
			return nil, fmt.Errorf("logstore: load task %s: %w", taskID, err)
		}
		s.logs[taskID] = lines
	}

	return s, nil
}

// Append adds log lines for a task. Notifies all subscribers.
func (s *Store) Append(taskID string, lines []Line) {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing := s.logs[taskID]
	baseNum := int64(len(existing))

	for i := range lines {
		lines[i].TaskID = taskID
		lines[i].LineNumber = baseNum + int64(i)
		if lines[i].Timestamp.IsZero() {
			lines[i].Timestamp = time.Now()
		}
	}

	s.logs[taskID] = append(existing, lines...)

	// Write-through to disk if file backend is configured.
	if s.file != nil {
		// Best-effort — don't fail the in-memory append on disk errors.
		_ = s.file.Append(taskID, lines)
	}

	// Notify subscribers.
	for _, sub := range s.subscribers[taskID] {
		for _, line := range lines {
			sub.send(line)
		}
	}
}

// Complete marks a task as done. Closes all subscribers for that task.
func (s *Store) Complete(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.completed[taskID] = true
	for _, sub := range s.subscribers[taskID] {
		sub.Close()
	}
	delete(s.subscribers, taskID)
}

// Get returns stored log lines for a task with pagination.
func (s *Store) Get(taskID string, offset, limit int64) ([]Line, int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	lines := s.logs[taskID]
	total := int64(len(lines))

	if offset >= total {
		return nil, total
	}

	end := total
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}

	result := make([]Line, end-offset)
	copy(result, lines[offset:end])
	return result, total
}

// Subscribe creates a real-time log subscriber for a task.
// If the task is already complete, returns nil.
// The subscriber receives all lines from sinceLine onwards.
func (s *Store) Subscribe(taskID string, sinceLine int64) (*Subscriber, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.completed[taskID] {
		return nil, fmt.Errorf("task %q already completed, use Get instead", taskID)
	}

	sub := newSubscriber(256)

	// Send buffered lines first.
	lines := s.logs[taskID]
	for i := sinceLine; i < int64(len(lines)); i++ {
		sub.send(lines[i])
	}

	s.subscribers[taskID] = append(s.subscribers[taskID], sub)
	return sub, nil
}

// IsComplete returns true if the task's logs are finalized.
func (s *Store) IsComplete(taskID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.completed[taskID]
}

// TaskCount returns the number of tasks with logs.
func (s *Store) TaskCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.logs)
}

// LineCount returns the number of log lines for a task.
func (s *Store) LineCount(taskID string) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return int64(len(s.logs[taskID]))
}
