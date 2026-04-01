package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/ci-system/ci/gen/ci/v1"
	"github.com/ci-system/ci/pkg/dag"
	"github.com/ci-system/ci/pkg/logstore"
	"github.com/ci-system/ci/pkg/observability"
	"github.com/ci-system/ci/pkg/scheduler"
	"github.com/ci-system/ci/pkg/scm"
	"github.com/ci-system/ci/pkg/worker"
)

// workerRegistryServer implements the WorkerRegistryService gRPC interface.
type workerRegistryServer struct {
	pb.UnimplementedWorkerRegistryServiceServer
	registry  *worker.Registry
	sched     *scheduler.Scheduler
	scmRouter *scm.Router
	logs      *logstore.Store
	disp      *dispatcher
	logger    *slog.Logger
	publicURL string
}

func newWorkerRegistryServer(reg *worker.Registry, sched *scheduler.Scheduler, scmRouter *scm.Router, logs *logstore.Store, disp *dispatcher, logger *slog.Logger, publicURL string) *workerRegistryServer {
	return &workerRegistryServer{
		registry:  reg,
		sched:     sched,
		scmRouter: scmRouter,
		logs:      logs,
		disp:      disp,
		logger:    logger,
		publicURL: publicURL,
	}
}

func (s *workerRegistryServer) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	if req.WorkerId == nil || req.WorkerId.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "worker_id is required")
	}

	cap := req.Capacity
	if cap == nil {
		return nil, status.Error(codes.InvalidArgument, "capacity is required")
	}

	info := &worker.Info{
		ID:                req.WorkerId.Id,
		Platforms:         req.SupportedPlatforms,
		Labels:            req.Labels,
		TotalCPU:          cap.TotalCpuMillicores,
		AvailableCPU:      cap.AvailableCpuMillicores,
		TotalMemoryMB:     cap.TotalMemoryMb,
		AvailableMemoryMB: cap.AvailableMemoryMb,
		TotalDiskMB:       cap.TotalDiskMb,
		AvailableDiskMB:   cap.AvailableDiskMb,
		MaxTasks:          cap.MaxTasks,
		RunningTasks:      cap.RunningTasks,
	}

	if err := s.registry.Register(info); err != nil {
		return nil, status.Errorf(codes.AlreadyExists, "%v", err)
	}

	s.logger.Info("worker registered",
		"worker_id", req.WorkerId.Id,
		"cpu", cap.TotalCpuMillicores,
		"mem_mb", cap.TotalMemoryMb,
		"max_tasks", cap.MaxTasks,
	)

	return &pb.RegisterResponse{Accepted: true}, nil
}

func (s *workerRegistryServer) Heartbeat(stream pb.WorkerRegistryService_HeartbeatServer) error {
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		if req.WorkerId == nil {
			continue
		}

		err = s.registry.Heartbeat(req.WorkerId.Id, worker.Capacity{
			AvailableCPU:      req.Capacity.AvailableCpuMillicores,
			AvailableMemoryMB: req.Capacity.AvailableMemoryMb,
			AvailableDiskMB:   req.Capacity.AvailableDiskMb,
			RunningTasks:      req.Capacity.RunningTasks,
		})
		if err != nil {
			s.logger.Warn("heartbeat from unknown worker", "worker_id", req.WorkerId.Id)
			continue
		}

		// Send back any commands (drain, cancel, etc.).
		resp := &pb.HeartbeatResponse{}
		if err := stream.Send(resp); err != nil {
			return err
		}
	}
}

func (s *workerRegistryServer) ReportTaskResult(ctx context.Context, req *pb.ReportTaskResultRequest) (*pb.ReportTaskResultResponse, error) {
	if req.Result == nil || req.Result.TaskId == nil {
		return nil, status.Error(codes.InvalidArgument, "result with task_id is required")
	}

	taskID := req.Result.TaskId.Id
	taskState := protoToTaskState(req.Result.State)

	// Look up which build this task belongs to.
	buildID, ok := s.sched.FindBuildByTask(taskID)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "no build found for task %q", taskID)
	}

	completion, err := s.sched.HandleTaskResult(scheduler.TaskResultReport{
		BuildID:  buildID,
		TaskID:   taskID,
		State:    taskState,
		ExitCode: int(req.Result.ExitCode),
		Error:    req.Result.ErrorMessage,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "handling task result: %v", err)
	}

	// Record task metrics.
	observability.TasksTotal.WithLabelValues(taskState.String()).Inc()
	if req.Result.StartedAt != nil && req.Result.FinishedAt != nil {
		dur := req.Result.FinishedAt.AsTime().Sub(req.Result.StartedAt.AsTime()).Seconds()
		if dur > 0 {
			observability.TaskDuration.WithLabelValues(taskState.String()).Observe(dur)
		}
	}

	// If the build just finished, record build metrics.
	if completion.BuildID != "" {
		finalState := "failed"
		if completion.Passed {
			finalState = "passed"
		}
		observability.BuildsTotal.WithLabelValues(finalState).Inc()
		observability.BuildsInProgress.Dec()
		if build, ok := s.sched.GetBuild(buildID); ok && !build.StartedAt.IsZero() {
			dur := time.Since(build.StartedAt).Seconds()
			observability.BuildDuration.WithLabelValues(finalState).Observe(dur)
		}
	}

	// Report per-task status to SCM.
	// Look up the human-readable task name from the build graph.
	taskName := taskID
	if build, ok := s.sched.GetBuild(buildID); ok {
		if t, ok := build.Graph.GetTask(taskID); ok && t.Name != "" {
			taskName = t.Name
		}
	}
	// Fire SCM task status in a goroutine — never block the gRPC handler.
	switch taskState {
	case dag.TaskPassed:
		go s.reportTaskStatus(context.Background(), buildID, taskName, scm.StatusSuccess, "Passed")
	case dag.TaskFailed:
		go s.reportTaskStatus(context.Background(), buildID, taskName, scm.StatusFailure, "Failed")
	case dag.TaskTimedOut:
		go s.reportTaskStatus(context.Background(), buildID, taskName, scm.StatusFailure, "Timed out")
	case dag.TaskCancelled:
		go s.reportTaskStatus(context.Background(), buildID, taskName, scm.StatusError, "Cancelled")
	}

	// If the build just finished, report overall status, post PR comment,
	// and clean up workspace volumes on workers.
	if completion.BuildID != "" {
		go s.reportBuildCompletion(context.Background(), completion)
		go s.cleanupBuildVolumes(context.Background(), completion.BuildID)
	}

	s.logger.Info("task result received",
		"task_id", taskID,
		"build_id", buildID,
		"state", taskState,
		"exit_code", req.Result.ExitCode,
		"worker_id", req.WorkerId.GetId(),
	)

	return &pb.ReportTaskResultResponse{}, nil
}

// reportTaskStatus posts a single task's status to the SCM provider.
// context: "ci/<taskName>", e.g. "ci/lint" or "ci/code review".
func (s *workerRegistryServer) reportTaskStatus(ctx context.Context, buildID, taskName string, state scm.StatusState, description string) {
	if s.sched == nil {
		return
	}
	build, ok := s.sched.GetBuild(buildID)
	if !ok || build.SCMToken == "" || build.CommitSHA == "" || build.RepoFullName == "" {
		return
	}
	client, ok := s.scmRouter.GetClient(build.SCMProvider)
	if !ok {
		return
	}
	var targetURL string
	if s.publicURL != "" {
		targetURL = fmt.Sprintf("%s/logs?build_id=%s", s.publicURL, buildID)
	}
	if err := client.ReportStatus(ctx, build.SCMToken, scm.StatusReport{
		Provider:     build.SCMProvider,
		RepoFullName: build.RepoFullName,
		CommitSHA:    build.CommitSHA,
		State:        state,
		Context:      "ci/" + taskName,
		Description:  description,
		TargetURL:    targetURL,
	}); err != nil {
		s.logger.Warn("failed to report task status to SCM",
			"build_id", buildID, "task", taskName, "err", err)
	}
}

func (s *workerRegistryServer) reportBuildCompletion(ctx context.Context, c scheduler.BuildCompletion) {
	b := c.Build
	if b.SCMToken == "" || b.CommitSHA == "" || b.RepoFullName == "" {
		return
	}
	client, ok := s.scmRouter.GetClient(b.SCMProvider)
	if !ok {
		return
	}

	state := scm.StatusSuccess
	description := "Build passed"
	if !c.Passed {
		state = scm.StatusFailure
		description = "Build failed"
	}

	var targetURL string
	if s.publicURL != "" {
		targetURL = fmt.Sprintf("%s/logs?build_id=%s", s.publicURL, b.ID)
	}

	if err := client.ReportStatus(ctx, b.SCMToken, scm.StatusReport{
		Provider:     b.SCMProvider,
		RepoFullName: b.RepoFullName,
		CommitSHA:    b.CommitSHA,
		State:        state,
		Context:      "ci/build",
		Description:  description,
		TargetURL:    targetURL,
	}); err != nil {
		s.logger.Warn("failed to report build completion to SCM",
			"build_id", b.ID, "err", err)
	}

	// Post a summary comment on the PR if this was a PR build.
	if b.PRNumber != "" {
		body := s.buildPRComment(c, targetURL)
		if err := client.PostPRComment(ctx, b.SCMToken, scm.PRComment{
			RepoFullName: b.RepoFullName,
			PRNumber:     b.PRNumber,
			Body:         body,
		}); err != nil {
			s.logger.Warn("failed to post PR comment",
				"build_id", b.ID, "pr", b.PRNumber, "err", err)
		}
	}
}

// buildPRComment formats a Markdown PR comment summarising the build result.
func (s *workerRegistryServer) buildPRComment(c scheduler.BuildCompletion, logsURL string) string {
	b := c.Build
	var sb strings.Builder

	if c.Passed {
		sb.WriteString("## ✅ Relay CI — Build Passed\n\n")
	} else {
		sb.WriteString("## ❌ Relay CI — Build Failed\n\n")
	}

	sb.WriteString(fmt.Sprintf("**Build:** `%s`", b.ID))
	if b.Branch != "" {
		sb.WriteString(fmt.Sprintf(" · **Branch:** `%s`", b.Branch))
	}
	if logsURL != "" {
		sb.WriteString(fmt.Sprintf(" · [View Logs](%s)", logsURL))
	}
	sb.WriteString("\n\n")

	// Task summary table.
	sb.WriteString("| Task | Result | Duration | Details |\n")
	sb.WriteString("|---|---|---|---|\n")
	for _, task := range b.Graph.Tasks() {
		icon := taskStateIcon(task.State)

		dur := ""
		if !task.StartedAt.IsZero() && !task.FinishedAt.IsZero() {
			dur = task.FinishedAt.Sub(task.StartedAt).Round(time.Second).String()
		}

		details := ""
		switch {
		case task.ID == "review-pr" && task.State != dag.TaskSkipped:
			// Show full review text in a collapsible block.
			details = s.codeReviewFull(task.ID)
		case task.State == dag.TaskFailed || task.State == dag.TaskTimedOut:
			// Show last log lines for any failed task.
			details = s.taskLogSnippet(task.ID, 10)
		}

		sb.WriteString(fmt.Sprintf("| %s | %s %s | %s | %s |\n",
			task.Name, icon, task.State, dur, details))
	}

	sb.WriteString("\n---\n")
	sb.WriteString("*Posted by [Relay CI](https://github.com/ci-system/ci)*")
	return sb.String()
}

// taskStateIcon returns an emoji for a task state.
func taskStateIcon(s dag.TaskState) string {
	switch s {
	case dag.TaskPassed:
		return "✅"
	case dag.TaskFailed:
		return "❌"
	case dag.TaskSkipped:
		return "⏭️"
	case dag.TaskCancelled:
		return "🚫"
	case dag.TaskTimedOut:
		return "⏱️"
	default:
		return "⏳"
	}
}

// taskLogSnippet fetches the last n lines of a task's logs and formats them
// as a fenced code block for embedding in a PR comment.
func (s *workerRegistryServer) taskLogSnippet(taskID string, n int) string {
	if s.logs == nil {
		return ""
	}
	total := s.logs.LineCount(taskID)
	offset := total - int64(n)
	if offset < 0 {
		offset = 0
	}
	lines, _ := s.logs.Get(taskID, offset, int64(n))
	if len(lines) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<details><summary>last log lines</summary>\n\n```\n")
	for _, l := range lines {
		sb.WriteString(l.Content)
		sb.WriteString("\n")
	}
	sb.WriteString("```\n</details>")
	return sb.String()
}

// codeReviewFull returns the full code review output as a collapsible
// Markdown block. It strips system log lines (prefixed with [SYS]) and
// extracts the LLM-generated review text starting from the first "##" heading.
func (s *workerRegistryServer) codeReviewFull(taskID string) string {
	if s.logs == nil {
		return ""
	}
	total := s.logs.LineCount(taskID)
	lines, _ := s.logs.Get(taskID, 0, total)
	if len(lines) == 0 {
		return ""
	}

	// Find the verdict line for the summary header.
	verdict := ""
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i].Content, "Ready to merge?") {
			verdict = strings.TrimSpace(lines[i].Content)
			break
		}
	}

	// Collect only the review text lines (skip [SYS]/[ERR] infrastructure lines).
	var reviewLines []string
	inReview := false
	for _, l := range lines {
		c := l.Content
		// Start capturing from the first markdown heading the LLM outputs.
		if !inReview && strings.HasPrefix(c, "##") {
			inReview = true
		}
		if inReview && !strings.HasPrefix(c, "[SYS]") && !strings.HasPrefix(c, "[ERR]") {
			reviewLines = append(reviewLines, c)
		}
	}

	if len(reviewLines) == 0 {
		// Fallback: just show the verdict.
		if verdict != "" {
			return "`" + verdict + "`"
		}
		return ""
	}

	summary := "View review"
	if verdict != "" {
		summary = verdict
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<details><summary>%s</summary>\n\n", summary))
	for _, line := range reviewLines {
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	sb.WriteString("\n</details>")
	return sb.String()
}

// cleanupBuildVolumes asks each worker that participated in a build to
// remove the workspace volume. Best-effort — failures are logged but ignored.
func (s *workerRegistryServer) cleanupBuildVolumes(ctx context.Context, buildID string) {
	if s.sched == nil || s.disp == nil {
		return
	}

	workerIDs := s.sched.BuildWorkers(buildID)
	if len(workerIDs) == 0 {
		return
	}

	for _, wID := range workerIDs {
		client, err := s.disp.getClient(wID)
		if err != nil {
			s.logger.Debug("cannot connect to worker for cleanup", "worker_id", wID, "err", err)
			continue
		}

		cleanCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		resp, err := client.CleanupBuild(cleanCtx, &pb.CleanupBuildRequest{
			BuildId: &pb.BuildID{Id: buildID},
		})
		cancel()

		if err != nil {
			s.logger.Warn("volume cleanup RPC failed", "build_id", buildID, "worker_id", wID, "err", err)
		} else if resp.Success {
			s.logger.Info("workspace volume cleaned up", "build_id", buildID, "worker_id", wID, "result", resp.Message)
		} else {
			s.logger.Warn("workspace volume cleanup unsuccessful", "build_id", buildID, "worker_id", wID, "msg", resp.Message)
		}
	}
}

func protoToTaskState(s pb.TaskState) dag.TaskState {
	switch s {
	case pb.TaskState_TASK_STATE_PASSED:
		return dag.TaskPassed
	case pb.TaskState_TASK_STATE_FAILED:
		return dag.TaskFailed
	case pb.TaskState_TASK_STATE_SKIPPED:
		return dag.TaskSkipped
	case pb.TaskState_TASK_STATE_CANCELLED:
		return dag.TaskCancelled
	case pb.TaskState_TASK_STATE_TIMED_OUT:
		return dag.TaskTimedOut
	default:
		return dag.TaskPending
	}
}
