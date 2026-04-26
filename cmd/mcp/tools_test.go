package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeRepoReference_LocalPath(t *testing.T) {
	tmp := t.TempDir()

	got, err := normalizeRepoReference("", tmp)
	if err != nil {
		t.Fatalf("normalizeRepoReference returned error: %v", err)
	}
	if !strings.HasPrefix(got, "file://") {
		t.Fatalf("expected file:// URI, got %q", got)
	}
	abs, err := filepath.Abs(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, filepath.ToSlash(abs)) {
		t.Fatalf("expected %q to contain %q", got, filepath.ToSlash(abs))
	}
}

func TestNormalizeRepoReference_RepoURLPassThrough(t *testing.T) {
	got, err := normalizeRepoReference("https://example.com/org/repo.git", "")
	if err != nil {
		t.Fatalf("normalizeRepoReference returned error: %v", err)
	}
	if got != "https://example.com/org/repo.git" {
		t.Fatalf("expected pass-through URL, got %q", got)
	}
}
