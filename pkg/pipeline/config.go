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
	SonarQube *SonarQubeConfig `yaml:"sonarqube" json:"sonarqube,omitempty"`
	Linters   *LinterConfig    `yaml:"linters" json:"linters,omitempty"`
	Security  *SecurityConfig  `yaml:"security" json:"security,omitempty"`
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
