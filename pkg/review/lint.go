package review

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// LintRunner runs host-installed linters against a diff and returns findings.
// Language is auto-detected from file extensions present in the diff if not
// provided. Missing linter binaries are silently skipped.
type LintRunner struct {
	policy ReviewPolicy
}

// NewLintRunner creates a LintRunner with the given policy.
func NewLintRunner(policy ReviewPolicy) *LintRunner {
	return &LintRunner{policy: policy}
}

// Run applies the diff to a temporary directory and runs appropriate linters.
func (r *LintRunner) Run(ctx context.Context, reviewID, diff, language string) ([]Finding, error) {
	if strings.TrimSpace(diff) == "" {
		return nil, nil
	}

	tmpDir, err := applyDiffToTemp(diff)
	if err != nil {
		return nil, fmt.Errorf("apply diff: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	langs := detectLanguages(diff, language)

	var findings []Finding
	for _, lang := range langs {
		f, err := r.runForLanguage(ctx, reviewID, lang, tmpDir, diff)
		if err != nil {
			// Non-fatal: log and continue with other linters.
			_ = err
			continue
		}
		findings = append(findings, f...)
	}
	return findings, nil
}

func (r *LintRunner) runForLanguage(ctx context.Context, reviewID, lang, dir, _ string) ([]Finding, error) {
	switch lang {
	case "go":
		return r.runGolangciLint(ctx, reviewID, dir)
	case "python":
		return r.runRuff(ctx, reviewID, dir)
	case "typescript", "javascript":
		return r.runESLint(ctx, reviewID, dir)
	case "shell":
		return r.runShellcheck(ctx, reviewID, dir)
	}
	return nil, nil
}

func (r *LintRunner) runGolangciLint(ctx context.Context, reviewID, dir string) ([]Finding, error) {
	if _, err := exec.LookPath("golangci-lint"); err != nil {
		return nil, nil // not installed, skip silently
	}
	out, err := runCmd(ctx, dir, "golangci-lint", "run", "--out-format=json", "./...")
	if len(out) == 0 {
		return nil, nil
	}
	findings, parseErr := ParseGolangciLint(out, reviewID)
	if parseErr != nil && err != nil {
		return nil, parseErr
	}
	return findings, nil
}

func (r *LintRunner) runRuff(ctx context.Context, reviewID, dir string) ([]Finding, error) {
	if _, err := exec.LookPath("ruff"); err != nil {
		return nil, nil
	}
	out, err := runCmd(ctx, dir, "ruff", "check", "--output-format=json", ".")
	if len(out) == 0 {
		return nil, nil
	}
	findings, parseErr := ParseRuff(out, reviewID)
	if parseErr != nil && err != nil {
		return nil, parseErr
	}
	return findings, nil
}

func (r *LintRunner) runESLint(ctx context.Context, reviewID, dir string) ([]Finding, error) {
	if _, err := exec.LookPath("eslint"); err != nil {
		return nil, nil
	}
	out, err := runCmd(ctx, dir, "eslint", "--format=json", ".")
	if len(out) == 0 {
		return nil, nil
	}
	findings, parseErr := ParseESLint(out, reviewID)
	if parseErr != nil && err != nil {
		return nil, parseErr
	}
	return findings, nil
}

func (r *LintRunner) runShellcheck(ctx context.Context, reviewID, dir string) ([]Finding, error) {
	if _, err := exec.LookPath("shellcheck"); err != nil {
		return nil, nil
	}
	// Find shell files.
	var shellFiles []string
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if strings.HasSuffix(path, ".sh") {
			shellFiles = append(shellFiles, path)
		}
		return nil
	})
	if len(shellFiles) == 0 {
		return nil, nil
	}
	args := append([]string{"--format=json1"}, shellFiles...)
	out, err := runCmd(ctx, dir, "shellcheck", args...)
	if len(out) == 0 {
		return nil, nil
	}
	findings, parseErr := ParseShellcheck(out, reviewID)
	if parseErr != nil && err != nil {
		return nil, parseErr
	}
	return findings, nil
}

// --- helpers ---

// runCmd runs a command and returns its combined output. Exit code 1 from
// linters indicates findings (not a fatal error), so we return output even
// when err != nil.
func runCmd(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.Bytes(), err
}

// applyDiffToTemp writes changed files from a unified diff into a temp dir
// so linters can run against real files rather than raw diff text.
func applyDiffToTemp(diff string) (string, error) {
	b := make([]byte, 6)
	rand.Read(b)
	dir, err := os.MkdirTemp("", "relay-review-"+hex.EncodeToString(b))
	if err != nil {
		return "", err
	}

	// Write the diff to a file and apply it to the tmpdir.
	diffFile := filepath.Join(dir, "input.patch")
	if err := os.WriteFile(diffFile, []byte(diff), 0o600); err != nil {
		os.RemoveAll(dir)
		return "", err
	}

	// Try GNU patch; if absent, do a best-effort file extraction.
	patchPath, err := exec.LookPath("patch")
	if err != nil {
		return extractFilesFromDiff(diff, dir)
	}

	cmd := exec.Command(patchPath, "-p1", "--forward", "--reject-file=/dev/null", "-d", dir, "-i", diffFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		// Patch errors are common for context mismatches; fall back.
		_ = out
		return extractFilesFromDiff(diff, dir)
	}
	return dir, nil
}

// extractFilesFromDiff is a fallback that writes the new content of each
// file changed by the diff directly to tmpdir, without using `patch`.
func extractFilesFromDiff(diff, dir string) (string, error) {
	var curFile string
	var newLines []string
	inNew := false

	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+++ b/") {
			if curFile != "" && len(newLines) > 0 {
				writeTempFile(dir, curFile, newLines)
			}
			curFile = strings.TrimPrefix(line, "+++ b/")
			newLines = nil
			inNew = true
			continue
		}
		if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "diff ") || strings.HasPrefix(line, "@@") {
			inNew = false
			continue
		}
		if inNew {
			if strings.HasPrefix(line, "+") {
				newLines = append(newLines, line[1:])
			} else if !strings.HasPrefix(line, "-") {
				newLines = append(newLines, line)
			}
		}
	}
	if curFile != "" && len(newLines) > 0 {
		writeTempFile(dir, curFile, newLines)
	}
	return dir, nil
}

func writeTempFile(dir, relPath string, lines []string) {
	full := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		return
	}
	_ = os.WriteFile(full, []byte(strings.Join(lines, "\n")), 0o600)
}

// detectLanguages returns a deduplicated list of programming languages
// present in the changed files of the diff. language overrides auto-detect.
func detectLanguages(diff, language string) []string {
	if language != "" {
		return []string{language}
	}
	seen := make(map[string]bool)
	for _, line := range strings.Split(diff, "\n") {
		if !strings.HasPrefix(line, "+++ b/") {
			continue
		}
		path := strings.TrimPrefix(line, "+++ b/")
		switch {
		case strings.HasSuffix(path, ".go"):
			seen["go"] = true
		case strings.HasSuffix(path, ".py"):
			seen["python"] = true
		case strings.HasSuffix(path, ".ts"), strings.HasSuffix(path, ".tsx"):
			seen["typescript"] = true
		case strings.HasSuffix(path, ".js"), strings.HasSuffix(path, ".jsx"):
			seen["javascript"] = true
		case strings.HasSuffix(path, ".sh"):
			seen["shell"] = true
		}
	}
	var langs []string
	for l := range seen {
		langs = append(langs, l)
	}
	if len(langs) == 0 {
		return []string{"go"} // default
	}
	return langs
}
