// Package etcdtest spins up an embedded etcd in-process for the
// store packages' integration tests. NOT used by production code ;
// the import path makes that explicit.
//
// Why embedded vs a test container : tests stay self-contained and
// hermetic (no Docker, no port allocation, no flaky network). The
// dep tree gain is real (~50 transitive modules) but only paid in
// the test binary — production builds don't touch this package.
package etcdtest

import (
	"net/url"
	"path/filepath"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"
)

// New starts a single-member embedded etcd against a unique
// loopback port. Returns a connected client + a Stop function tests
// MUST defer. Caller cleanup-paths (t.Cleanup) are wired so a
// crashing test still tears the etcd down.
//
// Listen-port collisions are avoided by binding to :0 (kernel-picked
// free port). Storage lives under t.TempDir() so each test run is
// independent.
func New(t *testing.T) (*clientv3.Client, func()) {
	t.Helper()
	cfg := embed.NewConfig()
	cfg.Dir = filepath.Join(t.TempDir(), "etcd")
	cfg.Name = "test"
	// Bind to loopback:0 — kernel allocates a free port. The
	// embedded etcd dials its own peer / client URLs, so we have
	// to commit to specific local URLs ; loopback + :0 is the
	// portable pair.
	lcURL, _ := url.Parse("http://127.0.0.1:0")
	lpURL, _ := url.Parse("http://127.0.0.1:0")
	cfg.ListenClientUrls = []url.URL{*lcURL}
	cfg.AdvertiseClientUrls = []url.URL{*lcURL}
	cfg.ListenPeerUrls = []url.URL{*lpURL}
	cfg.AdvertisePeerUrls = []url.URL{*lpURL}
	// The InitialCluster string must match the kernel-allocated
	// peer URL ; embed resolves the placeholder during Start.
	cfg.InitialCluster = cfg.Name + "=" + lpURL.String()
	// Squelch etcd's own logger ; we don't want its journal noise
	// interleaved with go-test output. Level=error still surfaces
	// real problems.
	cfg.LogLevel = "error"
	cfg.Logger = "zap"

	e, err := embed.StartEtcd(cfg)
	if err != nil {
		t.Fatalf("embed.StartEtcd : %v", err)
	}

	select {
	case <-e.Server.ReadyNotify():
	case <-time.After(10 * time.Second):
		e.Close()
		t.Fatal("embed.Etcd did not become ready in 10s")
	}

	// The actual client URL is what the listener bound to ; this
	// resolves the :0 placeholder we passed in.
	actualURL := e.Clients[0].Addr().String()

	c, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{"http://" + actualURL},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		e.Close()
		t.Fatalf("clientv3.New : %v", err)
	}

	stop := func() {
		_ = c.Close()
		e.Close()
	}
	t.Cleanup(stop)
	return c, stop
}
