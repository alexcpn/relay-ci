package pipeline

import (
	"encoding/base64"
	"fmt"
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

	// Code review
	"code-review": "python:3.12-slim",
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

// commandsForCodeReview returns the two commands that run the code review task:
//  1. pip install the appropriate LLM SDK
//  2. decode + execute the embedded Python script with config as env vars
//
// The script is base64-encoded to avoid shell quoting issues.
func commandsForCodeReview(cfg *CodeReviewConfig) []string {
	provider := cfg.Provider
	if provider == "" {
		provider = "anthropic"
	}
	baseBranch := cfg.BaseBranch
	if baseBranch == "" {
		baseBranch = "main"
	}
	promptPath := cfg.ReviewerPrompt
	if promptPath == "" {
		promptPath = "code-reviewer.md"
	}
	sdkPkg, defaultModel := "anthropic", "claude-sonnet-4-6"
	switch provider {
	case "openai":
		sdkPkg, defaultModel = "openai", "gpt-4o"
	case "ollama":
		sdkPkg, defaultModel = "openai", "llama3.2"
	}
	model := cfg.Model
	if model == "" {
		model = defaultModel
	}
	ollamaURL := cfg.OllamaURL
	if ollamaURL == "" {
		ollamaURL = "http://localhost:11434"
	}

	encoded := base64.StdEncoding.EncodeToString([]byte(codeReviewScript))
	install := fmt.Sprintf("pip install -q %s 2>/dev/null || true", sdkPkg)
	run := fmt.Sprintf(
		"echo '%s' | base64 -d | "+
			"REVIEW_PROVIDER='%s' REVIEW_BASE_BRANCH='%s' REVIEW_PROMPT_PATH='%s' "+
			"REVIEW_MODEL='%s' REVIEW_OLLAMA_URL='%s' CODE_REVIEW_SERVER_URL='%s' python3",
		encoded, provider, baseBranch, promptPath, model, ollamaURL, cfg.ServerURL,
	)
	return []string{install, run}
}

// codeReviewScript is the Python script executed inside the review-pr container.
// It supports four paths (checked in order):
//  1. CODE_REVIEW_SERVER_URL set  → delegate to the agentic_codereview HTTP service
//  2. REVIEW_PROVIDER=ollama      → call Ollama via OpenAI-compatible API
//  3. REVIEW_PROVIDER=openai      → call OpenAI API
//  4. REVIEW_PROVIDER=anthropic   → call Anthropic Claude API (default)
var codeReviewScript = `import os, sys, subprocess

def get_diff(base_branch):
    subprocess.run(['git', 'fetch', '--depth=50', 'origin', base_branch],
        cwd='/workspace', capture_output=True)
    diff = subprocess.run(['git', 'diff', 'origin/' + base_branch + '...HEAD'],
        cwd='/workspace', capture_output=True, text=True).stdout
    if not diff.strip():
        diff = subprocess.run(['git', 'diff', 'HEAD~1'],
            cwd='/workspace', capture_output=True, text=True).stdout
    return diff

def get_prompt(path):
    try:
        return open(os.path.join('/workspace', path)).read()
    except FileNotFoundError:
        return ("You are a code reviewer. Review the following diff for correctness, "
                "security, and quality. Categorize issues as Critical, Important, or Minor.")

base_branch = os.environ.get('REVIEW_BASE_BRANCH', 'main')
prompt_path = os.environ.get('REVIEW_PROMPT_PATH', 'code-reviewer.md')
model       = os.environ.get('REVIEW_MODEL', 'claude-sonnet-4-6')
provider    = os.environ.get('REVIEW_PROVIDER', 'anthropic')
server_url  = os.environ.get('CODE_REVIEW_SERVER_URL', '')
ollama_url  = os.environ.get('REVIEW_OLLAMA_URL', 'http://localhost:11434')

if server_url:
    import urllib.request, urllib.parse
    params = urllib.parse.urlencode({
        'repo_url':  os.environ.get('REPO_URL', ''),
        'pr_number': os.environ.get('PR_NUMBER', ''),
    })
    with urllib.request.urlopen(server_url + '/review?' + params, timeout=300) as r:
        print(r.read().decode())
    sys.exit(0)

diff = get_diff(base_branch)
if not diff.strip():
    print("Nothing to review: no diff found.")
    sys.exit(0)

prompt  = get_prompt(prompt_path)
content = prompt + "\n\n## Diff\n\n" + diff

if provider in ('ollama', 'openai'):
    import openai
    kwargs = dict(model=model, messages=[{"role": "user", "content": content}], max_tokens=4096)
    if provider == 'ollama':
        client = openai.OpenAI(base_url=ollama_url + '/v1', api_key='ollama')
    else:
        client = openai.OpenAI(api_key=os.environ['OPENAI_API_KEY'])
    print(client.chat.completions.create(**kwargs).choices[0].message.content)
    sys.exit(0)

import anthropic
client = anthropic.Anthropic(api_key=os.environ['ANTHROPIC_API_KEY'])
msg = client.messages.create(
    model=model, max_tokens=4096,
    messages=[{"role": "user", "content": content}])
print(msg.content[0].text)
`

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
