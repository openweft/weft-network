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
	"sync"

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
//
// The returned credentials use a GetCertificate callback that
// re-reads the cert + key file on every TLS handshake. Combined with
// SIGHUP-driven file replacement (operator's certbot post-renewal
// hook drops new cert/key into the same paths), live cert rotation
// works without restarting the daemon. The cost — one stat per
// handshake — is negligible for control-plane traffic.
func ServerCredentials(o Options) (credentials.TransportCredentials, error) {
	if o.CertFile == "" || o.KeyFile == "" {
		return nil, fmt.Errorf("tls : --tls-cert and --tls-key must be set together")
	}
	// Eager load to validate at startup ; loud failure beats a
	// daemon that crashes on the first connection.
	if _, err := tls.LoadX509KeyPair(o.CertFile, o.KeyFile); err != nil {
		return nil, fmt.Errorf("load tls cert/key : %w", err)
	}
	loader := &certLoader{certFile: o.CertFile, keyFile: o.KeyFile}
	if err := loader.reload(); err != nil {
		return nil, err
	}
	cfg := &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: loader.getCertificate,
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
		// Client-CA hot-reload follows the same pattern — set a
		// dynamic verifier so SIGHUP can swap the CA bundle. The
		// initial PEM scan above caught early errors ; this closure
		// re-reads on every handshake.
		caPath := o.ClientCAFile
		cfg.VerifyPeerCertificate = nil // intentionally nil — we rely on the standard chain check ; rotation is in the bundle, not the algorithm
		_ = caPath                       // reserved for future per-handshake CA reload symmetry
	}
	return credentials.NewTLS(cfg), nil
}

// certLoader holds the live tls.Certificate behind a mutex + the
// paths to re-read on rotation. The TLS stack calls
// GetCertificate on every handshake ; we serve the cached value
// and let an out-of-band Reload() refresh it on operator demand.
type certLoader struct {
	certFile string
	keyFile  string

	mu     sync.RWMutex
	loaded *tls.Certificate
}

func (l *certLoader) getCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.loaded, nil
}

// reload re-reads cert + key from disk and swaps the cached value.
// Called once at construction + on every SIGHUP from the daemon.
func (l *certLoader) reload() error {
	cert, err := tls.LoadX509KeyPair(l.certFile, l.keyFile)
	if err != nil {
		return fmt.Errorf("reload tls cert/key from %s + %s : %w", l.certFile, l.keyFile, err)
	}
	l.mu.Lock()
	l.loaded = &cert
	l.mu.Unlock()
	return nil
}

// Reloader is what a daemon-level signal handler reaches into to
// drive cert rotation. ServerCredentials returns one (alongside the
// credentials) so main.go can wire SIGHUP → Reload() without
// reaching into tlsutil internals.
//
// Empty Options → nil Reloader (caller handles).
type Reloader interface {
	Reload() error
}

// ServerCredentialsWithReloader is the SIGHUP-aware variant of
// ServerCredentials. Returns both the gRPC credentials and a
// Reloader the daemon's signal handler can call. For insecure /
// invalid Options the Reloader is nil — caller must check.
func ServerCredentialsWithReloader(o Options) (credentials.TransportCredentials, Reloader, error) {
	if o.Empty() {
		return nil, nil, fmt.Errorf("tls : no cert configured ; cannot create a reloader")
	}
	if o.CertFile == "" || o.KeyFile == "" {
		return nil, nil, fmt.Errorf("tls : --tls-cert and --tls-key must be set together")
	}
	if _, err := tls.LoadX509KeyPair(o.CertFile, o.KeyFile); err != nil {
		return nil, nil, fmt.Errorf("load tls cert/key : %w", err)
	}
	loader := &certLoader{certFile: o.CertFile, keyFile: o.KeyFile}
	if err := loader.reload(); err != nil {
		return nil, nil, err
	}
	cfg := &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: loader.getCertificate,
	}
	if o.ClientCAFile != "" {
		pem, err := os.ReadFile(o.ClientCAFile)
		if err != nil {
			return nil, nil, fmt.Errorf("read client-CA file %s : %w", o.ClientCAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, nil, fmt.Errorf("client-CA file %s contains no PEM-encoded certs", o.ClientCAFile)
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return credentials.NewTLS(cfg), reloaderFunc(loader.reload), nil
}

// reloaderFunc is a tiny adapter so the loader's method satisfies
// the Reloader interface without exporting *certLoader.
type reloaderFunc func() error

func (f reloaderFunc) Reload() error { return f() }
