package dag

import (
	"fmt"
	"time"
)

// TaskState represents the lifecycle state of a task.
type TaskState int

const (
	TaskPending   TaskState = iota // waiting for dependencies
	TaskReady                      // all deps met, can be scheduled
	TaskScheduled                  // assigned to a worker, not yet running
	TaskRunning                    // executing on a worker
	TaskPassed                     // completed successfully
	TaskFailed                     // completed with error
	TaskSkipped                    // skipped (upstream failed, or conditional)
	TaskCancelled                  // cancelled by user
	TaskTimedOut                   // exceeded timeout
)

func (s TaskState) String() string {
	switch s {
	case TaskPending:
		return "pending"
	case TaskReady:
		return "ready"
	case TaskScheduled:
		return "scheduled"
	case TaskRunning:
		return "running"
	case TaskPassed:
		return "passed"
	case TaskFailed:
		return "failed"
	case TaskSkipped:
		return "skipped"
	case TaskCancelled:
		return "cancelled"
	case TaskTimedOut:
		return "timed_out"
	default:
		return fmt.Sprintf("unknown(%d)", s)
	}
}

// IsTerminal returns true if the task is in a final state.
func (s TaskState) IsTerminal() bool {
	switch s {
	case TaskPassed, TaskFailed, TaskSkipped, TaskCancelled, TaskTimedOut:
		return true
	default:
		return false
	}
}

// IsSuccess returns true if the task completed successfully.
func (s TaskState) IsSuccess() bool {
	return s == TaskPassed
}

// validTransitions defines which state transitions are allowed.
var validTransitions = map[TaskState][]TaskState{
	TaskPending:   {TaskReady, TaskSkipped, TaskCancelled},
	TaskReady:     {TaskScheduled, TaskCancelled},
	TaskScheduled: {TaskRunning, TaskCancelled, TaskTimedOut},
	TaskRunning:   {TaskPassed, TaskFailed, TaskCancelled, TaskTimedOut},
}

// Task represents a single unit of work in the pipeline.
type Task struct {
	ID             string
	Name           string
	State          TaskState
	Condition      string // "on_success" (default), "on_failure", "always"
	ContainerImage string
	Commands       []string
	Env            map[string]string
	CPUMillicores  uint32
	MemoryMB       uint32
	DiskMB         uint32
	TimeoutSeconds uint32
	CacheMounts    []CacheMount
	SecretRefs     []string
	ExitCode       int
	ErrorMessage   string
	StartedAt      time.Time
	FinishedAt     time.Time
}

// ShouldRunAfterFailure returns true if this task should run even when
// an upstream dependency has failed (condition is "always" or "on_failure").
func (t *Task) ShouldRunAfterFailure() bool {
	return t.Condition == "always" || t.Condition == "on_failure"
}

// CacheMount defines a cache volume to mount into the task container.
type CacheMount struct {
	CacheKey  string
	MountPath string
	ReadOnly  bool
}

// CanTransitionTo checks if moving to the target state is valid.
func (t *Task) CanTransitionTo(target TaskState) bool {
	allowed, ok := validTransitions[t.State]
	if !ok {
		return false
	}
	for _, s := range allowed {
		if s == target {
			return true
		}
	}
	return false
}

// TransitionTo moves the task to a new state if the transition is valid.
func (t *Task) TransitionTo(target TaskState) error {
	if !t.CanTransitionTo(target) {
		return fmt.Errorf("invalid transition: %s -> %s for task %q", t.State, target, t.Name)
	}
	t.State = target
	now := time.Now()
	if target == TaskRunning {
		t.StartedAt = now
	}
	if target.IsTerminal() {
		t.FinishedAt = now
	}
	return nil
}
