// Package tlsutil builds the gRPC server's TLS configuration from
// operator-supplied cert / key / client-CA paths.
//
// Three modes :
//
//   - no flags : the gRPC server runs insecure. Only safe over a
//     unix socket (filesystem perms gate access). Production over
//     TCP MUST set at least the server cert.
//
//   - cert + key only : server-side TLS. Clients still go anonymous
//     ; the operator authorises via a network policy (WireGuard
//     mesh membership = trust).
//
//   - cert + key + client-CA : mutual TLS. Clients must present a
//     cert chained to client-CA. Required for cross-DC TCP
//     deployments where network membership isn't authentication
//     enough.
//
// All three paths are explicit ; the daemon never auto-generates a
// self-signed cert. An operator who hasn't planned cert distribution
// runs over unix socket — anything else gets a startup error.
package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"google.golang.org/grpc/credentials"
)

// Options bundles the cert / key / client-CA file paths. All empty
// = insecure (intended for unix sockets only).
type Options struct {
	CertFile     string
	KeyFile      string
	ClientCAFile string // empty = no mTLS ; non-empty = require + verify client cert
}

// Empty reports whether no TLS material is configured. Caller uses
// this to decide between grpc.NewServer() and
// grpc.NewServer(grpc.Creds(creds)).
func (o Options) Empty() bool {
	return o.CertFile == "" && o.KeyFile == "" && o.ClientCAFile == ""
}

// Mode returns a short string describing the chosen mode, useful
// in startup logs : "insecure" | "tls" | "mtls".
func (o Options) Mode() string {
	switch {
	case o.Empty():
		return "insecure"
	case o.ClientCAFile != "":
		return "mtls"
	default:
		return "tls"
	}
}

// ServerCredentials builds credentials.TransportCredentials from
// Options. Returns an error if the cert / key / CA can't be loaded,
// rather than falling back to insecure — TLS misconfiguration must
// be loud so the operator can fix it before the daemon serves any
// traffic.
func ServerCredentials(o Options) (credentials.TransportCredentials, error) {
	if o.CertFile == "" || o.KeyFile == "" {
		return nil, fmt.Errorf("tls : --tls-cert and --tls-key must be set together")
	}
	cert, err := tls.LoadX509KeyPair(o.CertFile, o.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load tls cert/key : %w", err)
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
	if o.ClientCAFile != "" {
		pem, err := os.ReadFile(o.ClientCAFile)
		if err != nil {
			return nil, fmt.Errorf("read client-CA file %s : %w", o.ClientCAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("client-CA file %s contains no PEM-encoded certs", o.ClientCAFile)
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return credentials.NewTLS(cfg), nil
}
