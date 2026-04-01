package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"google.golang.org/grpc"

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

func printUsage() {
	fmt.Fprintln(os.Stderr, `ci - CI/CD system command line tool

Usage:
  ci submit <repo-url> [--branch <branch>] [--sha <sha>]
  ci status <build-id>
  ci list
  ci cancel <build-id>
  ci logs <build-id> <task-id> [--follow]
  ci watch <build-id>
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
