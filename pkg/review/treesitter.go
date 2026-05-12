package review

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// TreeContext holds the structural information extracted from one file.
type TreeContext struct {
	File      string   `json:"file"`
	Functions []string `json:"functions,omitempty"`
	Imports   []string `json:"imports,omitempty"`
	Types     []string `json:"types,omitempty"`
}

// TreeSitterClient calls an external tree-sitter MCP server to enrich diffs
// with AST-level context (function signatures, imports, type names). It is
// entirely optional: if TREESITTER_MCP_ADDR is unset or the server is
// unreachable, all calls are no-ops and the review continues without enrichment.
type TreeSitterClient struct {
	addr      string // e.g. "http://localhost:8090/mcp"
	sessionID string
	http      *http.Client
}

// NewTreeSitterClient reads TREESITTER_MCP_ADDR from env and initialises a
// session. Returns a no-op client if the address is unset.
func NewTreeSitterClient() *TreeSitterClient {
	addr := os.Getenv("TREESITTER_MCP_ADDR")
	c := &TreeSitterClient{
		addr: addr,
		http: &http.Client{Timeout: 10 * time.Second},
	}
	if addr == "" {
		return c
	}
	// Best-effort initialize; errors leave sessionID empty → no-op calls.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.initialize(ctx)
	return c
}

// Enrich extracts tree context for each changed file in the diff.
// Returns an empty slice if the server is unavailable.
func (c *TreeSitterClient) Enrich(ctx context.Context, diff string) []TreeContext {
	if c.addr == "" || c.sessionID == "" {
		return nil
	}
	files := changedFilesFromDiff(diff)
	newContent := newContentByFile(diff)

	var out []TreeContext
	for _, f := range files {
		content, ok := newContent[f]
		if !ok || content == "" {
			continue
		}
		lang := extToLanguage(f)
		if lang == "" {
			continue
		}
		tc, err := c.analyseFile(ctx, f, content, lang)
		if err != nil {
			continue
		}
		out = append(out, tc)
	}
	return out
}

// FormatContext renders TreeContext slices as a prompt-ready XML block.
func FormatContext(contexts []TreeContext) string {
	if len(contexts) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<code_structure>\n")
	for _, tc := range contexts {
		sb.WriteString("File: ")
		sb.WriteString(tc.File)
		sb.WriteString("\n")
		if len(tc.Functions) > 0 {
			sb.WriteString("  Functions:\n")
			for _, fn := range tc.Functions {
				sb.WriteString("    ")
				sb.WriteString(fn)
				sb.WriteString("\n")
			}
		}
		if len(tc.Imports) > 0 {
			sb.WriteString("  Imports: ")
			sb.WriteString(strings.Join(tc.Imports, ", "))
			sb.WriteString("\n")
		}
		if len(tc.Types) > 0 {
			sb.WriteString("  Types: ")
			sb.WriteString(strings.Join(tc.Types, ", "))
			sb.WriteString("\n")
		}
	}
	sb.WriteString("</code_structure>\n")
	return sb.String()
}

// --- MCP protocol helpers ---

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpResponse struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  *mcpError       `json:"error,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *TreeSitterClient) initialize(ctx context.Context) error {
	params, _ := json.Marshal(map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "relay-ci-review", "version": "1.0"},
	})
	req := mcpRequest{JSONRPC: "2.0", ID: 1, Method: "initialize", Params: params}
	var resp mcpResponse
	if err := c.call(ctx, req, &resp); err != nil {
		return err
	}
	// Read session ID from response header — stored during call.
	return nil
}

func (c *TreeSitterClient) analyseFile(ctx context.Context, file, content, lang string) (TreeContext, error) {
	tc := TreeContext{File: file}

	// Try get_functions tool.
	fnArgs, _ := json.Marshal(map[string]string{"code": content, "language": lang})
	callParams, _ := json.Marshal(map[string]any{
		"name":      "get_functions",
		"arguments": json.RawMessage(fnArgs),
	})
	req := mcpRequest{JSONRPC: "2.0", ID: 2, Method: "tools/call", Params: callParams}
	var resp mcpResponse
	if err := c.call(ctx, req, &resp); err == nil {
		tc.Functions = extractTextLines(resp.Result)
	}

	// Try get_imports tool.
	impArgs, _ := json.Marshal(map[string]string{"code": content, "language": lang})
	callParams, _ = json.Marshal(map[string]any{
		"name":      "get_imports",
		"arguments": json.RawMessage(impArgs),
	})
	req.ID = 3
	req.Params = callParams
	if err := c.call(ctx, req, &resp); err == nil {
		tc.Imports = extractTextLines(resp.Result)
	}

	return tc, nil
}

func (c *TreeSitterClient) call(ctx context.Context, req mcpRequest, out *mcpResponse) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.addr, bytes.NewReader(body))
	if err != nil {
		return err
	}
	hr.Header.Set("Content-Type", "application/json")
	if c.sessionID != "" {
		hr.Header.Set("Mcp-Session-Id", c.sessionID)
	}

	resp, err := c.http.Do(hr)
	if err != nil {
		return fmt.Errorf("tree-sitter MCP call: %w", err)
	}
	defer resp.Body.Close()

	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.sessionID = sid
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode MCP response: %w", err)
	}
	if out.Error != nil {
		return fmt.Errorf("MCP error %d: %s", out.Error.Code, out.Error.Message)
	}
	return nil
}

// --- diff parsing helpers ---

func changedFilesFromDiff(diff string) []string {
	var files []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+++ b/") {
			f := strings.TrimPrefix(line, "+++ b/")
			if !seen[f] {
				seen[f] = true
				files = append(files, f)
			}
		}
	}
	return files
}

func newContentByFile(diff string) map[string]string {
	result := make(map[string]string)
	var curFile string
	var lines []string
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+++ b/") {
			if curFile != "" {
				result[curFile] = strings.Join(lines, "\n")
			}
			curFile = strings.TrimPrefix(line, "+++ b/")
			lines = nil
			continue
		}
		if curFile != "" && strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			lines = append(lines, line[1:])
		} else if curFile != "" && !strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "@@") {
			lines = append(lines, line)
		}
	}
	if curFile != "" {
		result[curFile] = strings.Join(lines, "\n")
	}
	return result
}

func extToLanguage(path string) string {
	switch {
	case strings.HasSuffix(path, ".go"):
		return "go"
	case strings.HasSuffix(path, ".py"):
		return "python"
	case strings.HasSuffix(path, ".ts"), strings.HasSuffix(path, ".tsx"):
		return "typescript"
	case strings.HasSuffix(path, ".js"), strings.HasSuffix(path, ".jsx"):
		return "javascript"
	case strings.HasSuffix(path, ".rs"):
		return "rust"
	}
	return ""
}

// extractTextLines pulls string values from a MCP tool result JSON.
// Handles both array-of-strings and {content:[{text:"..."}]} shapes.
func extractTextLines(raw json.RawMessage) []string {
	if raw == nil {
		return nil
	}
	// Try array of strings first.
	var arr []string
	if json.Unmarshal(raw, &arr) == nil {
		return arr
	}
	// Try MCP content shape.
	var content struct {
		Content []struct{ Text string `json:"text"` } `json:"content"`
	}
	if json.Unmarshal(raw, &content) == nil {
		var out []string
		for _, c := range content.Content {
			if c.Text != "" {
				out = append(out, c.Text)
			}
		}
		return out
	}
	return nil
}
