// weft-network is the control-plane daemon that reconciles Routers,
// Load Balancers, DNS zones / records, and Scheduling Rules from
// operator intent (stored in etcd under /weft/network/*) into the
// data plane (WireGuard mesh, weft-agent's embedded Caddy, CoreDNS,
// the agent's FirstFitScheduler).
//
// All 16 RPCs from netv1.NetworkControlPlane are implemented. Backing
// stores : in-memory (dev, no --etcd) or etcd-backed (production).
// Observability via /metrics — Prometheus on a separate port from
// the gRPC listener so the scrape surface doesn't share fate with
// the control plane.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/openweft/weft-network/internal/lifecycle"
	"github.com/openweft/weft-network/internal/metrics"
	"github.com/openweft/weft-network/internal/publisher"
	"github.com/openweft/weft-network/internal/server"
	"github.com/openweft/weft-network/internal/statusreceiver"
	"github.com/openweft/weft-network/internal/tlsutil"
	"github.com/openweft/weft-network/internal/tracing"
	weftslognats "github.com/openweft/weft-slognats"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
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
		listen       string
		metricsAddr  string
		etcdURL      string
		natsURL      string
		weftSocket   string
		routerImage  string
		routerProj   string
		logLevel     string
		tlsCertFile  string
		tlsKeyFile   string
		clientCAFile string
		otlpEndpoint string
		otlpInsecure bool
	)
	cmd := &cobra.Command{
		Use:   "weft-network",
		Short: "openweft network control plane (routers / LBs / DNS / scheduling)",
		Long: `weft-network reconciles operator intent (etcd-stored) into the
data plane. Runs as one infra microVM per DC ; etcd-elected leader owns
the reconciler, followers serve read-only snapshots and forward writes.

All 16 NetworkControlPlane RPCs are implemented. Backing stores are
in-memory by default ; pass --etcd to persist. The /metrics endpoint
exposes Prometheus metrics (build info, RPC counters + latency,
etcd-connected gauge) on a separate listener.`,
		Version: version + " (" + commit + " " + date + ")",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd, runOpts{
				listen:       listen,
				metricsAddr:  metricsAddr,
				etcdURL:      etcdURL,
				natsURL:      natsURL,
				weftSocket:   weftSocket,
				routerImage:  routerImage,
				routerProj:   routerProj,
				logLevel:     logLevel,
				tlsCertFile:  tlsCertFile,
				tlsKeyFile:   tlsKeyFile,
				clientCAFile: clientCAFile,
				otlpEndpoint: otlpEndpoint,
				otlpInsecure: otlpInsecure,
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&listen, "listen", "unix:///var/run/weft-network/weft-network.sock",
		"gRPC listen address ; supports unix:///path or tcp:host:port")
	f.StringVar(&metricsAddr, "metrics-addr", ":9100",
		"Prometheus /metrics listen address ; empty disables. tcp:host:port shape.")
	f.StringVar(&etcdURL, "etcd", "",
		"etcd endpoints (comma-separated). Empty = in-memory state (dev only).")
	f.StringVar(&natsURL, "nats", "",
		"NATS URL for the weft-router publisher (e.g. nats://nats.weft.internal:4222). "+
			"Empty = Noop publisher (router CRUD persists but no DesiredState reaches micro-VMs).")
	f.StringVar(&weftSocket, "weft-socket", "",
		"Path to the weft daemon Unix socket. When set, weft-network drives "+
			"RegisterMicroVM / StopVM / DeleteVM for the matching weft-router micro-VMs "+
			"via lifecycle.WeftClient. Empty = Noop lifecycle (operators hand-spawn).")
	f.StringVar(&routerImage, "router-image", "ghcr.io/openweft/weft-router:latest",
		"OCI image ref used when WeftClient spawns a weft-router micro-VM.")
	f.StringVar(&routerProj, "router-project", "platform",
		"weft project to spawn router micro-VMs into.")
	f.StringVar(&logLevel, "log-level", "info", "log level : debug / info / warn / error")
	f.StringVar(&tlsCertFile, "tls-cert", "",
		"PEM-encoded server certificate. Pair with --tls-key to enable TLS ; unset = insecure (unix-socket only).")
	f.StringVar(&tlsKeyFile, "tls-key", "",
		"PEM-encoded server private key. Required when --tls-cert is set.")
	f.StringVar(&clientCAFile, "client-ca", "",
		"PEM-encoded CA bundle for client cert verification. Sets the server to mTLS-required mode ; unset = anonymous clients.")
	// Tracing knobs. Env-var sourced so the systemd unit can flip them
	// via /etc/default/weft-network without editing flags.
	f.StringVar(&otlpEndpoint, "otlp-endpoint", os.Getenv("WEFT_NETWORK_OTLP_ENDPOINT"),
		"OTLP/gRPC trace collector endpoint (host:port). Empty disables tracing. Defaults to $WEFT_NETWORK_OTLP_ENDPOINT.")
	f.BoolVar(&otlpInsecure, "otlp-insecure", true,
		"Skip TLS on the OTLP push connection. Fine inside the WireGuard mesh ; flip off when pointing at a TLS-fronted collector.")
	return cmd
}

// runOpts groups the parsed flag values so the run() signature stays
// readable. Same shape as the cobra flag bindings ; one-to-one map.
type runOpts struct {
	listen       string
	metricsAddr  string
	etcdURL      string
	natsURL      string
	weftSocket   string
	routerImage  string
	routerProj   string
	logLevel     string
	tlsCertFile  string
	tlsKeyFile   string
	clientCAFile string
	otlpEndpoint string
	otlpInsecure bool
}

func run(cmd *cobra.Command, o runOpts) error {
	// Fan slog records out to NATS (in addition to stderr) on a
	// per-host subject so weft-network logs aggregate alongside the
	// rest of the platform. Subject uses $WEFT_HOST_UUID when set,
	// falling back to hostname so it's always well-formed.
	hostID := os.Getenv("WEFT_HOST_UUID")
	if hostID == "" {
		if h, err := os.Hostname(); err == nil {
			hostID = h
		} else {
			hostID = "unknown"
		}
	}
	natsLogger, logCloser := weftslognats.SetupFromEnv("weft.network." + hostID + ".log")
	defer logCloser.Close()
	slog.SetDefault(natsLogger)

	logger := newLogger(o.logLevel)
	tlsOpts := tlsutil.Options{
		CertFile:     o.tlsCertFile,
		KeyFile:      o.tlsKeyFile,
		ClientCAFile: o.clientCAFile,
	}
	logger.Info("starting weft-network",
		"version", version, "commit", commit, "date", date,
		"listen", o.listen, "etcd", o.etcdURL, "tls", tlsOpts.Mode(),
		"otlp", o.otlpEndpoint)

	// Tracing is best-effort : exporter init runs against the boot
	// context so it gets cancelled along with everything else, and
	// errors are logged + swallowed so a missing collector never
	// blocks the control plane. The empty-endpoint case is a no-op
	// inside Init.
	traceShutdown, err := tracing.Init(cmd.Context(), tracing.Options{
		OTLPEndpoint: o.otlpEndpoint,
		Insecure:     o.otlpInsecure,
		ServiceName:  "weft-network",
		Version:      version,
	})
	if err != nil {
		logger.Warn("tracing init failed ; continuing without traces", "err", err)
		traceShutdown = func(context.Context) error { return nil }
	}
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), tracing.ShutdownTimeout)
		defer cancel()
		if err := traceShutdown(shutCtx); err != nil {
			logger.Warn("tracing shutdown", "err", err)
		}
	}()

	network, addr, err := parseListen(o.listen)
	if err != nil {
		return fmt.Errorf("parse --listen %q : %w", o.listen, err)
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

	// Router publisher : NATS in prod, Noop when --nats is empty (so a
	// single-host dev daemon doesn't refuse to start just because no
	// NATS cluster is up). Failure to connect logs loudly and falls
	// back to Noop ; the router CRUD still persists, the operator
	// notices via "router publish failed" warnings on Create.
	var routerPub publisher.RouterPublisher
	if o.natsURL != "" {
		np, err := publisher.NewNATS(logger, o.natsURL)
		if err != nil {
			logger.Error("nats publisher connect failed ; falling back to Noop",
				"url", o.natsURL, "err", err)
		} else {
			routerPub = np
			defer np.Close()
			logger.Info("router publisher wired", "nats_url", o.natsURL)
		}
	}

	// Router lifecycle : WeftClient when --weft-socket is set, Noop
	// otherwise. Same shape as the publisher's fallback — failures to
	// dial the weft socket log loudly and downgrade to Noop so the
	// daemon still serves Router CRUD ; operators see "router
	// lifecycle ensure failed" warnings on Create until they fix the
	// socket path.
	var routerLC lifecycle.RouterLifecycle
	var weftLC *lifecycle.WeftClient
	if o.weftSocket != "" {
		wc, err := lifecycle.NewWeftClient(logger, o.weftSocket, o.routerImage, o.routerProj)
		if err != nil {
			logger.Error("weft lifecycle dial failed ; falling back to Noop",
				"socket", o.weftSocket, "err", err)
		} else {
			routerLC = wc
			weftLC = wc
			logger.Info("router lifecycle wired",
				"weft_socket", o.weftSocket, "image", o.routerImage, "project", o.routerProj)
		}
	}
	if weftLC != nil {
		defer weftLC.Close()
	}

	netServer := server.New(server.Options{
		Logger:          logger,
		EtcdURL:         o.etcdURL,
		RouterPublisher: routerPub,
		RouterLifecycle: routerLC,
	})
	defer func() {
		if err := netServer.Close(); err != nil {
			logger.Warn("server close", "err", err)
		}
	}()

	// Initial resync : republish every router that's already in the
	// store so a fresh weft-network with surviving etcd state gets the
	// matching weft-router micro-VMs back in sync — NATS doesn't retain
	// messages across our restart. Best-effort, doesn't fail startup.
	if n, err := netServer.ResyncRouters(cmd.Context()); err != nil {
		logger.Warn("router resync failed at startup", "err", err)
	} else if n > 0 {
		logger.Info("router resync done", "count", n)
	}

	// Status receiver : subscribes to weft.router.*.status and feeds
	// the router store's UpdateStatus. Best-effort like the publisher
	// — start failures log loudly but don't refuse the daemon. Same
	// --nats URL as the publisher : one NATS cluster per DC, two
	// distinct connections (one for publish, one for subscribe) so a
	// subscriber callback hang can't stall the publisher path.
	if o.natsURL != "" {
		recv, err := statusreceiver.New(logger, o.natsURL, netServer.RouterStore())
		if err != nil {
			logger.Warn("status receiver construct failed ; skipping", "err", err)
		} else if err := recv.Start(cmd.Context()); err != nil {
			logger.Warn("status receiver start failed ; skipping", "url", o.natsURL, "err", err)
		} else {
			defer recv.Stop()
			logger.Info("status receiver wired", "nats_url", o.natsURL)
		}
	}

	rec := metrics.New(version, commit, date)
	rec.SetEtcdConnected(o.etcdURL != "")

	// gRPC server options. Order matters for readability, not
	// correctness :
	//
	//   - otelgrpc.NewServerHandler is a StatsHandler — it opens a
	//     span per RPC and is a no-op when the global tracer provider
	//     is otel's noop (i.e. --otlp-endpoint empty), so we install
	//     it unconditionally.
	//   - The Prometheus interceptor wraps every method so adding a
	//     new RPC to the proto records counters automatically.
	//
	// TLS creds are wired only when --tls-cert + --tls-key are set ;
	// any TLS misconfig is a hard startup error (no silent fallback
	// to insecure — see internal/tlsutil).
	grpcOpts := []grpc.ServerOption{
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.UnaryInterceptor(rec.UnaryInterceptor()),
	}
	var tlsReloader tlsutil.Reloader
	if !tlsOpts.Empty() {
		creds, reloader, err := tlsutil.ServerCredentialsWithReloader(tlsOpts)
		if err != nil {
			return fmt.Errorf("tls : %w", err)
		}
		grpcOpts = append(grpcOpts, grpc.Creds(creds))
		tlsReloader = reloader
	} else if network == "tcp" {
		// Anonymous tcp listen is allowed for dev but loud — the
		// operator opted out of transport security ; surface it so
		// it doesn't slip into production unnoticed.
		logger.Warn("running gRPC over TCP without TLS ; clients connect anonymously",
			"hint", "set --tls-cert + --tls-key for production deployments")
	}
	srv := grpc.NewServer(grpcOpts...)
	netv1.RegisterNetworkControlPlaneServer(srv, netServer)
	logger.Info("gRPC server registered ; awaiting connections", "addr", lis.Addr().String())

	// /metrics on its own listener — separate fate from gRPC so a
	// scrape-side issue can't take down the control plane.
	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// SIGHUP → TLS cert reload. Operator's certbot post-renewal hook
	// drops the new cert/key in place + sends SIGHUP ; the loader
	// re-reads on the next handshake. No restart, no in-flight RPC
	// loss. Insecure-mode daemons swallow the signal harmlessly.
	if tlsReloader != nil {
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		defer func() {
			signal.Stop(hup)
			close(hup)
		}()
		go func() {
			for range hup {
				if err := tlsReloader.Reload(); err != nil {
					logger.Error("tls reload (SIGHUP) failed ; previous cert still served", "err", err)
				} else {
					logger.Info("tls reloaded on SIGHUP")
				}
			}
		}()
	}

	var metricsSrv *http.Server
	if o.metricsAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", rec.Handler())
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok\n"))
		})
		metricsSrv = &http.Server{
			Addr:              o.metricsAddr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		}
		go func() {
			logger.Info("metrics listener", "addr", o.metricsAddr)
			if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Error("metrics serve", "err", err)
			}
		}()
	}

	// Cooperative shutdown : SIGINT / SIGTERM triggers GracefulStop so
	// in-flight RPCs finish before exit. Metrics HTTP listener stops
	// in parallel ; the 5s shutdown bound is plenty for an idle
	// scrape connection to drain.
	go func() {
		<-ctx.Done()
		logger.Info("signal received ; graceful stop")
		if metricsSrv != nil {
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = metricsSrv.Shutdown(shutCtx)
		}
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
