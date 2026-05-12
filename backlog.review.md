# Agentic Code Review — Product Backlog

## What this is

A code review service built on top of Relay CI's pipeline engine. Any code
generator, IDE extension, or agent can submit a diff and get back **structured,
actionable findings** fast enough to iterate in a single session. The target
consumer is an AI agent in a tight edit → review → fix loop, not a human
waiting 5 minutes for a CI run.

The pipeline (linters, security scanners, AI review) is kept as-is and reused.
What changes is the **interaction model**: instead of "submit a repo + branch
and wait", it's "submit a diff, get structured findings in < 10 seconds, fix,
resubmit, converge."

## What we keep from main

Everything in the pipeline stays:

- DAG-based pipeline execution — used for `mode: deep` (security scans, full lint)
- Linter integrations (golangci-lint, eslint, ruff, pylint, shellcheck, hadolint)
- Security scanners (trivy, grype, semgrep, gosec)
- AI review integration (Anthropic / OpenAI / Ollama)
- MCP server — extended, not replaced
- SQLite store — `reviews` and `findings` tables added alongside `builds`
- GitHub/GitLab PR commenting — reused for PR integration (P3)
- Web UI — review dashboard added in P3

## Product shape (MVP)

```
POST /api/v1/reviews                      # submit a diff for review
GET  /api/v1/reviews/{id}                 # poll for findings
GET  /api/v1/reviews/{id}/diff/{prev_id}  # what changed between two iterations
GET  /api/v1/reviews                      # list recent reviews (filterable by session)
GET  /api/v1/sessions/{id}                # full iteration chain for a review session
```

MCP tools:
```
submit_review(diff, language, context, session_id?)  →  review_id
get_review(review_id)                                →  {verdict, findings[], summary}
diff_reviews(review_id_a, review_id_b)               →  {fixed[], regressed[], new[]}
list_reviews(session_id?, limit?)                    →  reviews[]
```

---

## P0 — Minimum product (must ship together to be useful)

### Review submission API

- [ ] **`POST /api/v1/reviews`** — body: `{diff, language, policy_id?, context?, session_id?, mode?}`; returns `{review_id}` immediately (async)
- [ ] **`GET /api/v1/reviews/{id}`** — returns `{state, verdict, findings[], summary, duration_ms, iteration}`
- [ ] **`GET /api/v1/reviews`** — list with state, verdict, session_id, timestamp, language
- [ ] **`ReviewRecord` in store** — separate table from `builds`; no DAG, no graph overhead; just `review_id, session_id, diff, state, verdict, created_at, duration_ms`
- [ ] **`findings` table** — `{id, review_id, file, line, col, severity, rule, tool, message, suggestion, category}`
- [ ] **`sessions` table** — groups review iterations; `GET /api/v1/sessions/{id}` returns the full iteration chain with per-iteration verdict

### Structured findings format

The most important thing to get right. Agents can't act on free-text log lines.

- [ ] **`Finding` struct**: `{id, review_id, file, line, col, severity, rule, tool, message, suggestion, category}`
- [ ] **Severity tiers**: `critical | high | medium | low | info`
- [ ] **Categories**: `style | correctness | security | performance | maintainability | generated-noise`
- [ ] Every finding from every tool (linter, scanner, AI) converts to this format — one schema, not one-per-tool
- [ ] `suggestion` is mandatory for AI findings; linter findings populate it from `--fix` output where available

### Fast review path (no container cold-start)

The core architecture bet. Container cold-start (image pull + start) is 5–30s.
That's fine for CI; it kills interactive agent loops. The fast path shells out
to already-installed binaries on the worker host and calls the AI API directly.

- [ ] **In-process lint runners** — shell out to host-installed linters (`golangci-lint`, `eslint`, `ruff`, `shellcheck`), parse their JSON output into `Finding` objects; no Docker
- [ ] **Direct AI review** — call Anthropic/OpenAI/Ollama directly from the worker; no container; reuse the existing review prompt logic extracted into a `ReviewClient`
- [ ] **Target latency**: < 10s for a typical diff on the fast path (lint + AI)
- [ ] **`mode` field on submission**: `fast` (in-process, default) vs `deep` (full Docker pipeline, includes security scanners, async, minutes)
- [ ] Fast path workers are always warm — no scheduling queue needed; reviews run immediately in a goroutine pool

### Review policy

- [ ] **`review_policy.yaml`** in repo root, or passed inline as `policy` on the request
- [ ] Policy specifies: which linters, AI model + prompt, severity threshold for pass/fail, `generated_code: true` flag
- [ ] `generated_code: true` activates the generated-noise category rules (see P2)
- [ ] Default built-in policy; per-request override is valid
- [ ] Policy is versioned in the repo — diff it through the existing `ci verify` flow

### MCP tools (new)

- [ ] **`submit_review(diff, language, context?, session_id?)`** → `review_id`; non-blocking
- [ ] **`get_review(review_id)`** → `{verdict, findings[], summary, iteration, session_id}`; poll until `state == done`
- [ ] **`diff_reviews(review_id_a, review_id_b)`** → `{fixed[], regressed[], unchanged[], new[]}` — the key agent-loop tool; agent knows which fixes landed and which introduced regressions
- [ ] **`list_reviews(session_id?, limit?)`** → recent reviews

---

## P1 — Agent loop quality

These make the iteration loop actually converge rather than spin.

- [ ] **Finding deduplication across iterations** — same finding (same file + line + rule) not re-reported if unchanged; agent doesn't re-fix already-fixed issues
- [ ] **Per-finding status** — `open | fixed | acknowledged | wont-fix`; agent marks findings; `diff_reviews` respects status
- [ ] **Concrete `suggestion` quality gate** — AI review prompt explicitly requires a `suggestion` field with copy-pasteable code, not "consider refactoring"; lint-time validation of the AI response shape
- [ ] **Context window** — review submission can include surrounding file context (not just the diff) so the AI doesn't hallucinate missing imports or unknown types
- [ ] **Review budget** — per-session token budget (AI tokens, wall time, max iterations); surface to agent as `budget_remaining`; stop and escalate to human when exhausted
- [ ] **Webhook trigger** — `POST /api/v1/reviews/webhook` from IDE/code-gen tool; returns `review_id` immediately; agent polls; content-type detection auto-sets language
- [ ] **Rate limiting per session** — prevent a spinning agent from consuming unbounded AI tokens

---

## P2 — Generated code specific

The rules that catch what linters miss in LLM output.

- [ ] **Comment noise detector** — flag comments that restate the code (`// Increment i by 1\ni++`); severity `info`, category `generated-noise`; applied as a fast pre-pass before AI
- [ ] **Convention adherence check** — compare generated code style to a sample of existing codebase (loaded from context); flag deviations (naming convention, error handling pattern, logging style)
- [ ] **Dead code / unused imports** — common in generated output; in-process fast check per language (go vet, pylint F401, ts-unused-exports)
- [ ] **Idiomatic pattern checklist** — language-specific patterns fed into the AI review prompt as an explicit checklist: Go (`errors.Is`, `context.Context` propagation, no `panic` in library code), Python (f-strings, type hints), TS (`unknown` over `any`, no non-null assertion without comment)
- [ ] **Similarity check** — compare generated code to existing patterns in the repo; surface "this already exists at pkg/foo/bar.go, line 42, use that instead"

---

## P3 — Integration surface

- [ ] **GitHub / GitLab PR annotation** — post structured findings as inline PR review comments (already have PR commenting; wire to review findings, one comment per finding at the correct line)
- [ ] **Claude Code hook** — `PostToolUse` hook that submits changed files to `/api/v1/reviews` and blocks commit if verdict is `fail`; configured in `.claude/settings.json`
- [ ] **IDE diagnostic protocol** — review findings returned in LSP `textDocument/publishDiagnostics` shape so VS Code / JetBrains can render them natively in the Problems panel
- [ ] **Review dashboard in web UI** — list of review sessions, per-session iteration timeline (issues per iteration, should trend to zero), finding breakdown by category/severity/tool

---

## Explicitly out of scope

- **Full CI (builds, tests, deploys)** — `pipeline.yaml` stays, but a review submission doesn't require it; running tests is optional and gated behind `mode: deep`
- **Human-facing UX as primary** — humans can read findings in the web UI or PR comments, but the interaction model is designed for agents (structured data, machine-readable verdict, fast latency)
- **Per-tool plugin framework** — findings parsers are Go code in the server, not a plugin API
- **Real-time streaming of findings** — polling is fine; SSE can come later
- **Multi-repo policy federation** — one policy per repo/request for now; central policy server is a future concern

---

## Relationship to main backlog

The P0 items from `backlog.md` (SQLite store, auth, rate limiting) are already
done on `main` and flow into this branch. The `code_review` branch adds the
review-specific tables to the same SQLite DB and the new API surface alongside
the existing `/api/v1/builds` endpoints. The two products share one binary.
