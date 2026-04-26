package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/ci-system/ci/gen/ci/v1"
	"github.com/ci-system/ci/pkg/auth"
	"github.com/ci-system/ci/pkg/tlsutil"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	masterAddr := envOrDefault("CI_MASTER", "localhost:9090")
	tlsCfg := tlsutil.ConfigFromEnv()
	dialOpt, err := tlsCfg.GRPCDialOption()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: TLS setup failed: %v\n", err)
		os.Exit(1)
	}
	dialOpts := []grpc.DialOption{dialOpt}
	if tokenOpt := auth.TokenDialOption(auth.TokenFromEnv()); tokenOpt != nil {
		dialOpts = append(dialOpts, tokenOpt)
	}
	conn, err := grpc.NewClient(masterAddr, dialOpts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to connect to master at %s: %v\n", masterAddr, err)
		os.Exit(1)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch os.Args[1] {
	case "submit":
		cmdSubmit(ctx, conn)
	case "verify":
		cmdVerify(conn)
	case "status", "get":
		cmdStatus(ctx, conn)
	case "list", "ls":
		cmdList(ctx, conn)
	case "cancel":
		cmdCancel(ctx, conn)
	case "logs":
		cmdLogs(ctx, conn)
	case "watch":
		cmdWatch(ctx, conn)
	case "secret":
		cmdSecret(ctx, conn)
	case "pipeline-pin":
		cmdPipelinePin(ctx, conn)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func cmdSubmit(ctx context.Context, conn *grpc.ClientConn) {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: ci submit <repo-url> [--branch <branch>] [--sha <sha>]")
		os.Exit(1)
	}

	repoURL := os.Args[2]
	branch := flagValue("--branch", "main")
	sha := flagValue("--sha", "HEAD")

	client := pb.NewSchedulerServiceClient(conn)
	resp, err := client.SubmitBuild(ctx, &pb.SubmitBuildRequest{
		Source: &pb.GitSource{
			RepoUrl:   repoURL,
			CommitSha: sha,
			Branch:    branch,
		},
		TriggeredBy: "cli",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Build submitted: %s\n", resp.BuildId.Id)
}

func cmdStatus(ctx context.Context, conn *grpc.ClientConn) {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: ci status <build-id>")
		os.Exit(1)
	}

	buildID := os.Args[2]
	client := pb.NewSchedulerServiceClient(conn)
	resp, err := client.GetBuild(ctx, &pb.GetBuildRequest{
		BuildId: &pb.BuildID{Id: buildID},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	b := resp.Build
	fmt.Printf("Build:   %s\n", b.BuildId.Id)
	fmt.Printf("State:   %s\n", b.State)
	if b.Source != nil {
		fmt.Printf("Repo:    %s\n", b.Source.RepoUrl)
		fmt.Printf("Branch:  %s\n", b.Source.Branch)
		fmt.Printf("Commit:  %s\n", b.Source.CommitSha)
	}
	fmt.Printf("Trigger: %s\n", b.TriggeredBy)
	fmt.Println()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TASK\tSTATE\tEXIT\tDURATION")
	for _, task := range b.Tasks {
		dur := ""
		exit := ""
		if task.Result != nil {
			exit = fmt.Sprintf("%d", task.Result.ExitCode)
			if task.Result.StartedAt != nil && task.Result.FinishedAt != nil {
				d := task.Result.FinishedAt.AsTime().Sub(task.Result.StartedAt.AsTime())
				dur = d.Round(time.Millisecond).String()
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", task.Name, task.State, exit, dur)
	}
	w.Flush()
}

func cmdList(ctx context.Context, conn *grpc.ClientConn) {
	client := pb.NewSchedulerServiceClient(conn)
	resp, err := client.ListBuilds(ctx, &pb.ListBuildsRequest{
		Limit: 20,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "BUILD\tSTATE\tREPO\tBRANCH\tTRIGGER")
	for _, b := range resp.Builds {
		repo := ""
		branch := ""
		if b.Source != nil {
			repo = b.Source.RepoUrl
			branch = b.Source.Branch
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", b.BuildId.Id, b.State, repo, branch, b.TriggeredBy)
	}
	w.Flush()
}

func cmdCancel(ctx context.Context, conn *grpc.ClientConn) {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: ci cancel <build-id>")
		os.Exit(1)
	}

	buildID := os.Args[2]
	client := pb.NewSchedulerServiceClient(conn)
	_, err := client.CancelBuild(ctx, &pb.CancelBuildRequest{
		BuildId: &pb.BuildID{Id: buildID},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Build %s cancelled\n", buildID)
}

func cmdLogs(ctx context.Context, conn *grpc.ClientConn) {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: ci logs <build-id> <task-id> [--follow]")
		os.Exit(1)
	}

	buildID := os.Args[2]
	taskID := os.Args[3]
	follow := hasFlag("--follow") || hasFlag("-f")

	client := pb.NewLogServiceClient(conn)

	if follow {
		// Use longer timeout for following.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
	}

	stream, err := client.StreamLogs(ctx, &pb.StreamLogsRequest{
		BuildId: &pb.BuildID{Id: buildID},
		TaskId:  &pb.TaskID{Id: taskID},
		Follow:  follow,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	for {
		line, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(line.Content)
	}
}

func cmdWatch(ctx context.Context, conn *grpc.ClientConn) {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: ci watch <build-id>")
		os.Exit(1)
	}

	buildID := os.Args[2]
	client := pb.NewSchedulerServiceClient(conn)

	// Longer timeout for watching.
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	stream, err := client.WatchBuild(ctx, &pb.WatchBuildRequest{
		BuildId: &pb.BuildID{Id: buildID},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	for {
		event, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

		switch e := event.Event.(type) {
		case *pb.BuildEvent_TaskStateChanged:
			tc := e.TaskStateChanged
			fmt.Printf("[%s] %s → %s\n",
				event.Timestamp.AsTime().Format("15:04:05"),
				tc.TaskName,
				tc.Current,
			)
		case *pb.BuildEvent_BuildStateChanged:
			bc := e.BuildStateChanged
			fmt.Printf("[%s] build: %s → %s\n",
				event.Timestamp.AsTime().Format("15:04:05"),
				bc.Previous,
				bc.Current,
			)
		}
	}
}

// cmdSecret handles the "secret" subcommand: set, list, delete.
func cmdSecret(ctx context.Context, conn *grpc.ClientConn) {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: ci secret <set|list|delete> [args]")
		os.Exit(1)
	}

	client := pb.NewSecretsServiceClient(conn)

	switch os.Args[2] {
	case "set":
		// ci secret set <name>  — reads value from stdin
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: ci secret set <name>  (pipe value via stdin)")
			os.Exit(1)
		}
		name := os.Args[3]
		valueBytes, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading value from stdin: %v\n", err)
			os.Exit(1)
		}
		value := strings.TrimRight(string(valueBytes), "\r\n")
		if value == "" {
			fmt.Fprintln(os.Stderr, "error: secret value is empty (pipe the value via stdin)")
			os.Exit(1)
		}
		_, err = client.PutSecret(ctx, &pb.PutSecretRequest{Name: name, Value: value})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Secret %q stored.\n", name)

	case "list", "ls":
		// ci secret list
		resp, err := client.ListSecrets(ctx, &pb.ListSecretsRequest{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if len(resp.Secrets) == 0 {
			fmt.Println("No secrets stored.")
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSCOPE\tCREATED BY")
		for _, s := range resp.Secrets {
			fmt.Fprintf(w, "%s\t%s\t%s\n", s.Name, s.Scope, s.CreatedBy)
		}
		w.Flush()

	case "delete", "rm":
		// ci secret delete <name>
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: ci secret delete <name>")
			os.Exit(1)
		}
		name := os.Args[3]
		_, err := client.DeleteSecret(ctx, &pb.DeleteSecretRequest{Name: name})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Secret %q deleted.\n", name)

	default:
		fmt.Fprintf(os.Stderr, "unknown secret command: %s\n", os.Args[2])
		fmt.Fprintln(os.Stderr, "usage: ci secret <set|list|delete> [args]")
		os.Exit(1)
	}
}

func cmdVerify(conn *grpc.ClientConn) {
	repoPath := lastPositionalArg()
	if repoPath == "" {
		fmt.Fprintln(os.Stderr, "usage: ci verify [--branch <branch>] [--base-branch <branch>] [--accept-pipeline-change] <repo-path>")
		os.Exit(1)
	}

	absRepoPath, err := filepath.Abs(repoPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: resolving repo path: %v\n", err)
		os.Exit(3)
	}

	projectID, err := gitOutput(absRepoPath, "rev-list", "--max-parents=0", "HEAD")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reading root commit SHA: %v\n", err)
		os.Exit(3)
	}
	commitSHA, err := gitOutput(absRepoPath, "rev-parse", "HEAD")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reading HEAD commit SHA: %v\n", err)
		os.Exit(3)
	}
	branch := flagValue("--branch", "")
	if branch == "" {
		branch, err = gitOutput(absRepoPath, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: reading current branch: %v\n", err)
			os.Exit(3)
		}
	}
	baseBranch := flagValue("--base-branch", "main")
	acceptChange := hasFlag("--accept-pipeline-change")
	repoName := filepath.Base(absRepoPath)

	bundlePath, cleanup, err := createGitBundle(absRepoPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: creating bundle: %v\n", err)
		os.Exit(3)
	}
	defer cleanup()

	client := pb.NewSchedulerServiceClient(conn)
	stream, err := client.VerifyLocal(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: opening verify stream: %v\n", err)
		os.Exit(3)
	}

	header := &pb.VerifyLocalHeader{
		ProjectId:            projectID,
		RepoName:             repoName,
		Branch:               branch,
		BaseBranch:           baseBranch,
		CommitSha:            commitSHA,
		AcceptPipelineChange: acceptChange,
	}
	if err := stream.Send(&pb.VerifyLocalRequest{Payload: &pb.VerifyLocalRequest_Header{Header: header}}); err != nil {
		fmt.Fprintf(os.Stderr, "error: sending verify header: %v\n", err)
		os.Exit(3)
	}

	if err := sendBundleChunks(stream, bundlePath); err != nil {
		fmt.Fprintf(os.Stderr, "error: streaming bundle: %v\n", err)
		os.Exit(3)
	}

	resp, err := stream.CloseAndRecv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: receiving verify response: %v\n", err)
		os.Exit(3)
	}

	printVerifyResponse(resp)
	switch resp.DigestStatus {
	case pb.PipelineDigestStatus_PIPELINE_DIGEST_STATUS_CHANGED:
		os.Exit(2)
	case pb.PipelineDigestStatus_PIPELINE_DIGEST_STATUS_PINNED,
		pb.PipelineDigestStatus_PIPELINE_DIGEST_STATUS_OK,
		pb.PipelineDigestStatus_PIPELINE_DIGEST_STATUS_ACCEPTED:
	default:
		if resp.Error != "" {
			fmt.Fprintln(os.Stderr, resp.Error)
		}
	}

	if resp.BuildId == "" {
		os.Exit(3)
	}

	fmt.Printf("verify build submitted: %s\n", resp.BuildId)

	buildCtx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	build, err := waitForBuildCompletion(buildCtx, client, resp.BuildId)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: waiting for build completion: %v\n", err)
		os.Exit(3)
	}

	fmt.Printf("verify build finished: %s\n", build.State)
	if build.State != pb.BuildState_BUILD_STATE_PASSED {
		os.Exit(1)
	}
}

func cmdPipelinePin(ctx context.Context, conn *grpc.ClientConn) {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: ci pipeline-pin <list|unpin> [project_id]")
		os.Exit(1)
	}

	client := pb.NewSchedulerServiceClient(conn)
	switch os.Args[2] {
	case "list", "ls":
		resp, err := client.ListPipelinePins(ctx, &pb.ListPipelinePinsRequest{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if len(resp.Pins) == 0 {
			fmt.Println("No pipeline digests pinned.")
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "PROJECT\tDIGEST\tPIPELINE\tFIRST SEEN\tLAST SEEN\tREPO")
		for _, pin := range resp.Pins {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				pin.ProjectId,
				pin.Digest,
				pin.PipelinePath,
				formatTime(pin.FirstSeen),
				formatTime(pin.LastSeen),
				pin.RepoName,
			)
		}
		w.Flush()
	case "unpin", "rm":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: ci pipeline-pin unpin <project_id>")
			os.Exit(1)
		}
		projectID := os.Args[3]
		resp, err := client.UnpinPipeline(ctx, &pb.UnpinPipelineRequest{ProjectId: projectID})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if resp.Removed {
			fmt.Printf("Unpinned %s\n", projectID)
		} else {
			fmt.Printf("No pin found for %s\n", projectID)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown pipeline-pin command: %s\n", os.Args[2])
		fmt.Fprintln(os.Stderr, "usage: ci pipeline-pin <list|unpin> [project_id]")
		os.Exit(1)
	}
}

func printVerifyResponse(resp *pb.VerifyLocalResponse) {
	switch resp.DigestStatus {
	case pb.PipelineDigestStatus_PIPELINE_DIGEST_STATUS_PINNED:
		fmt.Printf("Pinned pipeline digest %s\n", resp.PinnedDigest)
	case pb.PipelineDigestStatus_PIPELINE_DIGEST_STATUS_OK:
		fmt.Printf("Pipeline digest matches pinned value %s\n", resp.PinnedDigest)
	case pb.PipelineDigestStatus_PIPELINE_DIGEST_STATUS_ACCEPTED:
		fmt.Printf("PIPELINE CHANGED — accepted and re-pinned\n")
		fmt.Printf("pinned:  %s\n", resp.PinnedDigest)
		fmt.Printf("current: %s\n", resp.CurrentDigest)
		if resp.PipelineDiff != "" {
			fmt.Println(resp.PipelineDiff)
		}
	case pb.PipelineDigestStatus_PIPELINE_DIGEST_STATUS_CHANGED:
		fmt.Fprintln(os.Stderr, "PIPELINE CHANGED — refusing to run")
		fmt.Fprintf(os.Stderr, "pinned:  %s\n", resp.PinnedDigest)
		fmt.Fprintf(os.Stderr, "current: %s\n", resp.CurrentDigest)
		if resp.PipelineDiff != "" {
			fmt.Println(resp.PipelineDiff)
		}
	default:
		if resp.Error != "" {
			fmt.Fprintln(os.Stderr, resp.Error)
		}
	}
}

func waitForBuildCompletion(ctx context.Context, client pb.SchedulerServiceClient, buildID string) (*pb.Build, error) {
	for {
		resp, err := client.GetBuild(ctx, &pb.GetBuildRequest{BuildId: &pb.BuildID{Id: buildID}})
		if err != nil {
			return nil, err
		}
		if resp.Build == nil {
			return nil, fmt.Errorf("build %s missing from response", buildID)
		}
		switch resp.Build.State {
		case pb.BuildState_BUILD_STATE_PASSED,
			pb.BuildState_BUILD_STATE_FAILED,
			pb.BuildState_BUILD_STATE_CANCELLED:
			return resp.Build, nil
		default:
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(1 * time.Second):
			}
		}
	}
}

func sendBundleChunks(stream pb.SchedulerService_VerifyLocalClient, bundlePath string) error {
	file, err := os.Open(bundlePath)
	if err != nil {
		return err
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	buf := make([]byte, 64*1024)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			if sendErr := stream.Send(&pb.VerifyLocalRequest{Payload: &pb.VerifyLocalRequest_Chunk{Chunk: append([]byte(nil), buf[:n]...)}}); sendErr != nil {
				return sendErr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func createGitBundle(repoPath string) (string, func(), error) {
	tmpDir, err := os.MkdirTemp("", "ci-verify-*")
	if err != nil {
		return "", func() {}, err
	}
	bundlePath := filepath.Join(tmpDir, "verify.bundle")
	cmd := exec.Command("git", "-C", repoPath, "bundle", "create", bundlePath, "--all")
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", func() {}, fmt.Errorf("%w\n%s", err, out)
	}
	return bundlePath, func() { _ = os.RemoveAll(tmpDir) }, nil
}

func gitOutput(repoPath string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repoPath}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func formatTime(ts *timestamppb.Timestamp) string {
	if ts == nil {
		return ""
	}
	return ts.AsTime().Format(time.RFC3339)
}

func lastPositionalArg() string {
	for i := len(os.Args) - 1; i >= 2; i-- {
		if !strings.HasPrefix(os.Args[i], "-") {
			return os.Args[i]
		}
	}
	return ""
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `ci - CI/CD system command line tool

Usage:
  ci submit <repo-url> [--branch <branch>] [--sha <sha>]
  ci verify [--branch <branch>] [--base-branch <branch>] [--accept-pipeline-change] <repo-path>
  ci status <build-id>
  ci list
  ci cancel <build-id>
  ci logs <build-id> <task-id> [--follow]
  ci watch <build-id>
  ci pipeline-pin list
  ci pipeline-pin unpin <project_id>
  ci secret set <name>          store a secret (pipe value via stdin)
  ci secret list                list secret names
  ci secret delete <name>       remove a secret

Environment:
  CI_MASTER  Master address (default: localhost:9090)`)
}

func flagValue(name, def string) string {
	for i, arg := range os.Args {
		if arg == name && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
	}
	return def
}

func hasFlag(name string) bool {
	for _, arg := range os.Args {
		if arg == name {
			return true
		}
	}
	return false
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
