package scm

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func signPayload(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestGitHubParsePush(t *testing.T) {
	payload := []byte(`{
		"ref": "refs/heads/main",
		"before": "aaa111",
		"after": "bbb222",
		"pusher": {"name": "alex"},
		"repository": {
			"full_name": "myorg/myrepo",
			"clone_url": "https://github.com/myorg/myrepo.git"
		},
		"commits": [
			{
				"id": "bbb222",
				"message": "fix: resolve crash on startup",
				"author": {"name": "alex"},
				"added": ["new_file.go"],
				"modified": ["main.go"],
				"removed": []
			}
		]
	}`)

	secret := "test-secret"
	req := httptest.NewRequest("POST", "/webhooks/github", bytes.NewReader(payload))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "delivery-123")
	req.Header.Set("X-Hub-Signature-256", signPayload(payload, secret))

	gh := NewGitHub(nil, "")
	event, err := gh.Parse(req, secret)
	if err != nil {
		t.Fatal(err)
	}

	if event.Type != EventPush {
		t.Fatalf("expected push event, got %d", event.Type)
	}
	if event.Push.RepoFullName != "myorg/myrepo" {
		t.Errorf("expected myorg/myrepo, got %s", event.Push.RepoFullName)
	}
	if event.Push.AfterSHA != "bbb222" {
		t.Errorf("expected bbb222, got %s", event.Push.AfterSHA)
	}
	if event.Push.Pusher != "alex" {
		t.Errorf("expected alex, got %s", event.Push.Pusher)
	}
	if len(event.Push.Commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(event.Push.Commits))
	}
	if event.Push.Commits[0].Added[0] != "new_file.go" {
		t.Errorf("expected new_file.go in added")
	}
}

func TestGitHubParsePR(t *testing.T) {
	payload := []byte(`{
		"action": "opened",
		"number": 42,
		"pull_request": {
			"title": "Add new feature",
			"head": {"sha": "abc123", "ref": "feature-branch"},
			"base": {"ref": "main"},
			"user": {"login": "alex"}
		},
		"repository": {
			"full_name": "myorg/myrepo",
			"clone_url": "https://github.com/myorg/myrepo.git"
		}
	}`)

	req := httptest.NewRequest("POST", "/webhooks/github", bytes.NewReader(payload))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-GitHub-Delivery", "delivery-456")

	gh := NewGitHub(nil, "")
	event, err := gh.Parse(req, "") // no secret verification
	if err != nil {
		t.Fatal(err)
	}

	if event.Type != EventPullRequest {
		t.Fatalf("expected PR event, got %d", event.Type)
	}
	if event.PR.Number != 42 {
		t.Errorf("expected PR #42, got %d", event.PR.Number)
	}
	if event.PR.Action != "opened" {
		t.Errorf("expected opened, got %s", event.PR.Action)
	}
	if event.PR.HeadSHA != "abc123" {
		t.Errorf("expected abc123, got %s", event.PR.HeadSHA)
	}
	if event.PR.HeadBranch != "feature-branch" {
		t.Errorf("expected feature-branch, got %s", event.PR.HeadBranch)
	}
}

func TestGitHubParseComment(t *testing.T) {
	payload := []byte(`{
		"action": "created",
		"issue": {
			"number": 42,
			"pull_request": {"url": "https://api.github.com/repos/myorg/myrepo/pulls/42"}
		},
		"comment": {
			"body": "/retry",
			"user": {"login": "alex"}
		},
		"repository": {
			"full_name": "myorg/myrepo",
			"clone_url": "https://github.com/myorg/myrepo.git"
		}
	}`)

	req := httptest.NewRequest("POST", "/webhooks/github", bytes.NewReader(payload))
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-GitHub-Delivery", "delivery-789")

	gh := NewGitHub(nil, "")
	event, err := gh.Parse(req, "")
	if err != nil {
		t.Fatal(err)
	}

	if event.Type != EventComment {
		t.Fatalf("expected comment event, got %d", event.Type)
	}
	if event.Comment.Body != "/retry" {
		t.Errorf("expected /retry, got %s", event.Comment.Body)
	}
	if event.Comment.PRNumber != 42 {
		t.Errorf("expected PR 42, got %d", event.Comment.PRNumber)
	}
}

func TestGitHubBadSignature(t *testing.T) {
	payload := []byte(`{"ref":"refs/heads/main"}`)
	req := httptest.NewRequest("POST", "/webhooks/github", bytes.NewReader(payload))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", "sha256=bad")

	gh := NewGitHub(nil, "")
	_, err := gh.Parse(req, "correct-secret")
	if err == nil {
		t.Fatal("expected signature verification error")
	}
}

func TestGitHubReportStatus(t *testing.T) {
	var receivedPath, receivedState string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		receivedState = body["state"]
		w.WriteHeader(201)
	}))
	defer server.Close()

	gh := NewGitHub(server.Client(), server.URL)
	err := gh.ReportStatus(t.Context(), "test-token", StatusReport{
		RepoFullName: "myorg/myrepo",
		CommitSHA:    "abc123",
		State:        StatusSuccess,
		Context:      "ci/unit-tests",
		Description:  "All tests passed",
	})
	if err != nil {
		t.Fatal(err)
	}

	if receivedPath != "/repos/myorg/myrepo/statuses/abc123" {
		t.Errorf("unexpected path: %s", receivedPath)
	}
	if receivedState != "success" {
		t.Errorf("expected success state, got %s", receivedState)
	}
}

var _ = json.Unmarshal // ensure encoding/json import is used
