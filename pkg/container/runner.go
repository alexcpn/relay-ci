package container

import (
	"bufio"
	"context"
	"fmt"
	"time"
)

// RunResult is the output of running a task in a container.
type RunResult struct {
	ExitCode  int
	Output    []string // log lines
	StartedAt time.Time
	Duration  time.Duration
	Error     string
}

// Runner executes tasks in containers using the Runtime interface.
type Runner struct {
	runtime Runtime
}

// NewRunner creates a container runner.
func NewRunner(rt Runtime) *Runner {
	return &Runner{runtime: rt}
}

// Run pulls the image, creates a container, starts it, waits for
// completion, and collects logs. This is the main execution path
// for a worker running a task.
func (r *Runner) Run(ctx context.Context, config ContainerConfig) (*RunResult, error) {
	result := &RunResult{}

	// Pull image.
	if err := r.runtime.Pull(ctx, config.Image); err != nil {
		return nil, fmt.Errorf("pulling image %s: %w", config.Image, err)
	}

	// Apply timeout.
	if config.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, config.Timeout)
		defer cancel()
	}

	// Create container.
	container, err := r.runtime.Create(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("creating container: %w", err)
	}
	defer container.Remove(ctx)

	// Start container.
	result.StartedAt = time.Now()
	if err := container.Start(ctx); err != nil {
		return nil, fmt.Errorf("starting container: %w", err)
	}

	// Collect logs in background.
	logsDone := make(chan struct{})
	go func() {
		defer close(logsDone)
		logs, err := container.Logs(ctx)
		if err != nil {
			return
		}
		defer logs.Close()
		scanner := bufio.NewScanner(logs)
		for scanner.Scan() {
			result.Output = append(result.Output, scanner.Text())
		}
	}()

	// Wait for completion.
	exitCode, err := container.Wait(ctx)
	result.Duration = time.Since(result.StartedAt)

	if err != nil {
		if ctx.Err() != nil {
			result.Error = "timeout exceeded"
			result.ExitCode = -1
			container.Stop(context.Background())
			return result, nil
		}
		return nil, fmt.Errorf("waiting for container: %w", err)
	}

	// Wait for log collection to finish.
	<-logsDone

	result.ExitCode = exitCode
	return result, nil
}
