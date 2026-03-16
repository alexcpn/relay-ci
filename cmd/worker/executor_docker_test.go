package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// TestDockerEntrypointOverride verifies that images which set a custom
// entrypoint (like alpine/git:latest which sets entrypoint=git) still execute
// arbitrary shell commands correctly when the executor passes --entrypoint sh.
//
// Without the fix, "docker run alpine/git:latest sh -c 'git --version'" would
// run "git sh -c git --version" and fail with "git: 'sh' is not a git command".
func TestDockerEntrypointOverride(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not in PATH")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not available")
	}

	ctx := context.Background()

	// Pull the image once so both sub-tests use the cached version.
	if out, err := exec.CommandContext(ctx, "docker", "pull", "alpine/git:latest").CombinedOutput(); err != nil {
		t.Skipf("could not pull alpine/git:latest: %v\n%s", err, out)
	}

	// Without --entrypoint override: should fail because alpine/git sets
	// entrypoint=git, so the container runs "git sh -c echo hello".
	t.Run("fails_without_entrypoint_override", func(t *testing.T) {
		cmd := exec.CommandContext(ctx, "docker", "run", "--rm",
			"alpine/git:latest",
			"sh", "-c", "echo hello",
		)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Errorf("expected failure without --entrypoint override, but command succeeded with output: %s", out)
		}
	})

	// With --entrypoint sh: should succeed and be able to run git commands.
	t.Run("succeeds_with_entrypoint_override", func(t *testing.T) {
		cmd := exec.CommandContext(ctx, "docker", "run", "--rm",
			"--entrypoint", "sh",
			"alpine/git:latest",
			"-c", "git --version",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("command failed with --entrypoint sh: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "git version") {
			t.Errorf("expected 'git version' in output, got: %s", out)
		}
	})

	// Simulate the exact executor behaviour: join commands with && and pass
	// as -c argument to the overridden sh entrypoint.
	t.Run("executor_command_style", func(t *testing.T) {
		commands := []string{
			"git --version",
			"echo clone done",
		}
		shellCmd := strings.Join(commands, " && ")

		cmd := exec.CommandContext(ctx, "docker", "run", "--rm",
			"--entrypoint", "sh",
			"alpine/git:latest",
			"-c", shellCmd,
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("executor-style command failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "git version") {
			t.Errorf("expected git version in output, got: %s", out)
		}
		if !strings.Contains(string(out), "clone done") {
			t.Errorf("expected 'clone done' in output, got: %s", out)
		}
	})
}
