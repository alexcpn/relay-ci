package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/ci-system/ci/gen/ci/v1"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	masterAddr := envOrDefault("MASTER_ADDR", "localhost:9090")
	workerID := envOrDefault("WORKER_ID", hostname())
	workerAddr := envOrDefault("WORKER_ADDR", ":9091")
	maxTasks := uint32(runtime.NumCPU())

	logger.Info("worker starting",
		"master", masterAddr,
		"worker_id", workerID,
		"listen", workerAddr,
		"max_tasks", maxTasks,
	)

	// Connect to master.
	conn, err := grpc.NewClient(masterAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		logger.Error("failed to connect to master", "err", err)
		os.Exit(1)
	}
	defer conn.Close()

	registryClient := pb.NewWorkerRegistryServiceClient(conn)
	logClient := pb.NewLogServiceClient(conn)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create the task executor.
	exec := newExecutor(registryClient, logClient, workerID, logger)

	// Start the worker's gRPC server (master calls this to assign tasks).
	workerGRPC := grpc.NewServer()
	pb.RegisterWorkerServiceServer(workerGRPC, newWorkerServer(exec, logger))

	go func() {
		lis, err := net.Listen("tcp", workerAddr)
		if err != nil {
			logger.Error("failed to listen", "addr", workerAddr, "err", err)
			os.Exit(1)
		}
		logger.Info("worker gRPC server starting", "addr", workerAddr)
		if err := workerGRPC.Serve(lis); err != nil {
			logger.Error("worker gRPC server error", "err", err)
		}
	}()

	// Register with master (include our gRPC address so master can reach us).
	_, err = registryClient.Register(ctx, &pb.RegisterRequest{
		WorkerId: &pb.WorkerID{Id: workerID},
		Capacity: &pb.WorkerCapacity{
			TotalCpuMillicores:     uint32(runtime.NumCPU()) * 1000,
			AvailableCpuMillicores: uint32(runtime.NumCPU()) * 1000,
			TotalMemoryMb:          8192,
			AvailableMemoryMb:      8192,
			TotalDiskMb:            50000,
			AvailableDiskMb:        50000,
			MaxTasks:               maxTasks,
		},
		SupportedPlatforms: []string{runtime.GOOS + "/" + runtime.GOARCH},
		Labels:             map[string]string{"addr": workerAddr},
	})
	if err != nil {
		logger.Error("failed to register with master", "err", err)
		os.Exit(1)
	}
	logger.Info("registered with master")

	// Start heartbeat loop.
	go heartbeatLoop(ctx, registryClient, workerID, maxTasks, logger)

	// Wait for shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	logger.Info("worker shutting down")
	cancel()
	workerGRPC.GracefulStop()
}

func heartbeatLoop(ctx context.Context, client pb.WorkerRegistryServiceClient, workerID string, maxTasks uint32, logger *slog.Logger) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			stream, err := client.Heartbeat(ctx)
			if err != nil {
				logger.Warn("heartbeat stream failed", "err", err)
				continue
			}

			err = stream.Send(&pb.HeartbeatRequest{
				WorkerId: &pb.WorkerID{Id: workerID},
				Capacity: &pb.WorkerCapacity{
					TotalCpuMillicores:     uint32(runtime.NumCPU()) * 1000,
					AvailableCpuMillicores: uint32(runtime.NumCPU()) * 1000,
					TotalMemoryMb:          8192,
					AvailableMemoryMb:      8192,
					TotalDiskMb:            50000,
					AvailableDiskMb:        50000,
					MaxTasks:               maxTasks,
				},
				Timestamp: timestamppb.Now(),
			})
			if err != nil {
				logger.Warn("heartbeat send failed", "err", err)
			}

			stream.CloseSend()
		case <-ctx.Done():
			return
		}
	}
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "worker-unknown"
	}
	return h
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
