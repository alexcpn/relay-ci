package main

import (
	"context"
	"io"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/ci-system/ci/gen/ci/v1"
	"github.com/ci-system/ci/pkg/dag"
	"github.com/ci-system/ci/pkg/scheduler"
	"github.com/ci-system/ci/pkg/worker"
)

// workerRegistryServer implements the WorkerRegistryService gRPC interface.
type workerRegistryServer struct {
	pb.UnimplementedWorkerRegistryServiceServer
	registry *worker.Registry
	sched    *scheduler.Scheduler
	logger   *slog.Logger
}

func newWorkerRegistryServer(reg *worker.Registry, sched *scheduler.Scheduler, logger *slog.Logger) *workerRegistryServer {
	return &workerRegistryServer{
		registry: reg,
		sched:    sched,
		logger:   logger,
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

	_, err := s.sched.HandleTaskResult(scheduler.TaskResultReport{
		BuildID:  buildID,
		TaskID:   taskID,
		State:    taskState,
		ExitCode: int(req.Result.ExitCode),
		Error:    req.Result.ErrorMessage,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "handling task result: %v", err)
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
