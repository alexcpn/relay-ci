package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/ci-system/ci/pkg/dag"
	"github.com/ci-system/ci/pkg/scheduler"
	"github.com/ci-system/ci/pkg/scm"
)

// webhookHandler handles incoming git provider webhooks.
type webhookHandler struct {
	router *scm.Router
	sched  *scheduler.Scheduler
	logger *slog.Logger
	// configStore would look up WebhookConfig per repo in production.
	// For now, use a default secret.
	defaultSecret string
}

func newWebhookHandler(router *scm.Router, sched *scheduler.Scheduler, logger *slog.Logger, secret string) *webhookHandler {
	return &webhookHandler{
		router:        router,
		sched:         sched,
		logger:        logger,
		defaultSecret: secret,
	}
}

func (h *webhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	event, err := h.router.Parse(r, h.defaultSecret)
	if err != nil {
		h.logger.Warn("webhook parse error", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	h.logger.Info("webhook received",
		"provider", event.Provider,
		"type", event.Type,
		"id", event.ID,
	)

	switch event.Type {
	case scm.EventPush:
		h.handlePush(w, event)
	case scm.EventPullRequest:
		h.handlePR(w, event)
	case scm.EventComment:
		h.handleComment(w, event)
	default:
		http.Error(w, "unsupported event type", http.StatusBadRequest)
	}
}

func (h *webhookHandler) handlePush(w http.ResponseWriter, event *scm.Event) {
	push := event.Push
	buildID := generateID()

	g := dag.New()
	g.AddTask(&dag.Task{
		ID:             "build",
		Name:           "build",
		ContainerImage: "alpine:latest",
		Commands:       []string{"echo", "building " + push.AfterSHA},
		CPUMillicores:  1000,
		MemoryMB:       512,
		DiskMB:         2000,
	})
	g.Validate()

	build := &scheduler.Build{
		ID:          buildID,
		Graph:       g,
		RepoURL:     push.RepoURL,
		CommitSHA:   push.AfterSHA,
		Branch:      push.Ref,
		TriggeredBy: "webhook:" + push.Pusher,
	}

	if err := h.sched.SubmitBuild(build); err != nil {
		h.logger.Error("submit build failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.logger.Info("build submitted from push",
		"build_id", buildID,
		"repo", push.RepoFullName,
		"sha", push.AfterSHA,
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"build_id": buildID})
}

func (h *webhookHandler) handlePR(w http.ResponseWriter, event *scm.Event) {
	pr := event.PR

	// Only trigger on opened or synchronize (new commits pushed).
	if pr.Action != "opened" && pr.Action != "synchronize" && pr.Action != "reopened" {
		w.WriteHeader(http.StatusOK)
		return
	}

	buildID := generateID()

	g := dag.New()
	g.AddTask(&dag.Task{
		ID:             "build",
		Name:           "build",
		ContainerImage: "alpine:latest",
		Commands:       []string{"echo", "building PR " + pr.HeadSHA},
		CPUMillicores:  1000,
		MemoryMB:       512,
		DiskMB:         2000,
	})
	g.Validate()

	build := &scheduler.Build{
		ID:          buildID,
		Graph:       g,
		RepoURL:     pr.RepoURL,
		CommitSHA:   pr.HeadSHA,
		Branch:      pr.HeadBranch,
		PRNumber:    fmt.Sprintf("%d", pr.Number),
		TriggeredBy: "webhook:" + pr.Author,
	}

	if err := h.sched.SubmitBuild(build); err != nil {
		h.logger.Error("submit build failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.logger.Info("build submitted from PR",
		"build_id", buildID,
		"repo", pr.RepoFullName,
		"pr", pr.Number,
		"sha", pr.HeadSHA,
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"build_id": buildID})
}

func (h *webhookHandler) handleComment(w http.ResponseWriter, event *scm.Event) {
	comment := event.Comment

	// Handle commands like /retry.
	switch comment.Body {
	case "/retry":
		h.logger.Info("retry requested via comment",
			"repo", comment.RepoFullName,
			"pr", comment.PRNumber,
			"by", comment.Author,
		)
		// TODO: find last build for this PR and retry it.
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusOK) // ignore non-command comments
	}
}
