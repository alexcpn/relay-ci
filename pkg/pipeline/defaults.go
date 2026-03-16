package pipeline

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
var defaultCommands = map[string][]string{
	"golangci-lint": {"golangci-lint", "run", "--timeout=5m"},
	"eslint":        {"npx", "eslint", "."},
	"ruff":          {"ruff", "check", "."},
	"pylint":        {"pylint", "."},
	"rubocop":       {"rubocop"},
	"shellcheck":    {"sh", "-c", "find . -name '*.sh' -exec shellcheck {} +"},
	"hadolint":      {"sh", "-c", "find . -name 'Dockerfile*' -exec hadolint {} +"},
	"trivy":         {"trivy", "fs", "--severity", "HIGH,CRITICAL", "."},
	"grype":         {"grype", "dir:."},
	"semgrep":       {"semgrep", "scan", "--config=auto", "."},
	"gosec":         {"gosec", "./..."},
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

// commandsForLinter builds the command for a linter tool.
func commandsForLinter(tool LinterTool) []string {
	if len(tool.Args) > 0 {
		return tool.Args
	}

	base, ok := defaultCommands[tool.Name]
	if !ok {
		return []string{tool.Name}
	}

	cmd := make([]string, len(base))
	copy(cmd, base)

	if tool.Config != "" {
		switch tool.Name {
		case "golangci-lint":
			cmd = append(cmd, "--config="+tool.Config)
		case "eslint":
			cmd = append(cmd, "--config", tool.Config)
		case "ruff":
			cmd = append(cmd, "--config="+tool.Config)
		}
	}

	if len(tool.Paths) > 0 {
		// Replace the default "." with specific paths.
		cmd = cmd[:len(cmd)-1]
		cmd = append(cmd, tool.Paths...)
	}

	return cmd
}

// commandsForScanner builds the command for a security scanner.
func commandsForScanner(tool SecurityTool) []string {
	if len(tool.Args) > 0 {
		return tool.Args
	}

	base, ok := defaultCommands[tool.Name]
	if !ok {
		return []string{tool.Name}
	}

	cmd := make([]string, len(base))
	copy(cmd, base)

	if tool.Severity != "" && tool.Name == "trivy" {
		// Replace default severity.
		for i, arg := range cmd {
			if arg == "HIGH,CRITICAL" {
				cmd[i] = tool.Severity
			}
		}
	}

	if tool.FailOnFindings && tool.Name == "trivy" {
		cmd = append(cmd, "--exit-code=1")
	}

	return cmd
}

// commandsForSonar builds the sonar-scanner command.
func commandsForSonar(cfg *SonarQubeConfig) []string {
	cmd := []string{
		"sonar-scanner",
		"-Dsonar.host.url=" + cfg.ServerURL,
		"-Dsonar.projectKey=" + cfg.ProjectKey,
		"-Dsonar.token=$SONAR_TOKEN",
	}

	if cfg.Sources != "" {
		cmd = append(cmd, "-Dsonar.sources="+cfg.Sources)
	}
	if cfg.Exclusions != "" {
		cmd = append(cmd, "-Dsonar.exclusions="+cfg.Exclusions)
	}
	if cfg.QualityGate {
		cmd = append(cmd, "-Dsonar.qualitygate.wait=true")
	}

	cmd = append(cmd, cfg.ExtraArgs...)
	return cmd
}
