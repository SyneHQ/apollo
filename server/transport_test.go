package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
)

func tlsFixture(t *testing.T) (*x509.CertPool, string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Apollo test CA"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err = os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
	return roots, certFile, keyFile
}

func TestTransportRequiresExplicitTLSConfiguration(t *testing.T) {
	t.Setenv("APOLLO_TLS_CERT_FILE", "")
	t.Setenv("APOLLO_TLS_KEY_FILE", "")
	t.Setenv("APOLLO_ALLOW_INSECURE", "")
	for _, env := range []string{"production", "development"} {
		if _, err := TransportOptions(env); err == nil {
			t.Fatal("missing TLS configuration accepted")
		}
	}
	t.Setenv("APOLLO_ALLOW_INSECURE", "true")
	if _, err := TransportOptions("production"); err == nil {
		t.Fatal("production accepted plaintext opt-out")
	}
	if _, err := TransportOptions("development"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APOLLO_TLS_CERT_FILE", "/missing-sensitive-location")
	if _, err := TransportOptions("development"); err == nil {
		t.Fatal("conflicting TLS and plaintext settings accepted")
	}
	t.Setenv("APOLLO_ALLOW_INSECURE", "")
	t.Setenv("APOLLO_TLS_KEY_FILE", "/missing-sensitive-key")
	if _, err := TransportOptions("production"); err == nil || strings.Contains(err.Error(), "/missing") {
		t.Fatal("missing files did not fail with redacted error")
	}
}

func TestTLSProtectsActualGRPCCallAndRejectsWrongIdentity(t *testing.T) {
	roots, cert, key := tlsFixture(t)
	t.Setenv("APOLLO_TLS_CERT_FILE", cert)
	t.Setenv("APOLLO_TLS_KEY_FILE", key)
	t.Setenv("APOLLO_ALLOW_INSECURE", "")
	options, err := TransportOptions("production")
	if err != nil {
		t.Fatal(err)
	}
	calls := make(chan bool, 4)
	options = append(options, grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		calls <- len(md.Get("x-service-token")) == 1 && md.Get("x-service-token")[0] == "fixture-token"
		return handler(ctx, req)
	}))
	server := grpc.NewServer(options...)
	healthpb.RegisterHealthServer(server, health.NewServer())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go server.Serve(listener)
	defer server.Stop()
	for _, fixture := range []struct {
		name, serverName string
		roots            *x509.CertPool
		success          bool
	}{
		{"verified", "localhost", roots, true},
		{"wrong-host", "other.example", roots, false},
		{"wrong-ca", "localhost", x509.NewCertPool(), false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			client, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: fixture.roots, ServerName: fixture.serverName, MinVersion: tls.VersionTLS12})))
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
			defer cancel()
			ctx = metadata.AppendToOutgoingContext(ctx, "x-service-token", "fixture-token")
			_, err = healthpb.NewHealthClient(client).Check(ctx, &healthpb.HealthCheckRequest{})
			if (err == nil) != fixture.success {
				t.Fatalf("unexpected transport outcome: %v", err)
			}
			select {
			case ok := <-calls:
				if !fixture.success || !ok {
					t.Fatal("invalid transport delivered request or token was missing")
				}
			default:
				if fixture.success {
					t.Fatal("verified request did not reach service")
				}
			}
		})
	}
}
