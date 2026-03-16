package worker

import (
	"fmt"
	"sync"
	"time"
)

// Info holds the current state of a registered worker.
type Info struct {
	ID                 string
	Platforms          []string          // e.g. "linux/amd64"
	Labels             map[string]string
	TotalCPU           uint32 // millicores
	AvailableCPU       uint32
	TotalMemoryMB      uint32
	AvailableMemoryMB  uint32
	TotalDiskMB        uint32
	AvailableDiskMB    uint32
	RunningTasks       uint32
	MaxTasks           uint32
	LastHeartbeat      time.Time
	RegisteredAt       time.Time
	State              WorkerState
}

// WorkerState represents the lifecycle of a worker.
type WorkerState int

const (
	WorkerActive  WorkerState = iota
	WorkerDraining             // accepting no new tasks, finishing current
	WorkerDead                 // missed heartbeats
)

func (s WorkerState) String() string {
	switch s {
	case WorkerActive:
		return "active"
	case WorkerDraining:
		return "draining"
	case WorkerDead:
		return "dead"
	default:
		return fmt.Sprintf("unknown(%d)", s)
	}
}

// CanAcceptTask returns true if the worker can take on a new task
// with the given resource requirements.
func (w *Info) CanAcceptTask(cpuMilli, memMB, diskMB uint32) bool {
	if w.State != WorkerActive {
		return false
	}
	if w.RunningTasks >= w.MaxTasks {
		return false
	}
	if w.AvailableCPU < cpuMilli {
		return false
	}
	if w.AvailableMemoryMB < memMB {
		return false
	}
	if w.AvailableDiskMB < diskMB {
		return false
	}
	return true
}

// Registry tracks all registered workers and their capacity.
type Registry struct {
	mu               sync.RWMutex
	workers          map[string]*Info
	heartbeatTimeout time.Duration
}

// NewRegistry creates a worker registry.
// heartbeatTimeout is how long before a worker is considered dead.
func NewRegistry(heartbeatTimeout time.Duration) *Registry {
	return &Registry{
		workers:          make(map[string]*Info),
		heartbeatTimeout: heartbeatTimeout,
	}
}

// Register adds a worker to the registry.
func (r *Registry) Register(w *Info) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if w.ID == "" {
		return fmt.Errorf("worker ID cannot be empty")
	}

	now := time.Now()
	w.RegisteredAt = now
	w.LastHeartbeat = now
	w.State = WorkerActive
	r.workers[w.ID] = w
	return nil
}

// Unregister removes a worker from the registry.
func (r *Registry) Unregister(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.workers, id)
}

// Heartbeat updates a worker's capacity and last heartbeat time.
func (r *Registry) Heartbeat(id string, capacity Capacity) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	w, ok := r.workers[id]
	if !ok {
		return fmt.Errorf("worker not registered: %q", id)
	}

	w.AvailableCPU = capacity.AvailableCPU
	w.AvailableMemoryMB = capacity.AvailableMemoryMB
	w.AvailableDiskMB = capacity.AvailableDiskMB
	w.RunningTasks = capacity.RunningTasks
	w.LastHeartbeat = time.Now()

	if w.State == WorkerDead {
		w.State = WorkerActive
	}

	return nil
}

// Capacity is the resource update sent with each heartbeat.
type Capacity struct {
	AvailableCPU      uint32
	AvailableMemoryMB uint32
	AvailableDiskMB   uint32
	RunningTasks      uint32
}

// Get returns a worker by ID.
func (r *Registry) Get(id string) (*Info, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	w, ok := r.workers[id]
	return w, ok
}

// Active returns all workers that can accept tasks.
func (r *Registry) Active() []*Info {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var active []*Info
	for _, w := range r.workers {
		if w.State == WorkerActive {
			active = append(active, w)
		}
	}
	return active
}

// All returns all registered workers.
func (r *Registry) All() []*Info {
	r.mu.RLock()
	defer r.mu.RUnlock()

	all := make([]*Info, 0, len(r.workers))
	for _, w := range r.workers {
		all = append(all, w)
	}
	return all
}

// Drain puts a worker into draining state.
func (r *Registry) Drain(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	w, ok := r.workers[id]
	if !ok {
		return fmt.Errorf("worker not found: %q", id)
	}
	w.State = WorkerDraining
	return nil
}

// ReserveCapacity decrements available resources on a worker when a
// task is assigned. Returns error if insufficient resources.
func (r *Registry) ReserveCapacity(id string, cpuMilli, memMB, diskMB uint32) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	w, ok := r.workers[id]
	if !ok {
		return fmt.Errorf("worker not found: %q", id)
	}
	if !w.CanAcceptTask(cpuMilli, memMB, diskMB) {
		return fmt.Errorf("worker %q has insufficient resources", id)
	}

	w.AvailableCPU -= cpuMilli
	w.AvailableMemoryMB -= memMB
	w.AvailableDiskMB -= diskMB
	w.RunningTasks++
	return nil
}

// ReleaseCapacity returns resources when a task finishes.
func (r *Registry) ReleaseCapacity(id string, cpuMilli, memMB, diskMB uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()

	w, ok := r.workers[id]
	if !ok {
		return
	}

	w.AvailableCPU += cpuMilli
	w.AvailableMemoryMB += memMB
	w.AvailableDiskMB += diskMB
	if w.RunningTasks > 0 {
		w.RunningTasks--
	}
}

// CheckHeartbeats marks workers as dead if they haven't sent a
// heartbeat within the timeout. Returns IDs of newly dead workers.
func (r *Registry) CheckHeartbeats() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	var dead []string
	for id, w := range r.workers {
		if w.State == WorkerDead {
			continue
		}
		if now.Sub(w.LastHeartbeat) > r.heartbeatTimeout {
			w.State = WorkerDead
			dead = append(dead, id)
		}
	}
	return dead
}

// Size returns the number of registered workers.
func (r *Registry) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.workers)
}
