package observability

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestBuildMetrics(t *testing.T) {
	BuildsTotal.WithLabelValues("passed").Inc()
	BuildsTotal.WithLabelValues("failed").Inc()
	BuildsTotal.WithLabelValues("failed").Inc()

	if got := testutil.ToFloat64(BuildsTotal.WithLabelValues("passed")); got != 1 {
		t.Errorf("passed builds: got %v, want 1", got)
	}
	if got := testutil.ToFloat64(BuildsTotal.WithLabelValues("failed")); got != 2 {
		t.Errorf("failed builds: got %v, want 2", got)
	}
}

func TestTaskMetrics(t *testing.T) {
	TasksTotal.WithLabelValues("passed").Inc()
	TaskDuration.WithLabelValues("passed").Observe(5.0)

	if got := testutil.ToFloat64(TasksTotal.WithLabelValues("passed")); got < 1 {
		t.Errorf("tasks total: got %v, want >= 1", got)
	}
}

func TestWorkerMetrics(t *testing.T) {
	WorkersActive.Set(3)
	if got := testutil.ToFloat64(WorkersActive); got != 3 {
		t.Errorf("workers active: got %v, want 3", got)
	}

	WorkerTasksRunning.WithLabelValues("worker-1").Set(2)
	if got := testutil.ToFloat64(WorkerTasksRunning.WithLabelValues("worker-1")); got != 2 {
		t.Errorf("worker tasks running: got %v, want 2", got)
	}
}

func TestWebhookMetrics(t *testing.T) {
	WebhooksTotal.WithLabelValues("github", "push").Inc()
	if got := testutil.ToFloat64(WebhooksTotal.WithLabelValues("github", "push")); got != 1 {
		t.Errorf("webhooks total: got %v, want 1", got)
	}
}

func TestSchedulingMetrics(t *testing.T) {
	SchedulingCycles.Inc()
	SchedulingCycles.Inc()
	if got := testutil.ToFloat64(SchedulingCycles); got < 2 {
		t.Errorf("scheduling cycles: got %v, want >= 2", got)
	}
}
