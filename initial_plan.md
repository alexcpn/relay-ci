# CI/CD System - Initial Plan

Build a distributed, DAG-based, containerized CI system with maximum parallelism, multi-server scaling, and strong security.

## Core Architecture (Active-Passive HA)

```
┌──────────────────────────────────────────────────────────────────┐
│                        API Gateway                               │
│                 (mTLS, Auth, Rate Limiting)                       │
├──────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌─────────┐   ┌───────────┐   ┌────────────────────┐           │
│  │ Web UI  │   │ CLI Tool  │   │ Git Webhook Recv.  │           │
│  └────┬────┘   └─────┬─────┘   └─────────┬──────────┘           │
│       └───────────────┼───────────────────┘                       │
│                       ▼                                           │
│   ┌─────────────────────────────────────────┐                    │
│   │         Master (Active-Passive)         │                    │
│   │                                         │                    │
│   │  ┌──────────┐       ┌──────────┐        │                    │
│   │  │ Master 1 │──────►│ Master 2 │        │                    │
│   │  │ (active) │ Raft  │ (standby)│        │                    │
│   │  └────┬─────┘       └──────────┘        │                    │
│   │       │  Leader election via etcd       │                    │
│   │       │                                  │                    │
│   │  ┌────▼─────────┐  ┌────────────────┐   │                    │
│   │  │  Scheduler / │  │ Shared State   │   │                    │
│   │  │  DAG Engine  │  │ (etcd or       │   │                    │
│   │  │              │  │  PostgreSQL)   │   │                    │
│   │  └──────┬───────┘  └────────────────┘   │                    │
│   └─────────┼───────────────────────────────┘                    │
│             │  ◄── Pipeline Definitions (code, not YAML)         │
│             │                                                     │
│         ┌───┴────────┬────────────┐                              │
│         ▼            ▼            ▼                               │
│   ┌──────────┐ ┌──────────┐ ┌──────────┐                        │
│   │ Worker 1 │ │ Worker 2 │ │ Worker N │  ← Scale out           │
│   │ (Server) │ │ (Server) │ │ (Server) │                        │
│   └──────────┘ └──────────┘ └──────────┘                        │
│       │              │            │                               │
│       ▼              ▼            ▼                               │
│   ┌─────────────────────────────────────┐                        │
│   │  Shared: Artifact Store, Cache,     │                        │
│   │  Log Aggregator, Metrics, Secrets   │                        │
│   └─────────────────────────────────────┘                        │
└──────────────────────────────────────────────────────────────────┘
```

### High Availability Design

- **Active-Passive master pair** with leader election via etcd (Raft consensus)
- Shared state stored in etcd or PostgreSQL (not in-memory on master)
- On master failure, standby promotes automatically in seconds
- NATS JetStream persists the task queue independently of either master
- Workers are stateless - reconnect to new master seamlessly
- All in-flight builds resume from last checkpointed task state

## The 10 Components

### 1. DAG Engine (the brain)

The most critical piece. Parses pipeline definitions into a directed acyclic graph and schedules tasks with maximum parallelism.

```
Pipeline Definition (code):

    compile_service_a ──┐
    compile_service_b ──┼──► integration_test ──► docker_build ──► push
    compile_service_c ──┘         │
    unit_tests ──────────────────►│
    lint ─────────────────────────► (independent, runs in parallel)
    security_scan ────────────────►
```

**What it does:**
- Parses dependencies between tasks
- Topological sort - finds all tasks with zero unmet dependencies - runs them simultaneously
- As each task completes, unlocks downstream tasks
- Tracks state: pending → scheduled → running → passed/failed/skipped

**Tech choices:**
- Rust or Go for performance
- petgraph (Rust) or gonum/graph (Go) for DAG operations
- State machine per task node

### 2. Distributed Task Queue

Distributes tasks from the DAG engine to workers across servers.

**Requirements:**
- At-least-once delivery with idempotency
- Priority queues (critical path tasks first)
- Task stealing - idle workers pull from busy workers
- Heartbeat / timeout detection for stuck tasks

**Tech choices:**
- NATS JetStream - lightweight, fast, built for this (Rust/Go clients)
- Or Redis Streams if simpler is preferred
- Avoid Kafka - overkill for CI workloads

### 3. Worker Agents

Run on every server. Pull tasks, execute them in containers, report results.

```
Worker Agent
├── Task Runner (pulls from queue, executes)
├── Container Runtime Interface
│   ├── Docker / Podman / containerd
│   └── Firecracker microVMs (for untrusted builds)
├── Log Streamer (real-time → log aggregator)
├── Cache Client (pull/push build cache)
├── Health Reporter (heartbeat to scheduler)
└── Resource Monitor (CPU, mem, disk)
```

**Key design decisions:**
- Each task runs in an ephemeral container - clean slate every time
- Worker reports available resources - scheduler does bin-packing
- Workers are stateless - lose one, no problem

### 4. Container Execution Layer

Everything builds inside containers.

```
Build Container
├── Source code (mounted or cloned)
├── Build tools (Make, Maven, etc.)
├── Cache mounts (.m2, .gradle, node_modules)
└── Produces → Artifacts (JARs, binaries, Docker images)
```

**Tech choices:**
- containerd directly (skip Docker daemon overhead)
- Buildkit for Docker image builds (parallel, cacheable)
- Firecracker microVMs for security isolation
- gVisor/Kata as middle ground between containers and VMs

### 5. Distributed Cache

This is where you win or lose on speed.

```
Cache Layers:
L1: Local NVMe on worker       (fastest, ~3GB/s)
L2: Cluster-local cache node   (fast, ~400MB/s)
L3: Object store (S3/MinIO)    (slow, persistent)
```

**What to cache:**
- Maven .m2/repository - huge win, avoid re-downloading
- Docker layers - content-addressable, dedup naturally
- Build artifacts from unchanged modules (fingerprint inputs → skip rebuild)
- Git repo mirrors

**Tech choices:**
- MinIO for S3-compatible artifact/cache store (self-hosted)
- Content-addressable storage (hash inputs → cache key)
- Turbocache / sccache patterns for build tool caching

### 6. Artifact Store

Stores build outputs - JARs, Docker images, test reports, logs.

```
artifact-store/
├── builds/
│   └── {build-id}/
│       ├── service-a.jar
│       ├── service-b.jar
│       └── test-reports/
├── images/
│   └── {image}:{tag}  ← OCI registry
└── logs/
    └── {build-id}/{task-id}.log
```

**Tech choices:**
- MinIO for artifacts
- Harbor or Zot for Docker/OCI registry (self-hosted, secure)
- Signed artifacts (cosign/notation) for supply chain security

### 7. Pipeline-as-Code Engine

Pipelines defined in real code, not YAML. This is what makes it AI-agent friendly.

```python
# pipeline.py
@pipeline
def build():
    services = ["service-a", "service-b", "service-c"]

    # These all run in parallel - DAG engine resolves it
    compiles = [compile(s) for s in services]
    tests = unit_test(depends_on=compiles)
    lint = lint_all()  # no dependency - runs immediately
    scan = security_scan()

    # These wait for upstream
    integration = integration_test(depends_on=[tests, lint])
    images = [docker_build(s, depends_on=[integration]) for s in services]
    push(images, depends_on=[scan])
```

The engine parses this into a DAG automatically. No manual dependency wiring.

### 8. Secrets Management

Non-negotiable for security.

```
Secrets Flow:
Vault/SOPS → Scheduler → Injected as env vars at runtime
                          (never written to disk, never in logs)
```

**Requirements:**
- Secrets injected at task execution time only
- Automatic log scrubbing (mask secrets in output)
- Short-lived credentials (OIDC tokens, not long-lived keys)
- Per-pipeline secret scoping (pipeline A can't read pipeline B's secrets)

**Tech choices:**
- HashiCorp Vault or SOPS + age encryption
- OIDC identity for workers (no static credentials)

### 9. Networking & Security Layer

```
Security Architecture:
├── mTLS between all components (scheduler ↔ workers ↔ cache)
├── Network policies - build containers have NO outbound by default
│   └── Allowlist: Maven Central, Docker Hub, internal registry
├── Read-only source mounts (builds can't modify source)
├── Non-root containers (all builds run as non-root)
├── Resource limits (CPU, memory, disk per task)
├── Audit log (who triggered what, when, with what secrets)
└── SBOM generation per Docker image
```

### 10. Observability

```
Observability Stack:
├── Logs:    Vector → Loki (or Elasticsearch)
├── Metrics: Prometheus (queue depth, build times, cache hit rate)
├── Traces:  OpenTelemetry → Jaeger (trace a build across workers)
└── Dashboard: Grafana
```

**Key metrics to track:**
- Queue wait time (are you starved for workers?)
- Cache hit rate (below 70%? fix your cache keys)
- Critical path duration (which task is the bottleneck?)
- Flaky test rate

## Tech Stack Summary

| Component | Recommended | Why |
|---|---|---|
| Language | Rust or Go | Performance, safety, great async |
| DAG Engine | Custom (petgraph/gonum) | Core differentiator |
| Task Queue | NATS JetStream | Fast, lightweight, clustering built-in |
| Container Runtime | containerd + Buildkit | Skip Docker daemon overhead |
| Isolation | Firecracker | microVM per build, boot in <150ms |
| Cache/Artifacts | MinIO | S3-compatible, self-hosted |
| Registry | Harbor or Zot | OCI-compliant, self-hosted, scanning |
| Secrets | Vault | Industry standard |
| Observability | OTel + Prometheus + Loki + Grafana | Full stack |
| Communication | gRPC + Protobuf | Fast, typed, streaming logs |
| Pipeline Defs | Python or TypeScript SDK | AI agents can read/write it |
| Cluster Mgmt | Nomad or K8s | Worker scaling, bin-packing |

## Build Phases

### Phase 1: Single-node MVP
- DAG engine + local container execution + CLI
- Can already run Make + Maven + Docker builds with max parallelism

### Phase 2: Distribution
- NATS queue + remote workers + shared cache (MinIO)
- Scales to multiple servers

### Phase 3: Security & Production
- Firecracker isolation, Vault, mTLS, audit logging
- Production-grade

### Phase 4: AI/Agent Integration
- MCP server, API for agents, pipeline-as-code SDK
- Agentic CI/CD as described in the article

## Reference Systems to Study

- **Tekton** - DAG + K8s-native (open source)
- **Dagger** - Containerized DAG execution (open source)
- **Buildkite** - Distributed worker architecture
- **Earthly** - Reproducible containerized builds
