# CLAUDE.md - Software Engineering 3.0

The question is not "how fast can we write code?" It is "how fast can we build, validate, ship, and learn from production?" Code is 10-20% of delivery. Optimize the full iceberg, not just the tip.

---

## Guiding Principle: Accelerate the Whole Pipeline

Faster code into a slow system produces longer queues, not faster delivery. Before writing any code, consider:
- Will this change be easy to test automatically?
- Will this change flow through CI in minutes, not hours?
- Will this change deploy independently without cross-team coordination?
- Will this change be observable in production?

If the answer to any of these is "no", fix the delivery path first — or at least flag it.

---

## Architecture: Small Context, Small Blast Radius

AI agents work best reasoning about a bounded context end-to-end. A monolith forces understanding everything to change anything.

- **Microservices / bounded modules**: An agent should be able to own a service completely — write code, run tests, push release — without cascading risk.
- **Strict interfaces as enforced contracts**: Versioned APIs (Protobuf/gRPC), schema validation, automated compatibility checks. An agent can change anything behind an interface as long as the contract holds.
- **All communication through service interfaces**: No backdoor data access, no shared databases between services, no implicit coupling. Bezos mandate applies.
- **Separate domain from infrastructure**: Business rules stay independent from frameworks, transport, and DB.

If working in a monolith, emulate this by isolating domain logic, extracting interfaces, and reducing coupling gradually.

---

## CI: Feedback in Minutes, Not Hours

If CI takes 45 minutes and fails 20% of the time due to flaky tests, fixing that matters more than any AI coding tool.

- **Pipeline as code**: Pipeline definitions live in the repo. Every developer and every AI agent can read, modify, and test them through the same review process.
- **Parallelize aggressively**: Unit tests, integration tests, contract tests, static analysis, security scans — run concurrently. Five 8-minute stages in parallel = 8 minutes. Sequential = 40.
- **Fix or delete flaky tests**: A test that fails randomly trains teams to ignore red builds. Quarantine immediately, fix within a sprint. A green build must mean something.
- **Automated merge gates**: Replace human approval gates with quality signals — test pass rate, coverage delta, security scan, contract compatibility. Humans review for design and intent. Machines gate on correctness.
- **Deterministic builds**: Same inputs → same outputs. Pinned dependencies, containerized builds, no host-specific state.

---

## CD: Actually Continuous

"We have a deployment pipeline" often means a Jenkins job that produces an artifact, deployed via ticket next Tuesday. That is not CD.

- **GitOps**: Git is source of truth for app code AND deployment state. Promoting to production is a PR, not a ticket.
- **Automated rollback**: Health checks detect degradation and auto-revert. Shipping faster must not mean breaking faster.
- **Infrastructure as Code**: Environments defined in version control, not in someone's head.
- **Ephemeral environments**: Spun up per PR or feature branch, torn down after merge.
- **Independent deployability**: Each service deploys on its own schedule without release trains or cross-team coordination.

---

## Test Automation That Scales With AI Code Speed

AI generates code fast. The test surface must keep pace.

- **API-level tests as primary validation**: Not UI-driven E2E tests that are slow and brittle.
- **Contract tests at service boundaries**: Especially critical with gRPC — proto schema changes silently break consumers. Product policies codified in text (e.g., CLAUDE.md) that agents look up while coding, validated by automation.
- **AI test agents**: A test suite written by the same model that wrote the code shares its blind spots — grading your own exam. Independent AI test agents watch the pipeline, generate edge cases, simulate failures, flag regressions.
- **Every task is idempotent**: Re-running with same inputs produces same result. This applies to builds, tests, and deployments.

---

## AI-Native Delivery (Second-Order Opportunity)

The real value of AI is not just writing code — it's improving testing, release engineering, infrastructure, and feedback loops.

- **AI-aware code review**: When AI generates a 200-line function, no human sat with the logic. Company, product, and module-level policies must be codified and enforced via AI-assisted review, because human review won't keep pace.
- **MCP-aware CI/CD**: The pipeline must expose programmatic interfaces (MCP or APIs) so AI agents can trigger builds, query test results, and promote deployments without human involvement.
- **AI for pipeline improvement**: Agents analyze stage dependencies for parallelization, triage flaky tests across CI runs, translate legacy scripts to pipeline-as-code, generate missing test infrastructure.

---

## Concurrency and Distributed Systems

This CI/CD system is heavily concurrent. These patterns are non-negotiable:

- **Idempotency**: Every retryable operation must be safe to run twice.
- **Message passing over shared state**: Prefer NATS over shared databases for coordination. Optimistic concurrency (versioned writes) over distributed locks.
- **Failure recovery**: Assume any component fails at any time. Heartbeats + timeouts for dead workers. Exponential backoff with jitter. Checkpoint task state — resume from last success, never restart from scratch. Dead-letter queue for exhausted retries.
- **Backpressure**: If workers are saturated, scheduler slows dispatch. Never buffer unboundedly.

---

## Security by Default

A CI system executes arbitrary build code. Security is foundational.

- **Secrets**: Inject at runtime only as ephemeral env vars. Auto-scrub from logs. Scope per-pipeline. Short-lived credentials (OIDC) over long-lived keys.
- **Isolation**: Non-root, ephemeral containers. Resource limits on everything. Firecracker/gVisor for untrusted code. Drop all unnecessary Linux capabilities.
- **Network**: No outbound by default. Allowlist registries only. mTLS internally. HMAC-validated webhooks.
- **Supply chain**: Sign all artifacts (cosign). SBOMs for every image. Scan dependencies during builds. Pin images by digest.
- **Audit**: Log every action — who, what, when, which secrets. RBAC on API access. Immutable audit logs.

---

## Performance

Speed is the product. Apply performance thinking on hot paths.

- **Hot paths**: Task scheduling/dispatch, cache lookups, log streaming, artifact transfer, container startup.
- **Async everywhere**: Never block on I/O in scheduler or workers. Stream, don't buffer.
- **Cache layers**: L1 local NVMe → L2 cluster → L3 object store. Content-addressed keys. Monitor hit rates — below 70% means broken cache key design.
- **Anti-patterns**: No polling when events work. No serializing what can run in parallel. No allocating on hot paths.

---

## Non-Negotiable Rules

- Do not optimize code speed while ignoring pipeline speed.
- Do not break interface contracts silently.
- Do not introduce hidden coupling between services.
- Do not write tests that share the blind spots of the code they test.
- Do not mix business logic with infrastructure.
- Do not create manual gates where automated quality signals suffice.
- Do not treat CI/CD as someone else's problem — it is part of every change.
- Do not add abstractions that are not yet justified.

---

## Legacy Systems

- Respect existing behavior. Avoid large speculative rewrites.
- Carve out bounded contexts gradually — extract seams, add interfaces.
- Add characterization tests before deep changes.
- The biggest bottleneck in legacy systems is architecture, not code. Call it out explicitly.
- Provide: the minimal safe fix now + the ideal architectural improvement later.
