// weft-network is the control-plane daemon that reconciles Routers,
// Load Balancers, DNS zones / records, and Scheduling Rules from
// operator intent (stored in etcd under /weft/network/*) into the
// data plane (WireGuard mesh, weft-agent's embedded Caddy, CoreDNS,
// the agent's FirstFitScheduler).
//
// Today this is a SCAFFOLD : the gRPC server registers the
// NetworkControlPlane service but every RPC returns codes.Unimplemented.
// As individual RPCs get implemented (etcd backend + reconciler), the
// dashboard's networking panels light up one at a time. The webui's
// live-first pattern degrades to mock state on Unimplemented, so
// partial rollout is safe.
package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/openweft/weft-network/internal/server"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	netv1 "github.com/openweft/weft-network-proto"
)

// Build-time stamps populated via -ldflags "-X main.version=…".
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	var (
		listen   string
		etcdURL  string
		logLevel string
	)
	cmd := &cobra.Command{
		Use:   "weft-network",
		Short: "openweft network control plane (routers / LBs / DNS / scheduling)",
		Long: `weft-network reconciles operator intent (etcd-stored) into the
data plane. Runs as one infra microVM per DC ; etcd-elected leader owns
the reconciler, followers serve read-only snapshots and forward writes.

This is a scaffold today : every RPC returns Unimplemented. The webui's
live-first pattern degrades gracefully, so individual RPCs can be wired
incrementally without breaking the dashboard.`,
		Version: version + " (" + commit + " " + date + ")",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd, listen, etcdURL, logLevel)
		},
	}
	f := cmd.Flags()
	f.StringVar(&listen, "listen", "unix:///var/run/weft-network/weft-network.sock",
		"gRPC listen address ; supports unix:///path or tcp:host:port")
	f.StringVar(&etcdURL, "etcd", "",
		"etcd endpoints (comma-separated). Empty = in-memory state (dev only).")
	f.StringVar(&logLevel, "log-level", "info", "log level : debug / info / warn / error")
	return cmd
}

func run(cmd *cobra.Command, listen, etcdURL, logLevel string) error {
	logger := newLogger(logLevel)
	logger.Info("starting weft-network",
		"version", version, "commit", commit, "date", date,
		"listen", listen, "etcd", etcdURL)

	network, addr, err := parseListen(listen)
	if err != nil {
		return fmt.Errorf("parse --listen %q : %w", listen, err)
	}
	if network == "unix" {
		_ = os.Remove(addr)
		// Permissions handled by the operator via the parent dir's mode.
		if err := os.MkdirAll(parentDir(addr), 0o755); err != nil {
			return fmt.Errorf("mkdir parent of %s : %w", addr, err)
		}
	}
	lis, err := net.Listen(network, addr)
	if err != nil {
		return fmt.Errorf("listen %s://%s : %w", network, addr, err)
	}
	defer lis.Close()

	netServer := server.New(server.Options{
		Logger:  logger,
		EtcdURL: etcdURL,
	})
	defer func() {
		if err := netServer.Close(); err != nil {
			logger.Warn("server close", "err", err)
		}
	}()

	srv := grpc.NewServer()
	netv1.RegisterNetworkControlPlaneServer(srv, netServer)
	logger.Info("gRPC server registered ; awaiting connections", "addr", lis.Addr().String())

	// Cooperative shutdown : SIGINT / SIGTERM triggers GracefulStop so
	// in-flight RPCs finish before exit.
	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		logger.Info("signal received ; graceful stop")
		srv.GracefulStop()
	}()

	if err := srv.Serve(lis); err != nil {
		return fmt.Errorf("grpc serve : %w", err)
	}
	logger.Info("weft-network exited cleanly")
	return nil
}

// parseListen splits "unix:///path" or "tcp:host:port" into the
// net.Listen pair. Defaults to tcp when no scheme is present.
func parseListen(s string) (network, addr string, err error) {
	switch {
	case strings.HasPrefix(s, "unix://"):
		return "unix", strings.TrimPrefix(s, "unix://"), nil
	case strings.HasPrefix(s, "tcp:"):
		return "tcp", strings.TrimPrefix(s, "tcp:"), nil
	case strings.Contains(s, "/"):
		return "unix", s, nil
	default:
		return "tcp", s, nil
	}
}

// parentDir returns the directory holding the given path. Used to
// mkdir-p the socket's parent before bind.
func parentDir(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i]
	}
	return "."
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
