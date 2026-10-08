package server

import (
	"crypto/tls"
	"errors"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// TransportOptions requires TLS unless a nonproduction fixture explicitly opts out.
func TransportOptions(environment string) ([]grpc.ServerOption, error) {
	production := strings.EqualFold(strings.TrimSpace(environment), "production")
	insecure := os.Getenv("APOLLO_ALLOW_INSECURE") == "true"
	certFile, keyFile := os.Getenv("APOLLO_TLS_CERT_FILE"), os.Getenv("APOLLO_TLS_KEY_FILE")
	if insecure {
		if production || certFile != "" || keyFile != "" {
			return nil, errors.New("APOLLO_ALLOW_INSECURE is permitted only outside production without TLS files")
		}
		return nil, nil
	}
	if certFile == "" || keyFile == "" {
		return nil, errors.New("APOLLO_TLS_CERT_FILE and APOLLO_TLS_KEY_FILE are required")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, errors.New("Apollo TLS certificate or key is invalid or unavailable")
	}
	return []grpc.ServerOption{grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}))}, nil
}
