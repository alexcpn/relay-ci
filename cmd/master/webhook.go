package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	pb "github.com/ci-system/ci/gen/ci/v1"
	"github.com/ci-system/ci/pkg/observability"
	"github.com/ci-system/ci/pkg/scheduler"
	"github.com/ci-system/ci/pkg/scm"
	"github.com/ci-system/ci/pkg/secrets"
)

// webhookHandler handles incoming git provider webhooks.
type webhookHandler struct {
	router        *scm.Router
	sched         *scheduler.Scheduler
	logger        *slog.Logger
	defaultSecret string
	secretStore   *secrets.Store
	publicURL     string // base URL of this master, e.g. "http://ci.example.com:8080" (optional)
}

func newWebhookHandler(router *scm.Router, sched *scheduler.Scheduler, logger *slog.Logger, secret string, secretStore *secrets.Store, publicURL string) *webhookHandler {
	return &webhookHandler{
		router:        router,
		sched:         sched,
		logger:        logger,
		defaultSecret: secret,
		secretStore:   secretStore,
		publicURL:     publicURL,
	}
}

// branchName strips the refs/heads/ prefix from a git ref, returning
// just the branch name suitable for git clone --branch.
func branchName(ref string) string {
	return strings.TrimPrefix(ref, "refs/heads/")
}

// lookupToken finds the SCM access token for the given provider.
// It tries a repo-scoped secret first (scope = repoFullName), then falls back to global.
func (h *webhookHandler) lookupToken(provider scm.Provider, repoFullName string) string {
	var name string
	switch provider {
	case scm.ProviderGitHub:
		name = "GITHUB_TOKEN"
	case scm.ProviderGitLab:
		name = "GITLAB_TOKEN"
	default:
		return ""
	}
	if tok, err := h.secretStore.Get(repoFullName, name); err == nil {
		return tok
	}
	if tok, err := h.secretStore.Get("global", name); err == nil {
		return tok
	}
	return ""
}

// reportStatus posts a commit status to the SCM provider.
// Silently skips if the build has no token or commit SHA.
func (h *webhookHandler) reportStatus(ctx context.Context, build *scheduler.Build, state scm.StatusState, description string) {
	if build.SCMToken == "" || build.CommitSHA == "" || build.RepoFullName == "" {
		return
	}
	client, ok := h.router.GetClient(build.SCMProvider)
	if !ok {
		return
	}
	var targetURL string
	if h.publicURL != "" {
		targetURL = fmt.Sprintf("%s/logs?build_id=%s", h.publicURL, build.ID)
	}
	if err := client.ReportStatus(ctx, build.SCMToken, scm.StatusReport{
		Provider:     build.SCMProvider,
		RepoFullName: build.RepoFullName,
		CommitSHA:    build.CommitSHA,
		State:        state,
		Context:      "ci/build",
		Description:  description,
		TargetURL:    targetURL,
	}); err != nil {
		h.logger.Warn("failed to report SCM status", "build_id", build.ID, "err", err)
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

	observability.WebhooksTotal.WithLabelValues(event.Provider.String(), event.Type.String()).Inc()

	h.logger.Info("webhook received",
		"provider", event.Provider,
		"type", event.Type,
		"id", event.ID,
	)

	switch event.Type {
	case scm.EventPush:
		h.handlePush(w, r, event)
	case scm.EventPullRequest:
		h.handlePR(w, r, event)
	case scm.EventComment:
		h.handleComment(w, r, event)
	default:
		http.Error(w, "unsupported event type", http.StatusBadRequest)
	}
}

func (h *webhookHandler) handlePush(w http.ResponseWriter, r *http.Request, event *scm.Event) {
	push := event.Push
	buildID := generateID()
	branch := branchName(push.Ref)

	src := &pb.GitSource{
		RepoUrl:   push.RepoURL,
		Branch:    branch,
		CommitSha: push.AfterSHA,
	}
	g, err := fetchAndBuildGraph(r.Context(), src)
	if err != nil {
		h.logger.Error("failed to load pipeline for push", "repo", push.RepoFullName, "err", err)
		http.Error(w, "failed to load pipeline: "+err.Error(), http.StatusInternalServerError)
		return
	}

	buildEnv := map[string]string{
		"REPO_URL":   push.RepoURL,
		"BRANCH":     branch,
		"COMMIT_SHA": push.AfterSHA,
	}
	for _, task := range g.Tasks() {
		if task.Env == nil {
			task.Env = make(map[string]string)
		}
		for k, v := range buildEnv {
			if _, exists := task.Env[k]; !exists {
				task.Env[k] = v
			}
		}
	}

	build := &scheduler.Build{
		ID:           buildID,
		Graph:        g,
		RepoURL:      push.RepoURL,
		RepoFullName: push.RepoFullName,
		SCMProvider:  event.Provider,
		SCMToken:     h.lookupToken(event.Provider, push.RepoFullName),
		CommitSHA:    push.AfterSHA,
		Branch:       branch,
		TriggeredBy:  "webhook:" + push.Pusher,
	}

	if err := h.sched.SubmitBuild(build); err != nil {
		h.logger.Error("submit build failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	observability.BuildsInProgress.Inc()

	h.reportStatus(r.Context(), build, scm.StatusPending, "Build queued")

	h.logger.Info("build submitted from push",
		"build_id", buildID,
		"repo", push.RepoFullName,
		"sha", push.AfterSHA,
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"build_id": buildID})
}

func (h *webhookHandler) handlePR(w http.ResponseWriter, r *http.Request, event *scm.Event) {
	pr := event.PR

	// Only trigger on opened or synchronize (new commits pushed).
	if pr.Action != "opened" && pr.Action != "synchronize" && pr.Action != "reopened" {
		w.WriteHeader(http.StatusOK)
		return
	}

	buildID := generateID()

	src := &pb.GitSource{
		RepoUrl:   pr.RepoURL,
		Branch:    pr.HeadBranch,
		CommitSha: pr.HeadSHA,
	}
	g, err := fetchAndBuildGraph(r.Context(), src)
	if err != nil {
		h.logger.Error("failed to load pipeline for PR", "repo", pr.RepoFullName, "err", err)
		http.Error(w, "failed to load pipeline: "+err.Error(), http.StatusInternalServerError)
		return
	}

	buildEnv := map[string]string{
		"REPO_URL":   pr.RepoURL,
		"BRANCH":     pr.HeadBranch,
		"COMMIT_SHA": pr.HeadSHA,
		"PR_NUMBER":  fmt.Sprintf("%d", pr.Number),
	}
	for _, task := range g.Tasks() {
		if task.Env == nil {
			task.Env = make(map[string]string)
		}
		for k, v := range buildEnv {
			if _, exists := task.Env[k]; !exists {
				task.Env[k] = v
			}
		}
	}

	build := &scheduler.Build{
		ID:           buildID,
		Graph:        g,
		RepoURL:      pr.RepoURL,
		RepoFullName: pr.RepoFullName,
		SCMProvider:  event.Provider,
		SCMToken:     h.lookupToken(event.Provider, pr.RepoFullName),
		CommitSHA:    pr.HeadSHA,
		Branch:       pr.HeadBranch,
		PRNumber:     fmt.Sprintf("%d", pr.Number),
		TriggeredBy:  "webhook:" + pr.Author,
	}

	if err := h.sched.SubmitBuild(build); err != nil {
		h.logger.Error("submit build failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	observability.BuildsInProgress.Inc()

	h.reportStatus(r.Context(), build, scm.StatusPending, "Build queued")

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

func (h *webhookHandler) handleComment(w http.ResponseWriter, r *http.Request, event *scm.Event) {
	_ = r // reserved for future use
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
