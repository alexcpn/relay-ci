package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/ci-system/ci/gen/ci/v1"
	"github.com/ci-system/ci/pkg/observability"
	"github.com/ci-system/ci/pkg/pipeline"
	"github.com/ci-system/ci/pkg/scheduler"
)

// verifyServer extends the schedulerServer with local-bundle verification.
// It is composed into schedulerServer at construction time so the existing
// SchedulerService gRPC implementation gains the new RPCs.
type verifyServer struct {
	digests   *digestStore
	bundleDir string
	logger    *slog.Logger
}

func newVerifyServer(dataRoot string, logger *slog.Logger) (*verifyServer, error) {
	bundleDir := filepath.Join(dataRoot, "bundles")
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating bundle dir: %w", err)
	}
	digests, err := newDigestStore(filepath.Join(dataRoot, "pipeline-digests.json"))
	if err != nil {
		return nil, err
	}
	return &verifyServer{
		digests:   digests,
		bundleDir: bundleDir,
		logger:    logger,
	}, nil
}

// VerifyLocal accepts a streamed git bundle, pins or checks the pipeline
// digest, and submits a build using the existing scheduler.
func (s *schedulerServer) VerifyLocal(stream pb.SchedulerService_VerifyLocalServer) error {
	if s.verify == nil {
		return status.Error(codes.Unimplemented, "verify is not enabled on this master")
	}

	// First message must carry the header.
	first, err := stream.Recv()
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "receiving header: %v", err)
	}
	header := first.GetHeader()
	if header == nil {
		return status.Error(codes.InvalidArgument, "first message must contain a header")
	}
	if header.ProjectId == "" || header.Branch == "" {
		return status.Error(codes.InvalidArgument, "header.project_id and header.branch are required")
	}
	baseBranch := header.BaseBranch
	if baseBranch == "" {
		baseBranch = "master"
	}

	// Allocate the build_id up front so the bundle file name is stable.
	buildID := generateID()
	bundlePath := filepath.Join(s.verify.bundleDir, buildID+".bundle")

	// Stream bundle bytes to disk. Master frees its memory; the file lives
	// until the build cleans up after itself.
	bundleFile, err := os.Create(bundlePath)
	if err != nil {
		return status.Errorf(codes.Internal, "creating bundle file: %v", err)
	}
	cleanupBundle := func() { _ = os.Remove(bundlePath) }

	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			bundleFile.Close()
			cleanupBundle()
			return status.Errorf(codes.InvalidArgument, "receiving chunk: %v", err)
		}
		chunk := msg.GetChunk()
		if chunk == nil {
			bundleFile.Close()
			cleanupBundle()
			return status.Error(codes.InvalidArgument, "after header, every message must contain a chunk")
		}
		if _, err := bundleFile.Write(chunk); err != nil {
			bundleFile.Close()
			cleanupBundle()
			return status.Errorf(codes.Internal, "writing bundle: %v", err)
		}
	}
	if err := bundleFile.Close(); err != nil {
		cleanupBundle()
		return status.Errorf(codes.Internal, "closing bundle: %v", err)
	}

	ctx := stream.Context()

	// Sanity-check the bundle.
	if out, err := exec.CommandContext(ctx, "git", "bundle", "verify", bundlePath).CombinedOutput(); err != nil {
		cleanupBundle()
		return status.Errorf(codes.InvalidArgument, "bundle verify failed: %v\n%s", err, out)
	}

	// Clone the base branch from the bundle to read pipeline.yaml. The base
	// branch is the trust anchor for the pipeline digest — pinning here means
	// pipeline.yaml changes flow through master and re-prompt the user.
	scratch, err := os.MkdirTemp("", "relay-verify-*")
	if err != nil {
		cleanupBundle()
		return status.Errorf(codes.Internal, "creating scratch dir: %v", err)
	}
	defer os.RemoveAll(scratch)

	cloneOut, err := exec.CommandContext(ctx, "git",
		"clone", "--branch", baseBranch, bundlePath, scratch,
	).CombinedOutput()
	if err != nil {
		cleanupBundle()
		return status.Errorf(codes.InvalidArgument,
			"cloning base branch %q from bundle: %v\n%s", baseBranch, err, cloneOut)
	}

	// Locate pipeline.yaml in the base branch.
	var pipelinePath, pipelineRel string
	for _, name := range []string{"pipeline.yml", "pipeline.yaml"} {
		p := filepath.Join(scratch, name)
		if _, err := os.Stat(p); err == nil {
			pipelinePath = p
			pipelineRel = name
			break
		}
	}
	if pipelinePath == "" {
		cleanupBundle()
		return status.Errorf(codes.FailedPrecondition,
			"no pipeline.yml or pipeline.yaml on base branch %q", baseBranch)
	}

	pipelineBytes, err := os.ReadFile(pipelinePath)
	if err != nil {
		cleanupBundle()
		return status.Errorf(codes.Internal, "reading pipeline file: %v", err)
	}
	currentDigest := "sha256:" + sha256Hex(pipelineBytes)

	// TOFU digest check.
	digestStatus := pb.PipelineDigestStatus_PIPELINE_DIGEST_STATUS_OK
	pinnedDigest := ""
	var acceptedDiff string
	currentContent := string(pipelineBytes)
	existing, hasPin := s.verify.digests.Get(header.ProjectId)
	switch {
	case !hasPin:
		if err := s.verify.digests.Pin(header.ProjectId, header.RepoName, currentDigest, pipelineRel, currentContent); err != nil {
			cleanupBundle()
			return status.Errorf(codes.Internal, "pinning digest: %v", err)
		}
		digestStatus = pb.PipelineDigestStatus_PIPELINE_DIGEST_STATUS_PINNED
		pinnedDigest = currentDigest
		s.verify.logger.Info("pipeline digest pinned",
			"project_id", header.ProjectId, "digest", currentDigest, "repo", header.RepoName)
	case existing.Digest == currentDigest:
		_ = s.verify.digests.Pin(header.ProjectId, header.RepoName, currentDigest, pipelineRel, currentContent)
		pinnedDigest = existing.Digest
	case header.AcceptPipelineChange:
		acceptedDiff = unifiedDiff(existing.PipelinePath, existing.Content, pipelineRel, currentContent)
		if err := s.verify.digests.Repin(header.ProjectId, header.RepoName, currentDigest, pipelineRel, currentContent); err != nil {
			cleanupBundle()
			return status.Errorf(codes.Internal, "re-pinning digest: %v", err)
		}
		digestStatus = pb.PipelineDigestStatus_PIPELINE_DIGEST_STATUS_ACCEPTED
		pinnedDigest = currentDigest
		s.verify.logger.Warn("pipeline digest re-pinned by caller",
			"project_id", header.ProjectId, "old", existing.Digest, "new", currentDigest)
	default:
		// Mismatch — refuse to schedule the build, return diff for the caller.
		diff := unifiedDiff(existing.PipelinePath, existing.Content, pipelineRel, currentContent)
		cleanupBundle()
		s.verify.logger.Warn("pipeline digest mismatch — build refused",
			"project_id", header.ProjectId, "pinned", existing.Digest, "current", currentDigest)
		return stream.SendAndClose(&pb.VerifyLocalResponse{
			DigestStatus:  pb.PipelineDigestStatus_PIPELINE_DIGEST_STATUS_CHANGED,
			PinnedDigest:  existing.Digest,
			CurrentDigest: currentDigest,
			PipelineDiff:  diff,
			PipelinePath:  pipelineRel,
			Error:         "pipeline.yaml on base branch differs from pinned digest",
		})
	}

	// Parse pipeline + build the DAG.
	cfg, err := pipeline.Parse(pipelineBytes)
	if err != nil {
		cleanupBundle()
		return status.Errorf(codes.InvalidArgument, "parsing pipeline: %v", err)
	}
	g, err := pipeline.BuildGraph(cfg)
	if err != nil {
		cleanupBundle()
		return status.Errorf(codes.InvalidArgument, "building pipeline graph: %v", err)
	}

	// Inject env so the clone task uses the bundle as if it were a remote.
	// The worker executor sees RELAY_BUNDLE_PATH and bind-mounts the file
	// read-only into the container at /relay-bundle.git.
	const containerBundlePath = "/relay-bundle.git"
	buildEnv := map[string]string{
		"REPO_URL":          containerBundlePath,
		"BRANCH":            header.Branch,
		"COMMIT_SHA":        header.CommitSha,
		"BASE_BRANCH":       baseBranch,
		"RELAY_BUNDLE_PATH": bundlePath,
		"RELAY_VERIFY":      "1",
	}
	for _, task := range g.Tasks() {
		if task.Env == nil {
			task.Env = make(map[string]string)
		}
		for k, v := range buildEnv {
			if _, exists := task.Env[k]; !exists {
				task.Env[k] = v
			}
		}
	}

	repoName := header.RepoName
	if repoName == "" {
		repoName = "local:" + header.ProjectId[:minInt(8, len(header.ProjectId))]
	}

	build := &scheduler.Build{
		ID:          buildID,
		Graph:       g,
		RepoURL:     "bundle://" + filepath.Base(bundlePath),
		CommitSHA:   header.CommitSha,
		Branch:      header.Branch,
		TriggeredBy: "verify:" + repoName,
	}
	if err := s.sched.SubmitBuild(build); err != nil {
		cleanupBundle()
		return status.Errorf(codes.Internal, "submitting build: %v", err)
	}
	observability.BuildsInProgress.Inc()

	s.verify.logger.Info("verify build submitted",
		"build_id", buildID, "project_id", header.ProjectId,
		"branch", header.Branch, "base", baseBranch, "repo", repoName,
	)

	return stream.SendAndClose(&pb.VerifyLocalResponse{
		BuildId:       buildID,
		DigestStatus:  digestStatus,
		PinnedDigest:  pinnedDigest,
		CurrentDigest: currentDigest,
		PipelineDiff:  acceptedDiff,
		PipelinePath:  pipelineRel,
	})
}

// ListPipelinePins returns all pinned pipeline digests.
func (s *schedulerServer) ListPipelinePins(ctx context.Context, req *pb.ListPipelinePinsRequest) (*pb.ListPipelinePinsResponse, error) {
	if s.verify == nil {
		return nil, status.Error(codes.Unimplemented, "verify is not enabled on this master")
	}
	pins := s.verify.digests.List()
	out := make([]*pb.PipelinePin, 0, len(pins))
	for _, p := range pins {
		out = append(out, &pb.PipelinePin{
			ProjectId:    p.ProjectID,
			RepoName:     p.RepoName,
			Digest:       p.Digest,
			PipelinePath: p.PipelinePath,
			FirstSeen:    timestamppb.New(p.FirstSeen),
			LastSeen:     timestamppb.New(p.LastSeen),
		})
	}
	return &pb.ListPipelinePinsResponse{Pins: out}, nil
}

// UnpinPipeline removes a project's pinned digest.
func (s *schedulerServer) UnpinPipeline(ctx context.Context, req *pb.UnpinPipelineRequest) (*pb.UnpinPipelineResponse, error) {
	if s.verify == nil {
		return nil, status.Error(codes.Unimplemented, "verify is not enabled on this master")
	}
	if req.ProjectId == "" {
		return nil, status.Error(codes.InvalidArgument, "project_id is required")
	}
	removed, err := s.verify.digests.Unpin(req.ProjectId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "unpinning: %v", err)
	}
	return &pb.UnpinPipelineResponse{Removed: removed}, nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// unifiedDiff produces a unified diff between two file contents using the
// system `diff -u` if available, falling back to a plain side-by-side dump.
func unifiedDiff(oldPath, oldContent, newPath, newContent string) string {
	tmp, err := os.MkdirTemp("", "relay-diff-*")
	if err != nil {
		return diffFallback(oldPath, oldContent, newPath, newContent)
	}
	defer os.RemoveAll(tmp)

	oldFile := filepath.Join(tmp, "pinned")
	newFile := filepath.Join(tmp, "current")
	if err := os.WriteFile(oldFile, []byte(oldContent), 0o644); err != nil {
		return diffFallback(oldPath, oldContent, newPath, newContent)
	}
	if err := os.WriteFile(newFile, []byte(newContent), 0o644); err != nil {
		return diffFallback(oldPath, oldContent, newPath, newContent)
	}

	cmd := exec.Command("diff", "-u",
		"--label", "pinned/"+oldPath,
		"--label", "current/"+newPath,
		oldFile, newFile)
	out, _ := cmd.Output() // exit code 1 means "files differ" — that is expected here
	if len(out) == 0 {
		return diffFallback(oldPath, oldContent, newPath, newContent)
	}
	return string(out)
}

func diffFallback(oldPath, oldContent, newPath, newContent string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- pinned/%s\n", oldPath)
	sb.WriteString(oldContent)
	if !strings.HasSuffix(oldContent, "\n") {
		sb.WriteString("\n")
	}
	fmt.Fprintf(&sb, "+++ current/%s\n", newPath)
	sb.WriteString(newContent)
	return sb.String()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
