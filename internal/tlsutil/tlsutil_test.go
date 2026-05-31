package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEmptyMode(t *testing.T) {
	cases := []struct {
		name string
		opts Options
		want string
	}{
		{"empty", Options{}, "insecure"},
		{"tls", Options{CertFile: "a", KeyFile: "b"}, "tls"},
		{"mtls", Options{CertFile: "a", KeyFile: "b", ClientCAFile: "c"}, "mtls"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.opts.Mode(); got != c.want {
				t.Errorf("Mode() = %q ; want %q", got, c.want)
			}
		})
	}
	if !(Options{}).Empty() {
		t.Error("empty Options should report Empty()=true")
	}
	if (Options{CertFile: "x"}).Empty() {
		t.Error("any non-empty field should flip Empty() to false")
	}
}

func TestServerCredentials_HappyPath(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeSelfSignedCert(t, dir)

	creds, err := ServerCredentials(Options{CertFile: certPath, KeyFile: keyPath})
	if err != nil {
		t.Fatalf("ServerCredentials : %v", err)
	}
	if creds == nil {
		t.Fatal("ServerCredentials returned nil")
	}
}

func TestServerCredentials_MTLSWithClientCA(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeSelfSignedCert(t, dir)
	caPath := filepath.Join(dir, "ca.pem")
	// Reuse the same cert as the client-CA — we just want to check the
	// PEM parsing path works, not validate a chain here.
	if err := os.Rename(certPath, caPath); err != nil {
		t.Fatalf("rename : %v", err)
	}
	certPath, keyPath = writeSelfSignedCert(t, dir)

	creds, err := ServerCredentials(Options{
		CertFile: certPath, KeyFile: keyPath, ClientCAFile: caPath,
	})
	if err != nil {
		t.Fatalf("ServerCredentials mtls : %v", err)
	}
	if creds == nil {
		t.Fatal("mtls ServerCredentials returned nil")
	}
}

func TestServerCredentials_ValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		opts Options
		want string
	}{
		{"missing both", Options{}, "must be set together"},
		{"missing key", Options{CertFile: "/no/such/cert.pem"}, "must be set together"},
		{"missing cert", Options{KeyFile: "/no/such/key.pem"}, "must be set together"},
		{"bad cert path", Options{CertFile: "/no/such/cert.pem", KeyFile: "/no/such/key.pem"}, "load tls cert/key"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ServerCredentials(c.opts)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %q ; want substring %q", err.Error(), c.want)
			}
		})
	}
}

func TestServerCredentials_ReloaderSwapsCertOnSighupEquivalent(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeSelfSignedCert(t, dir)

	_, reloader, err := ServerCredentialsWithReloader(Options{
		CertFile: certPath, KeyFile: keyPath,
	})
	if err != nil {
		t.Fatalf("ServerCredentialsWithReloader : %v", err)
	}
	if reloader == nil {
		t.Fatal("expected non-nil Reloader for TLS-mode Options")
	}

	// Overwrite the cert file in place with a fresh self-signed pair.
	// SIGHUP equivalent : Reload() re-reads from the same paths.
	newCertPath, newKeyPath := writeSelfSignedCert(t, dir)
	if err := os.Rename(newCertPath, certPath); err != nil {
		t.Fatalf("rename new cert : %v", err)
	}
	if err := os.Rename(newKeyPath, keyPath); err != nil {
		t.Fatalf("rename new key : %v", err)
	}

	if err := reloader.Reload(); err != nil {
		t.Errorf("Reload after on-disk rotation : %v", err)
	}

	// Corrupt the cert + reload should error loudly (operator's
	// renewal script botched it ; daemon must keep serving the
	// previous cert and surface the error).
	if err := os.WriteFile(certPath, []byte("not a pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reloader.Reload(); err == nil {
		t.Error("Reload of corrupt cert should error ; got nil")
	}
}

func TestServerCredentialsWithReloader_EmptyOpts(t *testing.T) {
	_, _, err := ServerCredentialsWithReloader(Options{})
	if err == nil || !strings.Contains(err.Error(), "no cert configured") {
		t.Errorf("empty Options → ServerCredentialsWithReloader returned %v ; want clear error", err)
	}
}

func TestServerCredentials_BadClientCA(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeSelfSignedCert(t, dir)
	caPath := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caPath, []byte("not a pem block"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ServerCredentials(Options{CertFile: certPath, KeyFile: keyPath, ClientCAFile: caPath})
	if err == nil || !strings.Contains(err.Error(), "no PEM-encoded certs") {
		t.Errorf("got %v ; want error mentioning no PEM-encoded certs", err)
	}
}

// writeSelfSignedCert generates an ECDSA self-signed cert + key for
// localhost and writes them as PEM in dir. Returns (certPath, keyPath).
func writeSelfSignedCert(t *testing.T, dir string) (string, string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "weft-network-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, "cert-"+randSuffix(t)+".pem")
	keyPath := filepath.Join(dir, "key-"+randSuffix(t)+".pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

// randSuffix returns a tiny random hex string so two cert writes in
// the same temp dir don't collide.
func randSuffix(t *testing.T) string {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return "" + // hex-ish enough for filename use
		string("0123456789abcdef"[b[0]>>4]) +
		string("0123456789abcdef"[b[0]&0xf]) +
		string("0123456789abcdef"[b[1]>>4]) +
		string("0123456789abcdef"[b[1]&0xf])
}
