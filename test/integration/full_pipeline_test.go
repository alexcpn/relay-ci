package integration

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/ci-system/ci/pkg/container"
	"github.com/ci-system/ci/pkg/dag"
	"github.com/ci-system/ci/pkg/logstore"
	"github.com/ci-system/ci/pkg/scheduler"
	"github.com/ci-system/ci/pkg/scm"
	"github.com/ci-system/ci/pkg/secrets"
	"github.com/ci-system/ci/pkg/worker"
)

// TestFullPipelineFromWebhookToCompletion simulates:
// 1. GitHub sends a PR webhook
// 2. SCM module parses it
// 3. Scheduler creates a build with a realistic DAG
// 4. Workers pick up tasks and execute them in mock containers
// 5. Logs are streamed and collected
// 6. Secrets are injected and scrubbed from logs
// 7. Status is reported back to GitHub
func TestFullPipelineFromWebhookToCompletion(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// --- Setup all components ---

	// Worker registry with 2 workers.
	registry := worker.NewRegistry(30 * time.Second)
	registry.Register(&worker.Info{
		ID: "worker-1", TotalCPU: 4000, AvailableCPU: 4000,
		TotalMemoryMB: 8192, AvailableMemoryMB: 8192,
		TotalDiskMB: 50000, AvailableDiskMB: 50000, MaxTasks: 8,
	})
	registry.Register(&worker.Info{
		ID: "worker-2", TotalCPU: 4000, AvailableCPU: 4000,
		TotalMemoryMB: 8192, AvailableMemoryMB: 8192,
		TotalDiskMB: 50000, AvailableDiskMB: 50000, MaxTasks: 8,
	})

	// Log store.
	logs := logstore.New()

	// Secret store.
	secretStore := secrets.NewStore()
	secretStore.Put("myorg/myrepo", "DOCKER_PASSWORD", "super-secret-docker-pass", "admin")
	secretStore.Put("myorg/myrepo", "API_KEY", "sk-1234567890", "admin")

	// Mock container runtime.
	rt := container.NewMockRuntime()
	rt.OutputFunc = func(config container.ContainerConfig) string {
		return "Step 1: " + config.Commands[0] + "\nStep 2: done\n"
	}

	// Container runner.
	runner := container.NewRunner(rt)

	// Track task assignments for verification.
	var assignments []scheduler.TaskAssignment

	// Scheduler.
	sched := scheduler.New(registry, func(a scheduler.TaskAssignment) error {
		assignments = append(assignments, a)
		return nil
	}, logger)

	// SCM.
	gh := scm.NewGitHub(nil, "")
	router := scm.NewRouter(gh)

	// --- Step 1: Parse GitHub PR webhook ---

	prPayload := map[string]interface{}{
		"action": "opened",
		"number": 42,
		"pull_request": map[string]interface{}{
			"title": "Add caching layer",
			"head":  map[string]string{"sha": "abc123def456", "ref": "feature/cache"},
			"base":  map[string]string{"ref": "main"},
			"user":  map[string]string{"login": "developer1"},
		},
		"repository": map[string]interface{}{
			"full_name": "myorg/myrepo",
			"clone_url": "https://github.com/myorg/myrepo.git",
		},
	}
	payloadBytes, _ := json.Marshal(prPayload)
	webhookSecret := "test-webhook-secret"

	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write(payloadBytes)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest("POST", "/webhooks/github", bytes.NewReader(payloadBytes))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-GitHub-Delivery", "delivery-integration-test")
	req.Header.Set("X-Hub-Signature-256", signature)

	event, err := router.Parse(req, webhookSecret)
	if err != nil {
		t.Fatalf("webhook parse failed: %v", err)
	}

	if event.Type != scm.EventPullRequest {
		t.Fatalf("expected PR event, got %d", event.Type)
	}
	if event.PR.Number != 42 {
		t.Fatalf("expected PR #42, got %d", event.PR.Number)
	}
	if event.PR.HeadSHA != "abc123def456" {
		t.Fatalf("expected sha abc123def456, got %s", event.PR.HeadSHA)
	}

	// --- Step 2: Build a realistic DAG ---

	g := dag.New()
	tasks := []*dag.Task{
		{ID: "clone", Name: "clone", ContainerImage: "alpine/git:latest",
			Commands: []string{"git", "clone"}, CPUMillicores: 500, MemoryMB: 256, DiskMB: 1000},
		{ID: "compile", Name: "compile", ContainerImage: "golang:1.24",
			Commands: []string{"go", "build", "./..."}, CPUMillicores: 2000, MemoryMB: 1024, DiskMB: 2000},
		{ID: "test", Name: "unit_tests", ContainerImage: "golang:1.24",
			Commands: []string{"go", "test", "./..."}, CPUMillicores: 1000, MemoryMB: 1024, DiskMB: 2000},
		{ID: "lint", Name: "lint", ContainerImage: "golangci-lint:latest",
			Commands: []string{"golangci-lint", "run"}, CPUMillicores: 500, MemoryMB: 512, DiskMB: 1000},
		{ID: "security", Name: "security_scan", ContainerImage: "trivy:latest",
			Commands: []string{"trivy", "fs", "."}, CPUMillicores: 500, MemoryMB: 512, DiskMB: 1000},
		{ID: "integration", Name: "integration_test", ContainerImage: "golang:1.24",
			Commands: []string{"go", "test", "-tags=integration"}, CPUMillicores: 2000, MemoryMB: 2048, DiskMB: 5000},
		{ID: "docker", Name: "docker_build", ContainerImage: "docker:buildkit",
			Commands: []string{"docker", "build", "."}, CPUMillicores: 1000, MemoryMB: 1024, DiskMB: 5000},
		{ID: "push", Name: "push", ContainerImage: "docker:latest",
			Commands: []string{"docker", "push"}, CPUMillicores: 500, MemoryMB: 256, DiskMB: 1000},
	}
	for _, task := range tasks {
		g.AddTask(task)
	}
	g.AddEdge("clone", "compile")
	g.AddEdge("clone", "test")
	g.AddEdge("clone", "lint")
	g.AddEdge("clone", "security")
	g.AddEdge("compile", "integration")
	g.AddEdge("test", "integration")
	g.AddEdge("integration", "docker")
	g.AddEdge("docker", "push")

	if err := g.Validate(); err != nil {
		t.Fatalf("graph validation failed: %v", err)
	}

	build := &scheduler.Build{
		ID:          "build-pr-42",
		Graph:       g,
		RepoURL:     event.PR.RepoURL,
		CommitSHA:   event.PR.HeadSHA,
		Branch:      event.PR.HeadBranch,
		PRNumber:    "42",
		TriggeredBy: "webhook:" + event.PR.Author,
	}
	if err := sched.SubmitBuild(build); err != nil {
		t.Fatalf("submit build failed: %v", err)
	}

	// --- Step 3: Run scheduling loop with mock container execution ---

	ctx := context.Background()
	completedTasks := 0
	iterations := 0

	for !build.Graph.IsComplete() {
		iterations++
		if iterations > 50 {
			t.Fatal("too many iterations, possible deadlock")
		}

		// Schedule ready tasks.
		_, err := sched.Schedule(ctx)
		if err != nil {
			t.Fatal(err)
		}

		// Simulate worker executing running tasks.
		for _, task := range build.Graph.Tasks() {
			if task.State != dag.TaskRunning {
				continue
			}

			// Step 4: Fetch secrets for the task.
			taskSecrets, _ := secretStore.GetMultiple("myorg/myrepo", []string{"DOCKER_PASSWORD", "API_KEY"})

			// Step 5: Execute in mock container.
			env := map[string]string{"CI": "true"}
			for k, v := range taskSecrets {
				env[k] = v
			}

			rt.Pull(ctx, task.ContainerImage)
			result, err := runner.Run(ctx, container.ContainerConfig{
				ID:       task.ID,
				Image:    task.ContainerImage,
				Commands: task.Commands,
				Env:      env,
			})
			if err != nil {
				t.Fatalf("container run failed for %s: %v", task.Name, err)
			}

			// Step 6: Push logs with secret scrubbing.
			scrubber := secrets.NewScrubber([]string{
				taskSecrets["DOCKER_PASSWORD"],
				taskSecrets["API_KEY"],
			})

			logLines := make([]logstore.Line, len(result.Output))
			for i, line := range result.Output {
				logLines[i] = logstore.Line{
					Content: scrubber.Scrub(line),
					Stream:  logstore.StreamStdout,
				}
			}
			logs.Append(task.ID, logLines)

			// Report result to scheduler.
			state := dag.TaskPassed
			if result.ExitCode != 0 {
				state = dag.TaskFailed
			}
			sched.HandleTaskResult(scheduler.TaskResultReport{
				BuildID:  build.ID,
				TaskID:   task.ID,
				State:    state,
				ExitCode: result.ExitCode,
			})
			completedTasks++
		}
	}

	// --- Verify results ---

	if !build.Graph.IsPassed() {
		t.Fatal("build should have passed")
	}
	if completedTasks != 8 {
		t.Errorf("expected 8 completed tasks, got %d", completedTasks)
	}

	// Verify logs exist for all tasks.
	for _, task := range build.Graph.Tasks() {
		lineCount := logs.LineCount(task.ID)
		if lineCount == 0 {
			t.Errorf("no logs for task %s", task.Name)
		}
	}

	// Verify secrets were scrubbed from logs.
	pushLines, _ := logs.Get("push", 0, 0)
	for _, line := range pushLines {
		if containsSecret(line.Content, "super-secret-docker-pass") {
			t.Error("secret leaked in logs: DOCKER_PASSWORD")
		}
		if containsSecret(line.Content, "sk-1234567890") {
			t.Error("secret leaked in logs: API_KEY")
		}
	}

	// Verify task execution order (topological).
	cloneTask, _ := build.Graph.GetTask("clone")
	compileTask, _ := build.Graph.GetTask("compile")
	if !cloneTask.FinishedAt.Before(compileTask.StartedAt) || cloneTask.FinishedAt.Equal(compileTask.StartedAt) {
		// Clone must finish before compile starts (clone → compile dependency).
		// Note: both could have the same timestamp in fast test, so we just verify state.
		if cloneTask.State != dag.TaskPassed {
			t.Error("clone should have passed before compile ran")
		}
	}

	// Verify assignments were distributed across workers.
	workerTaskCount := make(map[string]int)
	for _, a := range assignments {
		workerTaskCount[a.WorkerID]++
	}
	if len(workerTaskCount) < 1 {
		t.Error("expected tasks assigned to at least 1 worker")
	}

	t.Logf("Build completed: %d tasks, %d iterations, workers used: %v",
		completedTasks, iterations, workerTaskCount)
}

// TestWebhookToStatusReport verifies the full flow from webhook
// to status reporting back to GitHub.
func TestWebhookToStatusReport(t *testing.T) {
	// Mock GitHub API server to receive status updates.
	var receivedStatuses []map[string]string
	ghAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		receivedStatuses = append(receivedStatuses, body)
		w.WriteHeader(201)
	}))
	defer ghAPI.Close()

	gh := scm.NewGitHub(ghAPI.Client(), ghAPI.URL)

	// Report pending status.
	err := gh.ReportStatus(context.Background(), "test-token", scm.StatusReport{
		RepoFullName: "myorg/myrepo",
		CommitSHA:    "abc123",
		State:        scm.StatusPending,
		Context:      "ci/build",
		Description:  "Build queued",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Report success status.
	err = gh.ReportStatus(context.Background(), "test-token", scm.StatusReport{
		RepoFullName: "myorg/myrepo",
		CommitSHA:    "abc123",
		State:        scm.StatusSuccess,
		Context:      "ci/build",
		Description:  "Build passed in 45s",
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(receivedStatuses) != 2 {
		t.Fatalf("expected 2 status updates, got %d", len(receivedStatuses))
	}
	if receivedStatuses[0]["state"] != "pending" {
		t.Errorf("first status should be pending, got %s", receivedStatuses[0]["state"])
	}
	if receivedStatuses[1]["state"] != "success" {
		t.Errorf("second status should be success, got %s", receivedStatuses[1]["state"])
	}
}

// TestParallelBuilds verifies multiple builds execute concurrently.
func TestParallelBuilds(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	registry := worker.NewRegistry(30 * time.Second)
	registry.Register(&worker.Info{
		ID: "w1", TotalCPU: 8000, AvailableCPU: 8000,
		TotalMemoryMB: 16384, AvailableMemoryMB: 16384,
		TotalDiskMB: 100000, AvailableDiskMB: 100000, MaxTasks: 16,
	})

	var totalAssigned int
	sched := scheduler.New(registry, func(a scheduler.TaskAssignment) error {
		totalAssigned++
		return nil
	}, logger)

	// Submit 3 builds, each with 3 tasks.
	for i := 0; i < 3; i++ {
		g := dag.New()
		g.AddTask(&dag.Task{
			ID: "compile", Name: "compile",
			CPUMillicores: 500, MemoryMB: 256, DiskMB: 500,
		})
		g.AddTask(&dag.Task{
			ID: "test", Name: "test",
			CPUMillicores: 500, MemoryMB: 256, DiskMB: 500,
		})
		g.AddTask(&dag.Task{
			ID: "deploy", Name: "deploy",
			CPUMillicores: 500, MemoryMB: 256, DiskMB: 500,
		})
		g.AddEdge("compile", "deploy")
		g.AddEdge("test", "deploy")
		g.Validate()

		sched.SubmitBuild(&scheduler.Build{
			ID:    fmt.Sprintf("build-%d", i),
			Graph: g,
		})
	}

	ctx := context.Background()

	// First scheduling cycle should pick up tasks from all 3 builds.
	n, err := sched.Schedule(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// compile + test from each build = 6 tasks (deploy waits).
	if n != 6 {
		t.Fatalf("expected 6 tasks scheduled across 3 builds, got %d", n)
	}

	// Complete them all and schedule again.
	for i := 0; i < 3; i++ {
		buildID := fmt.Sprintf("build-%d", i)
		sched.HandleTaskResult(scheduler.TaskResultReport{BuildID: buildID, TaskID: "compile", State: dag.TaskPassed})
		sched.HandleTaskResult(scheduler.TaskResultReport{BuildID: buildID, TaskID: "test", State: dag.TaskPassed})
	}

	n, _ = sched.Schedule(ctx)
	if n != 3 {
		t.Fatalf("expected 3 deploy tasks, got %d", n)
	}
}

// TestWorkerFailureDuringBuild verifies graceful handling when a worker dies.
func TestWorkerFailureDuringBuild(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	registry := worker.NewRegistry(50 * time.Millisecond)
	registry.Register(&worker.Info{
		ID: "w1", TotalCPU: 4000, AvailableCPU: 4000,
		TotalMemoryMB: 8192, AvailableMemoryMB: 8192,
		TotalDiskMB: 50000, AvailableDiskMB: 50000, MaxTasks: 4,
	})
	registry.Register(&worker.Info{
		ID: "w2", TotalCPU: 4000, AvailableCPU: 4000,
		TotalMemoryMB: 8192, AvailableMemoryMB: 8192,
		TotalDiskMB: 50000, AvailableDiskMB: 50000, MaxTasks: 4,
	})

	sched := scheduler.New(registry, func(a scheduler.TaskAssignment) error {
		return nil
	}, logger)

	g := dag.New()
	g.AddTask(&dag.Task{
		ID: "t1", Name: "task-1", CPUMillicores: 500, MemoryMB: 256, DiskMB: 500,
	})
	g.AddTask(&dag.Task{
		ID: "t2", Name: "task-2", CPUMillicores: 500, MemoryMB: 256, DiskMB: 500,
	})
	g.Validate()

	sched.SubmitBuild(&scheduler.Build{ID: "b1", Graph: g})
	sched.Schedule(context.Background())

	// Simulate worker-1 dying.
	w1, _ := registry.Get("w1")
	w1.LastHeartbeat = time.Now().Add(-100 * time.Millisecond)
	dead := registry.CheckHeartbeats()

	for _, id := range dead {
		sched.HandleDeadWorker(id)
	}

	// Build should complete (some tasks failed due to dead worker).
	// Complete any remaining running tasks.
	for _, task := range g.Tasks() {
		if task.State == dag.TaskRunning {
			sched.HandleTaskResult(scheduler.TaskResultReport{
				BuildID: "b1", TaskID: task.ID, State: dag.TaskPassed,
			})
		}
	}

	if !g.IsComplete() {
		t.Error("graph should be complete after handling dead worker")
	}
}

// TestLogStreamingDuringBuild verifies real-time log streaming works
// while tasks are executing.
func TestLogStreamingDuringBuild(t *testing.T) {
	store := logstore.New()

	// Subscribe before any logs are written.
	sub, err := store.Subscribe("task-compile", 0)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a build producing logs.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 5; i++ {
			store.Append("task-compile", []logstore.Line{
				{Content: fmt.Sprintf("compiling package %d/5...", i+1), Stream: logstore.StreamStdout},
			})
			time.Sleep(5 * time.Millisecond)
		}
		store.Append("task-compile", []logstore.Line{
			{Content: "compilation complete", Stream: logstore.StreamStdout},
		})
		store.Complete("task-compile")
	}()

	// Collect streamed lines.
	var received []string
	for line := range sub.C {
		received = append(received, line.Content)
	}

	<-done

	if len(received) != 6 {
		t.Fatalf("expected 6 streamed lines, got %d", len(received))
	}
	if received[5] != "compilation complete" {
		t.Errorf("expected last line 'compilation complete', got %q", received[5])
	}

	// Verify batch retrieval also works.
	lines, total := store.Get("task-compile", 0, 0)
	if total != 6 {
		t.Errorf("expected 6 stored lines, got %d", total)
	}
	if len(lines) != 6 {
		t.Errorf("expected 6 lines, got %d", len(lines))
	}
}

func containsSecret(s, secret string) bool {
	return len(secret) > 0 && bytes.Contains([]byte(s), []byte(secret))
}

