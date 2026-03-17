package scm

import "time"

// Provider identifies the git hosting platform.
type Provider int

const (
	ProviderUnknown Provider = iota
	ProviderGitHub
	ProviderGitLab
)

func (p Provider) String() string {
	switch p {
	case ProviderGitHub:
		return "github"
	case ProviderGitLab:
		return "gitlab"
	default:
		return "unknown"
	}
}

// Event is the canonical representation of a git provider webhook event.
type Event struct {
	ID         string
	Provider   Provider
	ReceivedAt time.Time
	Type       EventType
	Push       *PushEvent
	PR         *PullRequestEvent
	Comment    *CommentEvent
}

// EventType classifies the webhook event.
type EventType int

const (
	EventUnknown EventType = iota
	EventPush
	EventPullRequest
	EventComment
)

// PushEvent is sent when commits are pushed to a branch.
type PushEvent struct {
	RepoURL      string
	RepoFullName string // e.g. "org/repo"
	Ref          string // e.g. "refs/heads/main"
	BeforeSHA    string
	AfterSHA     string
	Pusher       string
	Commits      []Commit
}

// PullRequestEvent is sent when a PR is opened, updated, or closed.
type PullRequestEvent struct {
	RepoURL      string
	RepoFullName string
	Action       string // opened, synchronize, closed, reopened
	Number       uint32
	Title        string
	HeadSHA      string
	HeadBranch   string
	BaseBranch   string
	Author       string
}

// CommentEvent is sent when someone comments on a PR.
type CommentEvent struct {
	RepoURL      string
	RepoFullName string
	PRNumber     uint32
	Body         string
	Author       string
	HeadSHA      string
}

// Commit is a single commit in a push event.
type Commit struct {
	SHA      string
	Message  string
	Author   string
	Added    []string
	Modified []string
	Removed  []string
}

// StatusState represents build status reported back to the provider.
type StatusState int

const (
	StatusPending StatusState = iota
	StatusRunning
	StatusSuccess
	StatusFailure
	StatusError
)

func (s StatusState) String() string {
	switch s {
	case StatusPending:
		return "pending"
	case StatusRunning:
		return "running"
	case StatusSuccess:
		return "success"
	case StatusFailure:
		return "failure"
	case StatusError:
		return "error"
	default:
		return "unknown"
	}
}

// StatusReport is sent to the git provider to update commit status.
type StatusReport struct {
	Provider     Provider
	RepoFullName string
	CommitSHA    string
	State        StatusState
	Context      string // e.g. "ci/unit-tests"
	Description  string // e.g. "Unit tests passed in 34s"
	TargetURL    string // link to build details
}

// PRComment is posted to a pull request as a bot comment.
type PRComment struct {
	RepoFullName string
	PRNumber     string
	Body         string // Markdown body
}

// WebhookConfig defines how a repository is connected to our CI.
type WebhookConfig struct {
	ID               string
	RepoFullName     string
	Provider         Provider
	WebhookSecret    string
	AccessToken      string
	PipelineFile     string   // default "pipeline.py"
	TriggerBranches  []string // empty = all
	TriggerOnPR      bool
	TriggerOnPush    bool
}
