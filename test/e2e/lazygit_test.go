// Package e2e contains end-to-end tests that run against a live CI master.
//
// These tests require a running master and worker. Set CI_MASTER to the
// master's gRPC address (default: localhost:9090). Tests are skipped unless
// the CI_E2E environment variable is set.
//
// Usage:
//
//	CI_E2E=1 go test ./test/e2e/... -v -timeout 10m
package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/ci-system/ci/gen/ci/v1"
)

const (
	testRepo   = "https://github.com/alexcpn/lazygit_ici_test.git"
	testBranch = "master"

	pollInterval = 5 * time.Second
	buildTimeout = 10 * time.Minute
)

// TestLazygitBuild submits a real build for the lazygit test repo and asserts
// the full pipeline passes. This is the canonical E2E smoke test for the CI
// system — it exercises the complete path from SubmitBuild through clone,
// build, test, and lint tasks running in real Docker containers.
func TestLazygitBuild(t *testing.T) {
	if os.Getenv("CI_E2E") == "" {
		t.Skip("set CI_E2E=1 to run end-to-end tests")
	}

	masterAddr := envOrDefault("CI_MASTER", "localhost:9090")

	conn, err := grpc.NewClient(masterAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("connecting to master at %s: %v", masterAddr, err)
	}
	defer conn.Close()

	sched := pb.NewSchedulerServiceClient(conn)
	logs := pb.NewLogServiceClient(conn)

	ctx := context.Background()

	// Submit the build.
	t.Logf("submitting build: %s @ %s", testRepo, testBranch)
	submitResp, err := sched.SubmitBuild(ctx, &pb.SubmitBuildRequest{
		Source: &pb.GitSource{
			RepoUrl:   testRepo,
			Branch:    testBranch,
			CommitSha: "HEAD",
		},
		TriggeredBy: "e2e-test",
	})
	if err != nil {
		t.Fatalf("SubmitBuild failed: %v", err)
	}

	buildID := submitResp.BuildId.Id
	t.Logf("build submitted: %s", buildID)

	// Poll until the build reaches a terminal state.
	deadline := time.Now().Add(buildTimeout)
	var finalBuild *pb.Build

	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)

		getResp, err := sched.GetBuild(ctx, &pb.GetBuildRequest{
			BuildId: &pb.BuildID{Id: buildID},
		})
		if err != nil {
			t.Fatalf("GetBuild failed: %v", err)
		}

		b := getResp.Build
		t.Logf("build %s state: %s (%d tasks)",
			buildID, b.State, len(b.Tasks))

		for _, task := range b.Tasks {
			dur := ""
			if task.Result != nil && task.Result.StartedAt != nil && task.Result.FinishedAt != nil {
				dur = task.Result.FinishedAt.AsTime().
					Sub(task.Result.StartedAt.AsTime()).
					Round(time.Millisecond).String()
			}
			t.Logf("  task %-20s %s %s", task.Name, task.State, dur)
		}

		if isTerminal(b.State) {
			finalBuild = b
			break
		}
	}

	if finalBuild == nil {
		t.Fatalf("build %s did not complete within %s", buildID, buildTimeout)
	}

	// On failure, dump logs for every failed task before asserting.
	if finalBuild.State != pb.BuildState_BUILD_STATE_PASSED {
		for _, task := range finalBuild.Tasks {
			if task.State != pb.TaskState_TASK_STATE_FAILED {
				continue
			}
			t.Logf("--- logs for failed task %q ---", task.Name)
			dumpTaskLogs(t, logs, buildID, task.TaskId.Id)
		}
		t.Fatalf("build %s ended with state %s (expected PASSED)", buildID, finalBuild.State)
	}

	t.Logf("build %s passed", buildID)

	// Verify every task either passed or was skipped — none should be failed.
	for _, task := range finalBuild.Tasks {
		switch task.State {
		case pb.TaskState_TASK_STATE_PASSED, pb.TaskState_TASK_STATE_SKIPPED:
			// expected
		default:
			t.Errorf("task %q in unexpected state %s", task.Name, task.State)
		}
	}
}

// isTerminal returns true for states that mean the build is done.
func isTerminal(s pb.BuildState) bool {
	switch s {
	case pb.BuildState_BUILD_STATE_PASSED,
		pb.BuildState_BUILD_STATE_FAILED,
		pb.BuildState_BUILD_STATE_CANCELLED:
		return true
	}
	return false
}

// dumpTaskLogs fetches and logs all lines for a task to the test output.
func dumpTaskLogs(t *testing.T, client pb.LogServiceClient, buildID, taskID string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stream, err := client.StreamLogs(ctx, &pb.StreamLogsRequest{
		BuildId: &pb.BuildID{Id: buildID},
		TaskId:  &pb.TaskID{Id: taskID},
		Follow:  false,
	})
	if err != nil {
		t.Logf("  (could not fetch logs: %v)", err)
		return
	}

	for {
		line, err := stream.Recv()
		if err != nil {
			break
		}
		prefix := ""
		switch line.Stream {
		case pb.LogStream_LOG_STREAM_STDERR:
			prefix = "[ERR] "
		case pb.LogStream_LOG_STREAM_SYSTEM:
			prefix = "[SYS] "
		}
		t.Logf("  %s%s", prefix, line.Content)
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// failedTaskSummary returns a human-readable summary of failed tasks.
func failedTaskSummary(tasks []*pb.Task) string {
	var failed []string
	for _, t := range tasks {
		if t.State == pb.TaskState_TASK_STATE_FAILED {
			exit := ""
			if t.Result != nil {
				exit = fmt.Sprintf(" (exit=%d)", t.Result.ExitCode)
			}
			failed = append(failed, t.Name+exit)
		}
	}
	if len(failed) == 0 {
		return "none"
	}
	return fmt.Sprintf("%v", failed)
}
