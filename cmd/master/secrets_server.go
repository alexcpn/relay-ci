package main

import (
	"context"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/ci-system/ci/gen/ci/v1"
	"github.com/ci-system/ci/pkg/secrets"
)

// secretsServer implements SecretsServiceServer backed by an in-memory store.
type secretsServer struct {
	pb.UnimplementedSecretsServiceServer
	store *secrets.Store
}

func newSecretsServer(store *secrets.Store) *secretsServer {
	return &secretsServer{store: store}
}

// GetSecrets is called by workers to fetch secret values before executing a task.
// All secrets are looked up in the "global" scope.
func (s *secretsServer) GetSecrets(_ context.Context, req *pb.GetSecretsRequest) (*pb.GetSecretsResponse, error) {
	values, err := s.store.GetMultiple("global", req.SecretNames)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	return &pb.GetSecretsResponse{Secrets: values}, nil
}

// PutSecret stores a secret. Scope defaults to "global" if not specified.
func (s *secretsServer) PutSecret(_ context.Context, req *pb.PutSecretRequest) (*pb.PutSecretResponse, error) {
	scope := strings.TrimSpace(req.Scope)
	if scope == "" {
		scope = "global"
	}
	if err := s.store.Put(scope, req.Name, req.Value, "cli"); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	return &pb.PutSecretResponse{}, nil
}

// DeleteSecret removes a secret. Scope defaults to "global".
func (s *secretsServer) DeleteSecret(_ context.Context, req *pb.DeleteSecretRequest) (*pb.DeleteSecretResponse, error) {
	scope := strings.TrimSpace(req.Scope)
	if scope == "" {
		scope = "global"
	}
	if err := s.store.Delete(scope, req.Name); err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	return &pb.DeleteSecretResponse{}, nil
}

// ListSecrets returns secret metadata (names only, no values) for a scope.
func (s *secretsServer) ListSecrets(_ context.Context, req *pb.ListSecretsRequest) (*pb.ListSecretsResponse, error) {
	scope := strings.TrimSpace(req.Scope)
	if scope == "" {
		scope = "global"
	}
	entries := s.store.List(scope)
	resp := &pb.ListSecretsResponse{}
	for _, e := range entries {
		resp.Secrets = append(resp.Secrets, &pb.SecretMetadata{
			Name:      e.Name,
			Scope:     e.Scope,
			CreatedBy: e.CreatedBy,
		})
	}
	return resp, nil
}
