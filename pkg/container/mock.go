package container

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// MockRuntime is a fake container runtime for testing.
// It simulates container lifecycle without actually running containers.
type MockRuntime struct {
	mu         sync.Mutex
	pulled     map[string]bool
	containers map[string]*MockContainer
	pullDelay  time.Duration
	execDelay  time.Duration

	// ExitCodeFunc allows tests to control exit codes per container.
	// If nil, all containers exit 0.
	ExitCodeFunc func(config ContainerConfig) int

	// OutputFunc allows tests to control container output.
	// If nil, containers produce default output.
	OutputFunc func(config ContainerConfig) string

	// FailPull makes Pull return an error for the given images.
	FailPull map[string]error
}

// NewMockRuntime creates a mock container runtime.
func NewMockRuntime() *MockRuntime {
	return &MockRuntime{
		pulled:     make(map[string]bool),
		containers: make(map[string]*MockContainer),
		pullDelay:  10 * time.Millisecond,
		execDelay:  50 * time.Millisecond,
		FailPull:   make(map[string]error),
	}
}

func (m *MockRuntime) Pull(ctx context.Context, image string) error {
	if err, ok := m.FailPull[image]; ok {
		return err
	}

	select {
	case <-time.After(m.pullDelay):
	case <-ctx.Done():
		return ctx.Err()
	}

	m.mu.Lock()
	m.pulled[image] = true
	m.mu.Unlock()
	return nil
}

func (m *MockRuntime) Create(ctx context.Context, config ContainerConfig) (Container, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.pulled[config.Image] {
		return nil, fmt.Errorf("image not pulled: %s", config.Image)
	}

	exitCode := 0
	if m.ExitCodeFunc != nil {
		exitCode = m.ExitCodeFunc(config)
	}

	output := fmt.Sprintf("Running: %s\n", strings.Join(config.Commands, " && "))
	if m.OutputFunc != nil {
		output = m.OutputFunc(config)
	}

	c := &MockContainer{
		id:        config.ID,
		config:    config,
		exitCode:  exitCode,
		output:    output,
		execDelay: m.execDelay,
		state:     "created",
	}
	m.containers[config.ID] = c
	return c, nil
}

func (m *MockRuntime) Close() error {
	return nil
}

// IsPulled returns true if the image was pulled.
func (m *MockRuntime) IsPulled(image string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pulled[image]
}

// GetContainer returns a mock container by ID.
func (m *MockRuntime) GetContainer(id string) (*MockContainer, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.containers[id]
	return c, ok
}

// MockContainer simulates a container instance.
type MockContainer struct {
	mu        sync.Mutex
	id        string
	config    ContainerConfig
	exitCode  int
	output    string
	execDelay time.Duration
	state     string
	startedAt time.Time
	stoppedAt time.Time
}

func (c *MockContainer) ID() string { return c.id }

func (c *MockContainer) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != "created" {
		return fmt.Errorf("container %s not in created state: %s", c.id, c.state)
	}
	c.state = "running"
	c.startedAt = time.Now()
	return nil
}

func (c *MockContainer) Wait(ctx context.Context) (int, error) {
	c.mu.Lock()
	if c.state != "running" {
		c.mu.Unlock()
		return -1, fmt.Errorf("container %s not running: %s", c.id, c.state)
	}
	delay := c.execDelay
	c.mu.Unlock()

	select {
	case <-time.After(delay):
	case <-ctx.Done():
		return -1, ctx.Err()
	}

	c.mu.Lock()
	c.state = "exited"
	c.stoppedAt = time.Now()
	exitCode := c.exitCode
	c.mu.Unlock()

	return exitCode, nil
}

func (c *MockContainer) Stop(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = "stopped"
	c.stoppedAt = time.Now()
	return nil
}

func (c *MockContainer) Remove(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = "removed"
	return nil
}

func (c *MockContainer) Logs(ctx context.Context) (io.ReadCloser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return io.NopCloser(bytes.NewBufferString(c.output)), nil
}

// State returns the current state of the mock container.
func (c *MockContainer) State() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}
