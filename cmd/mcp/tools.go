package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	pb "github.com/ci-system/ci/gen/ci/v1"
)

// --- Tool implementations ---

func (s *mcpServer) toolListBuilds(ctx context.Context, args json.RawMessage) *mcpToolResult {
	var params struct {
		Limit int32  `json:"limit"`
		State string `json:"state"`
	}
	json.Unmarshal(args, &params)
	if params.Limit == 0 {
		params.Limit = 20
	}

	resp, err := s.scheduler.ListBuilds(ctx, &pb.ListBuildsRequest{
		Limit: params.Limit,
	})
	if err != nil {
		return errorResult("failed to list builds: " + err.Error())
	}

	if len(resp.Builds) == 0 {
		return textResult("No builds found.")
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d builds:\n\n", len(resp.Builds)))
	for _, b := range resp.Builds {
		if params.State != "" && b.State.String() != params.State {
			continue
		}
		repo := ""
		branch := ""
		if b.Source != nil {
			repo = b.Source.RepoUrl
			branch = b.Source.Branch
		}
		sb.WriteString(fmt.Sprintf("• **%s** — %s\n  Repo: %s | Branch: %s | Trigger: %s\n",
			b.BuildId.Id, b.State, repo, branch, b.TriggeredBy))

		if b.StartedAt != nil && b.FinishedAt != nil {
			dur := b.FinishedAt.AsTime().Sub(b.StartedAt.AsTime())
			sb.WriteString(fmt.Sprintf("  Duration: %s\n", dur.Round(time.Millisecond)))
		}
		sb.WriteString("\n")
	}

	return textResult(sb.String())
}

func (s *mcpServer) toolGetBuild(ctx context.Context, args json.RawMessage) *mcpToolResult {
	var params struct {
		BuildID string `json:"build_id"`
	}
	json.Unmarshal(args, &params)
	if params.BuildID == "" {
		return errorResult("build_id is required")
	}

	resp, err := s.scheduler.GetBuild(ctx, &pb.GetBuildRequest{
		BuildId: &pb.BuildID{Id: params.BuildID},
	})
	if err != nil {
		return errorResult("build not found: " + err.Error())
	}

	b := resp.Build
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Build: %s\n", b.BuildId.Id))
	sb.WriteString(fmt.Sprintf("**State:** %s\n", b.State))
	if b.Source != nil {
		sb.WriteString(fmt.Sprintf("**Repo:** %s\n", b.Source.RepoUrl))
		sb.WriteString(fmt.Sprintf("**Branch:** %s\n", b.Source.Branch))
		sb.WriteString(fmt.Sprintf("**Commit:** %s\n", b.Source.CommitSha))
	}
	sb.WriteString(fmt.Sprintf("**Triggered by:** %s\n\n", b.TriggeredBy))

	if b.StartedAt != nil && b.FinishedAt != nil {
		dur := b.FinishedAt.AsTime().Sub(b.StartedAt.AsTime())
		sb.WriteString(fmt.Sprintf("**Duration:** %s\n\n", dur.Round(time.Millisecond)))
	}

	// Task table.
	sb.WriteString("### Tasks\n\n")
	sb.WriteString("| Task | State | Exit | Duration |\n")
	sb.WriteString("|------|-------|------|----------|\n")

	passed, failed, total := 0, 0, len(b.Tasks)
	for _, task := range b.Tasks {
		exit := ""
		dur := ""
		if task.Result != nil {
			exit = fmt.Sprintf("%d", task.Result.ExitCode)
			if task.Result.StartedAt != nil && task.Result.FinishedAt != nil {
				d := task.Result.FinishedAt.AsTime().Sub(task.Result.StartedAt.AsTime())
				dur = d.Round(time.Millisecond).String()
			}
		}

		state := task.State.String()
		switch task.State {
		case pb.TaskState_TASK_STATE_PASSED:
			passed++
		case pb.TaskState_TASK_STATE_FAILED, pb.TaskState_TASK_STATE_TIMED_OUT:
			failed++
		}

		sb.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n",
			task.Name, state, exit, dur))
	}

	sb.WriteString(fmt.Sprintf("\n**Summary:** %d/%d passed, %d failed\n", passed, total, failed))

	return textResult(sb.String())
}

func (s *mcpServer) toolGetTaskLogs(ctx context.Context, args json.RawMessage) *mcpToolResult {
	var params struct {
		BuildID string `json:"build_id"`
		TaskID  string `json:"task_id"`
		Limit   int64  `json:"limit"`
		Tail    int64  `json:"tail"`
	}
	json.Unmarshal(args, &params)
	if params.BuildID == "" || params.TaskID == "" {
		return errorResult("build_id and task_id are required")
	}

	resp, err := s.logs.GetLogs(ctx, &pb.GetLogsRequest{
		BuildId: &pb.BuildID{Id: params.BuildID},
		TaskId:  &pb.TaskID{Id: params.TaskID},
		Limit:   params.Limit,
	})
	if err != nil {
		return errorResult("failed to get logs: " + err.Error())
	}

	lines := resp.Lines
	if params.Tail > 0 && int64(len(lines)) > params.Tail {
		lines = lines[int64(len(lines))-params.Tail:]
	}

	if len(lines) == 0 {
		return textResult(fmt.Sprintf("No logs found for task %s in build %s", params.TaskID, params.BuildID))
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Logs for task: %s (build: %s)\n", params.TaskID, params.BuildID))
	sb.WriteString(fmt.Sprintf("Total lines: %d\n\n```\n", resp.TotalLines))

	for _, line := range lines {
		prefix := ""
		switch line.Stream {
		case pb.LogStream_LOG_STREAM_STDERR:
			prefix = "[ERR] "
		case pb.LogStream_LOG_STREAM_SYSTEM:
			prefix = "[SYS] "
		}
		sb.WriteString(prefix + line.Content + "\n")
	}
	sb.WriteString("```\n")

	return textResult(sb.String())
}

func (s *mcpServer) toolDiagnoseBuild(ctx context.Context, args json.RawMessage) *mcpToolResult {
	var params struct {
		BuildID string `json:"build_id"`
	}
	json.Unmarshal(args, &params)
	if params.BuildID == "" {
		return errorResult("build_id is required")
	}

	resp, err := s.scheduler.GetBuild(ctx, &pb.GetBuildRequest{
		BuildId: &pb.BuildID{Id: params.BuildID},
	})
	if err != nil {
		return errorResult("build not found: " + err.Error())
	}

	b := resp.Build
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Build Diagnosis: %s\n\n", b.BuildId.Id))
	sb.WriteString(fmt.Sprintf("**Overall State:** %s\n", b.State))

	if b.Source != nil {
		sb.WriteString(fmt.Sprintf("**Repo:** %s @ %s (%s)\n\n", b.Source.RepoUrl, b.Source.CommitSha, b.Source.Branch))
	}

	// Find failed tasks.
	var failedTasks []*pb.Task
	var skippedTasks []*pb.Task
	var passedTasks []*pb.Task
	var runningTasks []*pb.Task

	for _, task := range b.Tasks {
		switch task.State {
		case pb.TaskState_TASK_STATE_FAILED, pb.TaskState_TASK_STATE_TIMED_OUT:
			failedTasks = append(failedTasks, task)
		case pb.TaskState_TASK_STATE_SKIPPED:
			skippedTasks = append(skippedTasks, task)
		case pb.TaskState_TASK_STATE_PASSED:
			passedTasks = append(passedTasks, task)
		case pb.TaskState_TASK_STATE_RUNNING, pb.TaskState_TASK_STATE_SCHEDULED:
			runningTasks = append(runningTasks, task)
		}
	}

	if len(failedTasks) == 0 && len(runningTasks) == 0 {
		sb.WriteString("**No failures detected.** All tasks passed.\n")
		return textResult(sb.String())
	}

	if len(runningTasks) > 0 {
		sb.WriteString("### Still Running\n")
		for _, t := range runningTasks {
			sb.WriteString(fmt.Sprintf("- **%s** (id: %s) — %s\n", t.Name, t.TaskId.Id, t.State))
		}
		sb.WriteString("\n")
	}

	if len(failedTasks) > 0 {
		sb.WriteString("### Failed Tasks\n\n")
		for _, t := range failedTasks {
			sb.WriteString(fmt.Sprintf("#### ❌ %s (id: %s)\n", t.Name, t.TaskId.Id))
			sb.WriteString(fmt.Sprintf("- **State:** %s\n", t.State))
			if t.Result != nil {
				sb.WriteString(fmt.Sprintf("- **Exit code:** %d\n", t.Result.ExitCode))
				if t.Result.ErrorMessage != "" {
					sb.WriteString(fmt.Sprintf("- **Error:** %s\n", t.Result.ErrorMessage))
				}
			}

			// Fetch last 30 lines of logs for this task.
			logResp, logErr := s.logs.GetLogs(ctx, &pb.GetLogsRequest{
				BuildId: &pb.BuildID{Id: params.BuildID},
				TaskId:  &pb.TaskID{Id: t.TaskId.Id},
			})
			if logErr == nil && len(logResp.Lines) > 0 {
				lines := logResp.Lines
				if len(lines) > 30 {
					lines = lines[len(lines)-30:]
				}
				sb.WriteString("\n**Last log lines:**\n```\n")
				for _, line := range lines {
					prefix := ""
					if line.Stream == pb.LogStream_LOG_STREAM_STDERR {
						prefix = "[ERR] "
					}
					sb.WriteString(prefix + line.Content + "\n")
				}
				sb.WriteString("```\n")
			}

			// Show what depends on this task (cascade impact).
			if len(t.DependsOn) > 0 {
				deps := make([]string, len(t.DependsOn))
				for i, d := range t.DependsOn {
					deps[i] = d.Id
				}
				sb.WriteString(fmt.Sprintf("- **Depended on:** %s\n", strings.Join(deps, ", ")))
			}
			sb.WriteString("\n")
		}
	}

	if len(skippedTasks) > 0 {
		sb.WriteString("### Skipped Tasks (due to upstream failure)\n")
		for _, t := range skippedTasks {
			sb.WriteString(fmt.Sprintf("- %s\n", t.Name))
		}
		sb.WriteString("\n")
	}

	sb.WriteString(fmt.Sprintf("### Summary\n"))
	sb.WriteString(fmt.Sprintf("- ✅ Passed: %d\n", len(passedTasks)))
	sb.WriteString(fmt.Sprintf("- ❌ Failed: %d\n", len(failedTasks)))
	sb.WriteString(fmt.Sprintf("- ⏭️ Skipped: %d\n", len(skippedTasks)))
	sb.WriteString(fmt.Sprintf("- ⏳ Running: %d\n", len(runningTasks)))

	if len(failedTasks) > 0 {
		sb.WriteString(fmt.Sprintf("\n**Root cause:** Task `%s` failed first. ", failedTasks[0].Name))
		sb.WriteString(fmt.Sprintf("Use `suggest_fix` with task_id `%s` for remediation advice.\n", failedTasks[0].TaskId.Id))
	}

	return textResult(sb.String())
}

func (s *mcpServer) toolSubmitBuild(ctx context.Context, args json.RawMessage) *mcpToolResult {
	var params struct {
		RepoURL   string `json:"repo_url"`
		Branch    string `json:"branch"`
		CommitSHA string `json:"commit_sha"`
	}
	json.Unmarshal(args, &params)
	if params.RepoURL == "" {
		return errorResult("repo_url is required")
	}
	if params.Branch == "" {
		params.Branch = "main"
	}
	if params.CommitSHA == "" {
		params.CommitSHA = "HEAD"
	}

	resp, err := s.scheduler.SubmitBuild(ctx, &pb.SubmitBuildRequest{
		Source: &pb.GitSource{
			RepoUrl:   params.RepoURL,
			CommitSha: params.CommitSHA,
			Branch:    params.Branch,
		},
		TriggeredBy: "mcp-agent",
	})
	if err != nil {
		return errorResult("failed to submit build: " + err.Error())
	}

	return textResult(fmt.Sprintf("Build submitted successfully.\n\n**Build ID:** %s\n\nUse `get_build` or `watch_build` to monitor progress.", resp.BuildId.Id))
}

func (s *mcpServer) toolCancelBuild(ctx context.Context, args json.RawMessage) *mcpToolResult {
	var params struct {
		BuildID string `json:"build_id"`
	}
	json.Unmarshal(args, &params)
	if params.BuildID == "" {
		return errorResult("build_id is required")
	}

	_, err := s.scheduler.CancelBuild(ctx, &pb.CancelBuildRequest{
		BuildId: &pb.BuildID{Id: params.BuildID},
		Reason:  "cancelled via MCP agent",
	})
	if err != nil {
		return errorResult("failed to cancel build: " + err.Error())
	}

	return textResult(fmt.Sprintf("Build %s has been cancelled.", params.BuildID))
}

func (s *mcpServer) toolRetryBuild(ctx context.Context, args json.RawMessage) *mcpToolResult {
	var params struct {
		BuildID     string `json:"build_id"`
		FromScratch bool   `json:"from_scratch"`
	}
	json.Unmarshal(args, &params)
	if params.BuildID == "" {
		return errorResult("build_id is required")
	}

	resp, err := s.scheduler.RetryBuild(ctx, &pb.RetryBuildRequest{
		BuildId:     &pb.BuildID{Id: params.BuildID},
		FromScratch: params.FromScratch,
	})
	if err != nil {
		return errorResult("failed to retry build: " + err.Error())
	}

	return textResult(fmt.Sprintf("Build retried. New build ID: %s\n\nUse `watch_build` to monitor progress.", resp.BuildId.Id))
}

func (s *mcpServer) toolGetFailedBuilds(ctx context.Context, args json.RawMessage) *mcpToolResult {
	var params struct {
		Limit int32 `json:"limit"`
	}
	json.Unmarshal(args, &params)
	if params.Limit == 0 {
		params.Limit = 10
	}

	resp, err := s.scheduler.ListBuilds(ctx, &pb.ListBuildsRequest{
		Limit: 100, // fetch more, filter locally
	})
	if err != nil {
		return errorResult("failed to list builds: " + err.Error())
	}

	var sb strings.Builder
	count := 0
	for _, b := range resp.Builds {
		if b.State != pb.BuildState_BUILD_STATE_FAILED {
			continue
		}
		if int32(count) >= params.Limit {
			break
		}
		count++

		repo := ""
		branch := ""
		if b.Source != nil {
			repo = b.Source.RepoUrl
			branch = b.Source.Branch
		}

		// Count failed tasks.
		failedCount := 0
		var failedNames []string
		for _, t := range b.Tasks {
			if t.State == pb.TaskState_TASK_STATE_FAILED || t.State == pb.TaskState_TASK_STATE_TIMED_OUT {
				failedCount++
				failedNames = append(failedNames, t.Name)
			}
		}

		sb.WriteString(fmt.Sprintf("### %s — FAILED\n", b.BuildId.Id))
		sb.WriteString(fmt.Sprintf("- Repo: %s | Branch: %s\n", repo, branch))
		sb.WriteString(fmt.Sprintf("- Trigger: %s\n", b.TriggeredBy))
		sb.WriteString(fmt.Sprintf("- Failed tasks (%d): %s\n", failedCount, strings.Join(failedNames, ", ")))
		sb.WriteString("\n")
	}

	if count == 0 {
		return textResult("No failed builds found. All builds are healthy! 🎉")
	}

	header := fmt.Sprintf("## Failed Builds (%d found)\n\n", count)
	return textResult(header + sb.String() + "Use `diagnose_build` with a build_id for detailed failure analysis.")
}

func (s *mcpServer) toolSuggestFix(ctx context.Context, args json.RawMessage) *mcpToolResult {
	var params struct {
		BuildID string `json:"build_id"`
		TaskID  string `json:"task_id"`
	}
	json.Unmarshal(args, &params)
	if params.BuildID == "" || params.TaskID == "" {
		return errorResult("build_id and task_id are required")
	}

	// Get the build to find task details.
	buildResp, err := s.scheduler.GetBuild(ctx, &pb.GetBuildRequest{
		BuildId: &pb.BuildID{Id: params.BuildID},
	})
	if err != nil {
		return errorResult("build not found: " + err.Error())
	}

	// Find the specific task.
	var task *pb.Task
	for _, t := range buildResp.Build.Tasks {
		if t.TaskId.Id == params.TaskID {
			task = t
			break
		}
	}
	if task == nil {
		return errorResult(fmt.Sprintf("task %s not found in build %s", params.TaskID, params.BuildID))
	}

	if task.State != pb.TaskState_TASK_STATE_FAILED && task.State != pb.TaskState_TASK_STATE_TIMED_OUT {
		return textResult(fmt.Sprintf("Task %s is in state %s (not failed). No fix needed.", task.Name, task.State))
	}

	// Get logs for analysis.
	logResp, err := s.logs.GetLogs(ctx, &pb.GetLogsRequest{
		BuildId: &pb.BuildID{Id: params.BuildID},
		TaskId:  &pb.TaskID{Id: params.TaskID},
	})

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Fix Suggestion for: %s\n\n", task.Name))
	sb.WriteString(fmt.Sprintf("**Task:** %s (id: %s)\n", task.Name, task.TaskId.Id))
	sb.WriteString(fmt.Sprintf("**Image:** %s\n", task.ContainerImage))
	sb.WriteString(fmt.Sprintf("**Commands:** %s\n", strings.Join(task.Commands, " && ")))
	sb.WriteString(fmt.Sprintf("**Exit Code:** %d\n", task.Result.GetExitCode()))
	sb.WriteString(fmt.Sprintf("**State:** %s\n\n", task.State))

	// Classify the error type and extract relevant lines.
	errorType := "unknown"
	var errorLines []string
	var allLogLines []string

	if err == nil && logResp != nil {
		for _, line := range logResp.Lines {
			allLogLines = append(allLogLines, line.Content)
			if line.Stream == pb.LogStream_LOG_STREAM_STDERR {
				errorLines = append(errorLines, line.Content)
			}
		}
	}

	allLog := strings.Join(allLogLines, "\n")

	// Classify error type from task name and log content.
	switch {
	case task.State == pb.TaskState_TASK_STATE_TIMED_OUT:
		errorType = "timeout"
	case containsAny(task.Name, "compile", "build"):
		errorType = "compilation"
	case containsAny(task.Name, "test", "unit_test", "integration"):
		errorType = "test_failure"
	case containsAny(task.Name, "lint"):
		errorType = "lint"
	case containsAny(task.Name, "security", "scan", "trivy", "gosec"):
		errorType = "security_finding"
	case containsAny(task.Name, "sonar"):
		errorType = "quality_gate"
	case containsAny(allLog, "could not resolve", "module not found", "package not found", "dependency"):
		errorType = "dependency"
	case containsAny(allLog, "permission denied", "access denied"):
		errorType = "permission"
	case containsAny(allLog, "no space left", "disk quota"):
		errorType = "disk_space"
	}

	sb.WriteString(fmt.Sprintf("### Error Classification: **%s**\n\n", errorType))

	// Provide specific advice based on error type.
	switch errorType {
	case "compilation":
		sb.WriteString("**Likely cause:** Code does not compile. Check for syntax errors, missing imports, or type mismatches.\n\n")
		sb.WriteString("**Suggested actions:**\n")
		sb.WriteString("1. Read the error output below to find the file and line number\n")
		sb.WriteString("2. Fix the compilation error in the source file\n")
		sb.WriteString("3. Run the build command locally to verify\n")
		sb.WriteString("4. Push the fix and use `submit_build` to re-trigger CI\n")
	case "test_failure":
		sb.WriteString("**Likely cause:** One or more tests are failing.\n\n")
		sb.WriteString("**Suggested actions:**\n")
		sb.WriteString("1. Identify which test(s) failed from the output below\n")
		sb.WriteString("2. Check if the test expectation is wrong or if the code has a bug\n")
		sb.WriteString("3. Run the failing test locally to reproduce\n")
		sb.WriteString("4. Fix the test or the code, then push and use `submit_build`\n")
	case "lint":
		sb.WriteString("**Likely cause:** Code style or quality issues detected by linter.\n\n")
		sb.WriteString("**Suggested actions:**\n")
		sb.WriteString("1. Review the lint warnings/errors below\n")
		sb.WriteString("2. Apply the suggested fixes (most linters provide auto-fix hints)\n")
		sb.WriteString("3. Run the linter locally with `--fix` to auto-correct where possible\n")
		sb.WriteString("4. Push and use `submit_build` to re-trigger\n")
	case "security_finding":
		sb.WriteString("**Likely cause:** Security vulnerabilities detected in dependencies or code.\n\n")
		sb.WriteString("**Suggested actions:**\n")
		sb.WriteString("1. Review the vulnerability report below\n")
		sb.WriteString("2. Update affected dependencies to patched versions\n")
		sb.WriteString("3. If no patch exists, evaluate if the vulnerability applies to your use case\n")
		sb.WriteString("4. Add exceptions for false positives if justified\n")
	case "quality_gate":
		sb.WriteString("**Likely cause:** SonarQube quality gate failed — likely low coverage, code duplication, or code smells.\n\n")
		sb.WriteString("**Suggested actions:**\n")
		sb.WriteString("1. Check the SonarQube dashboard for specific gate conditions\n")
		sb.WriteString("2. Add missing tests to increase coverage\n")
		sb.WriteString("3. Refactor duplicated code\n")
		sb.WriteString("4. Address flagged code smells\n")
	case "dependency":
		sb.WriteString("**Likely cause:** A dependency could not be resolved.\n\n")
		sb.WriteString("**Suggested actions:**\n")
		sb.WriteString("1. Check if the dependency registry/mirror is accessible\n")
		sb.WriteString("2. Verify the dependency exists and the version is correct\n")
		sb.WriteString("3. Run `go mod tidy` / `npm install` / equivalent locally\n")
		sb.WriteString("4. Check if a cache mount is configured correctly in pipeline.yaml\n")
	case "timeout":
		sb.WriteString("**Likely cause:** Task exceeded its time limit.\n\n")
		sb.WriteString("**Suggested actions:**\n")
		sb.WriteString("1. Increase the `timeout` in pipeline.yaml for this task\n")
		sb.WriteString("2. Check for infinite loops or hanging processes in the task\n")
		sb.WriteString("3. Consider splitting the task into smaller parallel tasks\n")
		sb.WriteString("4. Check if dependency caching is working (slow downloads?)\n")
	default:
		sb.WriteString("**Unable to classify automatically.** Review the logs below for details.\n\n")
	}

	// Show error logs.
	if len(errorLines) > 0 {
		sb.WriteString("\n### Error Output\n```\n")
		shown := errorLines
		if len(shown) > 50 {
			shown = shown[len(shown)-50:]
		}
		for _, line := range shown {
			sb.WriteString(line + "\n")
		}
		sb.WriteString("```\n")
	} else if len(allLogLines) > 0 {
		sb.WriteString("\n### Full Output (last 50 lines)\n```\n")
		shown := allLogLines
		if len(shown) > 50 {
			shown = shown[len(shown)-50:]
		}
		for _, line := range shown {
			sb.WriteString(line + "\n")
		}
		sb.WriteString("```\n")
	} else {
		sb.WriteString("\n*No logs available for this task.*\n")
	}

	sb.WriteString(fmt.Sprintf("\n---\nAfter fixing, use `submit_build` with repo_url `%s` to re-trigger CI.\n",
		buildResp.Build.Source.GetRepoUrl()))

	return textResult(sb.String())
}

func (s *mcpServer) toolWatchBuild(ctx context.Context, args json.RawMessage) *mcpToolResult {
	var params struct {
		BuildID string `json:"build_id"`
	}
	json.Unmarshal(args, &params)
	if params.BuildID == "" {
		return errorResult("build_id is required")
	}

	resp, err := s.scheduler.GetBuild(ctx, &pb.GetBuildRequest{
		BuildId: &pb.BuildID{Id: params.BuildID},
	})
	if err != nil {
		return errorResult("build not found: " + err.Error())
	}

	b := resp.Build
	total := len(b.Tasks)
	completed := 0
	for _, t := range b.Tasks {
		if t.State == pb.TaskState_TASK_STATE_PASSED ||
			t.State == pb.TaskState_TASK_STATE_FAILED ||
			t.State == pb.TaskState_TASK_STATE_SKIPPED ||
			t.State == pb.TaskState_TASK_STATE_CANCELLED ||
			t.State == pb.TaskState_TASK_STATE_TIMED_OUT {
			completed++
		}
	}

	progress := 0
	if total > 0 {
		progress = (completed * 100) / total
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Build: %s\n", b.BuildId.Id))
	sb.WriteString(fmt.Sprintf("**State:** %s\n", b.State))
	sb.WriteString(fmt.Sprintf("**Progress:** %d%% (%d/%d tasks complete)\n\n", progress, completed, total))

	isActive := b.State == pb.BuildState_BUILD_STATE_RUNNING || b.State == pb.BuildState_BUILD_STATE_QUEUED

	for _, t := range b.Tasks {
		icon := "⏳"
		switch t.State {
		case pb.TaskState_TASK_STATE_PASSED:
			icon = "✅"
		case pb.TaskState_TASK_STATE_FAILED, pb.TaskState_TASK_STATE_TIMED_OUT:
			icon = "❌"
		case pb.TaskState_TASK_STATE_RUNNING:
			icon = "🔄"
		case pb.TaskState_TASK_STATE_SKIPPED:
			icon = "⏭️"
		case pb.TaskState_TASK_STATE_CANCELLED:
			icon = "🚫"
		}
		sb.WriteString(fmt.Sprintf("%s %s — %s\n", icon, t.Name, t.State))
	}

	if isActive {
		sb.WriteString("\n*Build is still in progress. Call `watch_build` again to check for updates.*\n")
	} else if b.State == pb.BuildState_BUILD_STATE_FAILED {
		sb.WriteString("\n*Build failed. Use `diagnose_build` for detailed failure analysis.*\n")
	}

	return textResult(sb.String())
}

// --- Helpers ---

func containsAny(s string, substrs ...string) bool {
	lower := strings.ToLower(s)
	for _, sub := range substrs {
		if strings.Contains(lower, strings.ToLower(sub)) {
			return true
		}
	}
	return false
}

