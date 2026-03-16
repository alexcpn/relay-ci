package pipeline

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/ci-system/ci/pkg/dag"
)

// ParseFile reads a pipeline.yaml file and returns a Config.
func ParseFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading pipeline file: %w", err)
	}
	return Parse(data)
}

// Parse parses pipeline YAML bytes into a Config.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing pipeline YAML: %w", err)
	}

	// Apply default values.
	if cfg.Defaults.CPUMillicores == 0 {
		cfg.Defaults.CPUMillicores = 1000
	}
	if cfg.Defaults.MemoryMB == 0 {
		cfg.Defaults.MemoryMB = 512
	}
	if cfg.Defaults.DiskMB == 0 {
		cfg.Defaults.DiskMB = 2000
	}
	if cfg.Defaults.TimeoutSeconds == 0 {
		cfg.Defaults.TimeoutSeconds = 600 // 10 min
	}

	return &cfg, nil
}

// BuildGraph converts a pipeline Config into a DAG ready for execution.
// It also generates tasks for configured integrations (linters, SonarQube,
// security scanners) and wires them into the graph.
func BuildGraph(cfg *Config) (*dag.Graph, error) {
	g := dag.New()

	// Add user-defined tasks.
	for _, tc := range cfg.Tasks {
		task := configToTask(tc, cfg.Defaults)
		if err := g.AddTask(task); err != nil {
			return nil, fmt.Errorf("adding task %q: %w", tc.ID, err)
		}
	}

	// Add user-defined dependencies.
	for _, tc := range cfg.Tasks {
		for _, depID := range tc.DependsOn {
			if err := g.AddEdge(depID, tc.ID); err != nil {
				return nil, fmt.Errorf("adding edge %s → %s: %w", depID, tc.ID, err)
			}
		}
	}

	// Generate integration tasks.
	if err := addLinterTasks(g, cfg); err != nil {
		return nil, err
	}
	if err := addSonarTask(g, cfg); err != nil {
		return nil, err
	}
	if err := addSecurityTasks(g, cfg); err != nil {
		return nil, err
	}

	if err := g.Validate(); err != nil {
		return nil, fmt.Errorf("pipeline validation: %w", err)
	}

	return g, nil
}

// configToTask converts a TaskConfig to a dag.Task, applying defaults.
func configToTask(tc TaskConfig, defaults Defaults) *dag.Task {
	name := tc.Name
	if name == "" {
		name = tc.ID
	}

	image := tc.Image
	if image == "" {
		image = defaults.Image
	}

	cpu := tc.CPUMillicores
	if cpu == 0 {
		cpu = defaults.CPUMillicores
	}
	mem := tc.MemoryMB
	if mem == 0 {
		mem = defaults.MemoryMB
	}
	disk := tc.DiskMB
	if disk == 0 {
		disk = defaults.DiskMB
	}
	timeout := tc.TimeoutSeconds
	if timeout == 0 {
		timeout = defaults.TimeoutSeconds
	}

	// Merge env vars (task overrides defaults).
	env := make(map[string]string)
	for k, v := range defaults.Env {
		env[k] = v
	}
	for k, v := range tc.Env {
		env[k] = v
	}

	cacheMounts := make([]dag.CacheMount, len(tc.Cache))
	for i, c := range tc.Cache {
		cacheMounts[i] = dag.CacheMount{
			CacheKey:  c.Key,
			MountPath: c.Path,
			ReadOnly:  c.ReadOnly,
		}
	}

	return &dag.Task{
		ID:             tc.ID,
		Name:           name,
		ContainerImage: image,
		Commands:       tc.Commands,
		Env:            env,
		CPUMillicores:  cpu,
		MemoryMB:       mem,
		DiskMB:         disk,
		TimeoutSeconds: timeout,
		CacheMounts:    cacheMounts,
		SecretRefs:     tc.SecretRefs,
	}
}

// addLinterTasks generates tasks for each configured linter.
// Linters run in parallel with no dependencies by default (independent).
func addLinterTasks(g *dag.Graph, cfg *Config) error {
	if cfg.Integrations.Linters == nil || !cfg.Integrations.Linters.Enabled {
		return nil
	}

	for _, tool := range cfg.Integrations.Linters.Tools {
		taskID := "lint-" + tool.Name
		image := imageForTool(tool.Name, tool.Image)
		if image == "" {
			return fmt.Errorf("unknown linter %q and no image specified", tool.Name)
		}

		// Linters (especially golangci-lint) are memory-hungry; use at least
		// 2GB regardless of the pipeline default.
		lintMemory := cfg.Defaults.MemoryMB
		if lintMemory < 2048 {
			lintMemory = 2048
		}

		task := &dag.Task{
			ID:             taskID,
			Name:           "lint: " + tool.Name,
			ContainerImage: image,
			Commands:       commandsForLinter(tool),
			CPUMillicores:  cfg.Defaults.CPUMillicores,
			MemoryMB:       lintMemory,
			DiskMB:         cfg.Defaults.DiskMB,
			TimeoutSeconds: 600, // 10 min for linters on large repos
			CacheMounts:    defaultCachesForLinter(tool.Name),
		}

		if err := g.AddTask(task); err != nil {
			return fmt.Errorf("adding linter task %q: %w", taskID, err)
		}

		// If there's a "clone" task, linters depend on it.
		if _, exists := g.GetTask("clone"); exists {
			g.AddEdge("clone", taskID)
		}
	}

	return nil
}

// addSonarTask generates a SonarQube analysis task.
// It depends on compile/test tasks if they exist (needs compiled code).
func addSonarTask(g *dag.Graph, cfg *Config) error {
	sq := cfg.Integrations.SonarQube
	if sq == nil || !sq.Enabled {
		return nil
	}

	if sq.ServerURL == "" {
		return fmt.Errorf("sonarqube: server_url is required")
	}
	if sq.ProjectKey == "" {
		return fmt.Errorf("sonarqube: project_key is required")
	}

	image := imageForTool("sonarqube", sq.Image)

	secretRefs := []string{}
	if sq.TokenSecret != "" {
		secretRefs = append(secretRefs, sq.TokenSecret)
	}

	task := &dag.Task{
		ID:             "sonarqube",
		Name:           "sonarqube analysis",
		ContainerImage: image,
		Commands:       commandsForSonar(sq),
		CPUMillicores:  2000,
		MemoryMB:       2048, // SonarQube scanner needs memory
		DiskMB:         cfg.Defaults.DiskMB,
		TimeoutSeconds: 600,
		SecretRefs:     secretRefs,
		Env:            map[string]string{"SONAR_TOKEN": "$SONAR_TOKEN"},
	}

	if err := g.AddTask(task); err != nil {
		return fmt.Errorf("adding sonarqube task: %w", err)
	}

	// SonarQube depends on compile and test tasks if they exist,
	// because it needs compiled bytecode and test coverage reports.
	for _, depID := range []string{"compile", "test", "build", "clone"} {
		if _, exists := g.GetTask(depID); exists {
			g.AddEdge(depID, "sonarqube")
		}
	}

	return nil
}

// addSecurityTasks generates tasks for each configured security scanner.
func addSecurityTasks(g *dag.Graph, cfg *Config) error {
	if cfg.Integrations.Security == nil || !cfg.Integrations.Security.Enabled {
		return nil
	}

	for _, tool := range cfg.Integrations.Security.Tools {
		taskID := "security-" + tool.Name
		image := imageForTool(tool.Name, tool.Image)
		if image == "" {
			return fmt.Errorf("unknown security scanner %q and no image specified", tool.Name)
		}

		task := &dag.Task{
			ID:             taskID,
			Name:           "security: " + tool.Name,
			ContainerImage: image,
			Commands:       commandsForScanner(tool),
			CPUMillicores:  cfg.Defaults.CPUMillicores,
			MemoryMB:       1024, // scanners need more memory
			DiskMB:         cfg.Defaults.DiskMB,
			TimeoutSeconds: 600,
			CacheMounts:    defaultCachesForScanner(tool.Name),
		}

		if err := g.AddTask(task); err != nil {
			return fmt.Errorf("adding security task %q: %w", taskID, err)
		}

		// Depend on clone if it exists.
		if _, exists := g.GetTask("clone"); exists {
			g.AddEdge("clone", taskID)
		}
	}

	return nil
}
