package scm

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// GitLab implements Client for GitLab webhooks and status API.
type GitLab struct {
	httpClient *http.Client
	apiBase    string // default "https://gitlab.com/api/v4"
}

// NewGitLab creates a new GitLab client.
func NewGitLab(httpClient *http.Client, apiBase string) *GitLab {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	if apiBase == "" {
		apiBase = "https://gitlab.com/api/v4"
	}
	return &GitLab{httpClient: httpClient, apiBase: apiBase}
}

func (g *GitLab) Provider() Provider { return ProviderGitLab }

// Parse parses a GitLab webhook request into a canonical Event.
func (g *GitLab) Parse(r *http.Request, secret string) (*Event, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("reading body: %w", err)
	}
	defer r.Body.Close()

	// Verify secret token.
	if secret != "" {
		token := r.Header.Get("X-Gitlab-Token")
		if subtle.ConstantTimeCompare([]byte(token), []byte(secret)) != 1 {
			return nil, fmt.Errorf("webhook secret verification failed")
		}
	}

	eventType := r.Header.Get("X-Gitlab-Event")
	eventID := r.Header.Get("X-Gitlab-Event-UUID")

	event := &Event{
		ID:         eventID,
		Provider:   ProviderGitLab,
		ReceivedAt: time.Now(),
	}

	switch eventType {
	case "Push Hook":
		return g.parsePush(body, event)
	case "Merge Request Hook":
		return g.parseMergeRequest(body, event)
	case "Note Hook":
		return g.parseNote(body, event)
	default:
		return nil, fmt.Errorf("unsupported event type: %s", eventType)
	}
}

// ReportStatus sends a commit status to GitLab.
func (g *GitLab) ReportStatus(ctx context.Context, token string, report StatusReport) error {
	glState := gitlabStatusState(report.State)

	url := fmt.Sprintf("%s/projects/%s/statuses/%s?state=%s&name=%s",
		g.apiBase,
		gitlabProjectPath(report.RepoFullName),
		report.CommitSHA,
		glState,
		report.Context,
	)

	payload := map[string]string{
		"description": report.Description,
	}
	if report.TargetURL != "" {
		payload["target_url"] = report.TargetURL
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("reporting status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("gitlab status API returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// PostPRComment posts a markdown comment on a GitLab merge request.
func (g *GitLab) PostPRComment(ctx context.Context, token string, comment PRComment) error {
	body, err := json.Marshal(map[string]string{"body": comment.Body})
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/projects/%s/merge_requests/%s/notes",
		g.apiBase,
		gitlabProjectPath(comment.RepoFullName),
		comment.PRNumber,
	)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("posting MR comment: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("gitlab MR comment API returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// --- GitLab payload parsing ---

type glPushPayload struct {
	Before     string `json:"before"`
	After      string `json:"after"`
	Ref        string `json:"ref"`
	UserName   string `json:"user_name"`
	Project    glProject `json:"project"`
	Commits    []struct {
		ID       string   `json:"id"`
		Message  string   `json:"message"`
		Author   struct{ Name string } `json:"author"`
		Added    []string `json:"added"`
		Modified []string `json:"modified"`
		Removed  []string `json:"removed"`
	} `json:"commits"`
}

type glProject struct {
	PathWithNamespace string `json:"path_with_namespace"`
	GitHTTPURL        string `json:"git_http_url"`
}

func (g *GitLab) parsePush(body []byte, event *Event) (*Event, error) {
	var p glPushPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("parsing push payload: %w", err)
	}

	commits := make([]Commit, len(p.Commits))
	for i, c := range p.Commits {
		commits[i] = Commit{
			SHA: c.ID, Message: c.Message, Author: c.Author.Name,
			Added: c.Added, Modified: c.Modified, Removed: c.Removed,
		}
	}

	event.Type = EventPush
	event.Push = &PushEvent{
		RepoURL:      p.Project.GitHTTPURL,
		RepoFullName: p.Project.PathWithNamespace,
		Ref:          p.Ref,
		BeforeSHA:    p.Before,
		AfterSHA:     p.After,
		Pusher:       p.UserName,
		Commits:      commits,
	}
	return event, nil
}

type glMRPayload struct {
	ObjectAttributes struct {
		Action     string `json:"action"`
		IID        uint32 `json:"iid"`
		Title      string `json:"title"`
		LastCommit struct {
			ID string `json:"id"`
		} `json:"last_commit"`
		SourceBranch string `json:"source_branch"`
		TargetBranch string `json:"target_branch"`
	} `json:"object_attributes"`
	User struct {
		Username string `json:"username"`
	} `json:"user"`
	Project glProject `json:"project"`
}

func (g *GitLab) parseMergeRequest(body []byte, event *Event) (*Event, error) {
	var p glMRPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("parsing MR payload: %w", err)
	}

	// Map GitLab actions to our canonical actions.
	action := p.ObjectAttributes.Action
	switch action {
	case "open":
		action = "opened"
	case "update":
		action = "synchronize"
	case "close":
		action = "closed"
	case "reopen":
		action = "reopened"
	}

	event.Type = EventPullRequest
	event.PR = &PullRequestEvent{
		RepoURL:      p.Project.GitHTTPURL,
		RepoFullName: p.Project.PathWithNamespace,
		Action:       action,
		Number:       p.ObjectAttributes.IID,
		Title:        p.ObjectAttributes.Title,
		HeadSHA:      p.ObjectAttributes.LastCommit.ID,
		HeadBranch:   p.ObjectAttributes.SourceBranch,
		BaseBranch:   p.ObjectAttributes.TargetBranch,
		Author:       p.User.Username,
	}
	return event, nil
}

type glNotePayload struct {
	ObjectAttributes struct {
		Note         string `json:"note"`
		NoteableType string `json:"noteable_type"`
	} `json:"object_attributes"`
	User struct {
		Username string `json:"username"`
	} `json:"user"`
	MergeRequest *struct {
		IID        uint32 `json:"iid"`
		LastCommit struct {
			ID string `json:"id"`
		} `json:"last_commit"`
	} `json:"merge_request"`
	Project glProject `json:"project"`
}

func (g *GitLab) parseNote(body []byte, event *Event) (*Event, error) {
	var p glNotePayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("parsing note payload: %w", err)
	}

	if p.ObjectAttributes.NoteableType != "MergeRequest" || p.MergeRequest == nil {
		return nil, fmt.Errorf("not a merge request comment")
	}

	event.Type = EventComment
	event.Comment = &CommentEvent{
		RepoURL:      p.Project.GitHTTPURL,
		RepoFullName: p.Project.PathWithNamespace,
		PRNumber:     p.MergeRequest.IID,
		Body:         p.ObjectAttributes.Note,
		Author:       p.User.Username,
		HeadSHA:      p.MergeRequest.LastCommit.ID,
	}
	return event, nil
}

// --- Helpers ---

func gitlabStatusState(s StatusState) string {
	switch s {
	case StatusPending:
		return "pending"
	case StatusRunning:
		return "running"
	case StatusSuccess:
		return "success"
	case StatusFailure:
		return "failed"
	case StatusError:
		return "failed"
	default:
		return "failed"
	}
}

// gitlabProjectPath URL-encodes the project path for API calls.
func gitlabProjectPath(fullName string) string {
	// GitLab API expects URL-encoded path: "org/repo" -> "org%2Frepo"
	return strings.ReplaceAll(fullName, "/", "%2F")
}
