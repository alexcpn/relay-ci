// Package observability provides Prometheus metrics for the CI system.
//
// Call Register() once at startup to register all collectors with the
// default Prometheus registry. Then use the exported metric variables
// to record events from the scheduler, worker registry, and webhook handler.
//
// Expose metrics by mounting promhttp.Handler() on your HTTP mux at /metrics.
package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// --- Build metrics ---

var (
	BuildsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "relay_ci",
		Name:      "builds_total",
		Help:      "Total number of builds by final state.",
	}, []string{"state"})

	BuildDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "relay_ci",
		Name:      "build_duration_seconds",
		Help:      "Build duration from submission to completion.",
		Buckets:   prometheus.ExponentialBuckets(1, 2, 14), // 1s to ~4.5h
	}, []string{"state"})

	BuildsInProgress = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "relay_ci",
		Name:      "builds_in_progress",
		Help:      "Number of builds currently running or queued.",
	})
)

// --- Task metrics ---

var (
	TasksTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "relay_ci",
		Name:      "tasks_total",
		Help:      "Total number of tasks by final state.",
	}, []string{"state"})

	TaskDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "relay_ci",
		Name:      "task_duration_seconds",
		Help:      "Task execution duration.",
		Buckets:   prometheus.ExponentialBuckets(0.5, 2, 12), // 0.5s to ~34min
	}, []string{"state"})

	TaskQueueDepth = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "relay_ci",
		Name:      "task_queue_depth",
		Help:      "Number of tasks in ready/pending state waiting to be scheduled.",
	})
)

// --- Worker metrics ---

var (
	WorkersActive = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "relay_ci",
		Name:      "workers_active",
		Help:      "Number of active (healthy) workers.",
	})

	WorkerTasksRunning = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "relay_ci",
		Name:      "worker_tasks_running",
		Help:      "Number of tasks currently running on each worker.",
	}, []string{"worker_id"})

	WorkerCPUUtilization = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "relay_ci",
		Name:      "worker_cpu_utilization_ratio",
		Help:      "CPU utilization ratio (0-1) per worker.",
	}, []string{"worker_id"})

	WorkerMemoryUtilization = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "relay_ci",
		Name:      "worker_memory_utilization_ratio",
		Help:      "Memory utilization ratio (0-1) per worker.",
	}, []string{"worker_id"})
)

// --- Webhook metrics ---

var (
	WebhooksTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "relay_ci",
		Name:      "webhooks_total",
		Help:      "Total webhook events received by provider and type.",
	}, []string{"provider", "event"})

	WebhookErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "relay_ci",
		Name:      "webhook_errors_total",
		Help:      "Total webhook processing errors.",
	}, []string{"provider", "reason"})
)

// --- Scheduling metrics ---

var (
	SchedulingCycles = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "relay_ci",
		Name:      "scheduling_cycles_total",
		Help:      "Total number of scheduling loop iterations.",
	})

	SchedulingErrors = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "relay_ci",
		Name:      "scheduling_errors_total",
		Help:      "Total number of scheduling errors.",
	})

	DispatchErrors = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "relay_ci",
		Name:      "dispatch_errors_total",
		Help:      "Total number of task dispatch failures.",
	})
)
