package dag

import (
	"strings"
	"testing"
)

// helper to build the example DAG from the plan:
//
//	compile_a ──┐
//	compile_b ──┼──► integration_test ──► docker_build ──► push
//	compile_c ──┘         │
//	unit_tests ──────────►│
//	lint ─────────────────► (independent)
//	security_scan ────────►
func buildPipelineGraph(t *testing.T) *Graph {
	t.Helper()
	g := New()

	tasks := []*Task{
		{ID: "compile_a", Name: "compile_a", ContainerImage: "golang:1.24"},
		{ID: "compile_b", Name: "compile_b", ContainerImage: "golang:1.24"},
		{ID: "compile_c", Name: "compile_c", ContainerImage: "golang:1.24"},
		{ID: "unit_tests", Name: "unit_tests", ContainerImage: "golang:1.24"},
		{ID: "lint", Name: "lint", ContainerImage: "golangci-lint:latest"},
		{ID: "security_scan", Name: "security_scan", ContainerImage: "trivy:latest"},
		{ID: "integration", Name: "integration_test", ContainerImage: "golang:1.24"},
		{ID: "docker_build", Name: "docker_build", ContainerImage: "docker:buildkit"},
		{ID: "push", Name: "push", ContainerImage: "docker:latest"},
	}
	for _, task := range tasks {
		if err := g.AddTask(task); err != nil {
			t.Fatal(err)
		}
	}

	edges := [][2]string{
		{"compile_a", "integration"},
		{"compile_b", "integration"},
		{"compile_c", "integration"},
		{"unit_tests", "integration"},
		{"integration", "docker_build"},
		{"docker_build", "push"},
		// lint and security_scan are independent — no edges
	}
	for _, e := range edges {
		if err := g.AddEdge(e[0], e[1]); err != nil {
			t.Fatal(err)
		}
	}

	return g
}

func TestValidate_NoCycle(t *testing.T) {
	g := buildPipelineGraph(t)
	if err := g.Validate(); err != nil {
		t.Fatalf("expected valid graph, got: %v", err)
	}
	if g.Size() != 9 {
		t.Fatalf("expected 9 tasks, got %d", g.Size())
	}
}

func TestValidate_DetectsCycle(t *testing.T) {
	g := New()
	g.AddTask(&Task{ID: "a", Name: "a"})
	g.AddTask(&Task{ID: "b", Name: "b"})
	g.AddTask(&Task{ID: "c", Name: "c"})
	g.AddEdge("a", "b")
	g.AddEdge("b", "c")
	g.AddEdge("c", "a") // cycle

	err := g.Validate()
	if err == nil {
		t.Fatal("expected cycle detection error")
	}
	if !strings.Contains(err.Error(), "cycle detected") {
		t.Fatalf("expected cycle error, got: %v", err)
	}
}

func TestInitialReady(t *testing.T) {
	g := buildPipelineGraph(t)
	g.Validate()

	ready := g.MarkReady()
	names := taskNames(ready)

	// Tasks with no deps should be ready: compile_a, compile_b, compile_c,
	// unit_tests, lint, security_scan
	expected := map[string]bool{
		"compile_a":     true,
		"compile_b":     true,
		"compile_c":     true,
		"unit_tests":    true,
		"lint":          true,
		"security_scan": true,
	}

	if len(ready) != len(expected) {
		t.Fatalf("expected %d ready tasks, got %d: %v", len(expected), len(ready), names)
	}
	for _, name := range names {
		if !expected[name] {
			t.Errorf("unexpected ready task: %s", name)
		}
	}
}

func TestCompletionUnlocksDownstream(t *testing.T) {
	g := buildPipelineGraph(t)
	g.Validate()
	g.MarkReady()

	// Complete the independent tasks — lint and security_scan have no children
	// in the dependency chain to integration.
	// Complete all 4 deps of integration.
	for _, id := range []string{"compile_a", "compile_b", "compile_c"} {
		task, _ := g.GetTask(id)
		task.TransitionTo(TaskScheduled)
		task.TransitionTo(TaskRunning)
		newlyReady, err := g.Complete(id, TaskPassed, 0, "")
		if err != nil {
			t.Fatal(err)
		}
		// integration shouldn't be ready yet (still waiting on unit_tests)
		if len(newlyReady) != 0 {
			t.Fatalf("expected no newly ready after %s, got: %v", id, taskNames(newlyReady))
		}
	}

	// Complete unit_tests — this should unlock integration
	task, _ := g.GetTask("unit_tests")
	task.TransitionTo(TaskScheduled)
	task.TransitionTo(TaskRunning)
	newlyReady, err := g.Complete("unit_tests", TaskPassed, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(newlyReady) != 1 || newlyReady[0].ID != "integration" {
		t.Fatalf("expected integration to be ready, got: %v", taskNames(newlyReady))
	}
}

func TestFailureSkipsDownstream(t *testing.T) {
	g := buildPipelineGraph(t)
	g.Validate()
	g.MarkReady()

	// Fail compile_a — should skip integration, docker_build, push
	task, _ := g.GetTask("compile_a")
	task.TransitionTo(TaskScheduled)
	task.TransitionTo(TaskRunning)
	_, err := g.Complete("compile_a", TaskFailed, 1, "compilation error")
	if err != nil {
		t.Fatal(err)
	}

	integration, _ := g.GetTask("integration")
	dockerBuild, _ := g.GetTask("docker_build")
	push, _ := g.GetTask("push")

	if integration.State != TaskSkipped {
		t.Errorf("integration should be skipped, got %s", integration.State)
	}
	if dockerBuild.State != TaskSkipped {
		t.Errorf("docker_build should be skipped, got %s", dockerBuild.State)
	}
	if push.State != TaskSkipped {
		t.Errorf("push should be skipped, got %s", push.State)
	}

	// lint and security_scan should still be pending/ready (independent)
	lint, _ := g.GetTask("lint")
	scan, _ := g.GetTask("security_scan")
	if lint.State == TaskSkipped {
		t.Error("lint should NOT be skipped (independent)")
	}
	if scan.State == TaskSkipped {
		t.Error("security_scan should NOT be skipped (independent)")
	}
}

func TestFullPipelineExecution(t *testing.T) {
	g := buildPipelineGraph(t)
	g.Validate()

	// Simulate full execution.
	step := 0
	for !g.IsComplete() {
		step++
		if step > 20 {
			t.Fatal("too many steps, possible infinite loop")
		}

		ready := g.MarkReady()
		if len(ready) == 0 && !g.IsComplete() {
			t.Fatal("deadlock: no ready tasks but graph not complete")
		}

		for _, task := range ready {
			task.TransitionTo(TaskScheduled)
			task.TransitionTo(TaskRunning)
			g.Complete(task.ID, TaskPassed, 0, "")
		}
	}

	if !g.IsPassed() {
		t.Fatal("expected all tasks to pass")
	}
}

func TestCancelAll(t *testing.T) {
	g := buildPipelineGraph(t)
	g.Validate()
	g.MarkReady()

	// Complete one task.
	task, _ := g.GetTask("lint")
	task.TransitionTo(TaskScheduled)
	task.TransitionTo(TaskRunning)
	g.Complete("lint", TaskPassed, 0, "")

	// Cancel everything.
	g.Cancel()

	// lint should stay passed (terminal), everything else cancelled.
	lint, _ := g.GetTask("lint")
	if lint.State != TaskPassed {
		t.Errorf("lint should remain passed, got %s", lint.State)
	}

	for _, task := range g.Tasks() {
		if task.ID == "lint" {
			continue
		}
		if task.State != TaskCancelled {
			t.Errorf("task %s should be cancelled, got %s", task.Name, task.State)
		}
	}
}

func TestInvalidTransitions(t *testing.T) {
	task := &Task{ID: "t1", Name: "test", State: TaskPending}

	// Can't go from pending to running directly.
	if err := task.TransitionTo(TaskRunning); err == nil {
		t.Error("expected error for pending -> running")
	}

	// Can't go from pending to passed.
	if err := task.TransitionTo(TaskPassed); err == nil {
		t.Error("expected error for pending -> passed")
	}
}

func TestDuplicateTask(t *testing.T) {
	g := New()
	g.AddTask(&Task{ID: "a", Name: "a"})
	err := g.AddTask(&Task{ID: "a", Name: "a"})
	if err == nil {
		t.Error("expected error for duplicate task ID")
	}
}

func TestSelfDependency(t *testing.T) {
	g := New()
	g.AddTask(&Task{ID: "a", Name: "a"})
	err := g.AddEdge("a", "a")
	if err == nil {
		t.Error("expected error for self-dependency")
	}
}

func TestEmptyGraph(t *testing.T) {
	g := New()
	if err := g.Validate(); err != nil {
		t.Fatalf("empty graph should be valid: %v", err)
	}
	if !g.IsComplete() {
		t.Error("empty graph should be complete")
	}
}

func TestDependenciesAndDependents(t *testing.T) {
	g := buildPipelineGraph(t)

	deps := g.Dependencies("integration")
	if len(deps) != 4 {
		t.Errorf("integration should have 4 deps, got %d", len(deps))
	}

	children := g.Dependents("integration")
	if len(children) != 1 || children[0] != "docker_build" {
		t.Errorf("integration should have 1 child (docker_build), got %v", children)
	}
}

func taskNames(tasks []*Task) []string {
	names := make([]string, len(tasks))
	for i, t := range tasks {
		names[i] = t.Name
	}
	return names
}
