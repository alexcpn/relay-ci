package auth

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

func TestTokenFromEnv(t *testing.T) {
	t.Setenv("API_TOKEN", "test-secret")
	if got := TokenFromEnv(); got != "test-secret" {
		t.Fatalf("expected test-secret, got %q", got)
	}
}

func TestTokenFromEnv_Empty(t *testing.T) {
	t.Setenv("API_TOKEN", "")
	if got := TokenFromEnv(); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestTokenDialOption_Nil_When_Empty(t *testing.T) {
	opt := TokenDialOption("")
	if opt != nil {
		t.Fatal("expected nil dial option when token is empty")
	}
}

func TestTokenDialOption_NonNil(t *testing.T) {
	opt := TokenDialOption("my-token")
	if opt == nil {
		t.Fatal("expected non-nil dial option")
	}
}

// startServer starts a gRPC server with the health service and auth interceptors.
func startServer(t *testing.T, token string) (addr string, stop func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(UnaryInterceptor(token)),
		grpc.StreamInterceptor(StreamInterceptor(token)),
	)
	healthpb.RegisterHealthServer(srv, health.NewServer())
	go srv.Serve(lis)
	return lis.Addr().String(), srv.Stop
}

func TestAuth_Disabled(t *testing.T) {
	addr, stop := startServer(t, "")
	defer stop()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	client := healthpb.NewHealthClient(conn)
	_, err = client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("expected success when auth disabled, got: %v", err)
	}
}

func TestAuth_ValidToken(t *testing.T) {
	addr, stop := startServer(t, "secret123")
	defer stop()

	opt := TokenDialOption("secret123")
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		opt,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	client := healthpb.NewHealthClient(conn)
	_, err = client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("expected success with valid token, got: %v", err)
	}
}

func TestAuth_InvalidToken(t *testing.T) {
	addr, stop := startServer(t, "secret123")
	defer stop()

	opt := TokenDialOption("wrong-token")
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		opt,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	client := healthpb.NewHealthClient(conn)
	_, err = client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err == nil {
		t.Fatal("expected error with invalid token")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got: %v", err)
	}
}

func TestAuth_MissingToken(t *testing.T) {
	addr, stop := startServer(t, "secret123")
	defer stop()

	// No token dial option — should fail
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	client := healthpb.NewHealthClient(conn)
	_, err = client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err == nil {
		t.Fatal("expected error with missing token")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got: %v", err)
	}
}

func TestAuth_BearerPrefix(t *testing.T) {
	addr, stop := startServer(t, "secret123")
	defer stop()

	// TokenDialOption sends "Bearer secret123" — server should accept
	opt := TokenDialOption("secret123")
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		opt,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	client := healthpb.NewHealthClient(conn)
	_, err = client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("expected success with Bearer prefix, got: %v", err)
	}
}
