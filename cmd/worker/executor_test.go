package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/ci-system/ci/gen/ci/v1"
)

func TestDockerRunArgs_BindsVerifyBundle(t *testing.T) {
	tmpDir := t.TempDir()
	bundlePath := filepath.Join(tmpDir, "verify.bundle")
	if err := os.WriteFile(bundlePath, []byte("bundle"), 0o644); err != nil {
		t.Fatal(err)
	}

	args, err := dockerRunArgs(&pb.AssignTaskRequest{
		ContainerImage: "alpine/git:latest",
		Commands:       []string{"git status"},
		Env: map[string]string{
			"RELAY_BUNDLE_PATH": bundlePath,
		},
	}, "build-123")
	if err != nil {
		t.Fatalf("dockerRunArgs returned error: %v", err)
	}

	mount := bundlePath + ":/relay-bundle.git:ro"
	if !contains(args, mount) {
		t.Fatalf("expected bundle mount %q in args: %v", mount, args)
	}
}

func TestDockerRunArgs_OmitsVerifyBundleMountWhenUnset(t *testing.T) {
	args, err := dockerRunArgs(&pb.AssignTaskRequest{
		ContainerImage: "alpine/git:latest",
		Commands:       []string{"git status"},
		Env: map[string]string{
			"CI": "true",
		},
	}, "build-123")
	if err != nil {
		t.Fatalf("dockerRunArgs returned error: %v", err)
	}
	for _, a := range args {
		if strings.Contains(a, "/relay-bundle.git") {
			t.Fatalf("did not expect verify bundle mount in args: %v", args)
		}
	}
}

func TestDockerRunArgs_BindsLocalRepo(t *testing.T) {
	tmpDir := t.TempDir()

	args, err := dockerRunArgs(&pb.AssignTaskRequest{
		ContainerImage: "alpine/git:latest",
		Commands:       []string{"git status"},
		Env: map[string]string{
			"RELAY_LOCAL_REPO_PATH": tmpDir,
			"REPO_URL":              "/relay-local-repo",
		},
	}, "build-123")
	if err != nil {
		t.Fatalf("dockerRunArgs returned error: %v", err)
	}

	mount := tmpDir + ":/relay-local-repo:ro"
	if !contains(args, mount) {
		t.Fatalf("expected local repo mount %q in args: %v", mount, args)
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
