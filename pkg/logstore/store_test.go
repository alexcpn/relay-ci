package logstore

import (
	"testing"
	"time"
)

func TestAppendAndGet(t *testing.T) {
	s := New()

	s.Append("task-1", []Line{
		{Content: "line 1", Stream: StreamStdout},
		{Content: "line 2", Stream: StreamStdout},
		{Content: "error!", Stream: StreamStderr},
	})

	lines, total := s.Get("task-1", 0, 0)
	if total != 3 {
		t.Fatalf("expected 3 total, got %d", total)
	}
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if lines[0].Content != "line 1" {
		t.Errorf("expected 'line 1', got %q", lines[0].Content)
	}
	if lines[0].LineNumber != 0 {
		t.Errorf("expected line number 0, got %d", lines[0].LineNumber)
	}
	if lines[2].Stream != StreamStderr {
		t.Errorf("expected stderr, got %d", lines[2].Stream)
	}
}

func TestGetWithPagination(t *testing.T) {
	s := New()
	for i := 0; i < 10; i++ {
		s.Append("task-1", []Line{{Content: "line"}})
	}

	lines, total := s.Get("task-1", 3, 4)
	if total != 10 {
		t.Fatalf("expected 10 total, got %d", total)
	}
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines, got %d", len(lines))
	}
	if lines[0].LineNumber != 3 {
		t.Errorf("expected line number 3, got %d", lines[0].LineNumber)
	}
}

func TestGetBeyondEnd(t *testing.T) {
	s := New()
	s.Append("task-1", []Line{{Content: "line"}})

	lines, total := s.Get("task-1", 100, 0)
	if total != 1 {
		t.Fatalf("expected 1 total, got %d", total)
	}
	if len(lines) != 0 {
		t.Fatalf("expected 0 lines, got %d", len(lines))
	}
}

func TestSubscribeRealtime(t *testing.T) {
	s := New()

	sub, err := s.Subscribe("task-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	// Append lines after subscribing.
	go func() {
		time.Sleep(10 * time.Millisecond)
		s.Append("task-1", []Line{{Content: "live line 1"}})
		s.Append("task-1", []Line{{Content: "live line 2"}})
		s.Complete("task-1")
	}()

	var received []Line
	for line := range sub.C {
		received = append(received, line)
	}

	if len(received) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(received))
	}
	if received[0].Content != "live line 1" {
		t.Errorf("expected 'live line 1', got %q", received[0].Content)
	}
}

func TestSubscribeWithBackfill(t *testing.T) {
	s := New()

	// Add some lines before subscribing.
	s.Append("task-1", []Line{
		{Content: "old line 1"},
		{Content: "old line 2"},
	})

	sub, err := s.Subscribe("task-1", 0)
	if err != nil {
		t.Fatal(err)
	}

	// Should receive backfilled lines immediately.
	line := <-sub.C
	if line.Content != "old line 1" {
		t.Errorf("expected 'old line 1', got %q", line.Content)
	}
	line = <-sub.C
	if line.Content != "old line 2" {
		t.Errorf("expected 'old line 2', got %q", line.Content)
	}

	sub.Close()
}

func TestSubscribeWithSinceLine(t *testing.T) {
	s := New()
	s.Append("task-1", []Line{
		{Content: "line 0"},
		{Content: "line 1"},
		{Content: "line 2"},
	})

	sub, err := s.Subscribe("task-1", 2)
	if err != nil {
		t.Fatal(err)
	}

	// Should only get line 2 as backfill.
	line := <-sub.C
	if line.Content != "line 2" {
		t.Errorf("expected 'line 2', got %q", line.Content)
	}

	sub.Close()
}

func TestSubscribeCompletedTask(t *testing.T) {
	s := New()
	s.Append("task-1", []Line{{Content: "done"}})
	s.Complete("task-1")

	_, err := s.Subscribe("task-1", 0)
	if err == nil {
		t.Fatal("expected error subscribing to completed task")
	}
}

func TestComplete(t *testing.T) {
	s := New()
	s.Append("task-1", []Line{{Content: "line"}})

	if s.IsComplete("task-1") {
		t.Error("should not be complete yet")
	}

	s.Complete("task-1")

	if !s.IsComplete("task-1") {
		t.Error("should be complete")
	}
}

func TestMultipleTasks(t *testing.T) {
	s := New()
	s.Append("task-1", []Line{{Content: "a"}})
	s.Append("task-2", []Line{{Content: "b"}, {Content: "c"}})

	if s.TaskCount() != 2 {
		t.Errorf("expected 2 tasks, got %d", s.TaskCount())
	}
	if s.LineCount("task-1") != 1 {
		t.Errorf("expected 1 line for task-1, got %d", s.LineCount("task-1"))
	}
	if s.LineCount("task-2") != 2 {
		t.Errorf("expected 2 lines for task-2, got %d", s.LineCount("task-2"))
	}
}
