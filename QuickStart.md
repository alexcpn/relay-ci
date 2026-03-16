# Relay CI — Quick Start Guide

Get Relay CI running on Linux and trigger a build from a GitHub repo.

## 1. Build

```bash
# Install Go if not present
wget -q https://go.dev/dl/go1.24.1.linux-amd64.tar.gz -O /tmp/go.tar.gz
mkdir -p ~/go-sdk && tar -C ~/go-sdk -xzf /tmp/go.tar.gz && rm /tmp/go.tar.gz
export PATH=$HOME/go-sdk/go/bin:$HOME/go/bin:$PATH

# Clone and build
git clone https://github.com/ci-system/ci.git && cd ci
make proto
make build
```

Three binaries land in `bin/`:
- `bin/ci-master` — scheduler, API, webhook receiver
- `bin/ci-worker` — task executor
- `bin/ci-cli` — command line client

## 2. Start the Master

```bash
# Required
export WEBHOOK_SECRET="your-webhook-secret"    # same secret you set in GitHub

# Optional
export GRPC_ADDR=":9090"                       # gRPC API (default :9090)
export HTTP_ADDR=":8080"                       # webhooks + health (default :8080)

./bin/ci-master
```

You should see:
```
level=INFO msg="gRPC server starting" addr=:9090
level=INFO msg="HTTP server starting" addr=:8080
```

## 3. Start a Worker

Open a second terminal:

```bash
export MASTER_ADDR="localhost:9090"
export WORKER_ID="worker-1"                    # optional, defaults to hostname

./bin/ci-worker
```

You should see:
```
level=INFO msg="registered with master"
```

For multiple workers, start more instances on the same or different machines:
```bash
MASTER_ADDR="192.168.1.10:9090" WORKER_ID="worker-2" ./bin/ci-worker
```

## 4. Set Up GitHub Webhook

Go to your GitHub repo:

**Settings → Webhooks → Add webhook**

| Field | Value |
|---|---|
| Payload URL | `https://your-server:8080/webhooks` |
| Content type | `application/json` |
| Secret | Same value as `WEBHOOK_SECRET` above |
| Events | Select: **Pushes** and **Pull requests** |

If your server is behind a firewall, use a tunnel for testing:
```bash
# Option A: ngrok
ngrok http 8080
# Use the ngrok URL as the Payload URL

# Option B: cloudflared
cloudflared tunnel --url http://localhost:8080
```

## 5. Add a Pipeline to Your Repo

Create `pipeline.yaml` in your repo root:

```yaml
name: my-app

defaults:
  image: golang:1.24

tasks:
  - id: clone
    image: alpine/git:latest
    commands:
      - git clone --depth=1 $REPO_URL /workspace
      - cd /workspace && git checkout $COMMIT_SHA

  - id: build
    commands:
      - go build ./...
    depends_on: [clone]

  - id: test
    commands:
      - go test ./...
    depends_on: [clone]

integrations:
  linters:
    enabled: true
    tools:
      - name: golangci-lint

triggers:
  pull_requests: true
  branches: [main]
```

Commit and push this file.

## 6. Trigger a Build

### Option A: Open a Pull Request

Push a branch and open a PR on GitHub. The webhook fires automatically and you'll see:

```
level=INFO msg="webhook received" provider=github type=1
level=INFO msg="build submitted from PR" build_id=abc123 repo=myorg/myrepo pr=1 sha=def456
```

### Option B: Push to main

```bash
git push origin main
```

### Option C: Use the CLI

```bash
# Submit a build manually
./bin/ci-cli submit https://github.com/myorg/myrepo.git --branch main --sha HEAD

# Check status
./bin/ci-cli list
./bin/ci-cli status <build-id>

# Stream logs
./bin/ci-cli logs <build-id> <task-id> --follow

# Watch build events
./bin/ci-cli watch <build-id>

# Cancel a build
./bin/ci-cli cancel <build-id>
```

### Option D: curl the webhook endpoint

Simulate a GitHub push event:

```bash
PAYLOAD='{"ref":"refs/heads/main","before":"aaa","after":"bbb","pusher":{"name":"you"},"repository":{"full_name":"myorg/myrepo","clone_url":"https://github.com/myorg/myrepo.git"},"commits":[]}'

SIGNATURE=$(echo -n "$PAYLOAD" | openssl dgst -sha256 -hmac "$WEBHOOK_SECRET" | awk '{print "sha256="$2}')

curl -X POST http://localhost:8080/webhooks \
  -H "Content-Type: application/json" \
  -H "X-GitHub-Event: push" \
  -H "X-GitHub-Delivery: test-$(date +%s)" \
  -H "X-Hub-Signature-256: $SIGNATURE" \
  -d "$PAYLOAD"
```

Response:
```json
{"build_id":"abc123def456"}
```

## 7. Verify It Works

```bash
# Health check
curl http://localhost:8080/health
# {"status":"ok"}

# List builds via CLI
./bin/ci-cli list

# Output:
# BUILD           STATE    REPO                                      BRANCH  TRIGGER
# abc123def456    RUNNING  https://github.com/myorg/myrepo.git       main    webhook:you
```

## 8. MCP Server (for AI Agents)

The MCP server lets AI agents (Claude, etc.) monitor builds, diagnose failures, and suggest fixes.

### Option A: Remote HTTP Server (for remote agents)

Run the MCP server as an HTTP service on the CI machine:

```bash
CI_MASTER="localhost:9090" MCP_HTTP_ADDR=":8081" ./bin/ci-mcp
```

Agents connect via HTTP:
```bash
# Initialize a session
curl -X POST http://ci-server:8081/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"agent","version":"1.0"}}}'

# Response includes Mcp-Session-Id header — pass it in subsequent requests

# List tools
curl -X POST http://ci-server:8081/mcp \
  -H "Content-Type: application/json" \
  -H "Mcp-Session-Id: <session-id>" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'

# Call a tool
curl -X POST http://ci-server:8081/mcp \
  -H "Content-Type: application/json" \
  -H "Mcp-Session-Id: <session-id>" \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_builds","arguments":{}}}'
```

MCP Inspector can connect to it at `http://ci-server:8081/mcp`.

### Option B: stdio mode (for local MCP clients)

```bash
CI_MASTER="localhost:9090" ./bin/ci-mcp
```

### Configure in Claude Desktop (stdio)

Add to `~/.config/claude_desktop_config.json` (Linux) or `~/Library/Application Support/Claude/claude_desktop_config.json` (macOS):

```json
{
  "mcpServers": {
    "relay-ci": {
      "command": "/ssd/ideas/new_ci_system/bin/ci-mcp",
      "env": {
        "CI_MASTER": "localhost:9090"
      }
    }
  }
}
```

###  Claude Code

```
claude mcp add --transport http ci-system http://your-ci-server:8081/mcp 
```

Or for your local setup: 

```
claude mcp add --transport http ci-system http://localhost:8081/mcp 
```

To scope it to the project (shared with team via .mcp.json): 
  
`claude mcp add --transport http ci-system --scope project http://localhost:8081/mcp` 
  
You can verify it's connecte with:

`claude mcp list`

Or inside Claude Code, run `/mcp` to check server status. 


### Available Tools

| Tool | Description |
|---|---|
| `list_builds` | List builds with optional state filter |
| `get_build` | Get detailed build status with all tasks |
| `get_task_logs` | Get stdout/stderr for a task |
| `diagnose_build` | Analyze a failed build: failed tasks, error logs, affected dependencies |
| `submit_build` | Submit a new build for a repo |
| `cancel_build` | Cancel a running build |
| `retry_build` | Retry failed tasks in a build |
| `get_failed_builds` | List all failed builds with summaries |
| `suggest_fix` | Classify error type and suggest corrective action |
| `watch_build` | Get build progress as a percentage |

## Ports Summary

| Port | Protocol | Purpose |
|---|---|---|
| 8080 | HTTP | Webhooks (`/webhooks`), health check (`/health`) |
| 8081 | HTTP | MCP server (`/mcp`), health check (`/health`) — set via `MCP_HTTP_ADDR` |
| 9090 | gRPC | CLI, worker registration, build API |

## Troubleshooting

**Webhook returns 400 Bad Request**
- Check that `WEBHOOK_SECRET` matches what you set in GitHub
- Verify the `Content-Type` is `application/json`

**Worker can't connect**
- Ensure `MASTER_ADDR` points to the master's gRPC port (9090, not 8080)
- Check firewall rules between worker and master

**No tasks scheduled**
- Verify at least one worker is registered: check master logs for "worker registered"
- The scheduling loop runs every 500ms, wait a moment

**Build stays in QUEUED state**
- Workers may lack sufficient resources for the task
- Check worker capacity vs task resource requirements in `pipeline.yaml`
