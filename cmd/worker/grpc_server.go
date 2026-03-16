package main

import (
	"context"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/ci-system/ci/gen/ci/v1"
)

// workerServer implements the WorkerService gRPC interface.
// The master calls this to assign tasks and cancel them.
type workerServer struct {
	pb.UnimplementedWorkerServiceServer
	exec   *executor
	logger *slog.Logger
}

func newWorkerServer(exec *executor, logger *slog.Logger) *workerServer {
	return &workerServer{exec: exec, logger: logger}
}

func (s *workerServer) AssignTask(ctx context.Context, req *pb.AssignTaskRequest) (*pb.AssignTaskResponse, error) {
	if req.TaskId == nil || req.BuildId == nil {
		return nil, status.Error(codes.InvalidArgument, "task_id and build_id are required")
	}

	s.logger.Info("received task assignment",
		"task", req.TaskName,
		"task_id", req.TaskId.Id,
		"build_id", req.BuildId.Id,
		"image", req.ContainerImage,
	)

	// Execute the task asynchronously.
	go s.exec.executeTask(context.Background(), req)

	return &pb.AssignTaskResponse{Accepted: true}, nil
}

func (s *workerServer) CancelTask(ctx context.Context, req *pb.CancelTaskRequest) (*pb.CancelTaskResponse, error) {
	if req.TaskId == nil {
		return nil, status.Error(codes.InvalidArgument, "task_id is required")
	}

	s.logger.Info("cancelling task", "task_id", req.TaskId.Id, "reason", req.Reason)
	s.exec.cancelTask(req.TaskId.Id)

	return &pb.CancelTaskResponse{}, nil
}
