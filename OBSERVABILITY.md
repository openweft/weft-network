# Observability

The daemon exposes Prometheus metrics on a separate listener
(`--metrics-addr`, default `:9100`). Separate fate from the gRPC
listener so a scrape-side issue can't take down the control plane.

```sh
curl -s http://127.0.0.1:9100/metrics | head -20
# weft_network_build_info{commit="abc1234",date="2026-05-31T...",version="v0.1.0"} 1
# weft_network_etcd_connected 1
# weft_network_rpc_total{code="OK",method="...List..."} 42
# weft_network_rpc_duration_seconds_bucket{...} ...
```

## Metric catalog

| Metric                                           | Type      | Labels                       | What it tells you                                                                       |
| ------------------------------------------------ | --------- | ---------------------------- | --------------------------------------------------------------------------------------- |
| `weft_network_build_info`                        | Gauge (=1) | `version`, `commit`, `date`  | Boot fingerprint. Useful as a `group_left` to join build info onto other queries.       |
| `weft_network_rpc_total`                         | Counter   | `method`, `code`             | Call count per RPC, broken out by gRPC status code (`OK` / `NotFound` / …).             |
| `weft_network_rpc_duration_seconds`              | Histogram | `method`, `code`             | Latency. Default Prometheus buckets (5 ms → 10 s) ; fine for control-plane RPCs.         |
| `weft_network_etcd_connected`                    | Gauge     | —                            | 1 if the daemon holds an etcd connection ; 0 in memory-only mode or after a connection failure. Set ONCE at startup. |

## Sample PromQL

### Error rate per RPC

```promql
sum by (method) (
  rate(weft_network_rpc_total{code!="OK"}[5m])
)
/
sum by (method) (
  rate(weft_network_rpc_total[5m])
)
```

Alert when this exceeds ~5 % sustained on a non-trivial method.

### p99 latency

```promql
histogram_quantile(0.99,
  sum by (le, method) (
    rate(weft_network_rpc_duration_seconds_bucket[5m])
  )
)
```

Control-plane RPCs should sit well under 100 ms. Anything in the
seconds bucket is an etcd cliff (lost the leader, partition,
overloaded peer).

### Operations per second by domain

```promql
sum by (method) (rate(weft_network_rpc_total[1m]))
```

Lets you see whether DNS / scheduling rule / LB activity tracks
the workload you expect. Spikes that don't match tenant activity
suggest a runaway reconciler upstream.

### Etcd disconnection alert

```promql
weft_network_etcd_connected == 0 AND ON() weft_network_build_info{version!=""}
```

The `version` predicate filters scrape failures (a metric series
that hasn't reported recently looks indistinguishable from
`== 0` ; gating on `build_info` ensures we're asking a live
daemon).

### Replica fan-out (3-DC deployment)

In production each DC runs one weft-network replica ; alerts care
about at least one being reachable + the etcd-connected gauge
flipping back to 1 on follower restart.

```promql
count(up{job="weft-network"} == 1) < 2
# Page when fewer than 2 replicas are scrapeable. Quorum needs 2/3.
```

## Logs

slog handler → stderr, default `info` level, switchable via
`--log-level debug|info|warn|error`.

Key recurring entries to watch :

- `scheduling rule created uuid=… name=…` / `dns zone created …` etc.
  — mutation traffic.
- `etcd-backed stores wired url=… domains=…` at startup when
  `--etcd` is set.
- `etcd backend not yet implemented` warning when `--etcd` is set
  but the connection fails — daemon falls back to memory.
- `tls reloaded on SIGHUP` after a successful cert rotation.
- `tls reload (SIGHUP) failed ; previous cert still served` on
  botched renewals — operator's certbot post-renewal hook needs
  attention.

journalctl filter recipe (systemd deployments) :

```sh
# Tail the mutation events.
journalctl -u weft-network -f \
  --output cat \
  --grep '(created|deleted|backends updated|tls reloaded)'

# Catch errors in the last hour.
journalctl -u weft-network --since '1 hour ago' \
  --output cat \
  --grep 'level=ERROR'
```

## Health probe

`GET /healthz` on the same metrics listener returns 200 OK as long
as the HTTP server is up. It does NOT inspect etcd connectivity —
gauge `weft_network_etcd_connected` carries that signal. The split
is intentional : load-balancer probes care "is this replica
accepting traffic" ; alerting cares "is this replica fully wired".

```sh
curl -fsS http://127.0.0.1:9100/healthz
# ok
```

## Tracing

OpenTelemetry / OTLP is wired through `internal/tracing`. Every
NetworkControlPlane RPC is wrapped by `otelgrpc.NewServerHandler()`
(installed as a `grpc.StatsHandler` on the gRPC server) so adding a
new RPC to the proto is automatically traced — no per-method
instrumentation needed.

Enable it by pointing the daemon at an OTLP/gRPC collector :

```sh
weft-network \
  --otlp-endpoint=tempo.weft.internal:4317 \
  --listen=unix:///run/weft-network/weft-network.sock
```

| Flag                  | Env var                          | Default | What it does                                                                                       |
| --------------------- | -------------------------------- | ------- | -------------------------------------------------------------------------------------------------- |
| `--otlp-endpoint`     | `WEFT_NETWORK_OTLP_ENDPOINT`     | empty   | OTLP/gRPC collector address `host:port`. Empty disables tracing (a no-op tracer provider is installed). |
| `--otlp-insecure`     | —                                | `true`  | Skip TLS on the OTLP push connection. Fine inside the WireGuard mesh ; flip off for TLS-fronted collectors. |

### Shape

- **Resource attributes** : `service.name=weft-network`,
  `service.version=<build-stamp>`. Both are attached to every span,
  so the collector groups + filters trivially.
- **Pipeline** : `BatchSpanProcessor` → `otlptracegrpc` exporter →
  the configured collector. Batching means a slow collector can't
  block the hot path — the BSP flushes asynchronously.
- **Shutdown** : SIGTERM triggers a 10 s flush before exit, so the
  last batch of spans makes it out the door on graceful stop.

### Pointing it at common backends

- **Tempo** : `--otlp-endpoint=<tempo-host>:4317` (Tempo's distributor
  defaults to that port).
- **Jaeger** (with the OTLP receiver, Jaeger v1.35+) :
  `--otlp-endpoint=<jaeger-host>:4317`.
- **OTLP-compatible vendor** (Honeycomb, Lightstep, Datadog OTLP
  receiver, etc.) : `--otlp-endpoint=<vendor-host>:4317` and flip
  `--otlp-insecure=false` to enable TLS.

Tracing is best-effort : a misconfigured endpoint or unreachable
collector logs a warning at boot and the daemon keeps serving. The
control plane never fails open because traces aren't landing.
