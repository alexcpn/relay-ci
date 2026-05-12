package review

import (
	"encoding/json"

	"github.com/ci-system/ci/pkg/dag"
)

// TaskIDReviewAll is the single task ID for a review build. Using one task
// instead of three lets lint → tree-sitter → AI run sequentially in-process
// without needing inter-task data passing through the logstore.
const TaskIDReviewAll = "review-all"

// EnvTaskType is stored in Task.Env so the worker can identify review tasks
// and route them to ReviewTaskExecutor instead of the Docker executor.
const EnvTaskType = "REVIEW_TASK_TYPE"

// BuildReviewDAG creates a single-task DAG for a review submission.
// The task runs lint → tree-sitter enrichment → AI review in sequence.
// Multiple simultaneous reviews are parallelised by the worker pool.
func BuildReviewDAG(reviewID, diff, language string, policy ReviewPolicy) (*dag.Graph, error) {
	policyJSON, _ := json.Marshal(policy)

	g := dag.New()
	task := &dag.Task{
		ID:             TaskIDReviewAll,
		Name:           "code review",
		TimeoutSeconds: 180,
		Env: map[string]string{
			EnvTaskType:       TaskIDReviewAll,
			"REVIEW_ID":       reviewID,
			"REVIEW_DIFF":     diff,
			"REVIEW_LANGUAGE": language,
			"REVIEW_POLICY":   string(policyJSON),
		},
	}
	if err := g.AddTask(task); err != nil {
		return nil, err
	}
	return g, g.Validate()
}
