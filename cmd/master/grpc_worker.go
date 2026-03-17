package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/ci-system/ci/gen/ci/v1"
	"github.com/ci-system/ci/pkg/dag"
	"github.com/ci-system/ci/pkg/logstore"
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
	logger    *slog.Logger
	publicURL string
}

func newWorkerRegistryServer(reg *worker.Registry, sched *scheduler.Scheduler, scmRouter *scm.Router, logs *logstore.Store, logger *slog.Logger, publicURL string) *workerRegistryServer {
	return &workerRegistryServer{
		registry:  reg,
		sched:     sched,
		scmRouter: scmRouter,
		logs:      logs,
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

	// If the build just finished, report final status to SCM.
	if completion.BuildID != "" {
		s.reportBuildCompletion(ctx, completion)
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
	sb.WriteString("| Task | Result | Details |\n")
	sb.WriteString("|---|---|---|\n")
	for _, task := range b.Graph.Tasks() {
		icon := taskStateIcon(task.State)
		details := ""
		// For failed tasks include a snippet from the logs.
		if task.State == dag.TaskFailed {
			details = s.taskLogSnippet(task.ID, 5)
		}
		// For code review task include the verdict line.
		if task.ID == "review-pr" && task.State != dag.TaskSkipped {
			details = s.codeReviewVerdict(task.ID)
		}
		sb.WriteString(fmt.Sprintf("| %s | %s %s | %s |\n",
			task.Name, icon, task.State, details))
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

// codeReviewVerdict scans the code review task logs for the verdict line
// and returns it for display in the PR comment.
func (s *workerRegistryServer) codeReviewVerdict(taskID string) string {
	if s.logs == nil {
		return ""
	}
	total := s.logs.LineCount(taskID)
	lines, _ := s.logs.Get(taskID, 0, total)
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i].Content, "Ready to merge?") {
			return "`" + strings.TrimSpace(lines[i].Content) + "`"
		}
	}
	return ""
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
