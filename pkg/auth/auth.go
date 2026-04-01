// Package auth provides token-based authentication for gRPC services.
//
// Server side: use UnaryInterceptor and StreamInterceptor to enforce a
// bearer token on incoming RPCs. The expected token is read from the
// API_TOKEN environment variable; if unset, authentication is disabled.
//
// Client side: use TokenDialOption to attach a bearer token to every
// outgoing RPC via the "authorization" metadata key.
package auth

import (
	"context"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const metadataKey = "authorization"

// TokenFromEnv returns the API_TOKEN value (empty means auth disabled).
func TokenFromEnv() string {
	return os.Getenv("API_TOKEN")
}

// --- Server interceptors ---

// UnaryInterceptor returns a gRPC unary interceptor that validates the bearer token.
// If token is empty, all requests are allowed (auth disabled).
func UnaryInterceptor(token string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if token == "" {
			return handler(ctx, req)
		}
		if err := validateToken(ctx, token); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// StreamInterceptor returns a gRPC stream interceptor that validates the bearer token.
// If token is empty, all requests are allowed (auth disabled).
func StreamInterceptor(token string) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if token == "" {
			return handler(srv, ss)
		}
		if err := validateToken(ss.Context(), token); err != nil {
			return err
		}
		return handler(srv, ss)
	}
}

func validateToken(ctx context.Context, expected string) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing metadata")
	}

	values := md.Get(metadataKey)
	if len(values) == 0 {
		return status.Error(codes.Unauthenticated, "missing authorization token")
	}

	token := values[0]
	// Support "Bearer <token>" or plain "<token>".
	token = strings.TrimPrefix(token, "Bearer ")
	token = strings.TrimPrefix(token, "bearer ")

	if token != expected {
		return status.Error(codes.Unauthenticated, "invalid authorization token")
	}
	return nil
}

// --- Client credentials ---

// tokenCredentials implements grpc.PerRPCCredentials.
type tokenCredentials struct {
	token string
}

func (t tokenCredentials) GetRequestMetadata(ctx context.Context, uri ...string) (map[string]string, error) {
	return map[string]string{
		metadataKey: "Bearer " + t.token,
	}, nil
}

func (t tokenCredentials) RequireTransportSecurity() bool {
	return false // allow over insecure for dev; TLS is handled separately
}

// TokenDialOption returns a grpc.DialOption that attaches the token to every RPC.
// If token is empty, returns nil (no auth).
func TokenDialOption(token string) grpc.DialOption {
	if token == "" {
		return nil
	}
	return grpc.WithPerRPCCredentials(tokenCredentials{token: token})
}
