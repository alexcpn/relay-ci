package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/ci-system/ci/pkg/review"
)

// ReviewTaskExecutor runs the full review pipeline (lint → tree-sitter → AI)
// in-process on the worker, with no Docker cold-start. It is called when a
// task carries REVIEW_TASK_TYPE="review-all" in its environment.
type ReviewTaskExecutor struct {
	treeSitter *review.TreeSitterClient
	aiClient   review.ReviewClient
	logger     *slog.Logger
}

// NewReviewTaskExecutor initialises the executor and connects to the
// optional tree-sitter MCP server (TREESITTER_MCP_ADDR).
func NewReviewTaskExecutor(logger *slog.Logger) *ReviewTaskExecutor {
	return &ReviewTaskExecutor{
		treeSitter: review.NewTreeSitterClient(),
		aiClient:   review.NewReviewClient(),
		logger:     logger,
	}
}

// ReviewResult is the output of one review execution, serialised as JSON
// into the task's log stream so the master can read it back.
type ReviewResult struct {
	Verdict  string          `json:"verdict"`
	Summary  string          `json:"summary"`
	Findings []review.Finding `json:"findings"`
}

// IsReviewTask returns true when the task should be handled by this executor.
func IsReviewTask(env map[string]string) bool {
	return env[review.EnvTaskType] == review.TaskIDReviewAll
}

// Execute runs the three-phase review pipeline and returns the result as JSON.
// The JSON is written to the task's log output so the master's completion
// handler can extract findings and update the review record.
func (e *ReviewTaskExecutor) Execute(ctx context.Context, env map[string]string) (string, error) {
	reviewID := env["REVIEW_ID"]
	diff := env["REVIEW_DIFF"]
	language := env["REVIEW_LANGUAGE"]

	var policy review.ReviewPolicy
	if pJSON := env["REVIEW_POLICY"]; pJSON != "" {
		if err := json.Unmarshal([]byte(pJSON), &policy); err != nil {
			policy = review.DefaultPolicy()
		}
	} else {
		policy = review.DefaultPolicy()
	}

	start := time.Now()
	e.logger.Info("review task started", "review_id", reviewID, "language", language)

	// Phase 1: Lint
	lintRunner := review.NewLintRunner(policy)
	lintCtx, lintCancel := context.WithTimeout(ctx, 60*time.Second)
	defer lintCancel()
	lintFindings, err := lintRunner.Run(lintCtx, reviewID, diff, language)
	if err != nil {
		e.logger.Warn("lint phase failed, continuing without lint findings", "err", err)
		lintFindings = nil
	}
	e.logger.Info("lint phase done", "findings", len(lintFindings), "elapsed", time.Since(start))

	// Phase 2: Tree-sitter enrichment (optional)
	tsCtx, tsCancel := context.WithTimeout(ctx, 15*time.Second)
	defer tsCancel()
	treeCtx := e.treeSitter.Enrich(tsCtx, diff)
	e.logger.Info("tree-sitter phase done", "files_enriched", len(treeCtx))

	// Annotate lint findings with function names from tree-sitter.
	annotateWithTreeContext(lintFindings, treeCtx)

	// Phase 3: AI review
	aiCtx, aiCancel := context.WithTimeout(ctx, 120*time.Second)
	defer aiCancel()
	aiFindings, verdict, summary, err := e.aiClient.Review(aiCtx, reviewID, diff, lintFindings, treeCtx, policy)
	if err != nil {
		e.logger.Warn("AI review failed", "err", err)
		// Return lint-only result rather than failing entirely.
		verdict = computeVerdictFromFindings(lintFindings, policy)
		summary = fmt.Sprintf("AI review unavailable: %v. Lint findings only.", err)
		aiFindings = nil
	}

	// Assign IDs to lint findings (AI findings already have IDs from parser).
	for i := range lintFindings {
		if lintFindings[i].ID == "" {
			lintFindings[i].ID = newFindingID()
		}
	}

	all := append(lintFindings, aiFindings...)
	if verdict == "" {
		verdict = computeVerdictFromFindings(all, policy)
	}

	result := ReviewResult{
		Verdict:  verdict,
		Summary:  summary,
		Findings: all,
	}
	out, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshal review result: %w", err)
	}

	e.logger.Info("review task done",
		"review_id", reviewID,
		"verdict", verdict,
		"findings", len(all),
		"elapsed", time.Since(start),
	)
	return string(out), nil
}

// annotateWithTreeContext sets FunctionName on each finding whose file and
// line fall within a known function extracted by tree-sitter.
func annotateWithTreeContext(findings []review.Finding, contexts []review.TreeContext) {
	// Simple heuristic: tag the finding with the file's first function name.
	// A proper implementation would check line ranges, but that requires
	// tree-sitter to also return start/end lines.
	byFile := make(map[string]string, len(contexts))
	for _, tc := range contexts {
		if len(tc.Functions) > 0 {
			byFile[tc.File] = tc.Functions[0]
		}
	}
	for i := range findings {
		if fn, ok := byFile[findings[i].File]; ok && findings[i].FunctionName == "" {
			findings[i].FunctionName = fn
		}
	}
}

// computeVerdictFromFindings derives a verdict when the AI call is unavailable.
func computeVerdictFromFindings(findings []review.Finding, policy review.ReviewPolicy) string {
	failThreshold := policy.FailSeverity
	if failThreshold == "" {
		failThreshold = review.SeverityHigh
	}
	rank := map[review.Severity]int{
		review.SeverityInfo:     0,
		review.SeverityLow:      1,
		review.SeverityMedium:   2,
		review.SeverityHigh:     3,
		review.SeverityCritical: 4,
	}
	threshold := rank[failThreshold]
	hasWarn := false
	for _, f := range findings {
		if rank[f.Severity] >= threshold {
			return "fail"
		}
		if f.Severity == review.SeverityMedium {
			hasWarn = true
		}
	}
	if hasWarn {
		return "warn"
	}
	return "pass"
}

var findingIDCounter atomic.Uint64

func newFindingID() string {
	b := make([]byte, 4)
	rand.Read(b)
	n := findingIDCounter.Add(1)
	return fmt.Sprintf("%s%04x", hex.EncodeToString(b), n&0xffff)
}
