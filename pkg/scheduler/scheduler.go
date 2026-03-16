package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ci-system/ci/pkg/dag"
	"github.com/ci-system/ci/pkg/worker"
)

// TaskAssignment represents a task assigned to a specific worker.
type TaskAssignment struct {
	Task     *dag.Task
	WorkerID string
	BuildID  string
}

// TaskResultReport is what the worker sends back when a task finishes.
type TaskResultReport struct {
	BuildID  string
	TaskID   string
	State    dag.TaskState
	ExitCode int
	Error    string
}

// Build represents a running pipeline build.
type Build struct {
	ID          string
	Graph       *dag.Graph
	RepoURL     string
	CommitSHA   string
	Branch      string
	PRNumber    string
	TriggeredBy string
	CreatedAt   time.Time
	StartedAt   time.Time
	FinishedAt  time.Time
}

// Scheduler manages builds and assigns tasks to workers.
type Scheduler struct {
	mu       sync.Mutex
	builds   map[string]*Build
	registry *worker.Registry
	assignFn func(TaskAssignment) error // callback when task is assigned
	logger   *slog.Logger

	// taskWorker tracks which worker is running which task (taskID -> workerID).
	taskWorker map[string]string
	// taskBuild tracks which build a task belongs to (taskID -> buildID).
	taskBuild map[string]string
}

// New creates a scheduler with the given worker registry.
// assignFn is called when a task needs to be sent to a worker.
func New(registry *worker.Registry, assignFn func(TaskAssignment) error, logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{
		builds:     make(map[string]*Build),
		registry:   registry,
		assignFn:   assignFn,
		logger:     logger,
		taskWorker: make(map[string]string),
		taskBuild:  make(map[string]string),
	}
}

// SubmitBuild adds a new build to the scheduler. The graph must already
// be validated. Returns error if the build ID already exists.
func (s *Scheduler) SubmitBuild(build *Build) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.builds[build.ID]; exists {
		return fmt.Errorf("build already exists: %q", build.ID)
	}

	build.CreatedAt = time.Now()
	s.builds[build.ID] = build

	// Index task -> build mapping.
	for _, task := range build.Graph.Tasks() {
		s.taskBuild[task.ID] = build.ID
	}

	s.logger.Info("build submitted", "build_id", build.ID, "tasks", build.Graph.Size())
	return nil
}

// Schedule runs one scheduling cycle: finds ready tasks across all builds
// and assigns them to available workers. Returns the number of tasks assigned.
func (s *Scheduler) Schedule(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	assigned := 0

	for buildID, build := range s.builds {
		ready := build.Graph.MarkReady()
		if len(ready) == 0 {
			continue
		}

		if build.StartedAt.IsZero() {
			build.StartedAt = time.Now()
		}

		for _, task := range ready {
			if ctx.Err() != nil {
				return assigned, ctx.Err()
			}

			workerID, err := s.pickWorker(task)
			if err != nil {
				s.logger.Debug("no worker available",
					"task", task.Name,
					"build_id", buildID,
					"reason", err,
				)
				continue // try again next cycle
			}

			// Reserve capacity on the worker.
			err = s.registry.ReserveCapacity(
				workerID,
				task.CPUMillicores,
				task.MemoryMB,
				task.DiskMB,
			)
			if err != nil {
				continue
			}

			// Transition task state.
			if err := task.TransitionTo(dag.TaskScheduled); err != nil {
				s.logger.Error("state transition failed", "task", task.Name, "err", err)
				s.registry.ReleaseCapacity(workerID, task.CPUMillicores, task.MemoryMB, task.DiskMB)
				continue
			}

			s.taskWorker[task.ID] = workerID

			// Notify via callback.
			assignment := TaskAssignment{
				Task:     task,
				WorkerID: workerID,
				BuildID:  buildID,
			}
			if err := s.assignFn(assignment); err != nil {
				s.logger.Error("assign callback failed", "task", task.Name, "err", err)
				s.registry.ReleaseCapacity(workerID, task.CPUMillicores, task.MemoryMB, task.DiskMB)
				continue
			}

			if err := task.TransitionTo(dag.TaskRunning); err != nil {
				s.logger.Error("state transition to running failed", "task", task.Name, "err", err)
			}

			s.logger.Info("task assigned",
				"task", task.Name,
				"worker", workerID,
				"build_id", buildID,
			)
			assigned++
		}
	}

	return assigned, nil
}

// HandleTaskResult processes a completed task result from a worker.
// It updates the DAG, releases worker capacity, and returns any newly
// ready tasks that were scheduled as a result.
func (s *Scheduler) HandleTaskResult(result TaskResultReport) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	build, ok := s.builds[result.BuildID]
	if !ok {
		return 0, fmt.Errorf("build not found: %q", result.BuildID)
	}

	task, ok := build.Graph.GetTask(result.TaskID)
	if !ok {
		return 0, fmt.Errorf("task not found: %q in build %q", result.TaskID, result.BuildID)
	}

	// Release worker capacity.
	if workerID, ok := s.taskWorker[result.TaskID]; ok {
		s.registry.ReleaseCapacity(workerID, task.CPUMillicores, task.MemoryMB, task.DiskMB)
		delete(s.taskWorker, result.TaskID)
	}

	// Update the DAG.
	newlyReady, err := build.Graph.Complete(result.TaskID, result.State, result.ExitCode, result.Error)
	if err != nil {
		return 0, fmt.Errorf("completing task %q: %w", result.TaskID, err)
	}

	s.logger.Info("task completed",
		"task", task.Name,
		"state", result.State,
		"build_id", result.BuildID,
		"newly_ready", len(newlyReady),
	)

	// Check if build is done.
	if build.Graph.IsComplete() {
		build.FinishedAt = time.Now()
		state := "passed"
		if !build.Graph.IsPassed() {
			state = "failed"
		}
		s.logger.Info("build complete",
			"build_id", build.ID,
			"state", state,
			"duration", build.FinishedAt.Sub(build.StartedAt),
		)
	}

	return len(newlyReady), nil
}

// CancelBuild cancels all non-terminal tasks in a build.
func (s *Scheduler) CancelBuild(buildID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	build, ok := s.builds[buildID]
	if !ok {
		return fmt.Errorf("build not found: %q", buildID)
	}

	// Release capacity for all running tasks.
	for _, task := range build.Graph.Tasks() {
		if workerID, ok := s.taskWorker[task.ID]; ok {
			s.registry.ReleaseCapacity(workerID, task.CPUMillicores, task.MemoryMB, task.DiskMB)
			delete(s.taskWorker, task.ID)
		}
	}

	build.Graph.Cancel()
	build.FinishedAt = time.Now()
	s.logger.Info("build cancelled", "build_id", buildID)
	return nil
}

// GetBuild returns a build by ID.
func (s *Scheduler) GetBuild(id string) (*Build, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.builds[id]
	return b, ok
}

// ListBuilds returns all builds.
func (s *Scheduler) ListBuilds() []*Build {
	s.mu.Lock()
	defer s.mu.Unlock()

	builds := make([]*Build, 0, len(s.builds))
	for _, b := range s.builds {
		builds = append(builds, b)
	}
	return builds
}

// HandleDeadWorker handles a worker that has been detected as dead.
// It fails all tasks assigned to that worker.
func (s *Scheduler) HandleDeadWorker(workerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for taskID, wID := range s.taskWorker {
		if wID != workerID {
			continue
		}
		buildID := s.taskBuild[taskID]
		build, ok := s.builds[buildID]
		if !ok {
			continue
		}
		build.Graph.Complete(taskID, dag.TaskFailed, -1, "worker died: "+workerID)
		delete(s.taskWorker, taskID)

		s.logger.Warn("task failed due to dead worker",
			"task_id", taskID,
			"worker_id", workerID,
			"build_id", buildID,
		)
	}
}

// pickWorker finds the best worker for a task using bin-packing
// (least available resources that still fit = best fit).
func (s *Scheduler) pickWorker(task *dag.Task) (string, error) {
	active := s.registry.Active()
	if len(active) == 0 {
		return "", fmt.Errorf("no active workers")
	}

	cpu := task.CPUMillicores
	mem := task.MemoryMB
	disk := task.DiskMB

	var bestWorker *worker.Info
	var bestScore uint64 = ^uint64(0) // max uint64

	for _, w := range active {
		if !w.CanAcceptTask(cpu, mem, disk) {
			continue
		}
		// Score: sum of remaining resources after allocation (lower = tighter fit).
		score := uint64(w.AvailableCPU-cpu) +
			uint64(w.AvailableMemoryMB-mem) +
			uint64(w.AvailableDiskMB-disk)
		if score < bestScore {
			bestScore = score
			bestWorker = w
		}
	}

	if bestWorker == nil {
		return "", fmt.Errorf("no worker with sufficient resources (need cpu=%d mem=%d disk=%d)", cpu, mem, disk)
	}

	return bestWorker.ID, nil
}
