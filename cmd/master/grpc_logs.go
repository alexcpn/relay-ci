package main

import (
	"context"
	"io"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/ci-system/ci/gen/ci/v1"
	"github.com/ci-system/ci/pkg/logstore"
)

// logServer implements the LogService gRPC interface.
type logServer struct {
	pb.UnimplementedLogServiceServer
	store *logstore.Store
}

func newLogServer(store *logstore.Store) *logServer {
	return &logServer{store: store}
}

func (s *logServer) PushLogs(stream pb.LogService_PushLogsServer) error {
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			return stream.SendAndClose(&pb.PushLogResponse{})
		}
		if err != nil {
			return err
		}

		if req.TaskId == nil {
			continue
		}

		lines := make([]logstore.Line, len(req.Lines))
		for i, l := range req.Lines {
			lines[i] = logstore.Line{
				Content: l.Content,
				Stream:  protoToLogStream(l.Stream),
			}
			if l.Timestamp != nil {
				lines[i].Timestamp = l.Timestamp.AsTime()
			}
		}
		s.store.Append(req.TaskId.Id, lines)
	}
}

func (s *logServer) StreamLogs(req *pb.StreamLogsRequest, stream pb.LogService_StreamLogsServer) error {
	if req.BuildId == nil {
		return status.Error(codes.InvalidArgument, "build_id is required")
	}

	taskID := ""
	if req.TaskId != nil {
		taskID = req.TaskId.Id
	}
	if taskID == "" {
		return status.Error(codes.InvalidArgument, "task_id is required for streaming")
	}

	if req.Follow && !s.store.IsComplete(taskID) {
		// Stream in real time.
		sub, err := s.store.Subscribe(taskID, req.SinceLine)
		if err != nil {
			return status.Errorf(codes.FailedPrecondition, "%v", err)
		}

		for line := range sub.C {
			if err := stream.Send(logLineToProto(line)); err != nil {
				sub.Close()
				return err
			}
		}
		return nil
	}

	// Batch mode: return stored logs.
	lines, _ := s.store.Get(taskID, req.SinceLine, 0)
	for _, line := range lines {
		if err := stream.Send(logLineToProto(line)); err != nil {
			return err
		}
	}
	return nil
}

func (s *logServer) GetLogs(ctx context.Context, req *pb.GetLogsRequest) (*pb.GetLogsResponse, error) {
	if req.BuildId == nil {
		return nil, status.Error(codes.InvalidArgument, "build_id is required")
	}

	taskID := ""
	if req.TaskId != nil {
		taskID = req.TaskId.Id
	}
	if taskID == "" {
		return nil, status.Error(codes.InvalidArgument, "task_id is required")
	}

	lines, total := s.store.Get(taskID, req.Offset, req.Limit)

	pbLines := make([]*pb.LogLine, len(lines))
	for i, l := range lines {
		pbLines[i] = logLineToProto(l)
	}

	return &pb.GetLogsResponse{
		Lines:      pbLines,
		TotalLines: total,
	}, nil
}

func logLineToProto(l logstore.Line) *pb.LogLine {
	return &pb.LogLine{
		TaskId:     &pb.TaskID{Id: l.TaskID},
		LineNumber: l.LineNumber,
		Timestamp:  timestamppb.New(l.Timestamp),
		Content:    l.Content,
		Stream:     logStreamToProto(l.Stream),
	}
}

func logStreamToProto(s logstore.Stream) pb.LogStream {
	switch s {
	case logstore.StreamStdout:
		return pb.LogStream_LOG_STREAM_STDOUT
	case logstore.StreamStderr:
		return pb.LogStream_LOG_STREAM_STDERR
	case logstore.StreamSystem:
		return pb.LogStream_LOG_STREAM_SYSTEM
	default:
		return pb.LogStream_LOG_STREAM_UNSPECIFIED
	}
}

func protoToLogStream(s pb.LogStream) logstore.Stream {
	switch s {
	case pb.LogStream_LOG_STREAM_STDOUT:
		return logstore.StreamStdout
	case pb.LogStream_LOG_STREAM_STDERR:
		return logstore.StreamStderr
	case pb.LogStream_LOG_STREAM_SYSTEM:
		return logstore.StreamSystem
	default:
		return logstore.StreamStdout
	}
}
