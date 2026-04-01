package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/ci-system/ci/gen/ci/v1"
)

// executor runs tasks in containers and reports results back to the master.
type executor struct {
	registryClient pb.WorkerRegistryServiceClient
	logClient      pb.LogServiceClient
	secretsClient  pb.SecretsServiceClient
	workerID       string
	logger         *slog.Logger

	mu           sync.Mutex
	runningTasks map[string]context.CancelFunc
}

func newExecutor(
	registryClient pb.WorkerRegistryServiceClient,
	logClient pb.LogServiceClient,
	secretsClient pb.SecretsServiceClient,
	workerID string,
	logger *slog.Logger,
) *executor {
	return &executor{
		registryClient: registryClient,
		logClient:      logClient,
		secretsClient:  secretsClient,
		workerID:       workerID,
		logger:         logger,
		runningTasks:   make(map[string]context.CancelFunc),
	}
}

// executeTask runs a task in a container (or falls back to shell) and
// reports the result back to the master.
func (e *executor) executeTask(ctx context.Context, req *pb.AssignTaskRequest) {
	taskID := req.TaskId.Id
	buildID := req.BuildId.Id
	taskName := req.TaskName

	taskCtx, cancel := context.WithCancel(ctx)
	e.mu.Lock()
	e.runningTasks[taskID] = cancel
	e.mu.Unlock()

	defer func() {
		cancel()
		e.mu.Lock()
		delete(e.runningTasks, taskID)
		e.mu.Unlock()
	}()

	e.logger.Info("task started",
		"task", taskName,
		"task_id", taskID,
		"build_id", buildID,
		"image", req.ContainerImage,
	)

	startedAt := time.Now()

	// Push a system log line.
	e.pushLog(taskID, buildID, fmt.Sprintf("[system] task %q started on worker %s", taskName, e.workerID), pb.LogStream_LOG_STREAM_SYSTEM)
	e.pushLog(taskID, buildID, fmt.Sprintf("[system] image: %s", req.ContainerImage), pb.LogStream_LOG_STREAM_SYSTEM)
	e.pushLog(taskID, buildID, fmt.Sprintf("[system] commands: %s", strings.Join(req.Commands, " && ")), pb.LogStream_LOG_STREAM_SYSTEM)

	// Apply timeout.
	if req.Resources != nil && req.Resources.TimeoutSeconds > 0 {
		var timeoutCancel context.CancelFunc
		taskCtx, timeoutCancel = context.WithTimeout(taskCtx, time.Duration(req.Resources.TimeoutSeconds)*time.Second)
		defer timeoutCancel()
	}

	// Fetch secrets from master and merge into task env before execution.
	if len(req.SecretRefs) > 0 {
		fetched, err := e.fetchSecrets(taskCtx, req)
		if err != nil {
			e.pushLog(taskID, buildID, fmt.Sprintf("[system] warning: could not fetch secrets: %v", err), pb.LogStream_LOG_STREAM_SYSTEM)
		} else {
			if req.Env == nil {
				req.Env = make(map[string]string)
			}
			for k, v := range fetched {
				req.Env[k] = v
			}
		}
	}

	// Try Docker first, fall back to shell execution.
	exitCode, execErr := e.runInDocker(taskCtx, req, taskID, buildID)
	if execErr != nil && isDockerNotAvailable(execErr) {
		e.pushLog(taskID, buildID, "[system] docker not available, falling back to shell execution", pb.LogStream_LOG_STREAM_SYSTEM)
		exitCode, execErr = e.runInShell(taskCtx, req, taskID, buildID)
	}

	duration := time.Since(startedAt)

	// Determine final state.
	state := pb.TaskState_TASK_STATE_PASSED
	errMsg := ""
	if taskCtx.Err() == context.DeadlineExceeded {
		state = pb.TaskState_TASK_STATE_TIMED_OUT
		errMsg = "timeout exceeded"
	} else if taskCtx.Err() == context.Canceled {
		state = pb.TaskState_TASK_STATE_CANCELLED
		errMsg = "cancelled"
	} else if execErr != nil {
		state = pb.TaskState_TASK_STATE_FAILED
		errMsg = execErr.Error()
		exitCode = -1
	} else if exitCode != 0 {
		state = pb.TaskState_TASK_STATE_FAILED
		errMsg = fmt.Sprintf("exit code %d", exitCode)
	}

	e.pushLog(taskID, buildID, fmt.Sprintf("[system] task %q finished: %s (exit=%d, duration=%s)", taskName, state, exitCode, duration.Round(time.Millisecond)), pb.LogStream_LOG_STREAM_SYSTEM)

	e.logger.Info("task finished",
		"task", taskName,
		"task_id", taskID,
		"state", state,
		"exit_code", exitCode,
		"duration", duration.Round(time.Millisecond),
	)

	// Report result to master.
	_, err := e.registryClient.ReportTaskResult(ctx, &pb.ReportTaskResultRequest{
		WorkerId: &pb.WorkerID{Id: e.workerID},
		Result: &pb.TaskResult{
			TaskId:       &pb.TaskID{Id: taskID},
			State:        state,
			ExitCode:     int32(exitCode),
			StartedAt:    timestamppb.New(startedAt),
			FinishedAt:   timestamppb.Now(),
			ErrorMessage: errMsg,
		},
	})
	if err != nil {
		e.logger.Error("failed to report task result", "task_id", taskID, "err", err)
	}
}

// runInDocker runs the task commands inside a Docker container.
func (e *executor) runInDocker(ctx context.Context, req *pb.AssignTaskRequest, taskID, buildID string) (int, error) {
	// Build docker run command.
	args := []string{"run", "--rm"}

	// Set env vars.
	for k, v := range req.Env {
		args = append(args, "-e", k+"="+v)
	}

	// Resource limits.
	if req.Resources != nil {
		if req.Resources.MemoryMb > 0 {
			args = append(args, "--memory", fmt.Sprintf("%dm", req.Resources.MemoryMb))
		}
		if req.Resources.CpuMillicores > 0 {
			cpus := float64(req.Resources.CpuMillicores) / 1000.0
			args = append(args, "--cpus", fmt.Sprintf("%.1f", cpus))
		}
	}

	// Override the entrypoint to sh so arbitrary commands work regardless
	// of what the image sets as its default entrypoint (e.g. alpine/git
	// sets entrypoint=git, which would intercept "sh -c ...").
	args = append(args, "--entrypoint", "sh")

	// Mount a named volume shared across all tasks in this build so the
	// clone task's /workspace is visible to build/test/lint tasks.
	// Docker creates the volume automatically on first use.
	args = append(args, "--volume", "ci-workspace-"+buildID+":/workspace")

	// Mount cache volumes declared by the task (e.g. Go module cache,
	// npm cache, trivy DB). Volume names are derived from the cache key
	// so the same key reuses the same volume across builds.
	// Docker creates volumes automatically on first use.
	for _, cm := range req.CacheMounts {
		if cm.MountPath == "" {
			continue
		}
		// Sanitise the cache key into a valid Docker volume name.
		volName := "ci-cache-" + sanitiseVolumeName(cm.CacheKey)
		mount := volName + ":" + cm.MountPath
		if cm.ReadOnly {
			mount += ":ro"
		}
		args = append(args, "--volume", mount)
	}

	// All tasks run from /workspace where the repo was cloned.
	args = append(args, "--workdir", "/workspace")

	// Image.
	args = append(args, req.ContainerImage)

	// Commands — join with && for shell execution inside container.
	if len(req.Commands) > 0 {
		args = append(args, "-c", strings.Join(req.Commands, " && "))
	}

	return e.runCommand(ctx, "docker", args, taskID, buildID)
}

// runInShell runs the task commands directly in a shell (fallback when Docker is unavailable).
func (e *executor) runInShell(ctx context.Context, req *pb.AssignTaskRequest, taskID, buildID string) (int, error) {
	cmdStr := strings.Join(req.Commands, " && ")
	return e.runCommand(ctx, "sh", []string{"-c", cmdStr}, taskID, buildID)
}

// runCommand executes a command, streams stdout/stderr to the log service,
// and returns the exit code.
func (e *executor) runCommand(ctx context.Context, name string, args []string, taskID, buildID string) (int, error) {
	cmd := exec.CommandContext(ctx, name, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return -1, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return -1, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return -1, fmt.Errorf("starting command: %w", err)
	}

	// Stream stdout and stderr to log service.
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			e.pushLog(taskID, buildID, scanner.Text(), pb.LogStream_LOG_STREAM_STDOUT)
		}
	}()

	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			e.pushLog(taskID, buildID, scanner.Text(), pb.LogStream_LOG_STREAM_STDERR)
		}
	}()

	wg.Wait()

	err = cmd.Wait()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return -1, err
	}
	return 0, nil
}

// pushLog sends a single log line to the master's log service.
func (e *executor) pushLog(taskID, buildID, content string, stream pb.LogStream) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	logStream, err := e.logClient.PushLogs(ctx)
	if err != nil {
		e.logger.Debug("failed to open log stream", "err", err)
		return
	}

	logStream.Send(&pb.PushLogRequest{
		TaskId:   &pb.TaskID{Id: taskID},
		BuildId:  &pb.BuildID{Id: buildID},
		WorkerId: &pb.WorkerID{Id: e.workerID},
		Lines: []*pb.LogLine{
			{
				TaskId:    &pb.TaskID{Id: taskID},
				Timestamp: timestamppb.Now(),
				Content:   content,
				Stream:    stream,
			},
		},
	})

	logStream.CloseAndRecv()
}

// cancelTask cancels a running task.
func (e *executor) cancelTask(taskID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if cancel, ok := e.runningTasks[taskID]; ok {
		cancel()
	}
}

// fetchSecrets calls the master's SecretsService and returns secret name→value pairs.
func (e *executor) fetchSecrets(ctx context.Context, req *pb.AssignTaskRequest) (map[string]string, error) {
	resp, err := e.secretsClient.GetSecrets(ctx, &pb.GetSecretsRequest{
		WorkerId:    &pb.WorkerID{Id: e.workerID},
		TaskId:      req.TaskId,
		BuildId:     req.BuildId,
		SecretNames: req.SecretRefs,
	})
	if err != nil {
		return nil, err
	}
	return resp.Secrets, nil
}

// sanitiseVolumeName converts an arbitrary cache key into a valid Docker
// volume name (alphanumeric, dash, underscore only).
func sanitiseVolumeName(key string) string {
	var b strings.Builder
	for _, r := range key {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

// cleanupBuildVolume removes the workspace volume for a completed build.
func (e *executor) cleanupBuildVolume(ctx context.Context, buildID string) (string, error) {
	volName := "ci-workspace-" + buildID
	cmd := exec.CommandContext(ctx, "docker", "volume", "rm", "-f", volName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker volume rm %s: %w: %s", volName, err, strings.TrimSpace(string(out)))
	}
	return fmt.Sprintf("removed volume %s", volName), nil
}

func isDockerNotAvailable(err error) bool {
	errStr := err.Error()
	return strings.Contains(errStr, "executable file not found") ||
		strings.Contains(errStr, "Cannot connect to the Docker daemon") ||
		strings.Contains(errStr, "command not found") ||
		strings.Contains(errStr, "permission denied") ||
		strings.Contains(errStr, "Is the docker daemon running")
}
