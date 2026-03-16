package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	pb "github.com/ci-system/ci/gen/ci/v1"
)

// TestFetchAndBuildGraph_ParsesPipelineYML verifies that fetchAndBuildGraph
// clones a real git repo and correctly parses its pipeline config into a DAG.
// This catches bugs where the pipeline is hardcoded or commands are split on
// spaces instead of treated as full shell strings.
func TestFetchAndBuildGraph_ParsesPipelineYML(t *testing.T) {
	repoDir := t.TempDir()
	initTestRepo(t, repoDir, `name: test-app

defaults:
  image: golang:1.24

tasks:
  - id: clone
    image: alpine/git:latest
    commands:
      - git clone --depth=1 $REPO_URL /workspace
      - cd /workspace && git checkout $COMMIT_SHA

  - id: build
    commands:
      - go build ./...
    depends_on: [clone]

  - id: test
    commands:
      - go test ./...
    depends_on: [clone]
`)

	g, err := fetchAndBuildGraph(context.Background(), &pb.GitSource{
		RepoUrl: "file://" + repoDir,
	})
	if err != nil {
		t.Fatalf("fetchAndBuildGraph failed: %v", err)
	}

	if got := len(g.Tasks()); got != 3 {
		t.Errorf("expected 3 tasks, got %d", got)
	}

	clone, ok := g.GetTask("clone")
	if !ok {
		t.Fatal("expected clone task in graph")
	}

	// Commands must be full shell strings, not individual words.
	// If they were split on spaces, we'd get ["git", "clone", "--depth=1", ...].
	if len(clone.Commands) != 2 {
		t.Errorf("expected 2 commands in clone task, got %d: %v", len(clone.Commands), clone.Commands)
	}
	if clone.Commands[0] != "git clone --depth=1 $REPO_URL /workspace" {
		t.Errorf("unexpected clone command: %q", clone.Commands[0])
	}
	if clone.ContainerImage != "alpine/git:latest" {
		t.Errorf("unexpected image: %q", clone.ContainerImage)
	}

	// build and test must depend on clone.
	for _, id := range []string{"build", "test"} {
		task, ok := g.GetTask(id)
		if !ok {
			t.Fatalf("expected task %q in graph", id)
		}
		if task.ContainerImage != "golang:1.24" {
			t.Errorf("task %q: expected default image golang:1.24, got %q", id, task.ContainerImage)
		}
	}
}

// TestFetchAndBuildGraph_NoPipelineFile verifies a clear error when the repo
// has no pipeline.yml or pipeline.yaml.
func TestFetchAndBuildGraph_NoPipelineFile(t *testing.T) {
	repoDir := t.TempDir()
	initTestRepo(t, repoDir, "") // no pipeline file

	_, err := fetchAndBuildGraph(context.Background(), &pb.GitSource{
		RepoUrl: "file://" + repoDir,
	})
	if err == nil {
		t.Fatal("expected error when pipeline file is missing, got nil")
	}
}

// TestFetchAndBuildGraph_PipelineYamlExtension verifies pipeline.yaml
// (with 'a') is also found.
func TestFetchAndBuildGraph_PipelineYamlExtension(t *testing.T) {
	repoDir := t.TempDir()

	// Write as pipeline.yaml (not .yml).
	pipeline := `name: yaml-ext-test
defaults:
  image: golang:1.24
tasks:
  - id: build
    commands:
      - go build ./...
`
	gitInit(t, repoDir)
	if err := os.WriteFile(filepath.Join(repoDir, "pipeline.yaml"), []byte(pipeline), 0644); err != nil {
		t.Fatal(err)
	}
	gitCommit(t, repoDir)

	g, err := fetchAndBuildGraph(context.Background(), &pb.GitSource{
		RepoUrl: "file://" + repoDir,
	})
	if err != nil {
		t.Fatalf("fetchAndBuildGraph failed: %v", err)
	}
	if _, ok := g.GetTask("build"); !ok {
		t.Error("expected build task")
	}
}

// initTestRepo creates a git repo with an optional pipeline.yml at its root.
func initTestRepo(t *testing.T, dir, pipelineYML string) {
	t.Helper()
	gitInit(t, dir)
	if pipelineYML != "" {
		if err := os.WriteFile(filepath.Join(dir, "pipeline.yml"), []byte(pipelineYML), 0644); err != nil {
			t.Fatal(err)
		}
	} else {
		// Need at least one file to commit.
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	gitCommit(t, dir)
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "ci-test@example.com"},
		{"config", "user.name", "CI Test"},
	} {
		runGit(t, dir, args...)
	}
}

func gitCommit(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "initial")
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
