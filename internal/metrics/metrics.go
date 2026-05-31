// Package metrics owns the Prometheus instrumentation for
// weft-network. The daemon registers a small surface :
//
//   - weft_network_build_info{version,commit,date}  — boot fingerprint
//   - weft_network_rpc_total{method,code}           — call counter
//   - weft_network_rpc_duration_seconds{method,code} — latency histogram
//   - weft_network_etcd_connected                   — 0/1 gauge
//
// The gRPC interceptor wraps every Unary RPC so adding a new method
// to the proto doesn't require touching this package.
package metrics

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// Recorder bundles the metrics + the gRPC interceptor.
//
// Construct once at startup ; the same instance is registered both
// against the prometheus.Registry served on /metrics and as a
// grpc.UnaryServerInterceptor on the gRPC server.
type Recorder struct {
	reg *prometheus.Registry

	buildInfo     *prometheus.GaugeVec
	rpcTotal      *prometheus.CounterVec
	rpcDuration   *prometheus.HistogramVec
	etcdConnected prometheus.Gauge
}

// New builds + registers the recorder against a fresh registry.
// Pass version / commit / date strings from main.go's -ldflags
// stamps so the build_info metric is useful.
func New(version, commit, date string) *Recorder {
	r := &Recorder{
		reg: prometheus.NewRegistry(),
		buildInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "weft_network_build_info",
			Help: "Build fingerprint of the running weft-network binary. Value is always 1 ; the labels carry the version / commit / date stamped at build time.",
		}, []string{"version", "commit", "date"}),
		rpcTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "weft_network_rpc_total",
			Help: "Total NetworkControlPlane RPC calls, labelled by method and gRPC code.",
		}, []string{"method", "code"}),
		rpcDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "weft_network_rpc_duration_seconds",
			Help:    "NetworkControlPlane RPC latency histogram.",
			Buckets: prometheus.DefBuckets, // 5 ms → 10 s default buckets, fine for control-plane RPCs.
		}, []string{"method", "code"}),
		etcdConnected: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "weft_network_etcd_connected",
			Help: "1 if the daemon currently holds an etcd connection ; 0 if running on in-memory stores.",
		}),
	}
	r.reg.MustRegister(r.buildInfo, r.rpcTotal, r.rpcDuration, r.etcdConnected)
	r.buildInfo.WithLabelValues(version, commit, date).Set(1)
	return r
}

// SetEtcdConnected flips the gauge. Call once on startup ; gRPC
// reconnects are transparent to the daemon (the client handles
// them), so flipping back to 0 mid-flight isn't useful telemetry.
func (r *Recorder) SetEtcdConnected(connected bool) {
	v := 0.0
	if connected {
		v = 1.0
	}
	r.etcdConnected.Set(v)
}

// Handler returns the http.Handler serving /metrics. Caller wires
// it into a dedicated listener (different port from gRPC) so the
// scrape surface doesn't share fate with the control plane.
func (r *Recorder) Handler() http.Handler {
	return promhttp.HandlerFor(r.reg, promhttp.HandlerOpts{})
}

// UnaryInterceptor returns the grpc.UnaryServerInterceptor that
// records every NetworkControlPlane method on the recorder. Wire
// via grpc.NewServer(grpc.UnaryInterceptor(r.UnaryInterceptor())).
func (r *Recorder) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		code := status.Code(err).String()
		r.rpcTotal.WithLabelValues(info.FullMethod, code).Inc()
		r.rpcDuration.WithLabelValues(info.FullMethod, code).Observe(time.Since(start).Seconds())
		return resp, err
	}
}

// Registry exposes the underlying prometheus.Registry for tests +
// the rare case a caller wants to register a domain-specific metric
// alongside ours.
func (r *Recorder) Registry() *prometheus.Registry { return r.reg }
