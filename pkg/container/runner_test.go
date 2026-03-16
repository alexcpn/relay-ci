package container

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestRunSuccess(t *testing.T) {
	rt := NewMockRuntime()
	runner := NewRunner(rt)

	result, err := runner.Run(context.Background(), ContainerConfig{
		ID:       "test-1",
		Image:    "golang:1.24",
		Commands: []string{"go", "test", "./..."},
	})
	if err != nil {
		t.Fatal(err)
	}

	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}
	if result.StartedAt.IsZero() {
		t.Error("started_at should be set")
	}
	if result.Duration == 0 {
		t.Error("duration should be > 0")
	}
	if len(result.Output) == 0 {
		t.Error("expected some output")
	}
}

func TestRunFailure(t *testing.T) {
	rt := NewMockRuntime()
	rt.ExitCodeFunc = func(config ContainerConfig) int {
		return 1
	}

	runner := NewRunner(rt)
	result, err := runner.Run(context.Background(), ContainerConfig{
		ID:       "test-fail",
		Image:    "golang:1.24",
		Commands: []string{"go", "test", "./..."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 1 {
		t.Errorf("expected exit code 1, got %d", result.ExitCode)
	}
}

func TestRunCustomOutput(t *testing.T) {
	rt := NewMockRuntime()
	rt.OutputFunc = func(config ContainerConfig) string {
		return "PASS\nok  mypackage 0.003s\n"
	}

	runner := NewRunner(rt)
	result, err := runner.Run(context.Background(), ContainerConfig{
		ID:       "test-output",
		Image:    "golang:1.24",
		Commands: []string{"go", "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Output) != 2 {
		t.Fatalf("expected 2 lines, got %d: %v", len(result.Output), result.Output)
	}
	if result.Output[0] != "PASS" {
		t.Errorf("expected PASS, got %s", result.Output[0])
	}
}

func TestRunPullFailure(t *testing.T) {
	rt := NewMockRuntime()
	rt.FailPull["bad-image:latest"] = fmt.Errorf("image not found")

	runner := NewRunner(rt)
	_, err := runner.Run(context.Background(), ContainerConfig{
		ID:    "test-pull-fail",
		Image: "bad-image:latest",
	})
	if err == nil {
		t.Fatal("expected pull failure")
	}
}

func TestRunTimeout(t *testing.T) {
	rt := NewMockRuntime()
	rt.execDelay = 500 * time.Millisecond // slow container

	runner := NewRunner(rt)
	result, err := runner.Run(context.Background(), ContainerConfig{
		ID:      "test-timeout",
		Image:   "golang:1.24",
		Timeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "timeout exceeded" {
		t.Errorf("expected timeout error, got %q", result.Error)
	}
	if result.ExitCode != -1 {
		t.Errorf("expected exit code -1 on timeout, got %d", result.ExitCode)
	}
}

func TestRunContextCancellation(t *testing.T) {
	rt := NewMockRuntime()
	rt.execDelay = 500 * time.Millisecond

	runner := NewRunner(rt)
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	result, err := runner.Run(ctx, ContainerConfig{
		ID:    "test-cancel",
		Image: "golang:1.24",
	})
	// Should handle cancellation gracefully.
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "timeout exceeded" {
		t.Errorf("expected timeout error on cancel, got %q", result.Error)
	}
}

func TestRunContainerCleanup(t *testing.T) {
	rt := NewMockRuntime()
	runner := NewRunner(rt)

	runner.Run(context.Background(), ContainerConfig{
		ID:    "test-cleanup",
		Image: "golang:1.24",
	})

	c, ok := rt.GetContainer("test-cleanup")
	if !ok {
		t.Fatal("container should still be tracked")
	}
	if c.State() != "removed" {
		t.Errorf("container should be removed after run, got %s", c.State())
	}
}

func TestRunWithEnvAndMounts(t *testing.T) {
	rt := NewMockRuntime()
	rt.OutputFunc = func(config ContainerConfig) string {
		if config.Env["CI"] != "true" {
			return "ERROR: CI env not set\n"
		}
		if len(config.Mounts) != 1 {
			return "ERROR: mount not configured\n"
		}
		return "OK\n"
	}

	runner := NewRunner(rt)
	result, err := runner.Run(context.Background(), ContainerConfig{
		ID:       "test-env",
		Image:    "golang:1.24",
		Commands: []string{"echo", "hello"},
		Env:      map[string]string{"CI": "true"},
		Mounts: []Mount{{
			Source: "/cache/m2", Target: "/root/.m2", ReadOnly: false,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output[0] != "OK" {
		t.Errorf("expected OK, got %s", result.Output[0])
	}
}
