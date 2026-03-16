# Relay CI — Architecture

Relay CI is a distributed, DAG-based CI system built in Go. It is designed for agentic workflows: every operation is a gRPC call or MCP tool, not a web UI click. A build is a directed acyclic graph of tasks that execute in parallel across ephemeral Docker containers. Workers share a named volume (`/workspace`) so tasks can hand off files without copying. An embedded MCP server exposes the full system as callable tools for AI agents.

---

## Components

| Component | Source | Description |
|---|---|---|
| **ci-master** | [`cmd/master/`](cmd/master/) | Central orchestrator. Receives webhooks, builds task DAGs, schedules work to workers, stores logs, exposes gRPC and HTTP APIs. |
| **ci-worker** | [`cmd/worker/`](cmd/worker/) | Task executor. Receives assignments from master via gRPC, runs each task in a Docker container, streams logs back in real time, reports results. |
| **ci-cli** | [`cmd/cli/`](cmd/cli/) | Command-line client. Submit builds, check status, stream logs, cancel, watch progress. |
| **ci-mcp** | [`cmd/mcp/`](cmd/mcp/) | MCP server for AI agents. Runs in stdio or HTTP mode, exposes all CI operations as MCP tools callable by Claude, GPT-4, or any MCP-compatible agent. |
| **pkg/dag** | [`pkg/dag/`](pkg/dag/) | DAG engine. Defines the task state machine (Pending → Ready → Running → Passed/Failed/Skipped) and the graph that tracks dependencies, detects cycles, and cascades skips on failure. |
| **pkg/pipeline** | [`pkg/pipeline/`](pkg/pipeline/) | Pipeline parser. Reads `pipeline.yml` from a repo, applies defaults, generates integration tasks (linters, security scanners, SonarQube), and produces a `dag.Graph` ready for execution. |
| **pkg/scheduler** | [`pkg/scheduler/`](pkg/scheduler/) | Scheduler. Walks all active builds each cycle, finds ready tasks, bin-packs them onto available workers by resource requirements (CPU, memory, disk). |
| **pkg/worker** | [`pkg/worker/`](pkg/worker/) | Worker registry. Tracks registered workers, their capacity, heartbeat state, and availability. Marks workers dead if heartbeats lapse. |
| **pkg/logstore** | [`pkg/logstore/`](pkg/logstore/) | Log store. In-memory append-only log buffer per task. Supports real-time subscriber channels so log lines are delivered to streaming clients as they arrive. |
| **pkg/scm** | [`pkg/scm/`](pkg/scm/) | SCM integration. Parses GitHub and GitLab webhooks (with HMAC verification), reports commit and PR statuses back via provider APIs. |
| **pkg/container** | [`pkg/container/`](pkg/container/) | Container abstraction. Interface for running tasks in containers (Docker, mock for tests). Handles image pull, env injection, volume mounts, resource limits, and log capture. |
| **pkg/secrets** | [`pkg/secrets/`](pkg/secrets/) | Secret store. Scoped in-memory key/value store. Scrubs secret values from log output so they never appear in build logs. |
| **proto/ci/v1** | [`proto/ci/v1/`](proto/ci/v1/) | Protocol buffer definitions. Canonical message types and service contracts for all gRPC APIs (scheduler, worker, logs, cache, secrets, webhooks). |
| **gen/ci/v1** | [`gen/ci/v1/`](gen/ci/v1/) | Generated Go code from proto files (`make proto`). Not edited by hand. |
| **test/integration** | [`test/integration/`](test/integration/) | Integration tests. Exercises the full pipeline in-process: webhook → scheduler → mock worker → log store → status report. |
| **test/e2e** | [`test/e2e/`](test/e2e/) | End-to-end tests. Submits a real build to a live master + worker against a real GitHub repo. Requires `CI_E2E=1`. |

---

## Request Flow

```
Developer pushes / opens PR
          │
          ▼
  HTTP POST /webhooks                   ← GitHub or GitLab
          │
  pkg/scm: parse + HMAC verify
          │
          ▼
  cmd/master: clone repo, read pipeline.yml
          │
  pkg/pipeline: parse config → build dag.Graph
          │
          ▼
  pkg/scheduler: SubmitBuild
          │
   scheduling loop (every 500ms)
          │
  pkg/scheduler: find Ready tasks → bin-pack onto workers
          │
  cmd/master/dispatcher: gRPC AssignTask → worker
          │
          ▼
  cmd/worker: receive task
          │
  cmd/worker/executor: docker run
    - shared /workspace volume
    - cache volumes (gomod, npm, pip, trivy-db, …)
    - --entrypoint sh, -c "<commands>"
          │
   stdout/stderr → gRPC PushLogs → pkg/logstore
          │
          ▼
  cmd/worker: ReportTaskResult → master
          │
  pkg/dag: update task state, unblock dependents / cascade skips
          │
   repeat until all tasks terminal
          │
          ▼
  pkg/scm: report commit status to GitHub/GitLab
```

---

## Service Ports

| Port | Protocol | Service | Endpoint |
|---|---|---|---|
| 9090 | gRPC | ci-master | SchedulerService, WorkerRegistryService, LogService |
| 8080 | HTTP | ci-master | `POST /webhooks`, `GET /health`, log viewer |
| 9091 | gRPC | ci-worker | WorkerService (AssignTask, CancelTask) |
| 8081 | HTTP | ci-mcp | `POST /mcp` (MCP streamable transport), `GET /health` |

---

## DAG State Machine

Each task moves through the following states (defined in [`pkg/dag/task.go`](pkg/dag/task.go)):

```
Pending ──► Ready ──► Scheduled ──► Running ──► Passed
                                        │
                                        ├──► Failed ──► (dependents: Skipped)
                                        ├──► TimedOut
                                        └──► Cancelled
```

A build is complete when all tasks are in a terminal state. The build passes only if all non-skipped tasks passed.

---

## Pipeline YAML

Drop a `pipeline.yml` (or `pipeline.yaml`) in the root of your repo. The master fetches it on every build trigger.

```yaml
name: my-service

defaults:
  image: golang:1.24
  env:
    CI: "true"

tasks:
  - id: clone
    image: alpine/git:latest
    commands:
      - git clone --depth=1 $REPO_URL /workspace
      - cd /workspace && git checkout $COMMIT_SHA

  - id: build
    commands: [go build ./...]
    depends_on: [clone]

  - id: test
    commands: [go test ./...]
    depends_on: [clone]
    cache:
      - key: gomod
        path: /root/go/pkg/mod

integrations:
  linters:
    enabled: true
    tools:
      - name: golangci-lint

  security:
    enabled: true
    tools:
      - name: trivy
        severity: HIGH,CRITICAL
        fail_on_findings: true

triggers:
  branches: [main]
  pull_requests: true
```

Integration tasks (linters, scanners, SonarQube) are auto-generated by [`pkg/pipeline/parser.go`](pkg/pipeline/parser.go) and wired into the DAG — they depend on the `clone` task if one exists, get appropriate container images and cache volumes injected automatically, and run in parallel with user-defined tasks.

---

## MCP Tools

The MCP server ([`cmd/mcp/`](cmd/mcp/)) exposes ten tools to AI agents:

| Tool | What it does |
|---|---|
| `submit_build` | Trigger a build for any repo/branch/commit |
| `get_build` | Full build status with all task states, durations, exit codes |
| `list_builds` | List builds, filterable by state, repo, or branch |
| `watch_build` | Poll build progress as a percentage |
| `get_task_logs` | Fetch stdout/stderr for any task (paginated) |
| `diagnose_build` | Structured failure analysis — failed tasks, error lines, skipped dependents |
| `suggest_fix` | Analyse a failed task: error type, file:line reference, fix recommendation |
| `get_failed_builds` | All currently failed builds with failure summaries |
| `retry_build` | Re-run failed tasks only, or full rebuild from scratch |
| `cancel_build` | Kill a running build |

---

## Key Design Decisions

- **DAG-first execution** — tasks declare `depends_on`; independent tasks run in parallel automatically with no scheduler configuration needed.
- **Shared workspace volume** — all tasks in a build mount the same named Docker volume at `/workspace`; no inter-task file copying or artifact upload/download required.
- **Cache volumes** — Go module cache, npm, pip, trivy vulnerability DB, and similar heavy caches persist across builds as named Docker volumes, eliminating redundant downloads.
- **Agent-first API** — every operation is a gRPC call or MCP tool. There is no web UI required for automation.
- **Pipeline as code** — `pipeline.yml` lives in the repo; no vendor lock-in and agents can read and write it directly.
- **Entrypoint normalisation** — the worker always overrides the container entrypoint to `sh` so that any base image (including ones like `alpine/git` that set a non-shell entrypoint) can execute arbitrary shell commands.
