package main

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ci-system/ci/pkg/auth"
	"github.com/ci-system/ci/pkg/dag"
	"github.com/ci-system/ci/pkg/scheduler"
	"github.com/ci-system/ci/pkg/store"
	"github.com/ci-system/ci/pkg/worker"
)

// REST API for the web UI. URL shape mirrors the future grpc-gateway routes
// (/api/v1/builds, /api/v1/workers) so this can be replaced by generated
// handlers without changing the frontend.

type apiServer struct {
	sched         *scheduler.Scheduler
	registry      *worker.Registry
	store         store.Store
	allowedOrigin string // exact value sent in Access-Control-Allow-Origin
	apiToken      string // empty = auth disabled
}

func newAPIServer(sched *scheduler.Scheduler, registry *worker.Registry, st store.Store) *apiServer {
	// CORS_ALLOW_ORIGIN: a single allowed origin, or "*" for any. Defaults to
	// "*" for dev ergonomics; production deployments should pin it.
	origin := os.Getenv("CORS_ALLOW_ORIGIN")
	if origin == "" {
		origin = "*"
	}
	return &apiServer{
		sched:         sched,
		registry:      registry,
		store:         st,
		allowedOrigin: origin,
		apiToken:      auth.TokenFromEnv(),
	}
}

func (a *apiServer) register(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/builds", a.withCORS(a.handleBuilds))
	mux.HandleFunc("/api/v1/builds/", a.withCORS(a.handleBuildDetail))
	mux.HandleFunc("/api/v1/workers", a.withCORS(a.handleWorkers))
	mux.HandleFunc("/api/v1/audit", a.withCORS(a.handleAudit))
}

func (a *apiServer) withCORS(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", a.allowedOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Vary", "Origin")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// Auth check — skip if no token configured (dev mode).
		if a.apiToken != "" {
			bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			bearer = strings.TrimPrefix(bearer, "bearer ")
			if bearer != a.apiToken {
				writeJSONError(w, http.StatusUnauthorized, "invalid or missing token")
				return
			}
		}
		h(w, r)
	}
}

func (a *apiServer) handleBuilds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Active builds from memory (live state).
	activeBuilds := a.sched.ListBuilds()
	seen := make(map[string]bool, len(activeBuilds))
	out := make([]buildSummary, 0, len(activeBuilds)+64)
	for _, b := range activeBuilds {
		out = append(out, summarize(b))
		seen[b.ID] = true
	}

	// Historical builds from the store (completed, not in memory any more).
	if historical, err := a.sched.StoreListBuilds(500); err == nil {
		for _, rec := range historical {
			if seen[rec.ID] {
				continue
			}
			out = append(out, summarizeRecord(rec))
		}
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	writeJSON(w, http.StatusOK, map[string]any{"builds": out})
}

func (a *apiServer) handleBuildDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// Path is /api/v1/builds/{id} — anything with a sub-path is not a build.
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/builds/")
	rest = strings.TrimSuffix(rest, "/")
	if rest == "" {
		writeJSONError(w, http.StatusBadRequest, "build id required")
		return
	}
	if strings.ContainsRune(rest, '/') {
		writeJSONError(w, http.StatusNotFound, "not found")
		return
	}
	// Try live in-memory first; fall back to the store for completed builds.
	if b, ok := a.sched.GetBuild(rest); ok {
		writeJSON(w, http.StatusOK, detail(b))
		return
	}
	rec, ok, err := a.sched.StoreGetBuild(rest)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "store error")
		return
	}
	if !ok {
		writeJSONError(w, http.StatusNotFound, "build not found")
		return
	}
	writeJSON(w, http.StatusOK, detailFromRecord(rec))
}

func (a *apiServer) handleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if a.store == nil {
		writeJSON(w, http.StatusOK, map[string]any{"entries": []any{}})
		return
	}
	entries, err := a.store.ListAudit(200)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "store error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (a *apiServer) handleWorkers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	infos := a.registry.All()
	out := make([]workerSummary, 0, len(infos))
	for _, info := range infos {
		out = append(out, workerSummary{
			ID:            info.ID,
			State:         info.State.String(),
			RunningTasks:  info.RunningTasks,
			MaxTasks:      info.MaxTasks,
			LastHeartbeat: timeOrNil(info.LastHeartbeat),
			Labels:        info.Labels,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"workers": out})
}

// --- JSON shapes ---

type buildSummary struct {
	ID           string     `json:"id"`
	State        string     `json:"state"`
	RepoFullName string     `json:"repoFullName,omitempty"`
	RepoURL      string     `json:"repoUrl,omitempty"`
	Branch       string     `json:"branch,omitempty"`
	CommitSHA    string     `json:"commitSha,omitempty"`
	PRNumber     string     `json:"prNumber,omitempty"`
	TriggeredBy  string     `json:"triggeredBy,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
	TaskCount    int        `json:"taskCount"`
	TasksPassed  int        `json:"tasksPassed"`
	TasksFailed  int        `json:"tasksFailed"`
	TasksRunning int        `json:"tasksRunning"`
}

type buildDetail struct {
	buildSummary
	Tasks []taskView `json:"tasks"`
}

type taskView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	State      string     `json:"state"`
	ExitCode   int        `json:"exitCode"`
	Error      string     `json:"error,omitempty"`
	DependsOn  []string   `json:"dependsOn"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

type workerSummary struct {
	ID            string            `json:"id"`
	State         string            `json:"state"`
	RunningTasks  uint32            `json:"runningTasks"`
	MaxTasks      uint32            `json:"maxTasks"`
	LastHeartbeat *time.Time        `json:"lastHeartbeat,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
}

// --- helpers ---

func summarize(b *scheduler.Build) buildSummary {
	s := buildSummary{
		ID:           b.ID,
		State:        buildStateString(b.Graph),
		RepoFullName: b.RepoFullName,
		RepoURL:      b.RepoURL,
		Branch:       b.Branch,
		CommitSHA:    b.CommitSHA,
		PRNumber:     b.PRNumber,
		TriggeredBy:  b.TriggeredBy,
		CreatedAt:    b.CreatedAt,
		StartedAt:    timeOrNil(b.StartedAt),
		FinishedAt:   timeOrNil(b.FinishedAt),
	}
	if b.Graph != nil {
		for _, t := range b.Graph.Tasks() {
			s.TaskCount++
			switch t.State {
			case dag.TaskPassed:
				s.TasksPassed++
			case dag.TaskFailed, dag.TaskTimedOut:
				s.TasksFailed++
			case dag.TaskRunning, dag.TaskScheduled:
				s.TasksRunning++
			}
		}
	}
	return s
}

func detail(b *scheduler.Build) buildDetail {
	d := buildDetail{buildSummary: summarize(b)}
	if b.Graph == nil {
		return d
	}
	for _, t := range b.Graph.Tasks() {
		d.Tasks = append(d.Tasks, taskView{
			ID:         t.ID,
			Name:       t.Name,
			State:      t.State.String(),
			ExitCode:   t.ExitCode,
			Error:      t.ErrorMessage,
			DependsOn:  b.Graph.Dependencies(t.ID),
			StartedAt:  timeOrNil(t.StartedAt),
			FinishedAt: timeOrNil(t.FinishedAt),
		})
	}
	return d
}

func summarizeRecord(r *store.BuildRecord) buildSummary {
	s := buildSummary{
		ID:           r.ID,
		State:        r.State,
		RepoFullName: r.RepoFullName,
		RepoURL:      r.RepoURL,
		Branch:       r.Branch,
		CommitSHA:    r.CommitSHA,
		PRNumber:     r.PRNumber,
		TriggeredBy:  r.TriggeredBy,
		CreatedAt:    r.CreatedAt,
		StartedAt:    timeOrNil(r.StartedAt),
		FinishedAt:   timeOrNil(r.FinishedAt),
	}
	for _, t := range r.Tasks {
		s.TaskCount++
		switch t.State {
		case "passed":
			s.TasksPassed++
		case "failed", "timed_out":
			s.TasksFailed++
		case "running", "scheduled":
			s.TasksRunning++
		}
	}
	return s
}

func detailFromRecord(r *store.BuildRecord) buildDetail {
	d := buildDetail{buildSummary: summarizeRecord(r)}
	for _, t := range r.Tasks {
		d.Tasks = append(d.Tasks, taskView{
			ID:         t.ID,
			Name:       t.Name,
			State:      t.State,
			ExitCode:   t.ExitCode,
			Error:      t.ErrorMessage,
			DependsOn:  []string{},
			StartedAt:  timeOrNil(t.StartedAt),
			FinishedAt: timeOrNil(t.FinishedAt),
		})
	}
	return d
}

func buildStateString(g *dag.Graph) string {
	if g == nil {
		return "queued"
	}
	if g.IsComplete() {
		if g.IsPassed() {
			return "passed"
		}
		for _, t := range g.Tasks() {
			if t.State == dag.TaskCancelled {
				return "cancelled"
			}
		}
		return "failed"
	}
	for _, t := range g.Tasks() {
		if t.State == dag.TaskRunning || t.State == dag.TaskScheduled {
			return "running"
		}
	}
	return "queued"
}

func timeOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
