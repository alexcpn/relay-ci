package container

import (
	"context"
	"io"
	"time"
)

// Runtime is the interface for container execution backends.
// Implementations: containerd, Docker, Firecracker, mock (for testing).
type Runtime interface {
	// Pull ensures the image is available locally.
	Pull(ctx context.Context, image string) error

	// Create creates a container but does not start it.
	Create(ctx context.Context, config ContainerConfig) (Container, error)

	// Close cleans up runtime resources.
	Close() error
}

// Container represents a created container instance.
type Container interface {
	// ID returns the container identifier.
	ID() string

	// Start starts the container.
	Start(ctx context.Context) error

	// Wait blocks until the container exits and returns the exit code.
	Wait(ctx context.Context) (int, error)

	// Stop stops a running container.
	Stop(ctx context.Context) error

	// Remove removes the container and its resources.
	Remove(ctx context.Context) error

	// Logs returns a reader for container stdout/stderr.
	Logs(ctx context.Context) (io.ReadCloser, error)
}

// ContainerConfig defines how to create a container.
type ContainerConfig struct {
	ID         string            // unique identifier
	Image      string            // container image reference
	Commands   []string          // command to execute
	Env        map[string]string // environment variables
	WorkDir    string            // working directory inside container
	Mounts     []Mount           // volume mounts
	CPUQuota   int64             // CPU limit in microseconds per period
	MemoryMB   uint32            // memory limit
	DiskMB     uint32            // disk limit
	Timeout    time.Duration     // max execution time
	User       string            // user to run as (default: non-root)
	ReadOnly   bool              // read-only root filesystem
	NetworkOff bool              // disable network access
}

// Mount defines a volume mount into the container.
type Mount struct {
	Source   string // host path or volume name
	Target  string // container path
	ReadOnly bool
}

// ExecResult is the outcome of a container execution.
type ExecResult struct {
	ContainerID string
	ExitCode    int
	StartedAt   time.Time
	FinishedAt  time.Time
	Error       string
}
