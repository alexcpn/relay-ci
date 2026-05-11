package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ci-system/ci/pkg/dag"
	"github.com/ci-system/ci/pkg/scheduler"
	"github.com/ci-system/ci/pkg/worker"
)

// REST API for the web UI. URL shape mirrors the future grpc-gateway routes
// (/api/v1/builds, /api/v1/workers) so this can be replaced by generated
// handlers without changing the frontend.

type apiServer struct {
	sched    *scheduler.Scheduler
	registry *worker.Registry
}

func newAPIServer(sched *scheduler.Scheduler, registry *worker.Registry) *apiServer {
	return &apiServer{sched: sched, registry: registry}
}

func (a *apiServer) register(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/builds", a.withCORS(a.handleBuilds))
	mux.HandleFunc("/api/v1/builds/", a.withCORS(a.handleBuildDetail))
	mux.HandleFunc("/api/v1/workers", a.withCORS(a.handleWorkers))
}

func (a *apiServer) withCORS(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h(w, r)
	}
}

func (a *apiServer) handleBuilds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	builds := a.sched.ListBuilds()
	out := make([]buildSummary, 0, len(builds))
	for _, b := range builds {
		out = append(out, summarize(b))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	writeJSON(w, http.StatusOK, map[string]any{"builds": out})
}

func (a *apiServer) handleBuildDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/builds/")
	id = strings.TrimSuffix(id, "/")
	if id == "" {
		http.Error(w, "build id required", http.StatusBadRequest)
		return
	}
	b, ok := a.sched.GetBuild(id)
	if !ok {
		http.Error(w, "build not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, detail(b))
}

func (a *apiServer) handleWorkers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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

func buildStateString(g *dag.Graph) string {
	if g == nil {
		return "queued"
	}
	if g.IsComplete() {
		if g.IsPassed() {
			return "passed"
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
