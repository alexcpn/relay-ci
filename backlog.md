# Relay CI — Backlog

## What this is (and isn't)

Relay CI is an **agent-friendly CI system**: pipelines are YAML an agent can read,
diff, validate, and rewrite mechanically; every state change is observable
through MCP tools; the master/worker stack is small enough to run on a laptop or
a single VM. The bet is that *the consumer of CI is increasingly an agent, not a
human*, and the data model and call surface should be built for that cadence
from the inside — not bolted on.

**Explicit non-goal: Jenkins feature parity.** Plugin ecosystems, multi-platform
agents, Groovy DSLs, Blue Ocean UIs — out of scope. See *Out of scope* at the
bottom for the things we are choosing not to build.

The backlog is reordered around "what makes this usable in production for a
small team or as an agent-operated CI substrate," not "what features does
Jenkins have."

---

## P0 — Production blockers

You cannot responsibly run Relay CI for anything more than a personal demo
without these.

- [ ] **Durable build state** — Postgres (or SQLite for single-master) backing for `pkg/scheduler`. Today a master restart wipes all build history. Keep the in-memory map as a hot cache; DB is source of truth.
- [ ] **Multi-user auth** — replace the single shared `API_TOKEN` with per-user tokens or OIDC. "User X submitted this build" isn't a real claim today.
- [ ] **REST API auth** — `/api/v1/*` is unauthenticated. The SPA, MCP, and any external caller need a real check before this gets exposed.
- [ ] **Audit log** — secret access, build submissions/cancellations, pipeline pins, admin actions. Required for compliance and post-incident review.
- [ ] **Build retention / log rotation** — "keep last N builds" or "keep last D days." Builds and logs grow without bound today.
- [ ] **Webhook rate limiting** — protect `/webhooks` from flood / abuse.
- [ ] **Graceful upgrade** — drain workers, finish in-flight builds, swap master binary without losing state.
- [ ] **Backup / restore** — once persistence is in, export/import the DB + log dir + secrets metadata.

## P1 — Reliability

Without these, paged-at-3am incidents are routine.

- [ ] **Auto-reassign tasks on worker death** — `retry_build` is manual today. Workers die for normal reasons; reassignment must be automatic.
- [ ] **Pipeline-level build timeout** — only per-task timeouts exist; a runaway pipeline holds slots indefinitely.
- [ ] **Worker label selectors used in scheduling** — labels are parsed but ignored. Required to route GPU/heavy/special-image tasks correctly.
- [ ] **Container sandboxing (gVisor or Firecracker)** — agents will submit pipelines that run arbitrary code. Default Docker isolation isn't enough for untrusted workloads. This is on the critical path before any external agent gets submit privileges.
- [ ] **Vault / external secrets backend** — `.secrets.env` + `.env` is dev-grade. Required for rotation, encryption-at-rest, and scoped delegation.
- [ ] **Master HA (leader election)** — single-master SPOF. Lower priority than the above because "restart in 10s" is acceptable at this scale; revisit when uptime targets tighten.

## P2 — Agent loop (the differentiator)

This is the work that earns Relay CI the right to exist as something distinct
from "Jenkins with an MCP wrapper." Highest-leverage product surface.

- [ ] **Structured event logs, not text streams** — emit `{tool, kind, file, line, msg, severity}` events from `go test`, ESLint, golangci-lint, pytest, etc. Add MCP tool `find_events(task_id, kind=...)`. Today agents regex free-text. ~1 week per tool, pays off forever.
- [ ] **Per-task re-run with upstream reuse** — `retry_task(build_id, task_id, patch=...)` keeps cached outputs of unchanged upstream tasks. Compresses agent iteration from minutes to seconds. Probably the single highest-leverage item on the list.
- [ ] **`diff_builds(red_build, last_green_build)` MCP tool** — return code diff, pipeline diff, env diff, dep diff. Lets the agent focus immediately on the suspect surface instead of cold-reading failures.
- [ ] **`validate_pipeline(yaml_text)` MCP tool** — structured field-level errors. Agent iterates on `pipeline.yaml` locally without burning build slots. Same role `tsc --noEmit` plays for TypeScript.
- [ ] **JUnit-format test result ingestion + per-test history** — answers "new failure or known flake?" instantly. Without this the agent spends tokens "fixing" flakes that flip green on retry. JUnit XML is the cross-language standard; not Java-only.
- [ ] **Per-build cost/budget feedback** — wall time, container minutes, AI tokens spent in AI-tasks, retry count. The agent needs an economic stop signal; otherwise the loop is an infinite money pit.
- [ ] **Sandbox / experimental build stream** — agent-experimental builds tracked separately from main history, optionally auto-pruned on success. Stops 47 failed agent attempts from drowning the build list (and the agent's own reasoning over history).
- [ ] **First-class agent identity in the data model** — distinguish "agent-X submitted" from "alice submitted" at the audit/scheduling layer, not just a string in `triggered_by`. Needed for budget enforcement, rate limiting, and audit.

## P3 — Operability

- [ ] **Docker Compose for local dev** — `docker compose up` brings up master + worker + MCP + UI.
- [ ] **Helm chart** — K8s deployment with configurable replicas and resource limits.
- [ ] **Alerting** — webhook-out on master-down, worker-fleet-down, build-failure-rate-spike. Let users wire their own Slack/PagerDuty forwarder; no native integration.
- [ ] **Grafana dashboards** — pre-built panels on the existing Prometheus metrics (builds, queue, workers).
- [ ] **Pipeline validation CLI** — `ci validate pipeline.yaml` (shares logic with the P2 MCP tool).
- [ ] **Self-security-scanning** — enable Relay CI's own pipeline on this repo (trivy/gosec on the codebase, plus the new web pipeline).

## P4 — UI

Scoped to "operate and observe." Not a Blue Ocean clone.

- [ ] **Auth on the UI** — wire to the P0 multi-user auth.
- [ ] **Real-time updates** — SSE on the existing `WatchBuild` and `StreamLogs` server-streams, replacing polling.
- [ ] **Streaming log viewer** — virtualized renderer, ANSI color, follow-tail, in-log search/regex, permalink to a line.
- [ ] **DAG visualization** — interactive graph (dagre/cytoscape) on the build detail page with live task status.
- [ ] **Worker status page** — capacity, running tasks, drain state, labels.
- [ ] **Secret management UI** — list/add/remove/rotate scoped secrets behind reveal-on-confirm. Depends on P0 audit log.

## P5 — Convenience / future

Useful but not on the critical path; pull up as user feedback demands.

- [ ] **Artifact upload/download** — implement the parsed-but-unused `artifacts:` block. Pick one S3-compatible backend; resist abstracting.
- [ ] **Cache volume TTL / eviction** — caches grow unbounded today.
- [ ] **Template expansion** — `{{ checksum "go.sum" }}` is parsed but not expanded.
- [ ] **Manual approval gates** — pause pipeline and wait for human/agent approval (useful for deploy stages).
- [ ] **Re-run individual tasks (UI/CLI surface)** — user-facing wrapper over the P2 `retry_task`.
- [ ] **Matrix builds** — `matrix: {go: [1.21, 1.22]}`. Defer until requested.
- [ ] **Improved scheduling** — least-loaded / affinity / label-based. Current first-fit is fine for the common case.
- [ ] **Horizontal worker autoscaling** — scale on queue depth. Defer until single workers are saturated.
- [ ] **NATS / Redis task queue** — durable queue replacing in-memory scheduling. Defer until P0 Postgres persistence proves insufficient.
- [ ] **gRPC-gateway / proper REST layer** — replace the hand-rolled `/api/v1/*` shim in `cmd/master/api.go` with generated handlers from `google.api.http` annotations once the API stabilizes.

---

## Out of scope (intentional non-goals)

These are listed so we stop accidentally drifting into Jenkins-parity work.

- **Plugin ecosystem.** Every integration is Go code in `pkg/pipeline`. No classloader, no marketplace, no update center.
- **Shared pipeline libraries / Groovy-equivalent templating.** Pipelines are YAML so agents can read and rewrite them mechanically; templating layered on top compromises that property.
- **Multi-platform agents (Windows / macOS / iOS).** Linux-via-Docker only. iOS/codesign workflows belong on Jenkins or a hosted runner.
- **Blue Ocean / Jenkins-class UI.** UI is for operability, not IDE replacement.
- **Native notifications zoo (Slack, Teams, email-ext, Jira).** Emit a webhook on build events; users forward.
- **Multibranch with auto-discovery / branch indexing.** Webhooks are explicit.
- **Parameterized builds with rich input types.** Env vars are enough; rich `parameters {}` UIs aren't worth the surface area.
- **Generalized test/coverage publisher framework.** JUnit ingestion in P2 covers the actual need; we won't build a plugin chain for it.
- **Feature parity with Jenkins, as a goal in itself.** If a feature shows up in this backlog because "Jenkins has it," delete it. The bar is "does it make this usable in production for a small team, or does it make the agent loop tighter."

---

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
- [x] SonarQube integration (scaffold; gate config lives on the SonarQube server)
- [x] AI code review integration (Anthropic, OpenAI, Ollama, agentic service)
- [x] CLI client (submit, status, list, logs, watch, cancel, secret)
- [x] Cache mounts via Docker named volumes
- [x] Worker draining via heartbeat commands
- [x] Structured logging (slog)
- [x] TLS/mTLS between master and workers (PR #2)
- [x] Token-based auth on gRPC APIs (PR #3)
- [x] Prometheus metrics (PR #4)
- [x] Durable log storage — FileBackend (PR #5)
- [x] Conditional step enforcement — `on_success` / `on_failure` / `always` (PR #6)
- [x] Workspace volume cleanup (PR #7)
- [x] Basic Angular web UI — builds list + build detail with polling (commits e2dbb4f → da216a3)
- [x] REST API on master — `/api/v1/builds`, `/builds/{id}`, `/workers` with CORS + JSON errors
- [x] Local `file://` repo submit shares the host folder verbatim (uncommitted changes flow into the build; `GOFLAGS=-buildvcs=false` to avoid host-UID friction)
