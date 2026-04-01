package scheduler

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/ci-system/ci/pkg/dag"
	"github.com/ci-system/ci/pkg/worker"
)

func newTestRegistry() *worker.Registry {
	r := worker.NewRegistry(30 * time.Second)
	r.Register(&worker.Info{
		ID:                "w1",
		TotalCPU:          4000,
		AvailableCPU:      4000,
		TotalMemoryMB:     8192,
		AvailableMemoryMB: 8192,
		TotalDiskMB:       50000,
		AvailableDiskMB:   50000,
		MaxTasks:          8,
	})
	r.Register(&worker.Info{
		ID:                "w2",
		TotalCPU:          4000,
		AvailableCPU:      4000,
		TotalMemoryMB:     8192,
		AvailableMemoryMB: 8192,
		TotalDiskMB:       50000,
		AvailableDiskMB:   50000,
		MaxTasks:          8,
	})
	return r
}

func newTestGraph(t *testing.T) *dag.Graph {
	t.Helper()
	g := dag.New()

	tasks := []*dag.Task{
		{ID: "compile_a", Name: "compile_a", CPUMillicores: 500, MemoryMB: 512, DiskMB: 1000},
		{ID: "compile_b", Name: "compile_b", CPUMillicores: 500, MemoryMB: 512, DiskMB: 1000},
		{ID: "test", Name: "unit_tests", CPUMillicores: 1000, MemoryMB: 1024, DiskMB: 2000},
		{ID: "lint", Name: "lint", CPUMillicores: 500, MemoryMB: 256, DiskMB: 500},
		{ID: "integration", Name: "integration_test", CPUMillicores: 2000, MemoryMB: 2048, DiskMB: 5000},
		{ID: "deploy", Name: "deploy", CPUMillicores: 500, MemoryMB: 512, DiskMB: 1000},
	}
	for _, task := range tasks {
		if err := g.AddTask(task); err != nil {
			t.Fatal(err)
		}
	}

	g.AddEdge("compile_a", "integration")
	g.AddEdge("compile_b", "integration")
	g.AddEdge("test", "integration")
	g.AddEdge("integration", "deploy")

	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestScheduleAssignsReadyTasks(t *testing.T) {
	reg := newTestRegistry()
	var assignments []TaskAssignment
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	s := New(reg, func(a TaskAssignment) error {
		assignments = append(assignments, a)
		return nil
	}, logger)

	build := &Build{
		ID:    "build-1",
		Graph: newTestGraph(t),
	}
	if err := s.SubmitBuild(build); err != nil {
		t.Fatal(err)
	}

	n, err := s.Schedule(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// compile_a, compile_b, test, lint should all be scheduled (4 tasks).
	if n != 4 {
		t.Fatalf("expected 4 tasks assigned, got %d", n)
	}
	if len(assignments) != 4 {
		t.Fatalf("expected 4 assignments, got %d", len(assignments))
	}
}

func TestScheduleAndComplete(t *testing.T) {
	reg := newTestRegistry()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	s := New(reg, func(a TaskAssignment) error { return nil }, logger)

	build := &Build{
		ID:    "build-1",
		Graph: newTestGraph(t),
	}
	s.SubmitBuild(build)

	// Schedule initial tasks.
	s.Schedule(context.Background())

	// Complete compile_a, compile_b, test — integration should not unlock
	// until all three are done.
	for _, id := range []string{"compile_a", "compile_b"} {
		completion, err := s.HandleTaskResult(TaskResultReport{
			BuildID: "build-1", TaskID: id, State: dag.TaskPassed,
		})
		if err != nil {
			t.Fatal(err)
		}
		// Build should not be complete yet.
		if completion.BuildID != "" {
			t.Errorf("expected build not complete after %s, but got completion", id)
		}
	}

	// Complete test — integration should now be ready to schedule.
	completion, err := s.HandleTaskResult(TaskResultReport{
		BuildID: "build-1", TaskID: "test", State: dag.TaskPassed,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Build is not complete yet (integration + deploy still pending).
	if completion.BuildID != "" {
		t.Fatalf("expected build not complete after test, but got completion")
	}
	// Verify integration is now ready (will be scheduled in next cycle).
	integration, _ := build.Graph.GetTask("integration")
	if integration.State != dag.TaskReady && integration.State != dag.TaskPending {
		t.Fatalf("expected integration to be ready, got %s", integration.State)
	}

	// Schedule again — should assign integration.
	n, _ := s.Schedule(context.Background())
	if n != 1 {
		t.Fatalf("expected 1 task assigned (integration), got %d", n)
	}
}

func TestFullBuildLifecycle(t *testing.T) {
	reg := newTestRegistry()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	s := New(reg, func(a TaskAssignment) error { return nil }, logger)

	build := &Build{
		ID:    "build-1",
		Graph: newTestGraph(t),
	}
	s.SubmitBuild(build)

	// Run scheduling loop until build is complete.
	for i := 0; i < 20; i++ {
		n, err := s.Schedule(context.Background())
		if err != nil {
			t.Fatal(err)
		}

		if n == 0 && build.Graph.IsComplete() {
			break
		}

		// Complete all running tasks.
		for _, task := range build.Graph.Tasks() {
			if task.State == dag.TaskRunning {
				s.HandleTaskResult(TaskResultReport{
					BuildID: "build-1", TaskID: task.ID, State: dag.TaskPassed,
				})
			}
		}
	}

	if !build.Graph.IsComplete() {
		t.Fatal("build should be complete")
	}
	if !build.Graph.IsPassed() {
		t.Fatal("build should have passed")
	}
	if build.FinishedAt.IsZero() {
		t.Error("build finished_at should be set")
	}
}

func TestTaskFailureCascade(t *testing.T) {
	reg := newTestRegistry()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	s := New(reg, func(a TaskAssignment) error { return nil }, logger)

	build := &Build{
		ID:    "build-1",
		Graph: newTestGraph(t),
	}
	s.SubmitBuild(build)
	s.Schedule(context.Background())

	// Fail compile_a — integration and deploy should be skipped.
	_, err := s.HandleTaskResult(TaskResultReport{
		BuildID: "build-1", TaskID: "compile_a", State: dag.TaskFailed, ExitCode: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	integration, _ := build.Graph.GetTask("integration")
	deploy, _ := build.Graph.GetTask("deploy")
	if integration.State != dag.TaskSkipped {
		t.Errorf("integration should be skipped, got %s", integration.State)
	}
	if deploy.State != dag.TaskSkipped {
		t.Errorf("deploy should be skipped, got %s", deploy.State)
	}

	// lint should still be running (independent).
	lint, _ := build.Graph.GetTask("lint")
	if lint.State != dag.TaskRunning {
		t.Errorf("lint should be running, got %s", lint.State)
	}
}

func TestCancelBuild(t *testing.T) {
	reg := newTestRegistry()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	s := New(reg, func(a TaskAssignment) error { return nil }, logger)

	build := &Build{
		ID:    "build-1",
		Graph: newTestGraph(t),
	}
	s.SubmitBuild(build)
	s.Schedule(context.Background())

	err := s.CancelBuild("build-1")
	if err != nil {
		t.Fatal(err)
	}

	if !build.Graph.IsComplete() {
		t.Error("all tasks should be terminal after cancel")
	}
	if build.FinishedAt.IsZero() {
		t.Error("finished_at should be set")
	}
}

func TestBinPackingSelectsTightestFit(t *testing.T) {
	reg := worker.NewRegistry(30 * time.Second)
	// w1 has lots of room.
	reg.Register(&worker.Info{
		ID: "w-big", TotalCPU: 8000, AvailableCPU: 8000,
		TotalMemoryMB: 16384, AvailableMemoryMB: 16384,
		TotalDiskMB: 100000, AvailableDiskMB: 100000, MaxTasks: 8,
	})
	// w2 has just enough room (tighter fit).
	reg.Register(&worker.Info{
		ID: "w-small", TotalCPU: 2000, AvailableCPU: 2000,
		TotalMemoryMB: 2048, AvailableMemoryMB: 2048,
		TotalDiskMB: 10000, AvailableDiskMB: 10000, MaxTasks: 4,
	})

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	var assigned []TaskAssignment
	s := New(reg, func(a TaskAssignment) error {
		assigned = append(assigned, a)
		return nil
	}, logger)

	g := dag.New()
	g.AddTask(&dag.Task{ID: "t1", Name: "small-task", CPUMillicores: 1000, MemoryMB: 1024, DiskMB: 5000})
	g.Validate()

	s.SubmitBuild(&Build{ID: "b1", Graph: g})
	s.Schedule(context.Background())

	if len(assigned) != 1 {
		t.Fatalf("expected 1 assignment, got %d", len(assigned))
	}
	if assigned[0].WorkerID != "w-small" {
		t.Errorf("expected bin-packing to pick w-small (tighter fit), got %s", assigned[0].WorkerID)
	}
}

func TestDeadWorkerFailsTasks(t *testing.T) {
	reg := newTestRegistry()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	s := New(reg, func(a TaskAssignment) error { return nil }, logger)

	g := dag.New()
	g.AddTask(&dag.Task{ID: "t1", Name: "task-1", CPUMillicores: 500, MemoryMB: 256, DiskMB: 500})
	g.Validate()

	s.SubmitBuild(&Build{ID: "b1", Graph: g})
	s.Schedule(context.Background())

	// Find which worker got the task.
	task, _ := g.GetTask("t1")
	if task.State != dag.TaskRunning {
		t.Fatalf("expected running, got %s", task.State)
	}

	// Simulate worker death.
	s.HandleDeadWorker("w1")
	s.HandleDeadWorker("w2") // one of these has the task

	task, _ = g.GetTask("t1")
	if task.State != dag.TaskFailed {
		t.Errorf("expected task to fail on dead worker, got %s", task.State)
	}
}

func TestNoWorkersAvailable(t *testing.T) {
	reg := worker.NewRegistry(30 * time.Second) // empty registry
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	s := New(reg, func(a TaskAssignment) error { return nil }, logger)

	g := dag.New()
	g.AddTask(&dag.Task{ID: "t1", Name: "task-1", CPUMillicores: 500, MemoryMB: 256, DiskMB: 500})
	g.Validate()

	s.SubmitBuild(&Build{ID: "b1", Graph: g})
	n, err := s.Schedule(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Should not fail, just skip (try again next cycle).
	if n != 0 {
		t.Errorf("expected 0 tasks assigned with no workers, got %d", n)
	}
}
