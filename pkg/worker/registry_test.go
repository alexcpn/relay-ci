package worker

import (
	"testing"
	"time"
)

func newTestWorker(id string) *Info {
	return &Info{
		ID:                id,
		Platforms:         []string{"linux/amd64"},
		TotalCPU:          4000,
		AvailableCPU:      4000,
		TotalMemoryMB:     8192,
		AvailableMemoryMB: 8192,
		TotalDiskMB:       50000,
		AvailableDiskMB:   50000,
		MaxTasks:          4,
	}
}

func TestRegisterAndGet(t *testing.T) {
	r := NewRegistry(30 * time.Second)

	w := newTestWorker("w1")
	if err := r.Register(w); err != nil {
		t.Fatal(err)
	}

	got, ok := r.Get("w1")
	if !ok {
		t.Fatal("worker not found")
	}
	if got.State != WorkerActive {
		t.Errorf("expected active, got %s", got.State)
	}
	if got.RegisteredAt.IsZero() {
		t.Error("registered_at should be set")
	}
}

func TestRegisterEmpty(t *testing.T) {
	r := NewRegistry(30 * time.Second)
	err := r.Register(&Info{})
	if err == nil {
		t.Error("expected error for empty ID")
	}
}

func TestHeartbeat(t *testing.T) {
	r := NewRegistry(30 * time.Second)
	r.Register(newTestWorker("w1"))

	err := r.Heartbeat("w1", Capacity{
		AvailableCPU:      2000,
		AvailableMemoryMB: 4096,
		AvailableDiskMB:   40000,
		RunningTasks:      2,
	})
	if err != nil {
		t.Fatal(err)
	}

	w, _ := r.Get("w1")
	if w.AvailableCPU != 2000 {
		t.Errorf("expected 2000 cpu, got %d", w.AvailableCPU)
	}
	if w.RunningTasks != 2 {
		t.Errorf("expected 2 running tasks, got %d", w.RunningTasks)
	}
}

func TestHeartbeatUnknownWorker(t *testing.T) {
	r := NewRegistry(30 * time.Second)
	err := r.Heartbeat("unknown", Capacity{})
	if err == nil {
		t.Error("expected error for unknown worker")
	}
}

func TestReserveAndReleaseCapacity(t *testing.T) {
	r := NewRegistry(30 * time.Second)
	r.Register(newTestWorker("w1"))

	// Reserve some capacity.
	err := r.ReserveCapacity("w1", 1000, 2048, 10000)
	if err != nil {
		t.Fatal(err)
	}

	w, _ := r.Get("w1")
	if w.AvailableCPU != 3000 {
		t.Errorf("expected 3000 cpu after reserve, got %d", w.AvailableCPU)
	}
	if w.RunningTasks != 1 {
		t.Errorf("expected 1 running task, got %d", w.RunningTasks)
	}

	// Release it.
	r.ReleaseCapacity("w1", 1000, 2048, 10000)
	w, _ = r.Get("w1")
	if w.AvailableCPU != 4000 {
		t.Errorf("expected 4000 cpu after release, got %d", w.AvailableCPU)
	}
}

func TestReserveInsufficientResources(t *testing.T) {
	r := NewRegistry(30 * time.Second)
	r.Register(newTestWorker("w1"))

	err := r.ReserveCapacity("w1", 5000, 2048, 10000) // only 4000 cpu
	if err == nil {
		t.Error("expected error for insufficient CPU")
	}
}

func TestReserveMaxTasks(t *testing.T) {
	r := NewRegistry(30 * time.Second)
	r.Register(newTestWorker("w1"))

	// Fill up all 4 task slots.
	for i := 0; i < 4; i++ {
		err := r.ReserveCapacity("w1", 500, 1024, 5000)
		if err != nil {
			t.Fatalf("reserve %d failed: %v", i, err)
		}
	}

	// 5th should fail.
	err := r.ReserveCapacity("w1", 500, 1024, 5000)
	if err == nil {
		t.Error("expected error when max tasks reached")
	}
}

func TestDrain(t *testing.T) {
	r := NewRegistry(30 * time.Second)
	r.Register(newTestWorker("w1"))

	r.Drain("w1")
	w, _ := r.Get("w1")
	if w.State != WorkerDraining {
		t.Errorf("expected draining, got %s", w.State)
	}

	// Should not accept tasks while draining.
	if w.CanAcceptTask(1000, 1024, 5000) {
		t.Error("draining worker should not accept tasks")
	}

	// Active list should exclude draining workers.
	active := r.Active()
	if len(active) != 0 {
		t.Errorf("expected 0 active workers, got %d", len(active))
	}
}

func TestCheckHeartbeats(t *testing.T) {
	r := NewRegistry(50 * time.Millisecond)
	r.Register(newTestWorker("w1"))
	r.Register(newTestWorker("w2"))

	// Simulate w1 missing heartbeats.
	w1, _ := r.Get("w1")
	w1.LastHeartbeat = time.Now().Add(-100 * time.Millisecond)

	dead := r.CheckHeartbeats()
	if len(dead) != 1 || dead[0] != "w1" {
		t.Errorf("expected w1 dead, got %v", dead)
	}

	w1, _ = r.Get("w1")
	if w1.State != WorkerDead {
		t.Errorf("expected dead, got %s", w1.State)
	}

	// w2 should still be active.
	w2, _ := r.Get("w2")
	if w2.State != WorkerActive {
		t.Errorf("expected active, got %s", w2.State)
	}
}

func TestDeadWorkerRevivesOnHeartbeat(t *testing.T) {
	r := NewRegistry(50 * time.Millisecond)
	r.Register(newTestWorker("w1"))

	// Kill it.
	w1, _ := r.Get("w1")
	w1.LastHeartbeat = time.Now().Add(-100 * time.Millisecond)
	r.CheckHeartbeats()

	// Revive it.
	r.Heartbeat("w1", Capacity{
		AvailableCPU:      4000,
		AvailableMemoryMB: 8192,
		AvailableDiskMB:   50000,
	})

	w1, _ = r.Get("w1")
	if w1.State != WorkerActive {
		t.Errorf("expected active after heartbeat, got %s", w1.State)
	}
}

func TestUnregister(t *testing.T) {
	r := NewRegistry(30 * time.Second)
	r.Register(newTestWorker("w1"))

	r.Unregister("w1")
	if r.Size() != 0 {
		t.Error("expected 0 workers after unregister")
	}
}
