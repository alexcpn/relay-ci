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
- [x] **Workspace volume cleanup** — master calls `CleanupBuild` RPC on workers after build completion (PR #7)
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
- [x] **Conditional step enforcement** — `condition: always | on_success | on_failure` enforced at runtime (PR #6)
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

## Priority 8: Web UI / Dashboard (Angular SPA)

### Foundation
- [ ] **Angular project scaffold** — Angular 17+ standalone components, signals, SSR off; `web/` workspace with `ng build` wired into `Makefile`
- [ ] **HTTP/JSON gateway on master** — `grpc-gateway` v2 mounted on the existing HTTP mux (`cmd/master/main.go:125`) at `/api/v1/...`; add `google.api.http` annotations to `SchedulerService`, `SecretsService`, `LogService`, and a new `WorkerRegistry` read API; server-streaming RPCs (`WatchBuild`, `StreamLogs`) exposed as chunked JSON / SSE. Chosen over gRPC-Web because the master already serves HTTP, no bidi streams are needed from the browser, and JSON is debuggable in DevTools without an Envoy hop.
- [ ] **Auth flow** — login screen exchanges credentials for the existing `API_TOKEN`, stored in `HttpOnly` cookie or memory; `AuthInterceptor` attaches it; 401 redirects to login
- [ ] **App shell & routing** — top nav (Builds / Workers / Secrets / Pipelines), lazy-loaded feature modules, 404 + error boundary
- [ ] **Theme & design system** — Angular Material or Tailwind + CDK; dark mode; consistent status color tokens (queued/running/success/failed/cancelled)
- [ ] **Real-time channel** — SSE or WebSocket client service for live build/log/worker updates; auto-reconnect with backoff

### Build views
- [ ] **Build list view** — paginated/filterable table (status, repo, branch, commit, duration, started-by); column sort; saved filters
- [ ] **Build detail page** — header with metadata + actions (cancel, retry, re-run task); tab layout (DAG, Tasks, Logs, Artifacts, Timeline)
- [ ] **DAG visualization** — interactive graph (dagre-d3 or cytoscape) with live task status colors; click node → jump to that task's logs
- [ ] **Task timeline / Gantt** — per-task start/end bars on a shared timeline to spot critical path and parallelism gaps
- [ ] **Submit-build dialog** — form to trigger a manual build (repo, branch, commit, pipeline override, env vars)

### Logs
- [ ] **Streaming log viewer** — virtualized renderer for large logs, ANSI color, auto-scroll lock, follow-tail toggle, jump-to-error
- [ ] **Log search & filter** — in-log substring/regex search, severity filter, permalink to a line, "copy as text" + download
- [ ] **Multi-task log split view** — view two tasks side-by-side for comparing parallel branches

### Workers
- [ ] **Worker status page** — list of workers with capacity, running tasks, last heartbeat, labels, drain state
- [ ] **Worker detail drawer** — live resource usage, current task assignments, drain/undrain action

### Secrets
- [ ] **Secret management UI** — list/add/remove/rotate scoped secrets; reveal-on-click behind confirmation; never display in plaintext by default
- [ ] **Secret usage audit panel** — show which builds/pipelines referenced a secret (depends on P1 audit logging)

### Pipelines
- [ ] **Pipeline editor** — Monaco-based YAML editor with schema validation, autocomplete from pipeline schema, and live DAG preview
- [ ] **Pipeline diff view** — visual diff between previous and proposed pipeline YAML before commit

### Cross-cutting
- [ ] **Notifications/toasts** — surface build-failed, worker-died, and auth-expired events to the active user
- [ ] **Frontend test suite** — Karma/Jest unit tests for services and components; Playwright e2e against a real master+worker
- [ ] **Build & deploy pipeline** — `web/` artifact served by master (embedded via `embed.FS`) or by a sidecar Nginx; cache-busting hashed filenames

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
