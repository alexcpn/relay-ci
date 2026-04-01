# Relay CI — Backlog

## Priority 1: Security Baseline

- [x] **TLS/mTLS between master and workers** — `pkg/tlsutil`, env-driven config (PR #2)
- [x] **Authentication on gRPC APIs** — `pkg/auth`, token-based via `API_TOKEN` env var (PR #3)
- [ ] **RBAC / multi-tenancy** — no user or org isolation; all builds share the same namespace
- [ ] **Vault integration** — secrets stored only in memory / flat `.secrets.env` file; no rotation, no encryption at rest
- [ ] **Audit logging** — secret access, build submissions, cancellations, and admin actions are untracked
- [ ] **Container sandboxing** — Docker runs with default privileges; consider Firecracker or gVisor for untrusted workloads
- [ ] **Rate limiting on webhook endpoint** — no protection against webhook floods or abuse

## Priority 2: Persistence

- [ ] **PostgreSQL for build history** — all builds lost on master restart (in-memory only)
- [x] **Durable log storage (disk)** — `FileBackend` in `pkg/logstore`, JSONL files, auto-reload (PR #5)
- [ ] **Artifact storage (MinIO / S3)** — `artifacts` config is parsed but never collected or uploaded
- [ ] **Workspace volume cleanup** — Docker volumes (`ci-workspace-{buildID}`) are never cleaned up after build completion
- [ ] **Cache volume TTL / eviction** — cache volumes grow unbounded; need expiration policy

## Priority 3: Observability

- [x] **Prometheus metrics** — `pkg/observability`, `/metrics` endpoint on master (PR #4)
- [ ] **Distributed tracing (OpenTelemetry)** — trace a build from webhook → scheduler → worker → container
- [ ] **Alerting integration** — Slack, PagerDuty, email notifications on build failure / worker death
- [ ] **Grafana dashboards** — pre-built dashboards for builds, workers, and queue health

## Priority 4: Scaling & Reliability

- [ ] **Eliminate single master SPOF** — leader election or active-passive replication
- [ ] **NATS / Redis task queue** — replace in-memory scheduling with durable message queue
- [ ] **Auto-reassignment on worker death** — currently requires manual `retry_build`
- [ ] **Improved scheduling algorithm** — current is first-fit bin-packing; add least-loaded, affinity, and label-based scheduling
- [ ] **Worker label selectors** — labels are parsed but not used in scheduling decisions
- [ ] **Horizontal worker auto-scaling** — scale workers up/down based on queue depth

## Priority 5: Pipeline Features

- [ ] **Matrix builds** — e.g., `matrix: {go: [1.21, 1.22], os: [linux, macos]}`
- [ ] **Conditional step enforcement** — `condition: always | on_success | on_failure` is parsed but not enforced at runtime
- [ ] **Artifact upload/download** — collect artifacts from containers, store, and make downloadable
- [ ] **Template expansion** — `{{ checksum "go.sum" }}` in cache keys is parsed but not expanded
- [ ] **Manual approval gates** — pause pipeline and wait for human/agent approval before continuing
- [ ] **Build timeout at pipeline level** — only per-task timeouts exist today
- [ ] **Re-run individual tasks** — currently only full build retry is supported

## Priority 6: Testing

- [ ] **Load / stress testing** — behavior under 100+ concurrent builds is unknown
- [ ] **Chaos testing** — worker crashes, network partitions, master failover
- [ ] **Benchmark suite** — scheduling throughput, log streaming performance, gRPC latency
- [ ] **Security scanning of CI system itself** — run trivy/gosec on Relay CI's own codebase in CI

## Priority 7: Operations & Deployment

- [ ] **Docker Compose for local dev** — single `docker compose up` for master + worker + deps
- [ ] **Helm chart for Kubernetes** — production deployment with configurable replicas, resource limits
- [ ] **Graceful rolling upgrades** — drain workers before upgrade, migrate state
- [ ] **Backup / restore** — once persistence is added, support state export and import
- [ ] **Configuration validation CLI** — `ci validate pipeline.yaml` to catch errors before push

## Priority 8: Web UI / Dashboard

- [ ] **Build list view** — table of recent builds with status, repo, branch, duration
- [ ] **Build detail view** — DAG visualization with live task status updates
- [ ] **Log viewer** — real-time streaming log viewer per task (current `/logs` endpoint is bare)
- [ ] **Worker status page** — capacity, running tasks, health
- [ ] **Secret management UI** — add/remove/rotate secrets via browser
- [ ] **Pipeline editor** — YAML editor with schema validation and preview

## Completed

- [x] DAG-based execution with topological sort, cycle detection, skip cascade
- [x] Multi-worker support with heartbeat health checks
- [x] GitHub & GitLab webhook integration with HMAC verification
- [x] Per-task and per-build SCM status reporting
- [x] PR commenting (code review results)
- [x] 10 MCP tools for AI agents (stdio + HTTP transport)
- [x] Secret management with scoped storage and log scrubbing
- [x] Docker containerization with shell fallback
- [x] Real-time log streaming with pagination
- [x] Built-in linter integration (golangci-lint, eslint, ruff, pylint, rubocop, shellcheck, hadolint)
- [x] Built-in security scanner integration (trivy, grype, semgrep, gosec)
- [x] SonarQube integration
- [x] AI code review integration (Anthropic, OpenAI, Ollama, agentic service)
- [x] CLI client (submit, status, list, logs, watch, cancel, secret)
- [x] Cache mounts via Docker named volumes
- [x] Worker draining via heartbeat commands
- [x] 89 unit + integration + e2e tests
- [x] Structured logging (slog)
