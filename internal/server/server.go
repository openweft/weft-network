// Package server implements the gRPC NetworkControlPlaneServer.
// Each domain (scheduling rules, DNS, routers, load balancers) has
// a concrete method overriding the embedded Unimplemented default.
// Backing stores are selected by Options.EtcdURL : empty = in-memory
// (dev), non-empty = etcd-backed (production).
package server

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	netv1 "github.com/openweft/weft-network-proto"
	"github.com/openweft/weft-network/internal/store"
	"github.com/openweft/weft-network/internal/store/dns"
	"github.com/openweft/weft-network/internal/store/lb"
	"github.com/openweft/weft-network/internal/store/router"
	"github.com/openweft/weft-network/internal/store/scheduling"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// Options bundles construction inputs.
type Options struct {
	Logger  *slog.Logger
	EtcdURL string       // comma-separated endpoints ; empty = in-memory (dev only)
	Stores  *store.Stores // explicit injection ; nil = build defaults from EtcdURL
}

// Server implements netv1.NetworkControlPlaneServer.
//
// Embeds UnimplementedNetworkControlPlaneServer so any future RPC
// added to the proto compiles cleanly until a concrete method
// overrides it. The webui's live-first pattern degrades to its
// mock store on Unimplemented, so partial rollout is safe.
type Server struct {
	netv1.UnimplementedNetworkControlPlaneServer

	logger *slog.Logger
	opts   Options
	stores *store.Stores
	// etcdClient is the live connection when EtcdURL is set ; Close
	// must be called via Server.Close on shutdown so the watch
	// goroutines and connections drain cleanly.
	etcdClient *clientv3.Client
}

// New constructs a Server. Logger defaults to slog.Default when nil.
// Stores default to in-memory backends when nil + EtcdURL is empty,
// or to etcd-backed stores when EtcdURL is set.
//
// Today only the SchedulingRules domain has an etcd backend ; DNS,
// routers, and load balancers stay in-memory regardless of EtcdURL.
// As each domain gets its etcd impl (same pattern as scheduling/etcd.go),
// flip its line below ; the dashboard's live-first contract
// guarantees graceful behaviour during the partial-migration window.
func New(opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	var etcdClient *clientv3.Client
	stores := opts.Stores
	if stores == nil {
		stores = &store.Stores{
			SchedulingRules: scheduling.NewMemory(),
			DNS:             dns.NewMemory(),
			Routers:         router.NewMemory(),
			LBs:             lb.NewMemory(),
		}
		if opts.EtcdURL != "" {
			c, err := newEtcdClient(opts.EtcdURL)
			if err != nil {
				// Fall back to memory with a loud warning rather than
				// failing startup ; an operator who set --etcd but
				// typo'd a hostname is better served by a running
				// dashboard with mock state than a refusing daemon.
				opts.Logger.Error("etcd connection failed ; falling back to in-memory stores", "url", opts.EtcdURL, "err", err)
			} else {
				etcdClient = c
				stores.SchedulingRules = scheduling.NewEtcd(c)
				opts.Logger.Info("etcd-backed stores wired", "url", opts.EtcdURL, "domains", "scheduling-rules")
				// TODO : DNS / routers / load balancers — same pattern.
			}
		}
	}
	return &Server{logger: opts.Logger, opts: opts, stores: stores, etcdClient: etcdClient}
}

// Close releases the etcd connection when one was opened. Idempotent.
// Daemons call this from their shutdown handler ; tests don't need
// to bother for memory-only Servers.
func (s *Server) Close() error {
	if s.etcdClient == nil {
		return nil
	}
	err := s.etcdClient.Close()
	s.etcdClient = nil
	return err
}

// newEtcdClient parses the comma-separated endpoint list and dials.
func newEtcdClient(rawURL string) (*clientv3.Client, error) {
	endpoints := strings.Split(rawURL, ",")
	for i, e := range endpoints {
		endpoints[i] = strings.TrimSpace(e)
	}
	c, err := clientv3.New(clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("dial etcd %v : %w", endpoints, err)
	}
	return c, nil
}
