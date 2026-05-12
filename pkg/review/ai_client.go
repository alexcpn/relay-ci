package review

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// staticSystemPrompt is the review instruction sent to the LLM. It is identical
// across all calls from this binary so Anthropic can cache it across requests.
const staticSystemPrompt = `You are a code review engine. Output ONLY a single JSON object — no prose, no markdown fences, no explanations outside the JSON.

Schema:
{
  "verdict": "pass" | "fail" | "warn",
  "summary": "<one sentence>",
  "findings": [
    {
      "file": "<repo-relative path>",
      "line": <integer, 1-based; 0 = file-level>,
      "col": <integer, 0 if unknown>,
      "severity": "critical" | "high" | "medium" | "low" | "info",
      "rule": "<short-tag, e.g. missing-error-check>",
      "category": "style" | "correctness" | "security" | "performance" | "maintainability" | "generated-noise",
      "message": "<what is wrong>",
      "suggestion": "<copy-pasteable fix or concrete action — REQUIRED, never vague>"
    }
  ]
}

Rules:
- "suggestion" is REQUIRED for every finding. It must be copy-pasteable code or a specific action.
- "verdict" is "fail" only when there is at least one critical or high finding.
- "verdict" is "warn" when there are medium findings and no critical/high.
- Do NOT duplicate findings already listed in <lint_findings>.
- Flag "generated-noise" category for: comments that restate the code, unused variables, copy-paste artifacts, overly verbose naming, missing idiomatic patterns.
- Output exactly one top-level JSON object. Nothing else.`

// ReviewClient calls an LLM to produce AI findings for a diff.
type ReviewClient interface {
	Review(ctx context.Context, reviewID, diff string, lintFindings []Finding, treeCtx []TreeContext, policy ReviewPolicy) ([]Finding, string, string, error)
}

// NewReviewClient returns the appropriate ReviewClient based on policy.Provider.
func NewReviewClient() ReviewClient {
	policy := DefaultPolicy()
	switch policy.Provider {
	case "openai":
		return &openAIReviewClient{}
	case "ollama":
		return &ollamaReviewClient{}
	default:
		return &anthropicReviewClient{}
	}
}

// --- Anthropic ---

type anthropicReviewClient struct{}

type anthropicMsg struct {
	Role    string              `json:"role"`
	Content []anthropicContent  `json:"content"`
}

type anthropicContent struct {
	Type         string            `json:"type"`
	Text         string            `json:"text"`
	CacheControl *anthropicCache   `json:"cache_control,omitempty"`
}

type anthropicCache struct {
	Type string `json:"type"` // "ephemeral"
}

type anthropicRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	System    []anthropicContent `json:"system"`
	Messages  []anthropicMsg  `json:"messages"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (c *anthropicReviewClient) Review(ctx context.Context, reviewID, diff string, lintFindings []Finding, treeCtx []TreeContext, policy ReviewPolicy) ([]Finding, string, string, error) {
	apiKey := os.Getenv(policy.APIKeySecret)
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if apiKey == "" {
		return nil, "", "", fmt.Errorf("ANTHROPIC_API_KEY not set")
	}

	userMsg := buildUserMessage(lintFindings, treeCtx, diff, policy)
	reqBody := anthropicRequest{
		Model:     policy.Model,
		MaxTokens: 4096,
		// System prompt with cache_control so Anthropic can cache it across
		// repeated review submissions in the same agent session.
		System: []anthropicContent{
			{Type: "text", Text: staticSystemPrompt, CacheControl: &anthropicCache{Type: "ephemeral"}},
		},
		Messages: []anthropicMsg{
			{Role: "user", Content: []anthropicContent{{Type: "text", Text: userMsg}}},
		},
	}
	body, _ := json.Marshal(reqBody)

	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, "", "", err
	}
	hr.Header.Set("x-api-key", apiKey)
	hr.Header.Set("anthropic-version", "2023-06-01")
	hr.Header.Set("anthropic-beta", "prompt-caching-2024-07-31")
	hr.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(hr)
	if err != nil {
		return nil, "", "", fmt.Errorf("anthropic API: %w", err)
	}
	defer resp.Body.Close()

	var ar anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return nil, "", "", fmt.Errorf("decode anthropic response: %w", err)
	}
	if ar.Error != nil {
		return nil, "", "", fmt.Errorf("anthropic error: %s", ar.Error.Message)
	}
	if len(ar.Content) == 0 {
		return nil, "", "", fmt.Errorf("anthropic returned empty content")
	}
	return parseAIResponse(ar.Content[0].Text, reviewID)
}

// --- OpenAI ---

type openAIReviewClient struct{}

func (c *openAIReviewClient) Review(ctx context.Context, reviewID, diff string, lintFindings []Finding, treeCtx []TreeContext, policy ReviewPolicy) ([]Finding, string, string, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return nil, "", "", fmt.Errorf("OPENAI_API_KEY not set")
	}
	userMsg := buildUserMessage(lintFindings, treeCtx, diff, policy)
	reqBody := map[string]any{
		"model":      policy.Model,
		"max_tokens": 4096,
		"messages": []map[string]string{
			{"role": "system", "content": staticSystemPrompt},
			{"role": "user", "content": userMsg},
		},
	}
	body, _ := json.Marshal(reqBody)
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, "", "", err
	}
	hr.Header.Set("Authorization", "Bearer "+apiKey)
	hr.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(hr)
	if err != nil {
		return nil, "", "", err
	}
	defer resp.Body.Close()
	var result struct {
		Choices []struct {
			Message struct{ Content string `json:"content"` } `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, "", "", err
	}
	if len(result.Choices) == 0 {
		return nil, "", "", fmt.Errorf("openai returned no choices")
	}
	return parseAIResponse(result.Choices[0].Message.Content, reviewID)
}

// --- Ollama ---

type ollamaReviewClient struct{}

func (c *ollamaReviewClient) Review(ctx context.Context, reviewID, diff string, lintFindings []Finding, treeCtx []TreeContext, policy ReviewPolicy) ([]Finding, string, string, error) {
	baseURL := policy.OllamaURL
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	userMsg := buildUserMessage(lintFindings, treeCtx, diff, policy)
	reqBody := map[string]any{
		"model":  policy.Model,
		"stream": false,
		"messages": []map[string]string{
			{"role": "system", "content": staticSystemPrompt},
			{"role": "user", "content": userMsg},
		},
	}
	body, _ := json.Marshal(reqBody)
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, "", "", err
	}
	hr.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 300 * time.Second}
	resp, err := client.Do(hr)
	if err != nil {
		return nil, "", "", err
	}
	defer resp.Body.Close()
	var result struct {
		Message struct{ Content string `json:"content"` } `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, "", "", err
	}
	return parseAIResponse(result.Message.Content, reviewID)
}

// --- shared helpers ---

func buildUserMessage(lintFindings []Finding, treeCtx []TreeContext, diff string, policy ReviewPolicy) string {
	var sb strings.Builder

	if ctxStr := FormatContext(treeCtx); ctxStr != "" {
		sb.WriteString(ctxStr)
		sb.WriteString("\n")
	}

	if len(lintFindings) > 0 {
		lintJSON, _ := json.MarshalIndent(lintFindings, "", "  ")
		sb.WriteString("<lint_findings>\n")
		sb.Write(lintJSON)
		sb.WriteString("\n</lint_findings>\n\n")
	}

	if policy.GeneratedCode {
		sb.WriteString("<context>This code was AI-generated. Pay extra attention to generated-noise issues: comments that restate the code, copy-paste artifacts, missing idiomatic patterns, and overly verbose naming.</context>\n\n")
	}

	sb.WriteString("<diff>\n")
	sb.WriteString(diff)
	sb.WriteString("\n</diff>")
	return sb.String()
}

// aiReviewOutput is the JSON shape the LLM must produce.
type aiReviewOutput struct {
	Verdict  string      `json:"verdict"`
	Summary  string      `json:"summary"`
	Findings []aiFinding `json:"findings"`
}

type aiFinding struct {
	File       string `json:"file"`
	Line       int    `json:"line"`
	Col        int    `json:"col"`
	Severity   string `json:"severity"`
	Rule       string `json:"rule"`
	Category   string `json:"category"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion"`
}

// parseAIResponse strips markdown fences and unmarshals the JSON output.
// One silent retry with explicit instruction if the first parse fails.
func parseAIResponse(text, reviewID string) ([]Finding, string, string, error) {
	findings, verdict, summary, err := tryParseAI(text, reviewID)
	if err != nil {
		// One retry: strip everything outside the first { … }.
		if i := strings.Index(text, "{"); i >= 0 {
			if j := strings.LastIndex(text, "}"); j > i {
				findings, verdict, summary, err = tryParseAI(text[i:j+1], reviewID)
			}
		}
	}
	return findings, verdict, summary, err
}

func tryParseAI(text, reviewID string) ([]Finding, string, string, error) {
	// Strip markdown fences if present.
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	var out aiReviewOutput
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return nil, "", "", fmt.Errorf("AI response parse: %w — text: %.200s", err, text)
	}

	var findings []Finding
	for _, af := range out.Findings {
		b := make([]byte, 5)
		rand.Read(b)
		findings = append(findings, Finding{
			ID:         hex.EncodeToString(b),
			ReviewID:   reviewID,
			File:       af.File,
			Line:       af.Line,
			Col:        af.Col,
			Severity:   Severity(af.Severity),
			Rule:       af.Rule,
			Tool:       "ai-review",
			Message:    af.Message,
			Suggestion: af.Suggestion,
			Category:   Category(af.Category),
			Status:     "open",
		})
	}
	return findings, out.Verdict, out.Summary, nil
}
