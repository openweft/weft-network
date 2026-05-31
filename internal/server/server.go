// Package server implements the gRPC NetworkControlPlaneServer.
// Today every RPC returns codes.Unimplemented ; individual methods
// get wired one at a time to their etcd-backed stores + reconcilers
// (see ../../README.md for the recommended order).
package server

import (
	"log/slog"

	netv1 "github.com/openweft/weft-network-proto"
	"github.com/openweft/weft-network/internal/store"
	"github.com/openweft/weft-network/internal/store/scheduling"
)

// Options bundles construction inputs.
type Options struct {
	Logger  *slog.Logger
	EtcdURL string       // empty = in-memory (dev only)
	Stores  *store.Stores // explicit injection ; nil = build defaults from EtcdURL
}

// Server implements netv1.NetworkControlPlaneServer.
//
// Embeds UnimplementedNetworkControlPlaneServer so every method
// returns codes.Unimplemented by default. Concrete methods on
// *Server override individual RPCs as they get wired — the webui's
// live-first pattern degrades to its mock store on Unimplemented,
// so partial rollout is safe.
type Server struct {
	netv1.UnimplementedNetworkControlPlaneServer

	logger *slog.Logger
	opts   Options
	stores *store.Stores
}

// New constructs a Server. Logger defaults to slog.Default when nil.
// Stores default to in-memory backends when nil ; passing an etcd
// URL is reserved for the next milestone — today the daemon always
// uses memory stores.
func New(opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	stores := opts.Stores
	if stores == nil {
		stores = &store.Stores{
			SchedulingRules: scheduling.NewMemory(),
		}
	}
	if opts.EtcdURL != "" {
		// TODO(weft-network-etcd-store) : swap each in-memory backend
		// for the etcd-backed one. Today we log the URL and keep using
		// memory, so the dev path still works after the flag is set.
		opts.Logger.Warn("etcd backend not yet implemented ; falling back to in-memory stores", "url", opts.EtcdURL)
	}
	return &Server{logger: opts.Logger, opts: opts, stores: stores}
}
