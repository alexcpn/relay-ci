// Package review provides the agentic code review service built on top of
// relay-ci's pipeline engine. A review submission creates a lightweight build
// with a fixed DAG: lint → tree-sitter → ai-review. Workers execute each task
// in-process (no Docker cold-start). Findings are stored as structured records
// so agents can diff iterations and track convergence.
package review

import "time"

// Severity ranks how urgently a finding must be addressed.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
	SeverityInfo     Severity = "info"
)

// Category classifies the nature of a finding.
type Category string

const (
	CategoryStyle           Category = "style"
	CategoryCorrectness     Category = "correctness"
	CategorySecurity        Category = "security"
	CategoryPerformance     Category = "performance"
	CategoryMaintainability Category = "maintainability"
	// CategoryGeneratedNoise targets patterns common in LLM-generated code:
	// comments restating the code, unused variables, copy-paste artifacts.
	CategoryGeneratedNoise Category = "generated-noise"
)

// Finding is the canonical output unit of the review service. Every tool —
// linter, tree-sitter, and AI — maps its output to this struct so consumers
// get one schema regardless of source.
type Finding struct {
	ID       string `json:"id"`        // UUID, unique within the review
	ReviewID string `json:"review_id"` // parent review

	File string `json:"file"` // repo-relative path; "" = repo-level
	Line int    `json:"line"` // 1-based; 0 = file-level, not a specific line
	Col  int    `json:"col"`  // 1-based; 0 = unknown

	Severity Severity `json:"severity"`
	Rule     string   `json:"rule"` // e.g. "errcheck", "ai-review:generated-noise"
	Tool     string   `json:"tool"` // e.g. "golangci-lint", "ruff", "ai-review"

	Message string `json:"message"` // human-readable description of the issue

	// Suggestion is a copy-pasteable fix or concrete action. Required for AI
	// findings; populated from linter --fix output where available.
	Suggestion string `json:"suggestion,omitempty"`

	Category Category `json:"category"`

	// FunctionName is populated by the tree-sitter enrichment step when the
	// finding falls inside a known function body.
	FunctionName string `json:"function_name,omitempty"`

	// Status tracks the agent's handling of this finding across iterations.
	Status string `json:"status"` // open | fixed | acknowledged | wont-fix
}

// ReviewRecord is the top-level record for one review submission.
type ReviewRecord struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"` // groups iterations from one agent session
	BuildID   string    `json:"build_id"`   // links to the DAG build in the scheduler
	State     string    `json:"state"`      // pending | running | done | error
	Verdict   string    `json:"verdict"`    // pass | fail | warn | ""
	Language  string    `json:"language"`
	Summary   string    `json:"summary"`
	DurationMs int64    `json:"duration_ms"`
	Iteration int       `json:"iteration"` // 1-based position within the session
	CreatedAt  time.Time `json:"created_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Findings  []Finding `json:"findings,omitempty"`
}

// SessionRecord groups the review iterations for one agent session.
type SessionRecord struct {
	ID           string    `json:"id"`
	CreatedAt    time.Time `json:"created_at"`
	LastReviewID string    `json:"last_review_id"`
}

// DiffResult is the output of comparing two review iterations. It shows
// the agent exactly what improved and what regressed between submissions.
type DiffResult struct {
	Fixed     []Finding `json:"fixed"`     // in A, not in B (agent fixed them)
	Regressed []Finding `json:"regressed"` // severity increased from A to B
	New       []Finding `json:"new"`       // in B, not in A (newly introduced)
	Unchanged []Finding `json:"unchanged"` // same in both
}

// DiffFindings compares two slices of findings by (file, line, rule) triple.
func DiffFindings(a, b []Finding) DiffResult {
	keyOf := func(f Finding) string { return f.File + "\x00" + string(rune(f.Line)) + "\x00" + f.Rule }
	sevRank := map[Severity]int{
		SeverityInfo: 0, SeverityLow: 1, SeverityMedium: 2,
		SeverityHigh: 3, SeverityCritical: 4,
	}

	aMap := make(map[string]Finding, len(a))
	for _, f := range a {
		aMap[keyOf(f)] = f
	}
	bMap := make(map[string]Finding, len(b))
	for _, f := range b {
		bMap[keyOf(f)] = f
	}

	var res DiffResult
	for k, fa := range aMap {
		if fb, ok := bMap[k]; ok {
			if sevRank[fb.Severity] > sevRank[fa.Severity] {
				res.Regressed = append(res.Regressed, fb)
			} else {
				res.Unchanged = append(res.Unchanged, fa)
			}
		} else {
			res.Fixed = append(res.Fixed, fa)
		}
	}
	for k, fb := range bMap {
		if _, ok := aMap[k]; !ok {
			res.New = append(res.New, fb)
		}
	}
	return res
}
