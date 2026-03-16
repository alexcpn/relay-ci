package scm

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// GitHub implements Client for GitHub webhooks and status API.
type GitHub struct {
	httpClient *http.Client
	apiBase    string // default "https://api.github.com"
}

// NewGitHub creates a new GitHub client.
func NewGitHub(httpClient *http.Client, apiBase string) *GitHub {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	return &GitHub{httpClient: httpClient, apiBase: apiBase}
}

func (g *GitHub) Provider() Provider { return ProviderGitHub }

// Parse parses a GitHub webhook request into a canonical Event.
func (g *GitHub) Parse(r *http.Request, secret string) (*Event, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("reading body: %w", err)
	}
	defer r.Body.Close()

	// Verify HMAC-SHA256 signature.
	if secret != "" {
		sig := r.Header.Get("X-Hub-Signature-256")
		if err := verifyGitHubSignature(body, sig, secret); err != nil {
			return nil, err
		}
	}

	eventType := r.Header.Get("X-GitHub-Event")
	deliveryID := r.Header.Get("X-GitHub-Delivery")

	event := &Event{
		ID:         deliveryID,
		Provider:   ProviderGitHub,
		ReceivedAt: time.Now(),
	}

	switch eventType {
	case "push":
		return g.parsePush(body, event)
	case "pull_request":
		return g.parsePullRequest(body, event)
	case "issue_comment":
		return g.parseComment(body, event)
	default:
		return nil, fmt.Errorf("unsupported event type: %s", eventType)
	}
}

// ReportStatus sends a commit status to GitHub.
func (g *GitHub) ReportStatus(ctx context.Context, token string, report StatusReport) error {
	ghState := githubStatusState(report.State)

	payload := map[string]string{
		"state":       ghState,
		"context":     report.Context,
		"description": report.Description,
	}
	if report.TargetURL != "" {
		payload["target_url"] = report.TargetURL
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/repos/%s/statuses/%s", g.apiBase, report.RepoFullName, report.CommitSHA)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("reporting status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("github status API returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// --- GitHub payload parsing ---

type ghPushPayload struct {
	Ref    string `json:"ref"`
	Before string `json:"before"`
	After  string `json:"after"`
	Pusher struct {
		Name string `json:"name"`
	} `json:"pusher"`
	Repository struct {
		FullName string `json:"full_name"`
		CloneURL string `json:"clone_url"`
	} `json:"repository"`
	Commits []struct {
		ID       string   `json:"id"`
		Message  string   `json:"message"`
		Author   struct{ Name string } `json:"author"`
		Added    []string `json:"added"`
		Modified []string `json:"modified"`
		Removed  []string `json:"removed"`
	} `json:"commits"`
}

func (g *GitHub) parsePush(body []byte, event *Event) (*Event, error) {
	var p ghPushPayload
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
		RepoURL:      p.Repository.CloneURL,
		RepoFullName: p.Repository.FullName,
		Ref:          p.Ref,
		BeforeSHA:    p.Before,
		AfterSHA:     p.After,
		Pusher:       p.Pusher.Name,
		Commits:      commits,
	}
	return event, nil
}

type ghPRPayload struct {
	Action      string `json:"action"`
	Number      uint32 `json:"number"`
	PullRequest struct {
		Title string `json:"title"`
		Head  struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
		CloneURL string `json:"clone_url"`
	} `json:"repository"`
}

func (g *GitHub) parsePullRequest(body []byte, event *Event) (*Event, error) {
	var p ghPRPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("parsing PR payload: %w", err)
	}

	event.Type = EventPullRequest
	event.PR = &PullRequestEvent{
		RepoURL:      p.Repository.CloneURL,
		RepoFullName: p.Repository.FullName,
		Action:       p.Action,
		Number:       p.Number,
		Title:        p.PullRequest.Title,
		HeadSHA:      p.PullRequest.Head.SHA,
		HeadBranch:   p.PullRequest.Head.Ref,
		BaseBranch:   p.PullRequest.Base.Ref,
		Author:       p.PullRequest.User.Login,
	}
	return event, nil
}

type ghCommentPayload struct {
	Action string `json:"action"`
	Issue  struct {
		Number      uint32 `json:"number"`
		PullRequest *struct {
			URL string `json:"url"`
		} `json:"pull_request"`
	} `json:"issue"`
	Comment struct {
		Body string `json:"body"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"comment"`
	Repository struct {
		FullName string `json:"full_name"`
		CloneURL string `json:"clone_url"`
	} `json:"repository"`
}

func (g *GitHub) parseComment(body []byte, event *Event) (*Event, error) {
	var p ghCommentPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("parsing comment payload: %w", err)
	}

	// Only handle PR comments (not issue comments).
	if p.Issue.PullRequest == nil {
		return nil, fmt.Errorf("not a PR comment")
	}

	if p.Action != "created" {
		return nil, fmt.Errorf("ignoring comment action: %s", p.Action)
	}

	event.Type = EventComment
	event.Comment = &CommentEvent{
		RepoURL:      p.Repository.CloneURL,
		RepoFullName: p.Repository.FullName,
		PRNumber:     p.Issue.Number,
		Body:         p.Comment.Body,
		Author:       p.Comment.User.Login,
	}
	return event, nil
}

// --- Helpers ---

func verifyGitHubSignature(body []byte, signature, secret string) error {
	if signature == "" {
		return fmt.Errorf("missing X-Hub-Signature-256 header")
	}

	sig := strings.TrimPrefix(signature, "sha256=")
	decoded, err := hex.DecodeString(sig)
	if err != nil {
		return fmt.Errorf("invalid signature hex: %w", err)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := mac.Sum(nil)

	if !hmac.Equal(decoded, expected) {
		return fmt.Errorf("webhook signature verification failed")
	}
	return nil
}

func githubStatusState(s StatusState) string {
	switch s {
	case StatusPending, StatusRunning:
		return "pending"
	case StatusSuccess:
		return "success"
	case StatusFailure:
		return "failure"
	case StatusError:
		return "error"
	default:
		return "error"
	}
}
