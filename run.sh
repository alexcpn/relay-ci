#!/usr/bin/env bash
# run.sh — build and run ci-master, ci-worker, and ci-mcp on a single machine.
#
# Usage:
#   ./run.sh          # build + start all servers
#   ./run.sh stop     # kill all servers
#   ./run.sh restart  # stop + start
#   ./run.sh status   # show running processes
#   ./run.sh logs     # tail all logs

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PID_DIR="${SCRIPT_DIR}/.run"
LOG_DIR="${SCRIPT_DIR}/.run/logs"

MASTER_GRPC_ADDR="${MASTER_GRPC_ADDR:-:9090}"
MASTER_HTTP_ADDR="${MASTER_HTTP_ADDR:-:8080}"
WORKER_ADDR="${WORKER_ADDR:-:9091}"
MCP_HTTP_ADDR="${MCP_HTTP_ADDR:-:8081}"

GO="${GO:-$(which go 2>/dev/null || echo /home/alex/go-sdk/go/bin/go)}"

# ── helpers ──────────────────────────────────────────────────────────────────

red()   { echo -e "\033[31m$*\033[0m"; }
green() { echo -e "\033[32m$*\033[0m"; }
bold()  { echo -e "\033[1m$*\033[0m"; }

pid_file() { echo "${PID_DIR}/$1.pid"; }
log_file() { echo "${LOG_DIR}/$1.log"; }

is_running() {
    local name=$1
    local pf; pf=$(pid_file "$name")
    [[ -f "$pf" ]] && kill -0 "$(cat "$pf")" 2>/dev/null
}

stop_server() {
    local name=$1
    local pf; pf=$(pid_file "$name")
    if is_running "$name"; then
        local pid; pid=$(cat "$pf")
        kill "$pid" 2>/dev/null && echo "  stopped $name (pid $pid)" || true
        rm -f "$pf"
    else
        echo "  $name not running"
    fi
}

wait_tcp() {
    local addr=$1 name=$2 retries=20
    # Strip leading colon so :9090 → localhost:9090
    local host_port="${addr#:}"
    [[ "$host_port" == "$addr" ]] || host_port="localhost:${addr#:}"
    for i in $(seq 1 $retries); do
        if bash -c "echo >/dev/tcp/${host_port/:/\/}" 2>/dev/null; then
            return 0
        fi
        sleep 0.3
    done
    red "  $name did not become ready at $addr"
    return 1
}

# ── build ─────────────────────────────────────────────────────────────────────

cmd_build() {
    bold "Building binaries..."
    mkdir -p "${SCRIPT_DIR}/bin"
    "$GO" build -o "${SCRIPT_DIR}/bin/ci-master" ./cmd/master
    "$GO" build -o "${SCRIPT_DIR}/bin/ci-worker" ./cmd/worker
    "$GO" build -o "${SCRIPT_DIR}/bin/ci-mcp"    ./cmd/mcp
    "$GO" build -o "${SCRIPT_DIR}/bin/ci-cli"    ./cmd/cli
    green "  build OK"
}

# ── start ─────────────────────────────────────────────────────────────────────

cmd_start() {
    mkdir -p "$PID_DIR" "$LOG_DIR"

    # --- master ---
    if is_running master; then
        echo "  master already running (pid $(cat "$(pid_file master)"))"
    else
        GRPC_ADDR="$MASTER_GRPC_ADDR" \
        HTTP_ADDR="$MASTER_HTTP_ADDR" \
            "${SCRIPT_DIR}/bin/ci-master" \
            >"$(log_file master)" 2>&1 &
        echo $! >"$(pid_file master)"
        echo "  started master (pid $!) grpc=${MASTER_GRPC_ADDR} http=${MASTER_HTTP_ADDR}"
    fi

    # Wait for master gRPC to be ready before starting worker/mcp.
    wait_tcp "$MASTER_GRPC_ADDR" master

    # --- worker ---
    if is_running worker; then
        echo "  worker already running (pid $(cat "$(pid_file worker)"))"
    else
        MASTER_ADDR="localhost${MASTER_GRPC_ADDR}" \
        WORKER_ADDR="$WORKER_ADDR" \
        WORKER_ID="worker-1" \
            "${SCRIPT_DIR}/bin/ci-worker" \
            >"$(log_file worker)" 2>&1 &
        echo $! >"$(pid_file worker)"
        echo "  started worker (pid $!) addr=${WORKER_ADDR}"
    fi

    # --- mcp (HTTP mode) ---
    if is_running mcp; then
        echo "  mcp already running (pid $(cat "$(pid_file mcp)"))"
    else
        CI_MASTER="localhost${MASTER_GRPC_ADDR}" \
        MCP_HTTP_ADDR="$MCP_HTTP_ADDR" \
            "${SCRIPT_DIR}/bin/ci-mcp" \
            >"$(log_file mcp)" 2>&1 &
        echo $! >"$(pid_file mcp)"
        echo "  started mcp (pid $!) http=${MCP_HTTP_ADDR}"
    fi

    wait_tcp "$MCP_HTTP_ADDR" mcp
    green "All servers running."
    echo ""
    echo "  Master gRPC : localhost${MASTER_GRPC_ADDR}"
    echo "  Master HTTP : http://localhost${MASTER_HTTP_ADDR}"
    echo "  MCP HTTP    : http://localhost${MCP_HTTP_ADDR}/mcp"
    echo "  Logs        : ${LOG_DIR}/"
}

# ── stop ──────────────────────────────────────────────────────────────────────

cmd_stop() {
    bold "Stopping servers..."
    stop_server mcp
    stop_server worker
    stop_server master
    green "Done."
}

# ── status ────────────────────────────────────────────────────────────────────

cmd_status() {
    bold "Server status:"
    for name in master worker mcp; do
        if is_running "$name"; then
            green "  $name  running (pid $(cat "$(pid_file "$name")"))"
        else
            red   "  $name  stopped"
        fi
    done
}

# ── logs ──────────────────────────────────────────────────────────────────────

cmd_logs() {
    local targets=()
    for name in master worker mcp; do
        lf=$(log_file "$name")
        [[ -f "$lf" ]] && targets+=("$lf")
    done
    if [[ ${#targets[@]} -eq 0 ]]; then
        echo "No log files found. Have you started the servers?"
        exit 1
    fi
    tail -f "${targets[@]}"
}

# ── main ──────────────────────────────────────────────────────────────────────

cd "$SCRIPT_DIR"

case "${1:-start}" in
    start)
        cmd_build
        cmd_start
        ;;
    stop)
        cmd_stop
        ;;
    restart)
        cmd_stop
        sleep 1
        cmd_build
        cmd_start
        ;;
    status)
        cmd_status
        ;;
    logs)
        cmd_logs
        ;;
    build)
        cmd_build
        ;;
    *)
        echo "Usage: $0 {start|stop|restart|status|logs|build}"
        exit 1
        ;;
esac
