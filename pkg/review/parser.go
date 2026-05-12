package review

import (
	"encoding/json"
	"fmt"
	"strings"
)

// golangciOutput is the top-level JSON shape emitted by golangci-lint.
type golangciOutput struct {
	Issues []golangciIssue `json:"Issues"`
}

type golangciIssue struct {
	FromLinter  string          `json:"FromLinter"`
	Text        string          `json:"Text"`
	Severity    string          `json:"Severity"`
	Pos         golangciPos     `json:"Pos"`
	Replacement *golangciFix    `json:"Replacement,omitempty"`
}

type golangciPos struct {
	Filename string `json:"Filename"`
	Line     int    `json:"Line"`
	Column   int    `json:"Column"`
}

type golangciFix struct {
	NewLines []string `json:"NewLines"`
}

// ParseGolangciLint converts golangci-lint JSON output to Findings.
func ParseGolangciLint(data []byte, reviewID string) ([]Finding, error) {
	var out golangciOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse golangci-lint JSON: %w", err)
	}
	var findings []Finding
	for _, issue := range out.Issues {
		sev := mapGolangciSeverity(issue.FromLinter, issue.Severity)
		f := Finding{
			ReviewID: reviewID,
			File:     issue.Pos.Filename,
			Line:     issue.Pos.Line,
			Col:      issue.Pos.Column,
			Severity: sev,
			Rule:     issue.FromLinter,
			Tool:     "golangci-lint",
			Message:  issue.Text,
			Category: golangciCategory(issue.FromLinter),
			Status:   "open",
		}
		if issue.Replacement != nil && len(issue.Replacement.NewLines) > 0 {
			f.Suggestion = strings.Join(issue.Replacement.NewLines, "\n")
		}
		findings = append(findings, f)
	}
	return findings, nil
}

// ruffOutput matches ruff check --output-format=json.
type ruffDiagnostic struct {
	Code     string   `json:"code"`
	Message  string   `json:"message"`
	Filename string   `json:"filename"`
	Location ruffLoc  `json:"location"`
	Fix      *ruffFix `json:"fix,omitempty"`
}

type ruffLoc struct {
	Row    int `json:"row"`
	Column int `json:"column"`
}

type ruffFix struct {
	Message string `json:"message"`
}

// ParseRuff converts ruff JSON output to Findings.
func ParseRuff(data []byte, reviewID string) ([]Finding, error) {
	var diags []ruffDiagnostic
	if err := json.Unmarshal(data, &diags); err != nil {
		return nil, fmt.Errorf("parse ruff JSON: %w", err)
	}
	var findings []Finding
	for _, d := range diags {
		f := Finding{
			ReviewID: reviewID,
			File:     d.Filename,
			Line:     d.Location.Row,
			Col:      d.Location.Column,
			Severity: SeverityMedium,
			Rule:     d.Code,
			Tool:     "ruff",
			Message:  d.Message,
			Category: ruffCategory(d.Code),
			Status:   "open",
		}
		if d.Fix != nil {
			f.Suggestion = d.Fix.Message
		}
		findings = append(findings, f)
	}
	return findings, nil
}

// eslintOutput matches eslint --format=json.
type eslintFile struct {
	FilePath string         `json:"filePath"`
	Messages []eslintMsg    `json:"messages"`
}

type eslintMsg struct {
	RuleID   string `json:"ruleId"`
	Severity int    `json:"severity"` // 1=warn, 2=error
	Message  string `json:"message"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Fix      *struct {
		Text string `json:"text"`
	} `json:"fix,omitempty"`
}

// ParseESLint converts eslint JSON output to Findings.
func ParseESLint(data []byte, reviewID string) ([]Finding, error) {
	var files []eslintFile
	if err := json.Unmarshal(data, &files); err != nil {
		return nil, fmt.Errorf("parse eslint JSON: %w", err)
	}
	var findings []Finding
	for _, ef := range files {
		for _, msg := range ef.Messages {
			var sev Severity
			switch msg.Severity {
			case 2:
				sev = SeverityHigh
			case 1:
				sev = SeverityLow
			default:
				sev = SeverityMedium
			}
			f := Finding{
				ReviewID: reviewID,
				File:     ef.FilePath,
				Line:     msg.Line,
				Col:      msg.Column,
				Severity: sev,
				Rule:     msg.RuleID,
				Tool:     "eslint",
				Message:  msg.Message,
				Category: CategoryStyle,
				Status:   "open",
			}
			if msg.Fix != nil {
				f.Suggestion = msg.Fix.Text
			}
			findings = append(findings, f)
		}
	}
	return findings, nil
}

// shellcheckOutput matches shellcheck --format=json1.
type shellcheckOutput struct {
	Comments []shellcheckComment `json:"comments"`
}

type shellcheckComment struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Level   string `json:"level"` // error|warning|info|style
	Code    int    `json:"code"`
	Message string `json:"message"`
	Fix     *struct {
		Replacements []struct{ Replacement string } `json:"replacements"`
	} `json:"fix,omitempty"`
}

// ParseShellcheck converts shellcheck JSON output to Findings.
func ParseShellcheck(data []byte, reviewID string) ([]Finding, error) {
	var out shellcheckOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse shellcheck JSON: %w", err)
	}
	var findings []Finding
	for _, c := range out.Comments {
		f := Finding{
			ReviewID: reviewID,
			File:     c.File,
			Line:     c.Line,
			Col:      c.Column,
			Severity: shellcheckSeverity(c.Level),
			Rule:     fmt.Sprintf("SC%d", c.Code),
			Tool:     "shellcheck",
			Message:  c.Message,
			Category: CategoryCorrectness,
			Status:   "open",
		}
		if c.Fix != nil && len(c.Fix.Replacements) > 0 {
			f.Suggestion = c.Fix.Replacements[0].Replacement
		}
		findings = append(findings, f)
	}
	return findings, nil
}

// --- severity mappers ---

func mapGolangciSeverity(linter, s string) Severity {
	switch strings.ToLower(s) {
	case "error":
		return SeverityHigh
	case "warning":
		return SeverityMedium
	}
	// Linter-specific overrides for well-known high-severity rules.
	switch linter {
	case "gosec", "errcheck":
		return SeverityHigh
	case "govet", "staticcheck":
		return SeverityMedium
	}
	return SeverityLow
}

func shellcheckSeverity(level string) Severity {
	switch level {
	case "error":
		return SeverityHigh
	case "warning":
		return SeverityMedium
	case "info":
		return SeverityLow
	default:
		return SeverityInfo
	}
}

func golangciCategory(linter string) Category {
	switch linter {
	case "gosec":
		return CategorySecurity
	case "govet", "staticcheck", "errcheck", "ineffassign":
		return CategoryCorrectness
	case "gofmt", "goimports", "gci":
		return CategoryStyle
	case "gocritic", "revive":
		return CategoryMaintainability
	default:
		return CategoryStyle
	}
}

func ruffCategory(code string) Category {
	if len(code) == 0 {
		return CategoryStyle
	}
	switch code[0] {
	case 'S': // flake8-bandit security rules
		return CategorySecurity
	case 'F': // pyflakes — real errors
		return CategoryCorrectness
	case 'E', 'W': // pep8 style
		return CategoryStyle
	default:
		return CategoryMaintainability
	}
}
