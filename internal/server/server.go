// Package server implements the gRPC NetworkControlPlaneServer.
// Today every RPC returns codes.Unimplemented ; individual methods
// get wired one at a time to their etcd-backed stores + reconcilers
// (see ../../README.md for the recommended order).
package server

import (
	"log/slog"

	netv1 "github.com/openweft/weft-network-proto"
)

// Options bundles construction inputs.
type Options struct {
	Logger  *slog.Logger
	EtcdURL string // empty = in-memory (dev only)
}

// Server implements netv1.NetworkControlPlaneServer.
//
// Embeds UnimplementedNetworkControlPlaneServer so every method
// returns codes.Unimplemented by default. To wire an RPC, add a
// concrete method on *Server overriding the embedded default —
// the webui's live-first pattern degrades to its mock store when
// it sees Unimplemented, so partial rollout is safe.
type Server struct {
	netv1.UnimplementedNetworkControlPlaneServer

	logger *slog.Logger
	opts   Options
}

// New constructs a Server. Logger defaults to slog.Default when nil.
func New(opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Server{logger: opts.Logger, opts: opts}
}
