# CI System

A distributed, DAG-based, containerized CI system built in Go. Pipelines are defined as code, tasks execute in ephemeral containers on bare metal with maximum parallelism, and the system reports build status back to GitHub/GitLab.

## Architecture

```
Developer opens PR
       │
       ▼
GitHub/GitLab webhook POST ──► ci-master /webhooks
       │
       ▼
  SCM parses event ──► DAG engine builds graph ──► Scheduler assigns tasks
       │                                                    │
       ▼                                              ┌─────┴─────┐
  Status reported                                     ▼           ▼
  back to GitHub                                 worker-1     worker-N
  (✅ / ❌ on PR)                                    │           │
                                                 containerd  containerd
                                                 /Firecracker /Firecracker
```

### Deployable Services

| Service | Binary | Description |
|---|---|---|
| **ci-master** | `cmd/master` | API gateway, scheduler, DAG engine, webhook receiver |
| **ci-worker** | `cmd/worker` | Task execution, container lifecycle, log streaming |
| **ci-cli** | `cmd/cli` | Command-line client for submitting builds, viewing status |

### Shared Libraries

| Package | Description |
|---|---|
| `pkg/dag` | DAG construction, topological sort, cycle detection, task state machine |
| `pkg/scheduler` | Bin-packing task assignment, build lifecycle, dead worker handling |
| `pkg/worker` | Worker registry, capacity tracking, heartbeat monitoring |
| `pkg/scm` | GitHub/GitLab webhook parsing, HMAC verification, commit status reporting |
| `pkg/container` | Container runtime interface (containerd/Firecracker), mock runtime for testing |
| `pkg/logstore` | Log storage with real-time streaming via subscribers |
| `pkg/secrets` | Scoped secret store, log scrubbing, env var masking |

## Project Structure

```
ci-system/
├── README.md
├── Makefile                      # make proto / make build / make test
├── go.mod
├── go.sum
│
├── proto/ci/v1/                  # Protobuf definitions (source of truth)
│   ├── common.proto              #   Shared types: IDs, enums, GitSource, TaskResult
│   ├── scheduler.proto           #   SchedulerService (6 RPCs)
│   ├── worker.proto              #   WorkerService + WorkerRegistryService (5 RPCs)
│   ├── cache.proto               #   CacheService + ArtifactService (9 RPCs)
│   ├── logs.proto                #   LogService (3 RPCs)
│   ├── secrets.proto             #   SecretsService (4 RPCs)
│   └── webhook.proto             #   SCMEvent types, StatusReport, WebhookConfig
│
├── gen/ci/v1/                    # Generated Go code (do not edit)
│   ├── *.pb.go                   #   Protobuf message types
│   └── *_grpc.pb.go              #   gRPC client/server stubs
│
├── cmd/
│   ├── master/                   # ci-master binary
│   │   ├── main.go               #   Server startup, scheduling loop, graceful shutdown
│   │   ├── grpc_scheduler.go     #   SchedulerService gRPC implementation
│   │   ├── grpc_worker.go        #   WorkerRegistryService gRPC implementation
│   │   ├── grpc_logs.go          #   LogService gRPC implementation
│   │   └── webhook.go            #   HTTP webhook handler (GitHub/GitLab → scheduler)
│   │
│   ├── worker/                   # ci-worker binary
│   │   └── main.go               #   Worker registration, heartbeat loop
│   │
│   └── cli/                      # ci-cli binary
│       └── main.go               #   submit, status, list, cancel, logs, watch commands
│
├── pkg/
│   ├── dag/                      # DAG engine
│   │   ├── task.go               #   Task struct, state machine (8 states), transitions
│   │   ├── graph.go              #   Graph: add/validate/schedule/complete, cycle detection
│   │   └── graph_test.go         #   12 tests
│   │
│   ├── scheduler/                # Task scheduler
│   │   ├── scheduler.go          #   Build management, bin-packing, dead worker handling
│   │   └── scheduler_test.go     #   8 tests
│   │
│   ├── worker/                   # Worker registry
│   │   ├── registry.go           #   Registration, heartbeat, capacity tracking, drain
│   │   └── registry_test.go      #   11 tests
│   │
│   ├── scm/                      # Source control integration
│   │   ├── types.go              #   Canonical event types, StatusReport, WebhookConfig
│   │   ├── provider.go           #   WebhookParser + StatusReporter interfaces
│   │   ├── github.go             #   GitHub: webhook parsing, HMAC-SHA256, Commit Status API
│   │   ├── github_test.go        #   5 tests
│   │   ├── gitlab.go             #   GitLab: webhook parsing, secret token, Status API
│   │   ├── gitlab_test.go        #   6 tests
│   │   └── router.go             #   Auto-detect provider from request headers
│   │
│   ├── container/                # Container runtime
│   │   ├── runtime.go            #   Runtime + Container interfaces, ContainerConfig
│   │   ├── mock.go               #   MockRuntime for testing (configurable exit codes/output)
│   │   ├── runner.go             #   Runner: pull → create → start → wait → collect logs
│   │   └── runner_test.go        #   8 tests
│   │
│   ├── logstore/                 # Log storage + streaming
│   │   ├── store.go              #   Append, Get (batch), Subscribe (real-time), Complete
│   │   └── store_test.go         #   9 tests
│   │
│   ├── secrets/                  # Secret management
│   │   ├── store.go              #   Scoped secret store, Scrubber, ScrubEnv
│   │   └── store_test.go         #   13 tests
│   │
│   └── observability/            # (placeholder) OTel, Prometheus, structured logging
│
├── test/
│   └── integration/              # Integration tests
│       └── full_pipeline_test.go  #   5 tests: full pipeline, status reporting,
│                                  #   parallel builds, worker failure, log streaming
│
└── internal/                     # (placeholder) Shared internal helpers
```

## gRPC Services

| Service | Proto | Runs on | RPCs |
|---|---|---|---|
| `SchedulerService` | `scheduler.proto` | master | SubmitBuild, CancelBuild, RetryBuild, GetBuild, ListBuilds, WatchBuild |
| `WorkerService` | `worker.proto` | worker | AssignTask, CancelTask |
| `WorkerRegistryService` | `worker.proto` | master | Register, Heartbeat, ReportTaskResult |
| `CacheService` | `cache.proto` | cache node | Has, Get, Put, Delete, Stats |
| `ArtifactService` | `cache.proto` | cache node | Upload, Download, List, GetDownloadURL |
| `LogService` | `logs.proto` | master | PushLogs, StreamLogs, GetLogs |
| `SecretsService` | `secrets.proto` | master | GetSecrets, PutSecret, DeleteSecret, ListSecrets |

## Quick Start

### Prerequisites

- Go 1.24+
- `protoc` with `protoc-gen-go` and `protoc-gen-go-grpc`

### Build

```bash
make proto    # generate Go code from .proto files
make build    # build ci-master, ci-worker, ci-cli binaries
make test     # run all tests
```

### Run

```bash
# Terminal 1: start master
./bin/ci-master

# Terminal 2: start worker
MASTER_ADDR=localhost:9090 ./bin/ci-worker

# Terminal 3: submit a build
./bin/ci-cli submit https://github.com/myorg/myrepo.git --branch main
./bin/ci-cli list
./bin/ci-cli status <build-id>
./bin/ci-cli logs <build-id> <task-id> --follow
```

### Webhook Setup

Add a webhook to your GitHub/GitLab repository:

- **URL:** `https://your-ci-server.com/webhooks`
- **Events:** `push`, `pull_request` (GitHub) or `Push Hook`, `Merge Request Hook` (GitLab)
- **Secret:** Set `WEBHOOK_SECRET` env var on the master

Build status appears as checks on PRs — no GitHub Actions required.

## Test Summary

```
pkg/dag           12 tests   DAG operations, cycle detection, state machine
pkg/worker        11 tests   Registry, heartbeat, capacity, drain
pkg/scheduler      8 tests   Scheduling, bin-packing, failure cascade, dead workers
pkg/scm           11 tests   GitHub + GitLab webhook parsing, signature verification, status API
pkg/logstore       9 tests   Append, pagination, real-time streaming, completion
pkg/secrets       13 tests   Scoped storage, scrubbing, env masking
pkg/container      8 tests   Mock runtime, success/failure, timeout, cleanup
test/integration   5 tests   Full pipeline end-to-end with all modules
                  ──
                  77 total
```

## Build Phases

| Phase | Scope | Status |
|---|---|---|
| **Phase 1: Single-node MVP** | DAG engine, scheduler, container execution, CLI, webhooks | **In progress** |
| Phase 2: Distribution | NATS task queue, remote workers, shared cache (MinIO) | Planned |
| Phase 3: Security & Production | Firecracker isolation, Vault, mTLS, audit logging | Planned |
| Phase 4: AI/Agent Integration | MCP server, pipeline-as-code SDK, agent APIs | Planned |
