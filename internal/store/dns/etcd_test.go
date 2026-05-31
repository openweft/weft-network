package dns

import (
	"context"
	"net/url"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"
)

// startEmbedEtcd spins up a single-node etcd inside the test process.
// Returns a client and a cleanup function.
func startEmbedEtcd(t *testing.T) (*clientv3.Client, func()) {
	t.Helper()
	dir := t.TempDir()

	cfg := embed.NewConfig()
	cfg.Dir = dir
	cfg.LogLevel = "error"

	lpurl, _ := url.Parse("http://127.0.0.1:0")
	lcurl, _ := url.Parse("http://127.0.0.1:0")
	cfg.ListenPeerUrls = []url.URL{*lpurl}
	cfg.ListenClientUrls = []url.URL{*lcurl}
	cfg.AdvertisePeerUrls = []url.URL{*lpurl}
	cfg.AdvertiseClientUrls = []url.URL{*lcurl}
	cfg.InitialCluster = cfg.InitialClusterFromName(cfg.Name)

	e, err := embed.StartEtcd(cfg)
	if err != nil {
		t.Fatalf("start etcd : %v", err)
	}

	select {
	case <-e.Server.ReadyNotify():
	case <-time.After(20 * time.Second):
		e.Close()
		t.Fatal("etcd not ready in 20s")
	}

	endpoints := []string{e.Clients[0].Addr().String()}
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		e.Close()
		t.Fatalf("etcd client : %v", err)
	}

	cleanup := func() {
		_ = cli.Close()
		e.Close()
	}
	return cli, cleanup
}

func TestEtcd_Smoke(t *testing.T) {
	cli, cleanup := startEmbedEtcd(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cli.Put(ctx, "/k", "v"); err != nil {
		t.Fatalf("put : %v", err)
	}
}
