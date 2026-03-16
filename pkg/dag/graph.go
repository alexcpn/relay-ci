package dag

import (
	"fmt"
	"sync"
)

// Graph is a directed acyclic graph of tasks.
// It is safe for concurrent reads but mutations must be done
// before calling Validate or during exclusive access.
type Graph struct {
	mu    sync.RWMutex
	tasks map[string]*Task           // id -> task
	edges map[string]map[string]bool // parent -> set of children
	deps  map[string]map[string]bool // child -> set of parents
	order []string                   // topological order (set after Validate)
}

// New creates an empty graph.
func New() *Graph {
	return &Graph{
		tasks: make(map[string]*Task),
		edges: make(map[string]map[string]bool),
		deps:  make(map[string]map[string]bool),
	}
}

// AddTask adds a task to the graph. Returns error if ID already exists.
func (g *Graph) AddTask(t *Task) error {
	if t.ID == "" {
		return fmt.Errorf("task ID cannot be empty")
	}
	if _, exists := g.tasks[t.ID]; exists {
		return fmt.Errorf("duplicate task ID: %q", t.ID)
	}
	t.State = TaskPending
	g.tasks[t.ID] = t
	if g.edges[t.ID] == nil {
		g.edges[t.ID] = make(map[string]bool)
	}
	if g.deps[t.ID] == nil {
		g.deps[t.ID] = make(map[string]bool)
	}
	return nil
}

// AddEdge adds a dependency: child depends on parent.
// Parent must complete before child can run.
func (g *Graph) AddEdge(parentID, childID string) error {
	if _, ok := g.tasks[parentID]; !ok {
		return fmt.Errorf("parent task not found: %q", parentID)
	}
	if _, ok := g.tasks[childID]; !ok {
		return fmt.Errorf("child task not found: %q", childID)
	}
	if parentID == childID {
		return fmt.Errorf("self-dependency not allowed: %q", parentID)
	}
	g.edges[parentID][childID] = true
	g.deps[childID][parentID] = true
	return nil
}

// Validate checks the graph for cycles using Kahn's algorithm and
// computes the topological order. Must be called before execution.
func (g *Graph) Validate() error {
	// Compute in-degree for each node.
	inDegree := make(map[string]int, len(g.tasks))
	for id := range g.tasks {
		inDegree[id] = len(g.deps[id])
	}

	// Start with nodes that have no dependencies.
	var queue []string
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}

	var order []string
	for len(queue) > 0 {
		// Pop.
		node := queue[0]
		queue = queue[1:]
		order = append(order, node)

		// Reduce in-degree of children.
		for child := range g.edges[node] {
			inDegree[child]--
			if inDegree[child] == 0 {
				queue = append(queue, child)
			}
		}
	}

	if len(order) != len(g.tasks) {
		// Find which nodes are in the cycle for a useful error message.
		var cycleNodes []string
		for id, deg := range inDegree {
			if deg > 0 {
				cycleNodes = append(cycleNodes, g.tasks[id].Name)
			}
		}
		return fmt.Errorf("cycle detected involving tasks: %v", cycleNodes)
	}

	g.order = order
	return nil
}

// Ready returns all tasks that have zero unmet dependencies and are
// in Pending state. These can be scheduled immediately.
func (g *Graph) Ready() []*Task {
	g.mu.RLock()
	defer g.mu.RUnlock()

	var ready []*Task
	for id, task := range g.tasks {
		if task.State != TaskPending {
			continue
		}
		if g.allDepsComplete(id) {
			ready = append(ready, task)
		}
	}
	return ready
}

// MarkReady transitions all eligible pending tasks to Ready state
// and returns all tasks currently in Ready state (including those
// already marked ready by Complete).
func (g *Graph) MarkReady() []*Task {
	g.mu.Lock()
	defer g.mu.Unlock()

	// First, transition pending tasks whose deps are met.
	for id, task := range g.tasks {
		if task.State == TaskPending && g.allDepsComplete(id) {
			task.State = TaskReady
		}
	}

	// Return all ready tasks.
	var ready []*Task
	for _, task := range g.tasks {
		if task.State == TaskReady {
			ready = append(ready, task)
		}
	}
	return ready
}

// Complete marks a task as finished (passed/failed/etc) and returns
// any downstream tasks that are now ready to run.
func (g *Graph) Complete(taskID string, state TaskState, exitCode int, errMsg string) ([]*Task, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	task, ok := g.tasks[taskID]
	if !ok {
		return nil, fmt.Errorf("task not found: %q", taskID)
	}

	if err := task.TransitionTo(state); err != nil {
		return nil, err
	}
	task.ExitCode = exitCode
	task.ErrorMessage = errMsg

	// If the task failed, skip all downstream tasks.
	if state == TaskFailed || state == TaskTimedOut {
		g.skipDownstream(taskID)
	}

	// Find newly ready tasks.
	var newlyReady []*Task
	if state == TaskPassed {
		for childID := range g.edges[taskID] {
			child := g.tasks[childID]
			if child.State == TaskPending && g.allDepsComplete(childID) {
				child.State = TaskReady
				newlyReady = append(newlyReady, child)
			}
		}
	}

	return newlyReady, nil
}

// Cancel cancels all non-terminal tasks in the graph.
func (g *Graph) Cancel() {
	g.mu.Lock()
	defer g.mu.Unlock()

	for _, task := range g.tasks {
		if !task.State.IsTerminal() {
			task.State = TaskCancelled
		}
	}
}

// GetTask returns a task by ID.
func (g *Graph) GetTask(id string) (*Task, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	t, ok := g.tasks[id]
	return t, ok
}

// Tasks returns all tasks in topological order.
// Validate must be called first.
func (g *Graph) Tasks() []*Task {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if g.order == nil {
		// Fallback: return in arbitrary order.
		tasks := make([]*Task, 0, len(g.tasks))
		for _, t := range g.tasks {
			tasks = append(tasks, t)
		}
		return tasks
	}

	tasks := make([]*Task, 0, len(g.order))
	for _, id := range g.order {
		tasks = append(tasks, g.tasks[id])
	}
	return tasks
}

// IsComplete returns true if all tasks are in a terminal state.
func (g *Graph) IsComplete() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()

	for _, task := range g.tasks {
		if !task.State.IsTerminal() {
			return false
		}
	}
	return true
}

// IsPassed returns true if all tasks passed.
func (g *Graph) IsPassed() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()

	for _, task := range g.tasks {
		if task.State != TaskPassed {
			return false
		}
	}
	return true
}

// Dependencies returns the parent task IDs for the given task.
func (g *Graph) Dependencies(taskID string) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	var parents []string
	for p := range g.deps[taskID] {
		parents = append(parents, p)
	}
	return parents
}

// Dependents returns the child task IDs that depend on the given task.
func (g *Graph) Dependents(taskID string) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	var children []string
	for c := range g.edges[taskID] {
		children = append(children, c)
	}
	return children
}

// Size returns the number of tasks in the graph.
func (g *Graph) Size() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.tasks)
}

// --- internal helpers ---

// allDepsComplete returns true if all parents of taskID are in a
// successful terminal state. Caller must hold at least a read lock.
func (g *Graph) allDepsComplete(taskID string) bool {
	for parentID := range g.deps[taskID] {
		if !g.tasks[parentID].State.IsSuccess() {
			return false
		}
	}
	return true
}

// skipDownstream recursively marks all downstream tasks as skipped.
// Caller must hold the write lock.
func (g *Graph) skipDownstream(taskID string) {
	for childID := range g.edges[taskID] {
		child := g.tasks[childID]
		if !child.State.IsTerminal() {
			child.State = TaskSkipped
			g.skipDownstream(childID) // recurse
		}
	}
}
