package logstore

import (
	"testing"
	"time"
)

func TestFileBackend_AppendAndLoad(t *testing.T) {
	dir := t.TempDir()
	fb, err := NewFileBackend(dir)
	if err != nil {
		t.Fatalf("NewFileBackend: %v", err)
	}

	now := time.Now()
	lines := []Line{
		{TaskID: "task-1", LineNumber: 0, Timestamp: now, Content: "hello", Stream: StreamStdout},
		{TaskID: "task-1", LineNumber: 1, Timestamp: now, Content: "world", Stream: StreamStderr},
	}

	if err := fb.Append("task-1", lines); err != nil {
		t.Fatalf("Append: %v", err)
	}

	loaded, err := fb.Load("task-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(loaded))
	}
	if loaded[0].Content != "hello" || loaded[1].Content != "world" {
		t.Errorf("unexpected content: %v", loaded)
	}
	if loaded[0].Stream != StreamStdout || loaded[1].Stream != StreamStderr {
		t.Errorf("unexpected streams: %v, %v", loaded[0].Stream, loaded[1].Stream)
	}
}

func TestFileBackend_Load_Nonexistent(t *testing.T) {
	dir := t.TempDir()
	fb, err := NewFileBackend(dir)
	if err != nil {
		t.Fatalf("NewFileBackend: %v", err)
	}

	lines, err := fb.Load("no-such-task")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if lines != nil {
		t.Fatalf("expected nil, got %v", lines)
	}
}

func TestFileBackend_AppendMultipleBatches(t *testing.T) {
	dir := t.TempDir()
	fb, err := NewFileBackend(dir)
	if err != nil {
		t.Fatalf("NewFileBackend: %v", err)
	}

	now := time.Now()
	batch1 := []Line{{TaskID: "task-2", LineNumber: 0, Timestamp: now, Content: "line-0"}}
	batch2 := []Line{{TaskID: "task-2", LineNumber: 1, Timestamp: now, Content: "line-1"}}

	fb.Append("task-2", batch1)
	fb.Append("task-2", batch2)

	loaded, _ := fb.Load("task-2")
	if len(loaded) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(loaded))
	}
}

func TestFileBackend_ListTasks(t *testing.T) {
	dir := t.TempDir()
	fb, err := NewFileBackend(dir)
	if err != nil {
		t.Fatalf("NewFileBackend: %v", err)
	}

	now := time.Now()
	fb.Append("task-a", []Line{{TaskID: "task-a", Timestamp: now, Content: "a"}})
	fb.Append("task-b", []Line{{TaskID: "task-b", Timestamp: now, Content: "b"}})

	tasks, err := fb.ListTasks()
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
}

func TestNewWithFile_Persistence(t *testing.T) {
	dir := t.TempDir()

	// Create store, write some logs, then discard the store.
	store1, err := NewWithFile(dir)
	if err != nil {
		t.Fatalf("NewWithFile: %v", err)
	}

	store1.Append("task-x", []Line{
		{Content: "line 0", Stream: StreamStdout},
		{Content: "line 1", Stream: StreamStdout},
	})
	store1.Complete("task-x")

	// Create a new store from the same directory — should reload.
	store2, err := NewWithFile(dir)
	if err != nil {
		t.Fatalf("NewWithFile reload: %v", err)
	}

	lines, total := store2.Get("task-x", 0, 0)
	if total != 2 {
		t.Fatalf("expected 2 lines, got %d", total)
	}
	if lines[0].Content != "line 0" || lines[1].Content != "line 1" {
		t.Errorf("unexpected content after reload: %v", lines)
	}
}

func TestNewWithFile_InMemoryFallback(t *testing.T) {
	// NewWithFile should be fully compatible with in-memory operations.
	dir := t.TempDir()
	store, err := NewWithFile(dir)
	if err != nil {
		t.Fatalf("NewWithFile: %v", err)
	}

	store.Append("t1", []Line{{Content: "hello"}})
	lines, total := store.Get("t1", 0, 0)
	if total != 1 || lines[0].Content != "hello" {
		t.Errorf("expected hello, got %v", lines)
	}
}
