package pipeline

import (
	"strings"

	"github.com/ci-system/ci/pkg/dag"
)

// Default images for built-in integrations.
var defaultImages = map[string]string{
	// Linters
	"golangci-lint": "golangci/golangci-lint:v1.62",
	"eslint":        "node:22-slim",
	"ruff":          "ghcr.io/astral-sh/ruff:latest",
	"pylint":        "python:3.13-slim",
	"rubocop":       "ruby:3.3-slim",
	"shellcheck":    "koalaman/shellcheck-alpine:stable",
	"hadolint":      "hadolint/hadolint:latest",

	// Security scanners
	"trivy":   "aquasec/trivy:latest",
	"grype":   "anchore/grype:latest",
	"semgrep": "semgrep/semgrep:latest",
	"gosec":   "securego/gosec:latest",

	// SonarQube
	"sonarqube": "sonarsource/sonar-scanner-cli:latest",
}

// Default commands for built-in tools.
// Each value is a complete shell command string (not individual args).
// Multiple commands per tool can be added as additional slice elements.
var defaultCommands = map[string]string{
	"golangci-lint": "golangci-lint run --timeout=5m",
	"eslint":        "npx eslint .",
	"ruff":          "ruff check .",
	"pylint":        "pylint .",
	"rubocop":       "rubocop",
	"shellcheck":    "find . -name '*.sh' -exec shellcheck {} +",
	"hadolint":      "find . -name 'Dockerfile*' -exec hadolint {} +",
	"trivy":         "trivy fs --severity HIGH,CRITICAL .",
	"grype":         "grype dir:.",
	"semgrep":       "semgrep scan --config=auto .",
	"gosec":         "gosec ./...",
}

// defaultCachesForLinter returns cache mounts for a linter tool so that
// package managers and analysis caches persist across builds.
func defaultCachesForLinter(name string) []dag.CacheMount {
	switch name {
	case "golangci-lint":
		return []dag.CacheMount{
			{CacheKey: "gomod", MountPath: "/root/go/pkg/mod"},
			{CacheKey: "golangci-lint-cache", MountPath: "/root/.cache/golangci-lint"},
		}
	case "eslint":
		return []dag.CacheMount{
			{CacheKey: "npm", MountPath: "/root/.npm"},
		}
	case "ruff", "pylint":
		return []dag.CacheMount{
			{CacheKey: "pip", MountPath: "/root/.cache/pip"},
		}
	}
	return nil
}

// defaultCachesForScanner returns cache mounts for a security scanner.
func defaultCachesForScanner(name string) []dag.CacheMount {
	switch name {
	case "trivy":
		// Trivy downloads a ~200MB vulnerability DB on first run.
		return []dag.CacheMount{
			{CacheKey: "trivy-db", MountPath: "/root/.cache/trivy"},
		}
	case "grype":
		return []dag.CacheMount{
			{CacheKey: "grype-db", MountPath: "/root/.cache/grype"},
		}
	}
	return nil
}

// imageForTool returns the container image for a tool, using the
// override if provided or the built-in default.
func imageForTool(name, override string) string {
	if override != "" {
		return override
	}
	if img, ok := defaultImages[name]; ok {
		return img
	}
	return ""
}

// commandsForLinter builds the shell command string for a linter tool.
// Returns a single-element []string containing the full shell command.
func commandsForLinter(tool LinterTool) []string {
	if len(tool.Args) > 0 {
		return tool.Args
	}

	base, ok := defaultCommands[tool.Name]
	if !ok {
		return []string{tool.Name}
	}

	cmd := base

	if tool.Config != "" {
		switch tool.Name {
		case "golangci-lint":
			cmd += " --config=" + tool.Config
		case "eslint":
			cmd += " --config " + tool.Config
		case "ruff":
			cmd += " --config=" + tool.Config
		}
	}

	if len(tool.Paths) > 0 {
		// Replace the trailing "." with the specified paths.
		cmd = cmd[:len(cmd)-1] + strings.Join(tool.Paths, " ")
	}

	return []string{cmd}
}

// commandsForScanner builds the shell command string for a security scanner.
// Returns a single-element []string containing the full shell command.
func commandsForScanner(tool SecurityTool) []string {
	if len(tool.Args) > 0 {
		return tool.Args
	}

	base, ok := defaultCommands[tool.Name]
	if !ok {
		return []string{tool.Name}
	}

	cmd := base

	if tool.Severity != "" && tool.Name == "trivy" {
		cmd = strings.Replace(cmd, "HIGH,CRITICAL", tool.Severity, 1)
	}

	if tool.FailOnFindings && tool.Name == "trivy" {
		cmd += " --exit-code=1"
	}

	return []string{cmd}
}

// commandsForSonar builds the sonar-scanner shell command string.
// Returns a single-element []string containing the full shell command.
func commandsForSonar(cfg *SonarQubeConfig) []string {
	cmd := "sonar-scanner" +
		" -Dsonar.host.url=" + cfg.ServerURL +
		" -Dsonar.projectKey=" + cfg.ProjectKey +
		" -Dsonar.token=$SONAR_TOKEN"

	if cfg.Sources != "" {
		cmd += " -Dsonar.sources=" + cfg.Sources
	}
	if cfg.Exclusions != "" {
		cmd += " -Dsonar.exclusions=" + cfg.Exclusions
	}
	if cfg.QualityGate {
		cmd += " -Dsonar.qualitygate.wait=true"
	}
	for _, arg := range cfg.ExtraArgs {
		cmd += " " + arg
	}

	return []string{cmd}
}
