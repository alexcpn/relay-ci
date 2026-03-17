package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/ci-system/ci/gen/ci/v1"
	"github.com/ci-system/ci/pkg/dag"
	"github.com/ci-system/ci/pkg/pipeline"
	"github.com/ci-system/ci/pkg/scheduler"
	"github.com/ci-system/ci/pkg/scm"
)

// schedulerServer implements the SchedulerService gRPC interface.
type schedulerServer struct {
	pb.UnimplementedSchedulerServiceServer
	sched     *scheduler.Scheduler
	scmRouter *scm.Router
}

func newSchedulerServer(sched *scheduler.Scheduler, scmRouter *scm.Router) *schedulerServer {
	return &schedulerServer{sched: sched, scmRouter: scmRouter}
}

func (s *schedulerServer) SubmitBuild(ctx context.Context, req *pb.SubmitBuildRequest) (*pb.SubmitBuildResponse, error) {
	if req.Source == nil {
		return nil, status.Error(codes.InvalidArgument, "source is required")
	}

	buildID := generateID()

	// Clone the repo and parse the pipeline config.
	g, err := fetchAndBuildGraph(ctx, req.Source)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "loading pipeline: %v", err)
	}

	// Inject build-level env vars into every task so commands like
	// "git clone $REPO_URL" and "git checkout $COMMIT_SHA" work.
	buildEnv := map[string]string{
		"REPO_URL":   req.Source.RepoUrl,
		"BRANCH":     req.Source.Branch,
		"COMMIT_SHA": req.Source.CommitSha,
		"PR_NUMBER":  req.Source.PrNumber,
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
		ID:          buildID,
		Graph:       g,
		RepoURL:     req.Source.RepoUrl,
		CommitSHA:   req.Source.CommitSha,
		Branch:      req.Source.Branch,
		PRNumber:    req.Source.PrNumber,
		TriggeredBy: req.TriggeredBy,
	}

	if err := s.sched.SubmitBuild(build); err != nil {
		return nil, status.Errorf(codes.AlreadyExists, "%v", err)
	}

	return &pb.SubmitBuildResponse{
		BuildId: &pb.BuildID{Id: buildID},
	}, nil
}

// fetchAndBuildGraph clones the repo at the given source, finds the pipeline
// config file (pipeline.yml or pipeline.yaml), parses it, and builds the DAG.
func fetchAndBuildGraph(ctx context.Context, src *pb.GitSource) (*dag.Graph, error) {
	tmpDir, err := os.MkdirTemp("", "ci-pipeline-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Clone the repo (shallow, specific branch if provided).
	args := []string{"clone", "--depth=1"}
	if src.Branch != "" {
		args = append(args, "--branch", src.Branch)
	}
	args = append(args, src.RepoUrl, tmpDir)

	if out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("cloning repo: %w\n%s", err, out)
	}

	// Check out a specific commit if requested.
	if src.CommitSha != "" && src.CommitSha != "HEAD" {
		cmd := exec.CommandContext(ctx, "git", "-C", tmpDir, "checkout", src.CommitSha)
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("checking out commit %s: %w\n%s", src.CommitSha, err, out)
		}
	}

	// Find pipeline config (pipeline.yml or pipeline.yaml).
	var cfgPath string
	for _, name := range []string{"pipeline.yml", "pipeline.yaml"} {
		p := filepath.Join(tmpDir, name)
		if _, err := os.Stat(p); err == nil {
			cfgPath = p
			break
		}
	}
	if cfgPath == "" {
		return nil, fmt.Errorf("no pipeline.yml or pipeline.yaml found in repo root")
	}

	cfg, err := pipeline.ParseFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("parsing pipeline config: %w", err)
	}

	return pipeline.BuildGraph(cfg)
}

func (s *schedulerServer) CancelBuild(ctx context.Context, req *pb.CancelBuildRequest) (*pb.CancelBuildResponse, error) {
	if req.BuildId == nil {
		return nil, status.Error(codes.InvalidArgument, "build_id is required")
	}

	// Fetch build metadata before cancelling so we can report status.
	build, hasBuild := s.sched.GetBuild(req.BuildId.Id)

	if err := s.sched.CancelBuild(req.BuildId.Id); err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}

	// Report cancellation to SCM if the build had a token.
	if hasBuild && build.SCMToken != "" && build.CommitSHA != "" && build.RepoFullName != "" {
		if client, ok := s.scmRouter.GetClient(build.SCMProvider); ok {
			if err := client.ReportStatus(ctx, build.SCMToken, scm.StatusReport{
				Provider:     build.SCMProvider,
				RepoFullName: build.RepoFullName,
				CommitSHA:    build.CommitSHA,
				State:        scm.StatusError,
				Context:      "ci/build",
				Description:  "Build cancelled",
			}); err != nil {
				// Non-fatal — log and continue.
				_ = err
			}
		}
	}

	return &pb.CancelBuildResponse{}, nil
}

func (s *schedulerServer) GetBuild(ctx context.Context, req *pb.GetBuildRequest) (*pb.GetBuildResponse, error) {
	if req.BuildId == nil {
		return nil, status.Error(codes.InvalidArgument, "build_id is required")
	}

	build, ok := s.sched.GetBuild(req.BuildId.Id)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "build not found: %s", req.BuildId.Id)
	}

	return &pb.GetBuildResponse{
		Build: buildToProto(build),
	}, nil
}

func (s *schedulerServer) ListBuilds(ctx context.Context, req *pb.ListBuildsRequest) (*pb.ListBuildsResponse, error) {
	builds := s.sched.ListBuilds()

	limit := int(req.Limit)
	if limit == 0 || limit > len(builds) {
		limit = len(builds)
	}

	pbBuilds := make([]*pb.Build, 0, limit)
	for i := 0; i < limit; i++ {
		pbBuilds = append(pbBuilds, buildToProto(builds[i]))
	}

	return &pb.ListBuildsResponse{
		Builds: pbBuilds,
	}, nil
}

func (s *schedulerServer) WatchBuild(req *pb.WatchBuildRequest, stream pb.SchedulerService_WatchBuildServer) error {
	if req.BuildId == nil {
		return status.Error(codes.InvalidArgument, "build_id is required")
	}

	build, ok := s.sched.GetBuild(req.BuildId.Id)
	if !ok {
		return status.Errorf(codes.NotFound, "build not found: %s", req.BuildId.Id)
	}

	// Send current state of all tasks.
	for _, task := range build.Graph.Tasks() {
		event := &pb.BuildEvent{
			BuildId:   req.BuildId,
			Timestamp: timestamppb.Now(),
			Event: &pb.BuildEvent_TaskStateChanged{
				TaskStateChanged: &pb.TaskStateChanged{
					TaskId:   &pb.TaskID{Id: task.ID},
					TaskName: task.Name,
					Current:  taskStateToProto(task.State),
				},
			},
		}
		if err := stream.Send(event); err != nil {
			return err
		}
	}

	return nil
}

// --- Converters ---

func buildToProto(b *scheduler.Build) *pb.Build {
	pbBuild := &pb.Build{
		BuildId:     &pb.BuildID{Id: b.ID},
		TriggeredBy: b.TriggeredBy,
	}

	if b.RepoURL != "" {
		pbBuild.Source = &pb.GitSource{
			RepoUrl:   b.RepoURL,
			CommitSha: b.CommitSHA,
			Branch:    b.Branch,
			PrNumber:  b.PRNumber,
		}
	}

	if !b.CreatedAt.IsZero() {
		pbBuild.CreatedAt = timestamppb.New(b.CreatedAt)
	}
	if !b.StartedAt.IsZero() {
		pbBuild.StartedAt = timestamppb.New(b.StartedAt)
	}
	if !b.FinishedAt.IsZero() {
		pbBuild.FinishedAt = timestamppb.New(b.FinishedAt)
	}

	if b.Graph != nil {
		pbBuild.State = buildStateToProto(b.Graph)
		for _, task := range b.Graph.Tasks() {
			pbBuild.Tasks = append(pbBuild.Tasks, taskToProto(task, b.Graph))
		}
	}

	return pbBuild
}

func taskToProto(t *dag.Task, g *dag.Graph) *pb.Task {
	pbTask := &pb.Task{
		TaskId:         &pb.TaskID{Id: t.ID},
		Name:           t.Name,
		State:          taskStateToProto(t.State),
		ContainerImage: t.ContainerImage,
		Commands:       t.Commands,
		Resources: &pb.ResourceRequirements{
			CpuMillicores:  t.CPUMillicores,
			MemoryMb:       t.MemoryMB,
			DiskMb:         t.DiskMB,
			TimeoutSeconds: t.TimeoutSeconds,
		},
	}

	for _, depID := range g.Dependencies(t.ID) {
		pbTask.DependsOn = append(pbTask.DependsOn, &pb.TaskID{Id: depID})
	}

	if t.State.IsTerminal() {
		pbTask.Result = &pb.TaskResult{
			TaskId:   &pb.TaskID{Id: t.ID},
			State:    taskStateToProto(t.State),
			ExitCode: int32(t.ExitCode),
		}
		if !t.StartedAt.IsZero() {
			pbTask.Result.StartedAt = timestamppb.New(t.StartedAt)
		}
		if !t.FinishedAt.IsZero() {
			pbTask.Result.FinishedAt = timestamppb.New(t.FinishedAt)
		}
	}

	return pbTask
}

func taskStateToProto(s dag.TaskState) pb.TaskState {
	switch s {
	case dag.TaskPending:
		return pb.TaskState_TASK_STATE_PENDING
	case dag.TaskReady:
		return pb.TaskState_TASK_STATE_PENDING
	case dag.TaskScheduled:
		return pb.TaskState_TASK_STATE_SCHEDULED
	case dag.TaskRunning:
		return pb.TaskState_TASK_STATE_RUNNING
	case dag.TaskPassed:
		return pb.TaskState_TASK_STATE_PASSED
	case dag.TaskFailed:
		return pb.TaskState_TASK_STATE_FAILED
	case dag.TaskSkipped:
		return pb.TaskState_TASK_STATE_SKIPPED
	case dag.TaskCancelled:
		return pb.TaskState_TASK_STATE_CANCELLED
	case dag.TaskTimedOut:
		return pb.TaskState_TASK_STATE_TIMED_OUT
	default:
		return pb.TaskState_TASK_STATE_UNSPECIFIED
	}
}

func buildStateToProto(g *dag.Graph) pb.BuildState {
	if g.IsComplete() {
		if g.IsPassed() {
			return pb.BuildState_BUILD_STATE_PASSED
		}
		return pb.BuildState_BUILD_STATE_FAILED
	}
	for _, t := range g.Tasks() {
		if t.State == dag.TaskRunning || t.State == dag.TaskScheduled {
			return pb.BuildState_BUILD_STATE_RUNNING
		}
	}
	return pb.BuildState_BUILD_STATE_QUEUED
}

func generateID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}
