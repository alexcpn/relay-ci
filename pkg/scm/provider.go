package scm

import (
	"context"
	"net/http"
)

// WebhookParser parses provider-specific webhook payloads into canonical Events.
type WebhookParser interface {
	// Parse reads the HTTP request and returns a canonical Event.
	// It verifies the webhook signature using the provided secret.
	Parse(r *http.Request, secret string) (*Event, error)
}

// StatusReporter reports build status back to the git provider.
type StatusReporter interface {
	// ReportStatus updates the commit status on the provider.
	ReportStatus(ctx context.Context, token string, report StatusReport) error
}

// Client combines webhook parsing and status reporting for a provider.
type Client interface {
	WebhookParser
	StatusReporter
	Provider() Provider
}
