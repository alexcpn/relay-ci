// Package tlsutil provides TLS configuration helpers for gRPC and HTTP servers/clients.
//
// Configuration is driven by environment variables:
//
//	TLS_CERT     — path to PEM-encoded server/client certificate
//	TLS_KEY      — path to PEM-encoded private key
//	TLS_CA       — path to PEM-encoded CA certificate (enables mTLS when set)
//	TLS_ENABLED  — set to "true" to require TLS; auto-detected if cert/key are set
//
// When TLS_CA is provided on a server, client certificates signed by that CA
// are required (mutual TLS). When TLS_CA is provided on a client, it is used
// to verify the server's certificate.
package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// Config holds paths for TLS certificate material.
type Config struct {
	CertFile string // PEM certificate
	KeyFile  string // PEM private key
	CAFile   string // PEM CA certificate (enables mTLS)
	Enabled  bool   // master switch
}

// ConfigFromEnv reads TLS configuration from environment variables.
func ConfigFromEnv() Config {
	cert := os.Getenv("TLS_CERT")
	key := os.Getenv("TLS_KEY")
	ca := os.Getenv("TLS_CA")
	enabled := os.Getenv("TLS_ENABLED") == "true" || (cert != "" && key != "")
	return Config{
		CertFile: cert,
		KeyFile:  key,
		CAFile:   ca,
		Enabled:  enabled,
	}
}

// ServerTLSConfig returns a *tls.Config suitable for a server.
// If CAFile is set, client certificate verification is required (mTLS).
func (c Config) ServerTLSConfig() (*tls.Config, error) {
	if !c.Enabled {
		return nil, nil
	}

	cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("tlsutil: load server cert/key: %w", err)
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	if c.CAFile != "" {
		pool, err := loadCAPool(c.CAFile)
		if err != nil {
			return nil, err
		}
		tlsCfg.ClientCAs = pool
		tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return tlsCfg, nil
}

// ClientTLSConfig returns a *tls.Config suitable for a client.
// If CAFile is set, it is used to verify the server certificate.
// If CertFile and KeyFile are also set, the client presents its own certificate (mTLS).
func (c Config) ClientTLSConfig() (*tls.Config, error) {
	if !c.Enabled {
		return nil, nil
	}

	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	if c.CAFile != "" {
		pool, err := loadCAPool(c.CAFile)
		if err != nil {
			return nil, err
		}
		tlsCfg.RootCAs = pool
	}

	if c.CertFile != "" && c.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("tlsutil: load client cert/key: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	return tlsCfg, nil
}

// GRPCServerOption returns a grpc.ServerOption that enables TLS.
// Returns nil if TLS is not enabled.
func (c Config) GRPCServerOption() (grpc.ServerOption, error) {
	tlsCfg, err := c.ServerTLSConfig()
	if err != nil {
		return nil, err
	}
	if tlsCfg == nil {
		return nil, nil
	}
	return grpc.Creds(credentials.NewTLS(tlsCfg)), nil
}

// GRPCDialOption returns a grpc.DialOption for client connections.
// Returns insecure credentials if TLS is not enabled.
func (c Config) GRPCDialOption() (grpc.DialOption, error) {
	if !c.Enabled {
		return grpc.WithTransportCredentials(insecure.NewCredentials()), nil
	}
	tlsCfg, err := c.ClientTLSConfig()
	if err != nil {
		return nil, err
	}
	return grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)), nil
}

func loadCAPool(caFile string) (*x509.CertPool, error) {
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("tlsutil: read CA file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("tlsutil: no valid certificates in CA file %s", caFile)
	}
	return pool, nil
}
