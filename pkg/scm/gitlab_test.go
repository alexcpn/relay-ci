package scm

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGitLabParsePush(t *testing.T) {
	payload := []byte(`{
		"before": "aaa111",
		"after": "bbb222",
		"ref": "refs/heads/main",
		"user_name": "alex",
		"project": {
			"path_with_namespace": "myorg/myrepo",
			"git_http_url": "https://gitlab.com/myorg/myrepo.git"
		},
		"commits": [
			{
				"id": "bbb222",
				"message": "fix: something",
				"author": {"name": "alex"},
				"added": ["file.go"],
				"modified": [],
				"removed": []
			}
		]
	}`)

	req := httptest.NewRequest("POST", "/webhooks/gitlab", bytes.NewReader(payload))
	req.Header.Set("X-Gitlab-Event", "Push Hook")
	req.Header.Set("X-Gitlab-Event-UUID", "uuid-123")
	req.Header.Set("X-Gitlab-Token", "my-secret")

	gl := NewGitLab(nil, "")
	event, err := gl.Parse(req, "my-secret")
	if err != nil {
		t.Fatal(err)
	}

	if event.Type != EventPush {
		t.Fatalf("expected push, got %d", event.Type)
	}
	if event.Push.RepoFullName != "myorg/myrepo" {
		t.Errorf("expected myorg/myrepo, got %s", event.Push.RepoFullName)
	}
	if event.Push.AfterSHA != "bbb222" {
		t.Errorf("expected bbb222, got %s", event.Push.AfterSHA)
	}
}

func TestGitLabParseMergeRequest(t *testing.T) {
	payload := []byte(`{
		"object_attributes": {
			"action": "open",
			"iid": 7,
			"title": "New feature",
			"last_commit": {"id": "def456"},
			"source_branch": "feature-x",
			"target_branch": "main"
		},
		"user": {"username": "alex"},
		"project": {
			"path_with_namespace": "myorg/myrepo",
			"git_http_url": "https://gitlab.com/myorg/myrepo.git"
		}
	}`)

	req := httptest.NewRequest("POST", "/webhooks/gitlab", bytes.NewReader(payload))
	req.Header.Set("X-Gitlab-Event", "Merge Request Hook")

	gl := NewGitLab(nil, "")
	event, err := gl.Parse(req, "")
	if err != nil {
		t.Fatal(err)
	}

	if event.Type != EventPullRequest {
		t.Fatalf("expected PR event, got %d", event.Type)
	}
	if event.PR.Action != "opened" {
		t.Errorf("expected mapped action 'opened', got %s", event.PR.Action)
	}
	if event.PR.Number != 7 {
		t.Errorf("expected MR 7, got %d", event.PR.Number)
	}
	if event.PR.HeadBranch != "feature-x" {
		t.Errorf("expected feature-x, got %s", event.PR.HeadBranch)
	}
}

func TestGitLabParseNote(t *testing.T) {
	payload := []byte(`{
		"object_attributes": {
			"note": "/retry",
			"noteable_type": "MergeRequest"
		},
		"user": {"username": "alex"},
		"merge_request": {
			"iid": 7,
			"last_commit": {"id": "def456"}
		},
		"project": {
			"path_with_namespace": "myorg/myrepo",
			"git_http_url": "https://gitlab.com/myorg/myrepo.git"
		}
	}`)

	req := httptest.NewRequest("POST", "/webhooks/gitlab", bytes.NewReader(payload))
	req.Header.Set("X-Gitlab-Event", "Note Hook")

	gl := NewGitLab(nil, "")
	event, err := gl.Parse(req, "")
	if err != nil {
		t.Fatal(err)
	}

	if event.Type != EventComment {
		t.Fatalf("expected comment, got %d", event.Type)
	}
	if event.Comment.Body != "/retry" {
		t.Errorf("expected /retry, got %s", event.Comment.Body)
	}
}

func TestGitLabBadSecret(t *testing.T) {
	payload := []byte(`{}`)
	req := httptest.NewRequest("POST", "/webhooks/gitlab", bytes.NewReader(payload))
	req.Header.Set("X-Gitlab-Event", "Push Hook")
	req.Header.Set("X-Gitlab-Token", "wrong-secret")

	gl := NewGitLab(nil, "")
	_, err := gl.Parse(req, "correct-secret")
	if err == nil {
		t.Fatal("expected secret verification error")
	}
}

func TestGitLabReportStatus(t *testing.T) {
	var receivedRawPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedRawPath = r.URL.RawPath
		if receivedRawPath == "" {
			receivedRawPath = r.URL.Path
		}
		w.WriteHeader(201)
	}))
	defer server.Close()

	gl := NewGitLab(server.Client(), server.URL)
	err := gl.ReportStatus(t.Context(), "test-token", StatusReport{
		RepoFullName: "myorg/myrepo",
		CommitSHA:    "abc123",
		State:        StatusSuccess,
		Context:      "ci/unit-tests",
		Description:  "All tests passed",
	})
	if err != nil {
		t.Fatal(err)
	}

	expected := "/projects/myorg%2Fmyrepo/statuses/abc123"
	if receivedRawPath != expected {
		t.Errorf("expected path %s, got %s", expected, receivedRawPath)
	}
}

func TestRouterDetectsProvider(t *testing.T) {
	gh := NewGitHub(nil, "")
	gl := NewGitLab(nil, "")
	router := NewRouter(gh, gl)

	payload := []byte(`{
		"ref": "refs/heads/main", "before": "a", "after": "b",
		"pusher": {"name": "x"},
		"repository": {"full_name": "o/r", "clone_url": "https://x.com/o/r.git"},
		"commits": []
	}`)

	// GitHub request
	req := httptest.NewRequest("POST", "/webhooks", bytes.NewReader(payload))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "d1")

	event, err := router.Parse(req, "")
	if err != nil {
		t.Fatal(err)
	}
	if event.Provider != ProviderGitHub {
		t.Errorf("expected github, got %s", event.Provider)
	}
}

// Import json for compiler satisfaction
var _ = json.Unmarshal
