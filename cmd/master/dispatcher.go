package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/ci-system/ci/gen/ci/v1"
	"github.com/ci-system/ci/pkg/scheduler"
	"github.com/ci-system/ci/pkg/worker"
)

// dispatcher sends task assignments to workers via gRPC.
type dispatcher struct {
	mu       sync.Mutex
	conns    map[string]*grpc.ClientConn       // workerID -> connection
	clients  map[string]pb.WorkerServiceClient  // workerID -> client
	registry *worker.Registry
	logger   *slog.Logger
}

func newDispatcher(registry *worker.Registry, logger *slog.Logger) *dispatcher {
	return &dispatcher{
		conns:    make(map[string]*grpc.ClientConn),
		clients:  make(map[string]pb.WorkerServiceClient),
		registry: registry,
		logger:   logger,
	}
}

// dispatch sends a task assignment to the worker via gRPC.
func (d *dispatcher) dispatch(a scheduler.TaskAssignment) error {
	client, err := d.getClient(a.WorkerID)
	if err != nil {
		return fmt.Errorf("connecting to worker %s: %w", a.WorkerID, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pbCaches := make([]*pb.CacheMount, len(a.Task.CacheMounts))
	for i, cm := range a.Task.CacheMounts {
		pbCaches[i] = &pb.CacheMount{
			CacheKey:  cm.CacheKey,
			MountPath: cm.MountPath,
			ReadOnly:  cm.ReadOnly,
		}
	}

	resp, err := client.AssignTask(ctx, &pb.AssignTaskRequest{
		TaskId:         &pb.TaskID{Id: a.Task.ID},
		BuildId:        &pb.BuildID{Id: a.BuildID},
		TaskName:       a.Task.Name,
		ContainerImage: a.Task.ContainerImage,
		Commands:       a.Task.Commands,
		Env:            a.Task.Env,
		SecretRefs:     a.Task.SecretRefs,
		CacheMounts:    pbCaches,
		Resources: &pb.ResourceRequirements{
			CpuMillicores:  a.Task.CPUMillicores,
			MemoryMb:       a.Task.MemoryMB,
			DiskMb:         a.Task.DiskMB,
			TimeoutSeconds: a.Task.TimeoutSeconds,
		},
	})
	if err != nil {
		return fmt.Errorf("assigning task to worker %s: %w", a.WorkerID, err)
	}

	if !resp.Accepted {
		return fmt.Errorf("worker %s rejected task: %s", a.WorkerID, resp.RejectReason)
	}

	d.logger.Info("task dispatched to worker",
		"task", a.Task.Name,
		"task_id", a.Task.ID,
		"worker", a.WorkerID,
		"build_id", a.BuildID,
	)
	return nil
}

// getClient returns (or creates) a gRPC client for a worker.
func (d *dispatcher) getClient(workerID string) (pb.WorkerServiceClient, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if client, ok := d.clients[workerID]; ok {
		return client, nil
	}

	// Get worker's address from registry labels.
	info, ok := d.registry.Get(workerID)
	if !ok {
		return nil, fmt.Errorf("worker %s not found in registry", workerID)
	}

	addr := info.Labels["addr"]
	if addr == "" {
		return nil, fmt.Errorf("worker %s has no addr label", workerID)
	}

	// If addr is just a port like ":9091", assume localhost.
	if addr[0] == ':' {
		addr = "localhost" + addr
	}

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dialing worker %s at %s: %w", workerID, addr, err)
	}

	client := pb.NewWorkerServiceClient(conn)
	d.conns[workerID] = conn
	d.clients[workerID] = client

	d.logger.Info("connected to worker", "worker_id", workerID, "addr", addr)
	return client, nil
}

// close cleans up all worker connections.
func (d *dispatcher) close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, conn := range d.conns {
		conn.Close()
		delete(d.conns, id)
		delete(d.clients, id)
	}
}
