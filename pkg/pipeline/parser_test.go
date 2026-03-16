package pipeline

import (
	"strings"
	"testing"
)

func TestParseMinimalPipeline(t *testing.T) {
	yaml := []byte(`
name: my-service
defaults:
  image: golang:1.24
tasks:
  - id: build
    commands: ["go", "build", "./..."]
  - id: test
    commands: ["go", "test", "./..."]
    depends_on: [build]
`)

	cfg, err := Parse(yaml)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Name != "my-service" {
		t.Errorf("expected my-service, got %s", cfg.Name)
	}
	if len(cfg.Tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(cfg.Tasks))
	}
	if cfg.Tasks[1].DependsOn[0] != "build" {
		t.Errorf("expected test depends on build")
	}

	// Check defaults were applied.
	if cfg.Defaults.CPUMillicores != 1000 {
		t.Errorf("expected default cpu 1000, got %d", cfg.Defaults.CPUMillicores)
	}
}

func TestBuildGraphFromConfig(t *testing.T) {
	yaml := []byte(`
name: full-pipeline
defaults:
  image: golang:1.24
tasks:
  - id: clone
    image: alpine/git:latest
    commands: ["git", "clone"]
  - id: compile
    commands: ["go", "build", "./..."]
    depends_on: [clone]
  - id: test
    commands: ["go", "test", "./..."]
    depends_on: [clone]
  - id: deploy
    commands: ["./deploy.sh"]
    depends_on: [compile, test]
`)

	cfg, err := Parse(yaml)
	if err != nil {
		t.Fatal(err)
	}

	g, err := BuildGraph(cfg)
	if err != nil {
		t.Fatal(err)
	}

	if g.Size() != 4 {
		t.Fatalf("expected 4 tasks, got %d", g.Size())
	}

	// Compile and test should depend on clone.
	deps := g.Dependencies("compile")
	if len(deps) != 1 || deps[0] != "clone" {
		t.Errorf("compile should depend on clone, got %v", deps)
	}

	// Deploy depends on compile and test.
	deps = g.Dependencies("deploy")
	if len(deps) != 2 {
		t.Errorf("deploy should have 2 deps, got %d", len(deps))
	}
}

func TestPipelineWithLinters(t *testing.T) {
	yaml := []byte(`
name: go-service
defaults:
  image: golang:1.24
tasks:
  - id: clone
    image: alpine/git:latest
    commands: ["git", "clone"]
  - id: build
    commands: ["go", "build", "./..."]
    depends_on: [clone]
integrations:
  linters:
    enabled: true
    tools:
      - name: golangci-lint
        config: .golangci.yml
      - name: hadolint
`)

	cfg, err := Parse(yaml)
	if err != nil {
		t.Fatal(err)
	}

	g, err := BuildGraph(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// 2 user tasks + 2 linter tasks = 4.
	if g.Size() != 4 {
		t.Fatalf("expected 4 tasks, got %d", g.Size())
	}

	// Linters should depend on clone.
	lintGo, ok := g.GetTask("lint-golangci-lint")
	if !ok {
		t.Fatal("lint-golangci-lint task not found")
	}
	if lintGo.ContainerImage != "golangci/golangci-lint:v1.62" {
		t.Errorf("unexpected image: %s", lintGo.ContainerImage)
	}

	deps := g.Dependencies("lint-golangci-lint")
	if len(deps) != 1 || deps[0] != "clone" {
		t.Errorf("linter should depend on clone, got %v", deps)
	}

	// Hadolint should also be present.
	_, ok = g.GetTask("lint-hadolint")
	if !ok {
		t.Fatal("lint-hadolint task not found")
	}
}

func TestPipelineWithSonarQube(t *testing.T) {
	yaml := []byte(`
name: java-service
defaults:
  image: maven:3.9-eclipse-temurin-21
tasks:
  - id: clone
    image: alpine/git:latest
    commands: ["git", "clone"]
  - id: compile
    commands: ["mvn", "compile"]
    depends_on: [clone]
  - id: test
    commands: ["mvn", "test"]
    depends_on: [compile]
integrations:
  sonarqube:
    enabled: true
    server_url: https://sonar.example.com
    project_key: my-java-service
    token_secret: SONAR_TOKEN
    quality_gate: true
    sources: src/main/java
    exclusions: "**/*Test.java"
`)

	cfg, err := Parse(yaml)
	if err != nil {
		t.Fatal(err)
	}

	g, err := BuildGraph(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// 3 user tasks + 1 sonarqube = 4.
	if g.Size() != 4 {
		t.Fatalf("expected 4 tasks, got %d", g.Size())
	}

	sonar, ok := g.GetTask("sonarqube")
	if !ok {
		t.Fatal("sonarqube task not found")
	}

	if sonar.ContainerImage != "sonarsource/sonar-scanner-cli:latest" {
		t.Errorf("unexpected image: %s", sonar.ContainerImage)
	}

	// SonarQube should depend on compile and test (needs coverage data).
	deps := g.Dependencies("sonarqube")
	if len(deps) < 2 {
		t.Errorf("sonarqube should depend on compile and test, got %v", deps)
	}

	// Should have the token secret.
	if len(sonar.SecretRefs) != 1 || sonar.SecretRefs[0] != "SONAR_TOKEN" {
		t.Errorf("expected SONAR_TOKEN secret ref, got %v", sonar.SecretRefs)
	}

	// Check command includes quality gate.
	hasQG := false
	for _, cmd := range sonar.Commands {
		if strings.Contains(cmd, "-Dsonar.qualitygate.wait=true") {
			hasQG = true
		}
	}
	if !hasQG {
		t.Error("expected quality gate flag in sonar command")
	}
}

func TestPipelineWithSecurityScanners(t *testing.T) {
	yaml := []byte(`
name: secure-service
defaults:
  image: golang:1.24
tasks:
  - id: clone
    image: alpine/git:latest
    commands: ["git", "clone"]
integrations:
  security:
    enabled: true
    tools:
      - name: trivy
        severity: CRITICAL
        fail_on_findings: true
      - name: gosec
`)

	cfg, err := Parse(yaml)
	if err != nil {
		t.Fatal(err)
	}

	g, err := BuildGraph(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// 1 user task + 2 security = 3.
	if g.Size() != 3 {
		t.Fatalf("expected 3 tasks, got %d", g.Size())
	}

	trivy, ok := g.GetTask("security-trivy")
	if !ok {
		t.Fatal("security-trivy task not found")
	}
	if trivy.ContainerImage != "aquasec/trivy:latest" {
		t.Errorf("unexpected image: %s", trivy.ContainerImage)
	}

	// Trivy should have --exit-code=1 for fail_on_findings.
	hasExitCode := false
	for _, cmd := range trivy.Commands {
		if strings.Contains(cmd, "--exit-code=1") {
			hasExitCode = true
		}
	}
	if !hasExitCode {
		t.Error("expected --exit-code=1 for fail_on_findings")
	}
}

func TestFullPipelineWithAllIntegrations(t *testing.T) {
	yaml := []byte(`
name: full-service
defaults:
  image: golang:1.24
  env:
    CI: "true"
    GOFLAGS: "-count=1"
tasks:
  - id: clone
    image: alpine/git:latest
    commands: ["git", "clone"]
  - id: compile
    commands: ["go", "build", "./..."]
    depends_on: [clone]
    cache:
      - key: go-mod
        path: /go/pkg/mod
  - id: test
    commands: ["go", "test", "-cover", "./..."]
    depends_on: [clone]
    cache:
      - key: go-mod
        path: /go/pkg/mod
        readonly: true
  - id: docker
    image: docker:buildkit
    commands: ["docker", "build", "-t", "myapp", "."]
    depends_on: [compile, test]
    secrets: [DOCKER_PASSWORD]
  - id: push
    image: docker:latest
    commands: ["docker", "push", "myapp"]
    depends_on: [docker]
    secrets: [DOCKER_PASSWORD]
integrations:
  linters:
    enabled: true
    tools:
      - name: golangci-lint
        config: .golangci.yml
  sonarqube:
    enabled: true
    server_url: https://sonar.example.com
    project_key: full-service
    token_secret: SONAR_TOKEN
    quality_gate: true
  security:
    enabled: true
    tools:
      - name: trivy
        fail_on_findings: true
triggers:
  branches: [main, develop]
  pull_requests: true
`)

	cfg, err := Parse(yaml)
	if err != nil {
		t.Fatal(err)
	}

	g, err := BuildGraph(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// 5 user + 1 linter + 1 sonar + 1 security = 8 tasks.
	if g.Size() != 8 {
		t.Fatalf("expected 8 tasks, got %d", g.Size())
	}

	// Verify the complete DAG structure.
	// clone → compile → docker → push
	//       → test ──────┘
	//       → lint-golangci-lint (independent after clone)
	//       → security-trivy (independent after clone)
	// compile,test → sonarqube

	// Check env inheritance.
	compile, _ := g.GetTask("compile")
	if compile.Env["CI"] != "true" {
		t.Error("compile should inherit CI env from defaults")
	}
	if compile.Env["GOFLAGS"] != "-count=1" {
		t.Error("compile should inherit GOFLAGS from defaults")
	}

	// Check cache mounts.
	if len(compile.CacheMounts) != 1 {
		t.Fatal("compile should have 1 cache mount")
	}
	if compile.CacheMounts[0].MountPath != "/go/pkg/mod" {
		t.Error("cache mount path should be /go/pkg/mod")
	}

	// Docker task should have secret refs.
	docker, _ := g.GetTask("docker")
	if len(docker.SecretRefs) != 1 || docker.SecretRefs[0] != "DOCKER_PASSWORD" {
		t.Errorf("docker should have DOCKER_PASSWORD secret, got %v", docker.SecretRefs)
	}

	// Check triggers.
	if !cfg.Triggers.PullRequests {
		t.Error("pull_requests should be true")
	}
	if len(cfg.Triggers.Branches) != 2 {
		t.Error("expected 2 trigger branches")
	}

	// Verify the graph is valid (no cycles).
	tasks := g.Tasks()
	if len(tasks) != 8 {
		t.Errorf("expected 8 tasks in topo order, got %d", len(tasks))
	}
}

func TestSonarQubeRequiresURL(t *testing.T) {
	yaml := []byte(`
name: bad
tasks: []
integrations:
  sonarqube:
    enabled: true
    project_key: test
`)

	cfg, err := Parse(yaml)
	if err != nil {
		t.Fatal(err)
	}

	_, err = BuildGraph(cfg)
	if err == nil {
		t.Fatal("expected error for missing server_url")
	}
}

func TestCycleDetection(t *testing.T) {
	yaml := []byte(`
name: cycle
defaults:
  image: alpine:latest
tasks:
  - id: a
    commands: ["echo"]
    depends_on: [c]
  - id: b
    commands: ["echo"]
    depends_on: [a]
  - id: c
    commands: ["echo"]
    depends_on: [b]
`)

	cfg, err := Parse(yaml)
	if err != nil {
		t.Fatal(err)
	}

	_, err = BuildGraph(cfg)
	if err == nil {
		t.Fatal("expected cycle detection error")
	}
}

func TestCustomLinterImage(t *testing.T) {
	yaml := []byte(`
name: custom
defaults:
  image: node:22
tasks: []
integrations:
  linters:
    enabled: true
    tools:
      - name: eslint
        image: my-registry/custom-eslint:v2
        args: ["npx", "eslint", "--fix", "src/"]
`)

	cfg, err := Parse(yaml)
	if err != nil {
		t.Fatal(err)
	}

	g, err := BuildGraph(cfg)
	if err != nil {
		t.Fatal(err)
	}

	lint, ok := g.GetTask("lint-eslint")
	if !ok {
		t.Fatal("lint-eslint not found")
	}
	if lint.ContainerImage != "my-registry/custom-eslint:v2" {
		t.Errorf("expected custom image, got %s", lint.ContainerImage)
	}
	if lint.Commands[3] != "src/" {
		t.Errorf("expected custom args, got %v", lint.Commands)
	}
}
