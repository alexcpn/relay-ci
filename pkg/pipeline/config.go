package pipeline

// Config is the pipeline definition that lives in the repo as pipeline.yaml.
// It declares what tasks to run, their dependencies, and tool integrations.
type Config struct {
	// Name of the pipeline.
	Name string `yaml:"name" json:"name"`

	// Container image defaults. Tasks inherit these unless overridden.
	Defaults Defaults `yaml:"defaults" json:"defaults"`

	// Integrations configure external tools (SonarQube, linters, scanners).
	Integrations Integrations `yaml:"integrations" json:"integrations"`

	// Tasks to execute. Dependencies between tasks form the DAG.
	Tasks []TaskConfig `yaml:"tasks" json:"tasks"`

	// Triggers control when this pipeline runs.
	Triggers Triggers `yaml:"triggers" json:"triggers"`
}

// Defaults are inherited by all tasks unless overridden.
type Defaults struct {
	Image          string            `yaml:"image" json:"image"`
	Env            map[string]string `yaml:"env" json:"env"`
	CPUMillicores  uint32            `yaml:"cpu" json:"cpu"`
	MemoryMB       uint32            `yaml:"memory_mb" json:"memory_mb"`
	DiskMB         uint32            `yaml:"disk_mb" json:"disk_mb"`
	TimeoutSeconds uint32            `yaml:"timeout" json:"timeout"`
}

// TaskConfig defines a single task in the pipeline.
type TaskConfig struct {
	// ID is the unique identifier for this task within the pipeline.
	ID string `yaml:"id" json:"id"`

	// Name is a human-readable name (defaults to ID).
	Name string `yaml:"name" json:"name"`

	// Image overrides the default container image.
	Image string `yaml:"image" json:"image"`

	// Commands to run inside the container.
	Commands []string `yaml:"commands" json:"commands"`

	// DependsOn lists task IDs that must complete before this task runs.
	DependsOn []string `yaml:"depends_on" json:"depends_on"`

	// Env vars added to this task (merged with defaults).
	Env map[string]string `yaml:"env" json:"env"`

	// SecretRefs are secret names to inject as env vars at runtime.
	SecretRefs []string `yaml:"secrets" json:"secrets"`

	// Resource overrides.
	CPUMillicores  uint32 `yaml:"cpu" json:"cpu"`
	MemoryMB       uint32 `yaml:"memory_mb" json:"memory_mb"`
	DiskMB         uint32 `yaml:"disk_mb" json:"disk_mb"`
	TimeoutSeconds uint32 `yaml:"timeout" json:"timeout"`

	// Cache mounts for dependency caching.
	Cache []CacheMountConfig `yaml:"cache" json:"cache"`

	// Artifacts to collect after this task completes.
	Artifacts []ArtifactConfig `yaml:"artifacts" json:"artifacts"`

	// Condition controls whether this task runs.
	// "always", "on_success" (default), "on_failure".
	Condition string `yaml:"condition" json:"condition"`
}

// CacheMountConfig defines a cache volume to mount.
type CacheMountConfig struct {
	Key      string `yaml:"key" json:"key"`
	Path     string `yaml:"path" json:"path"`
	ReadOnly bool   `yaml:"readonly" json:"readonly"`
}

// ArtifactConfig defines an output to collect.
type ArtifactConfig struct {
	Name string `yaml:"name" json:"name"`
	Path string `yaml:"path" json:"path"`
}

// Triggers control when the pipeline runs.
type Triggers struct {
	// Branches to trigger on push (empty = all).
	Branches []string `yaml:"branches" json:"branches"`
	// Whether to trigger on pull requests.
	PullRequests bool `yaml:"pull_requests" json:"pull_requests"`
	// File path patterns — only trigger if changed files match.
	Paths []string `yaml:"paths" json:"paths"`
}

// Integrations configure external tool integrations.
type Integrations struct {
	SonarQube  *SonarQubeConfig  `yaml:"sonarqube" json:"sonarqube,omitempty"`
	Linters    *LinterConfig     `yaml:"linters" json:"linters,omitempty"`
	Security   *SecurityConfig   `yaml:"security" json:"security,omitempty"`
	CodeReview *CodeReviewConfig `yaml:"code_review" json:"code_review,omitempty"`
}

// CodeReviewConfig configures AI-powered code review.
type CodeReviewConfig struct {
	// Enabled toggles code review.
	Enabled bool `yaml:"enabled" json:"enabled"`

	// Provider selects the LLM backend: "anthropic" (default), "openai", "ollama".
	// Ignored when ServerURL is set.
	Provider string `yaml:"provider" json:"provider"`

	// APIKeySecret is the secret name holding the LLM API key.
	// Not required for Ollama (which uses no auth by default).
	APIKeySecret string `yaml:"api_key_secret" json:"api_key_secret"`

	// Model is the model name to use.
	// Defaults: anthropic→claude-sonnet-4-6, openai→gpt-4o, ollama→llama3.2
	Model string `yaml:"model" json:"model"`

	// OllamaURL is the base URL for the Ollama server (default: http://localhost:11434).
	// Only used when Provider is "ollama".
	OllamaURL string `yaml:"ollama_url" json:"ollama_url"`

	// ReviewerPrompt is the path to the reviewer prompt file relative to the
	// repo root (default: code-reviewer.md).
	ReviewerPrompt string `yaml:"reviewer_prompt" json:"reviewer_prompt"`

	// BaseBranch is the branch to diff against (default: main).
	BaseBranch string `yaml:"base_branch" json:"base_branch"`

	// ServerURL is the optional URL of an external agentic code review service
	// (e.g. the agentic_codereview HTTP API). When set, all LLM config is ignored
	// and the service is called with repo_url + pr_number instead.
	ServerURL string `yaml:"server_url" json:"server_url"`

	// FailOnCritical makes the task exit non-zero when the LLM verdict is
	// "No" or when the response contains a non-empty Critical issues section.
	// Defaults to true. Set to false for advisory-only reviews.
	FailOnCritical *bool `yaml:"fail_on_critical" json:"fail_on_critical,omitempty"`

	// Image overrides the default container image.
	Image string `yaml:"image" json:"image"`
}

// SonarQubeConfig configures SonarQube analysis.
type SonarQubeConfig struct {
	// Enabled toggles SonarQube analysis.
	Enabled bool `yaml:"enabled" json:"enabled"`

	// ServerURL is the SonarQube server URL.
	ServerURL string `yaml:"server_url" json:"server_url"`

	// ProjectKey is the SonarQube project key.
	ProjectKey string `yaml:"project_key" json:"project_key"`

	// TokenSecret is the name of the secret containing the SonarQube token.
	TokenSecret string `yaml:"token_secret" json:"token_secret"`

	// Image overrides the default sonar-scanner image.
	Image string `yaml:"image" json:"image"`

	// QualityGate controls whether to wait for the quality gate result.
	QualityGate bool `yaml:"quality_gate" json:"quality_gate"`

	// ExtraArgs are additional arguments passed to sonar-scanner.
	ExtraArgs []string `yaml:"extra_args" json:"extra_args"`

	// Sources defines the source directories to scan (default: ".").
	Sources string `yaml:"sources" json:"sources"`

	// Exclusions defines file patterns to exclude from analysis.
	Exclusions string `yaml:"exclusions" json:"exclusions"`
}

// LinterConfig configures linting tools.
type LinterConfig struct {
	// Enabled toggles linting.
	Enabled bool `yaml:"enabled" json:"enabled"`

	// Tools specifies which linters to run.
	Tools []LinterTool `yaml:"tools" json:"tools"`
}

// LinterTool defines a specific linter.
type LinterTool struct {
	// Name identifies the linter: "golangci-lint", "eslint", "ruff",
	// "pylint", "rubocop", "shellcheck", "hadolint".
	Name string `yaml:"name" json:"name"`

	// Image overrides the default image for this linter.
	Image string `yaml:"image" json:"image"`

	// Config is the path to the linter config file in the repo.
	Config string `yaml:"config" json:"config"`

	// Args are additional arguments.
	Args []string `yaml:"args" json:"args"`

	// Paths to lint (default: ".").
	Paths []string `yaml:"paths" json:"paths"`
}

// SecurityConfig configures security scanning.
type SecurityConfig struct {
	// Enabled toggles security scanning.
	Enabled bool `yaml:"enabled" json:"enabled"`

	// Tools specifies which scanners to run.
	Tools []SecurityTool `yaml:"tools" json:"tools"`
}

// SecurityTool defines a specific security scanner.
type SecurityTool struct {
	// Name identifies the scanner: "trivy", "grype", "semgrep", "gosec".
	Name string `yaml:"name" json:"name"`

	// Image overrides the default image.
	Image string `yaml:"image" json:"image"`

	// Severity threshold: "CRITICAL", "HIGH", "MEDIUM", "LOW".
	Severity string `yaml:"severity" json:"severity"`

	// FailOnFindings controls whether findings fail the build.
	FailOnFindings bool `yaml:"fail_on_findings" json:"fail_on_findings"`

	// Args are additional arguments.
	Args []string `yaml:"args" json:"args"`
}
