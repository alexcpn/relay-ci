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
	failOnCritical := "true"
	if cfg.FailOnCritical != nil && !*cfg.FailOnCritical {
		failOnCritical = "false"
	}

	encoded := base64.StdEncoding.EncodeToString([]byte(codeReviewScript))
	// Install git if apt-get is available (Debian/Ubuntu images); skip silently otherwise.
	// If apt-get is present but fails (e.g. network issue), emit a warning so the cause is visible.
	// pip install is always required — failure exits immediately.
	// Warnings go to stderr so they don't pollute captured stdout output.
	install := fmt.Sprintf(
		"command -v apt-get >/dev/null 2>&1 &&"+
			" (apt-get update -qq && apt-get install -y -qq git ||"+
			" echo 'WARNING: apt install of git failed' >&2);"+
			" command -v git >/dev/null 2>&1 || echo 'WARNING: git not found, diff may fail' >&2;"+
			" pip install -q %s || exit 1",
		sdkPkg,
	)
	run := fmt.Sprintf(
		"echo '%s' | base64 -d | "+
			"REVIEW_PROVIDER='%s' REVIEW_BASE_BRANCH='%s' REVIEW_PROMPT_PATH='%s' "+
			"REVIEW_MODEL='%s' REVIEW_OLLAMA_URL='%s' CODE_REVIEW_SERVER_URL='%s' "+
			"REVIEW_FAIL_ON_CRITICAL='%s' python3",
		encoded, provider, baseBranch, promptPath, model, ollamaURL, cfg.ServerURL, failOnCritical,
	)
	return []string{install, run}
}

// codeReviewScript is the Python script executed inside the review-pr container.
// It supports four paths (checked in order):
//  1. CODE_REVIEW_SERVER_URL set  → delegate to the agentic_codereview HTTP service
//  2. REVIEW_PROVIDER=ollama      → call Ollama via OpenAI-compatible API
//  3. REVIEW_PROVIDER=openai      → call OpenAI API
//  4. REVIEW_PROVIDER=anthropic   → call Anthropic Claude API (default)
var codeReviewScript = `import os, sys, re, subprocess

def get_diff(base_branch):
    # Deepen history enough to find the merge base with the target branch.
    # --update-shallow handles repos cloned with --depth=1.
    subprocess.run(['git', 'fetch', '--deepen=200', '--update-shallow', 'origin', base_branch],
        cwd='/workspace', capture_output=True)
    diff = subprocess.run(['git', 'diff', 'origin/' + base_branch + '...HEAD'],
        cwd='/workspace', capture_output=True, text=True).stdout
    if not diff.strip():
        # Fallback for branch pushes with no PR: show only the last commit.
        diff = subprocess.run(['git', 'diff', 'HEAD~1'],
            cwd='/workspace', capture_output=True, text=True).stdout
    return diff

def get_prompt(path):
    try:
        return open(os.path.join('/workspace', path)).read()
    except FileNotFoundError:
        return ("You are a code reviewer. Review the following diff for correctness, "
                "security, and quality. Categorize issues as Critical, Important, or Minor.\n"
                "End your review with a line: **Ready to merge?** Yes / No / With fixes")

def check_verdict(review_text, fail_on_critical):
    """Return (should_fail, reason) based on the review text."""
    if not fail_on_critical:
        return False, ""

    # Look for explicit verdict line: **Ready to merge?** <verdict>
    m = re.search(r'\*\*Ready to merge\?\*\*\s*(.+)', review_text, re.IGNORECASE)
    if m:
        verdict = m.group(1).strip().lower()
        if verdict.startswith('no'):
            return True, "Reviewer verdict: NOT ready to merge"
        if 'with fixes' in verdict:
            return True, "Reviewer verdict: requires fixes before merging"

    # Also fail if a non-empty Critical section exists.
    # Match content between "#### Critical" and the next "####" or end.
    m = re.search(r'####\s*Critical[^\n]*\n(.*?)(?=####|\Z)', review_text, re.DOTALL | re.IGNORECASE)
    if m:
        body = m.group(1).strip()
        # Ignore empty, placeholder lines like "[Bugs, security issues...]", or explicit "None"/"None."
        if body and not re.match(r'^(\[.*\]|[Nn]one\.?)$', body):
            return True, "Critical issues found — see review above"

    return False, ""

base_branch      = os.environ.get('REVIEW_BASE_BRANCH', 'main')
prompt_path      = os.environ.get('REVIEW_PROMPT_PATH', 'code-reviewer.md')
model            = os.environ.get('REVIEW_MODEL', 'claude-sonnet-4-6')
provider         = os.environ.get('REVIEW_PROVIDER', 'anthropic')
server_url       = os.environ.get('CODE_REVIEW_SERVER_URL', '')
ollama_url       = os.environ.get('REVIEW_OLLAMA_URL', 'http://localhost:11434')
fail_on_critical = os.environ.get('REVIEW_FAIL_ON_CRITICAL', 'true').lower() == 'true'

if server_url:
    import urllib.request, urllib.parse
    params = urllib.parse.urlencode({
        'repo_url':  os.environ.get('REPO_URL', ''),
        'pr_number': os.environ.get('PR_NUMBER', ''),
    })
    with urllib.request.urlopen(server_url + '/review?' + params, timeout=300) as r:
        review = r.read().decode()
    print(review)
    should_fail, reason = check_verdict(review, fail_on_critical)
    if should_fail:
        print("\n[review] FAIL:", reason, file=sys.stderr)
        sys.exit(1)
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
    review = client.chat.completions.create(**kwargs).choices[0].message.content
else:
    import anthropic
    client = anthropic.Anthropic(api_key=os.environ['ANTHROPIC_API_KEY'])
    review = client.messages.create(
        model=model, max_tokens=4096,
        messages=[{"role": "user", "content": content}]).content[0].text

print(review)
should_fail, reason = check_verdict(review, fail_on_critical)
if should_fail:
    print("\n[review] FAIL:", reason, file=sys.stderr)
    sys.exit(1)
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
