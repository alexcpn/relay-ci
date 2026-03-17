package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"

	pb "github.com/ci-system/ci/gen/ci/v1"
	"github.com/ci-system/ci/pkg/logstore"
	"github.com/ci-system/ci/pkg/scheduler"
	"github.com/ci-system/ci/pkg/scm"
	"github.com/ci-system/ci/pkg/secrets"
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
	publicURL := os.Getenv("PUBLIC_URL") // e.g. "http://ci.example.com:8080" for Details links

	// --- Initialize components ---

	// Secrets store: load from .secrets.env (or SECRETS_FILE) on startup.
	secretStore := secrets.NewStore()
	loadSecretsFile(secretStore, logger)

	registry := worker.NewRegistry(30 * time.Second)
	logs := logstore.New()

	// Dispatcher sends tasks to workers via gRPC.
	disp := newDispatcher(registry, logger)
	defer disp.close()

	// Scheduler calls dispatcher when assigning tasks.
	sched := scheduler.New(registry, func(a scheduler.TaskAssignment) error {
		return disp.dispatch(a)
	}, logger)

	// SCM router for webhooks.
	gh := scm.NewGitHub(nil, "")
	gl := scm.NewGitLab(nil, "")
	router := scm.NewRouter(gh, gl)

	// --- gRPC server ---

	grpcServer := grpc.NewServer()
	pb.RegisterSchedulerServiceServer(grpcServer, newSchedulerServer(sched, router))
	pb.RegisterWorkerRegistryServiceServer(grpcServer, newWorkerRegistryServer(registry, sched, router, logs, logger, publicURL))
	pb.RegisterLogServiceServer(grpcServer, newLogServer(logs))
	pb.RegisterSecretsServiceServer(grpcServer, newSecretsServer(secretStore))

	// --- HTTP server (webhooks + log viewer) ---

	mux := http.NewServeMux()
	mux.Handle("/webhooks", newWebhookHandler(router, sched, logger, webhookSecret, secretStore, publicURL))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/logs", func(w http.ResponseWriter, r *http.Request) {
		handleLogsHTTP(w, r, logs)
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

// loadSecretsFile parses a KEY=VALUE file and seeds the secrets store.
// The file path defaults to ".secrets.env" but can be overridden with
// the SECRETS_FILE environment variable. Missing file is silently ignored.
func loadSecretsFile(store *secrets.Store, logger *slog.Logger) {
	path := envOrDefault("SECRETS_FILE", ".secrets.env")
	data, err := os.ReadFile(path)
	if err != nil {
		return // file absent — normal for most deployments
	}

	loaded := 0
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if name == "" || value == "" {
			continue
		}
		if err := store.Put("global", name, value, "env-file"); err != nil {
			logger.Warn("failed to load secret from file", "name", name, "err", err)
			continue
		}
		loaded++
	}
	if loaded > 0 {
		logger.Info("loaded secrets from file", "path", path, "count", loaded)
	}
}
