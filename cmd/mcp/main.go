package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/ci-system/ci/gen/ci/v1"
)

// MCP Server for CI System
// Supports two transports:
//   - stdio:  JSON-RPC 2.0 over stdin/stdout (default, for local MCP clients)
//   - HTTP:   Streamable HTTP transport (set MCP_HTTP_ADDR, for remote agents)
// Connects to the ci-master via gRPC to expose CI operations as tools.

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	masterAddr := envOrDefault("CI_MASTER", "localhost:9090")
	httpAddr := os.Getenv("MCP_HTTP_ADDR") // e.g. ":8081" or "0.0.0.0:8081"

	conn, err := grpc.NewClient(masterAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		logger.Error("failed to connect to master", "addr", masterAddr, "err", err)
		os.Exit(1)
	}
	defer conn.Close()

	server := &mcpServer{
		scheduler: pb.NewSchedulerServiceClient(conn),
		logs:      pb.NewLogServiceClient(conn),
		logger:    logger,
	}

	if httpAddr != "" {
		// HTTP mode — remote agents connect over HTTP.
		transport := newHTTPTransport(server, logger)

		mux := http.NewServeMux()
		mux.Handle("/mcp", transport)
		mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"status":"ok"}`))
		})

		logger.Info("MCP HTTP server starting", "addr", httpAddr, "master", masterAddr)
		if err := http.ListenAndServe(httpAddr, mux); err != nil {
			logger.Error("HTTP server failed", "err", err)
			os.Exit(1)
		}
	} else {
		// stdio mode — local MCP clients (Claude Desktop, etc.)
		logger.Info("MCP stdio server started", "master", masterAddr)
		server.run()
	}
}

type mcpServer struct {
	scheduler pb.SchedulerServiceClient
	logs      pb.LogServiceClient
	logger    *slog.Logger
}

// --- JSON-RPC 2.0 types ---

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// --- MCP protocol types ---

type mcpToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpToolResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

func (s *mcpServer) run() {
	scanner := bufio.NewScanner(os.Stdin)
	// Increase buffer for large messages.
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			s.logger.Error("invalid JSON-RPC request", "err", err)
			continue
		}

		resp := s.handleRequest(req)
		if resp != nil {
			data, _ := json.Marshal(resp)
			fmt.Fprintf(os.Stdout, "%s\n", data)
		}
	}
}

func (s *mcpServer) handleRequest(req jsonRPCRequest) *jsonRPCResponse {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "initialized":
		return nil // notification, no response
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(req)
	case "ping":
		return &jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]interface{}{}}
	default:
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonRPCError{Code: -32601, Message: "method not found: " + req.Method},
		}
	}
}

func (s *mcpServer) handleInitialize(req jsonRPCRequest) *jsonRPCResponse {
	return &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{},
			},
			"serverInfo": map[string]interface{}{
				"name":    "ci-system-mcp",
				"version": "1.0.0",
			},
		},
	}
}

func (s *mcpServer) handleToolsList(req jsonRPCRequest) *jsonRPCResponse {
	tools := []mcpToolDef{
		{
			Name:        "list_builds",
			Description: "List all CI builds with their status. Shows build ID, state (QUEUED/RUNNING/PASSED/FAILED), repo, branch, and who triggered it. Use this first to get an overview of what's happening.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"limit": {"type": "integer", "description": "Max builds to return (default 20)"},
					"state": {"type": "string", "description": "Filter by state: BUILD_STATE_QUEUED, BUILD_STATE_RUNNING, BUILD_STATE_PASSED, BUILD_STATE_FAILED"}
				}
			}`),
		},
		{
			Name:        "get_build",
			Description: "Get detailed status of a specific build including all its tasks, their states, exit codes, and durations. Shows the full DAG execution status.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"build_id": {"type": "string", "description": "The build ID"}
				},
				"required": ["build_id"]
			}`),
		},
		{
			Name:        "get_task_logs",
			Description: "Get log output for a specific task in a build. Returns stdout, stderr, and system messages. Use this to diagnose why a task failed — look for compilation errors, test failures, linter warnings, etc.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"build_id": {"type": "string", "description": "The build ID"},
					"task_id": {"type": "string", "description": "The task ID within the build"},
					"limit": {"type": "integer", "description": "Max number of log lines (default: all)"},
					"tail": {"type": "integer", "description": "Return only the last N lines (useful for large logs)"}
				},
				"required": ["build_id", "task_id"]
			}`),
		},
		{
			Name:        "diagnose_build",
			Description: "Analyze a failed build and provide a structured diagnosis. Returns: which tasks failed, their error logs, the dependency chain that was affected, and which tasks were skipped as a result. This is the primary tool for understanding build failures.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"build_id": {"type": "string", "description": "The build ID to diagnose"}
				},
				"required": ["build_id"]
			}`),
		},
		{
			Name:        "submit_build",
			Description: "Submit a new CI build for a git repository. Triggers the pipeline defined in the repo's pipeline.yaml. Use this to re-run builds after pushing fixes.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"repo_url": {"type": "string", "description": "Git repository URL (e.g. https://github.com/org/repo.git)"},
					"branch": {"type": "string", "description": "Branch to build (default: main)"},
					"commit_sha": {"type": "string", "description": "Specific commit SHA to build (default: HEAD)"}
				},
				"required": ["repo_url"]
			}`),
		},
		{
			Name:        "cancel_build",
			Description: "Cancel a running build. All in-flight tasks will be killed and pending tasks will be skipped.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"build_id": {"type": "string", "description": "The build ID to cancel"}
				},
				"required": ["build_id"]
			}`),
		},
		{
			Name:        "retry_build",
			Description: "Retry a failed build. Re-runs only the failed tasks — passed tasks are not re-executed.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"build_id": {"type": "string", "description": "The build ID to retry"},
					"from_scratch": {"type": "boolean", "description": "If true, re-run all tasks (not just failed ones)"}
				},
				"required": ["build_id"]
			}`),
		},
		{
			Name:        "get_failed_builds",
			Description: "Get all currently failed builds with a summary of what went wrong in each. Useful for an agent monitoring CI health and looking for builds that need fixes.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"limit": {"type": "integer", "description": "Max builds to return (default 10)"}
				}
			}`),
		},
		{
			Name:        "suggest_fix",
			Description: "Analyze a failed task's logs and suggest a fix. Returns the error type (compilation, test failure, lint, dependency, timeout), the relevant error lines, the file and line numbers involved, and a suggested corrective action.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"build_id": {"type": "string", "description": "The build ID"},
					"task_id": {"type": "string", "description": "The failed task ID to analyze"}
				},
				"required": ["build_id", "task_id"]
			}`),
		},
		{
			Name:        "watch_build",
			Description: "Get the current state of a build and whether it is still in progress. Returns task states and overall build progress as a percentage.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"build_id": {"type": "string", "description": "The build ID to watch"}
				},
				"required": ["build_id"]
			}`),
		},
	}

	return &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]interface{}{"tools": tools},
	}
}

func (s *mcpServer) handleToolsCall(req jsonRPCRequest) *jsonRPCResponse {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonRPCError{Code: -32602, Message: "invalid params: " + err.Error()},
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var result *mcpToolResult
	switch params.Name {
	case "list_builds":
		result = s.toolListBuilds(ctx, params.Arguments)
	case "get_build":
		result = s.toolGetBuild(ctx, params.Arguments)
	case "get_task_logs":
		result = s.toolGetTaskLogs(ctx, params.Arguments)
	case "diagnose_build":
		result = s.toolDiagnoseBuild(ctx, params.Arguments)
	case "submit_build":
		result = s.toolSubmitBuild(ctx, params.Arguments)
	case "cancel_build":
		result = s.toolCancelBuild(ctx, params.Arguments)
	case "retry_build":
		result = s.toolRetryBuild(ctx, params.Arguments)
	case "get_failed_builds":
		result = s.toolGetFailedBuilds(ctx, params.Arguments)
	case "suggest_fix":
		result = s.toolSuggestFix(ctx, params.Arguments)
	case "watch_build":
		result = s.toolWatchBuild(ctx, params.Arguments)
	default:
		result = errorResult("unknown tool: " + params.Name)
	}

	return &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func textResult(text string) *mcpToolResult {
	return &mcpToolResult{
		Content: []mcpContent{{Type: "text", Text: text}},
	}
}

func errorResult(text string) *mcpToolResult {
	return &mcpToolResult{
		Content: []mcpContent{{Type: "text", Text: text}},
		IsError: true,
	}
}


