package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"

	pb "github.com/ci-system/ci/gen/ci/v1"
	"github.com/ci-system/ci/pkg/logstore"
	"github.com/ci-system/ci/pkg/scheduler"
	"github.com/ci-system/ci/pkg/scm"
	"github.com/ci-system/ci/pkg/worker"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	grpcAddr := envOrDefault("GRPC_ADDR", ":9090")
	httpAddr := envOrDefault("HTTP_ADDR", ":8080")
	webhookSecret := os.Getenv("WEBHOOK_SECRET")

	// --- Initialize components ---

	registry := worker.NewRegistry(30 * time.Second)
	logs := logstore.New()

	// Scheduler with assign callback (sends tasks to workers via gRPC).
	// For now, just log assignments. Real implementation sends via WorkerService.
	sched := scheduler.New(registry, func(a scheduler.TaskAssignment) error {
		logger.Info("task dispatched",
			"task", a.Task.Name,
			"worker", a.WorkerID,
			"build", a.BuildID,
		)
		return nil
	}, logger)

	// SCM router for webhooks.
	gh := scm.NewGitHub(nil, "")
	gl := scm.NewGitLab(nil, "")
	router := scm.NewRouter(gh, gl)

	// --- gRPC server ---

	grpcServer := grpc.NewServer()
	pb.RegisterSchedulerServiceServer(grpcServer, newSchedulerServer(sched))
	pb.RegisterWorkerRegistryServiceServer(grpcServer, newWorkerRegistryServer(registry, sched, logger))
	pb.RegisterLogServiceServer(grpcServer, newLogServer(logs))

	// --- HTTP server (webhooks) ---

	mux := http.NewServeMux()
	mux.Handle("/webhooks", newWebhookHandler(router, sched, logger, webhookSecret))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	httpServer := &http.Server{
		Addr:    httpAddr,
		Handler: mux,
	}

	// --- Scheduling loop ---

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				n, err := sched.Schedule(ctx)
				if err != nil {
					logger.Error("scheduling error", "err", err)
				}
				if n > 0 {
					logger.Info("scheduled tasks", "count", n)
				}

				// Check for dead workers.
				dead := registry.CheckHeartbeats()
				for _, id := range dead {
					logger.Warn("worker dead", "worker_id", id)
					sched.HandleDeadWorker(id)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// --- Start servers ---

	go func() {
		lis, err := net.Listen("tcp", grpcAddr)
		if err != nil {
			logger.Error("failed to listen", "addr", grpcAddr, "err", err)
			os.Exit(1)
		}
		logger.Info("gRPC server starting", "addr", grpcAddr)
		if err := grpcServer.Serve(lis); err != nil {
			logger.Error("gRPC server error", "err", err)
		}
	}()

	go func() {
		logger.Info("HTTP server starting", "addr", httpAddr)
		if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
			logger.Error("HTTP server error", "err", err)
		}
	}()

	// --- Graceful shutdown ---

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	logger.Info("shutting down...")
	cancel()
	grpcServer.GracefulStop()
	httpServer.Shutdown(context.Background())
	logger.Info("shutdown complete")
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
