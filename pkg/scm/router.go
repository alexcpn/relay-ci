package scm

import (
	"fmt"
	"net/http"
)

// Router routes incoming webhooks to the correct provider parser.
type Router struct {
	providers map[Provider]Client
}

// NewRouter creates a webhook router with the given provider clients.
func NewRouter(clients ...Client) *Router {
	r := &Router{providers: make(map[Provider]Client)}
	for _, c := range clients {
		r.providers[c.Provider()] = c
	}
	return r
}

// Parse detects the provider from request headers and delegates to
// the appropriate parser.
func (r *Router) Parse(req *http.Request, secret string) (*Event, error) {
	provider := r.detectProvider(req)
	client, ok := r.providers[provider]
	if !ok {
		return nil, fmt.Errorf("unsupported provider: %s", provider)
	}
	return client.Parse(req, secret)
}

// GetClient returns the client for a specific provider.
func (r *Router) GetClient(p Provider) (Client, bool) {
	c, ok := r.providers[p]
	return c, ok
}

func (r *Router) detectProvider(req *http.Request) Provider {
	if req.Header.Get("X-GitHub-Event") != "" {
		return ProviderGitHub
	}
	if req.Header.Get("X-Gitlab-Event") != "" {
		return ProviderGitLab
	}
	return ProviderUnknown
}
