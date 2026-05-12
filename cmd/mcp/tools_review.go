package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// masterHTTPBase returns the master's HTTP base URL.
// Uses CI_MASTER_HTTP if set, otherwise derives from CI_MASTER by
// swapping the port to 8080 (the default HTTP addr).
func masterHTTPBase() string {
	if v := os.Getenv("CI_MASTER_HTTP"); v != "" {
		return v
	}
	// Best-effort derivation: CI_MASTER is "host:grpcport", swap to http port.
	return "http://localhost:8080"
}

// httpCall makes an authenticated JSON call to the master's HTTP API.
func httpCall(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	base := masterHTTPBase()
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, base+path, reqBody)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok := os.Getenv("API_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("HTTP call to master: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return data, resp.StatusCode, err
}

// --- Review MCP tool definitions ---

var reviewToolDefs = []mcpToolDef{
	{
		Name:        "submit_review",
		Description: "Submit a code diff for structured review. Runs linters (in-process) and AI analysis. Returns immediately with a review_id — poll get_review until state=='done'.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"diff":       {"type": "string", "description": "Unified diff of changed files"},
				"language":   {"type": "string", "description": "Primary language: go, python, typescript, javascript, shell (auto-detected if omitted)"},
				"session_id": {"type": "string", "description": "Group review iterations under one session for diff_reviews tracking"},
				"context":    {"type": "string", "description": "Set to 'generated' to enable extra generated-code-noise rules"}
			},
			"required": ["diff"]
		}`),
	},
	{
		Name:        "get_review",
		Description: "Poll the status and findings for a review. Call repeatedly until state=='done'. Findings include file, line, severity, rule, message, and a copy-pasteable suggestion.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"review_id": {"type": "string", "description": "The review ID from submit_review"}
			},
			"required": ["review_id"]
		}`),
	},
	{
		Name:        "diff_reviews",
		Description: "Compare two review iterations to see what improved and what regressed. Essential for agent convergence tracking: fixed[] means you fixed those issues, regressed[] means a change made something worse.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"review_id_a": {"type": "string", "description": "Earlier review ID (baseline)"},
				"review_id_b": {"type": "string", "description": "Later review ID (after your fix)"}
			},
			"required": ["review_id_a", "review_id_b"]
		}`),
	},
	{
		Name:        "list_reviews",
		Description: "List recent reviews, optionally filtered to a session. Shows state, verdict, iteration number, and timestamp for each.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"session_id": {"type": "string", "description": "Filter to a specific session (optional)"},
				"limit":      {"type": "integer", "description": "Max results (default 20)"}
			}
		}`),
	},
}

// --- Tool implementations ---

func (s *mcpServer) toolSubmitReview(ctx context.Context, args json.RawMessage) *mcpToolResult {
	var params struct {
		Diff      string `json:"diff"`
		Language  string `json:"language"`
		SessionID string `json:"session_id"`
		Context   string `json:"context"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return errorResult("invalid arguments: " + err.Error())
	}
	if params.Diff == "" {
		return errorResult("diff is required")
	}

	body := map[string]any{
		"diff":       params.Diff,
		"language":   params.Language,
		"session_id": params.SessionID,
		"context":    params.Context,
	}
	data, status, err := httpCall(ctx, http.MethodPost, "/api/v1/reviews", body)
	if err != nil {
		return errorResult("submit_review: " + err.Error())
	}
	if status >= 400 {
		return errorResult(fmt.Sprintf("master returned %d: %s", status, string(data)))
	}

	var resp struct {
		ReviewID string `json:"review_id"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return errorResult("parse response: " + err.Error())
	}

	return textResult(fmt.Sprintf("Review submitted.\n\n**Review ID:** %s\n\nUse `get_review` to poll until state=='done'.", resp.ReviewID))
}

func (s *mcpServer) toolGetReview(ctx context.Context, args json.RawMessage) *mcpToolResult {
	var params struct {
		ReviewID string `json:"review_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return errorResult("invalid arguments: " + err.Error())
	}

	data, status, err := httpCall(ctx, http.MethodGet, "/api/v1/reviews/"+params.ReviewID, nil)
	if err != nil {
		return errorResult("get_review: " + err.Error())
	}
	if status == 404 {
		return errorResult("review not found: " + params.ReviewID)
	}
	if status >= 400 {
		return errorResult(fmt.Sprintf("master returned %d: %s", status, string(data)))
	}

	var rec struct {
		State    string `json:"state"`
		Verdict  string `json:"verdict"`
		Summary  string `json:"summary"`
		Iteration int   `json:"iteration"`
		Findings []struct {
			File         string `json:"file"`
			Line         int    `json:"line"`
			Severity     string `json:"severity"`
			Rule         string `json:"rule"`
			Tool         string `json:"tool"`
			Message      string `json:"message"`
			Suggestion   string `json:"suggestion"`
			Category     string `json:"category"`
			FunctionName string `json:"function_name,omitempty"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return errorResult("parse response: " + err.Error())
	}

	if rec.State != "done" {
		return textResult(fmt.Sprintf("Review %s is **%s** — call `get_review` again to check for updates.", params.ReviewID, rec.State))
	}

	var sb fmt.Stringer
	type writer struct{ b []byte }
	w := &struct{ s string }{}
	appendf := func(format string, a ...any) { w.s += fmt.Sprintf(format, a...) }

	appendf("## Review: %s\n", params.ReviewID)
	appendf("**Verdict:** %s  **Iteration:** %d\n\n", rec.Verdict, rec.Iteration)
	if rec.Summary != "" {
		appendf("**Summary:** %s\n\n", rec.Summary)
	}
	if len(rec.Findings) == 0 {
		appendf("No findings — code looks clean.\n")
	} else {
		appendf("### Findings (%d)\n\n", len(rec.Findings))
		for i, f := range rec.Findings {
			appendf("%d. **[%s/%s]** `%s:%d` — %s\n", i+1, f.Severity, f.Category, f.File, f.Line, f.Message)
			if f.FunctionName != "" {
				appendf("   *in `%s`*\n", f.FunctionName)
			}
			if f.Suggestion != "" {
				appendf("   > %s\n", f.Suggestion)
			}
		}
	}
	_ = sb
	return textResult(w.s)
}

func (s *mcpServer) toolDiffReviews(ctx context.Context, args json.RawMessage) *mcpToolResult {
	var params struct {
		ReviewIDA string `json:"review_id_a"`
		ReviewIDB string `json:"review_id_b"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return errorResult("invalid arguments: " + err.Error())
	}

	path := fmt.Sprintf("/api/v1/reviews/%s/diff/%s", params.ReviewIDA, params.ReviewIDB)
	data, status, err := httpCall(ctx, http.MethodGet, path, nil)
	if err != nil {
		return errorResult("diff_reviews: " + err.Error())
	}
	if status >= 400 {
		return errorResult(fmt.Sprintf("master returned %d: %s", status, string(data)))
	}

	var result struct {
		Fixed     []any `json:"fixed"`
		Regressed []any `json:"regressed"`
		New       []any `json:"new"`
		Unchanged []any `json:"unchanged"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return errorResult("parse response: " + err.Error())
	}

	text := fmt.Sprintf("## Review diff: %s → %s\n\n", params.ReviewIDA, params.ReviewIDB)
	text += fmt.Sprintf("✅ Fixed: %d  ❌ Regressed: %d  🆕 New: %d  ↔ Unchanged: %d\n",
		len(result.Fixed), len(result.Regressed), len(result.New), len(result.Unchanged))
	if len(result.Regressed) > 0 {
		text += "\n⚠️ Some issues got worse — check `regressed` findings in the raw response."
	}
	return textResult(text)
}

func (s *mcpServer) toolListReviews(ctx context.Context, args json.RawMessage) *mcpToolResult {
	var params struct {
		SessionID string `json:"session_id"`
		Limit     int    `json:"limit"`
	}
	json.Unmarshal(args, &params)
	if params.Limit == 0 {
		params.Limit = 20
	}

	path := fmt.Sprintf("/api/v1/reviews?limit=%d", params.Limit)
	if params.SessionID != "" {
		path += "&session_id=" + params.SessionID
	}
	data, status, err := httpCall(ctx, http.MethodGet, path, nil)
	if err != nil {
		return errorResult("list_reviews: " + err.Error())
	}
	if status >= 400 {
		return errorResult(fmt.Sprintf("master returned %d: %s", status, string(data)))
	}

	var resp struct {
		Reviews []struct {
			ID        string `json:"id"`
			State     string `json:"state"`
			Verdict   string `json:"verdict"`
			Language  string `json:"language"`
			Iteration int    `json:"iteration"`
			CreatedAt string `json:"created_at"`
		} `json:"reviews"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return errorResult("parse response: " + err.Error())
	}

	if len(resp.Reviews) == 0 {
		return textResult("No reviews found.")
	}

	text := fmt.Sprintf("## Reviews (%d)\n\n", len(resp.Reviews))
	for _, r := range resp.Reviews {
		verdict := r.Verdict
		if verdict == "" {
			verdict = r.State
		}
		text += fmt.Sprintf("- `%s` | %s | iter %d | %s | %s\n",
			r.ID, r.Language, r.Iteration, verdict, r.CreatedAt)
	}
	return textResult(text)
}
