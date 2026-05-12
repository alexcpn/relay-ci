package review

import "os"

// ReviewPolicy controls which linters run, which AI model is called,
// and what threshold triggers a fail verdict.
type ReviewPolicy struct {
	// Linters to run during the lint task. Empty = auto-detect from language.
	Linters []string `json:"linters,omitempty"`

	// AI provider: "anthropic" (default), "openai", "ollama".
	Provider string `json:"provider,omitempty"`

	// Model name; defaults chosen per provider if empty.
	Model string `json:"model,omitempty"`

	// APIKeySecret is the secret name for the LLM API key.
	APIKeySecret string `json:"api_key_secret,omitempty"`

	// OllamaURL for Ollama provider.
	OllamaURL string `json:"ollama_url,omitempty"`

	// FailSeverity — lowest severity that causes a "fail" verdict.
	// Default: "high" (critical and high findings → fail).
	FailSeverity Severity `json:"fail_severity,omitempty"`

	// GeneratedCode enables extra generated-noise rules in the AI prompt.
	GeneratedCode bool `json:"generated_code,omitempty"`
}

// DefaultPolicy returns a ReviewPolicy populated from environment variables.
// This is the fallback when no per-request policy is provided.
func DefaultPolicy() ReviewPolicy {
	p := ReviewPolicy{
		Provider:     envOr("REVIEW_PROVIDER", "anthropic"),
		Model:        envOr("REVIEW_MODEL", ""),
		APIKeySecret: envOr("REVIEW_API_KEY_SECRET", "ANTHROPIC_API_KEY"),
		OllamaURL:    envOr("REVIEW_OLLAMA_URL", "http://localhost:11434"),
		FailSeverity: Severity(envOr("REVIEW_FAIL_SEVERITY", string(SeverityHigh))),
	}
	if p.Model == "" {
		switch p.Provider {
		case "openai":
			p.Model = "gpt-4o"
		case "ollama":
			p.Model = "llama3.2"
		default:
			p.Model = "claude-opus-4-7"
		}
	}
	return p
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
