# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project aims to adhere to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-05-31

### Added

- gRPC `NetworkControlPlane` server with all 16 RPCs implemented across 4 domains :
    - **Scheduling rules** : `List` / `Create` / `Delete` (memory + etcd backends).
    - **DNS zones** : `List` / `Create` / `Delete` (zone delete cascades to records).
    - **DNS records** : `List` / `Create` / `Delete` (referential integrity to zones,
      record types restricted to A/AAAA/CNAME/SRV/TXT/NS/MX, TTL inherits from zone).
    - **Routers** : `List` / `Create` / `Delete` (kind ∈ {peer, egress} ; backend
      ∈ {wireguard, vyos, frr} ; per-kind validation).
    - **Load balancers** : `List` / `Create` / `Delete` / `SetBackends`. SetBackends
      uses an optimistic-concurrency-control loop against etcd ModRevision so
      two concurrent callers can't trample each other.
- In-memory + etcd persistence backends per domain. `--etcd <endpoints>` switches
  on the etcd path ; etcd connection failure logs an error and falls back to
  memory rather than refusing startup.
- Prometheus `/metrics` endpoint on a separate listener (default `:9100`,
  `--metrics-addr`) :
    - `weft_network_build_info{version,commit,date}`
    - `weft_network_rpc_total{method,code}`
    - `weft_network_rpc_duration_seconds{method,code}`
    - `weft_network_etcd_connected` (0/1 gauge).
- gRPC unary interceptor wraps every method so adding a new RPC to the proto
  records counters + latency automatically.
- `/healthz` endpoint on the metrics listener for load-balancer probes.
- Transport security : `--tls-cert` + `--tls-key` enable TLS ; `--client-ca`
  flips the daemon into mTLS-required mode. Misconfigured TLS is a hard startup
  error (no silent fallback to insecure).
- **SIGHUP-driven cert rotation** : the daemon re-reads cert+key files on
  signal ; certbot post-renewal hook pattern documented in `deploy/README.md`.
  Botched renewals (corrupt PEM) log an error and keep serving the previous
  cert.
- Cobra root with `--listen` (unix socket or tcp), `--etcd`, `--log-level`,
  `--metrics-addr`. `GracefulStop` on SIGINT / SIGTERM lets in-flight RPCs
  finish.
- **Deploy artifacts** :
    - `Dockerfile` (multi-stage scratch image, ~16 MB, vendored modules).
    - `deploy/systemd/weft-network.service` (hardened : NoNewPrivileges,
      ProtectSystem=strict, seccomp `@system-service`, Private{Tmp,Devices},
      Restrict{Namespaces,Realtime,SUIDSGID}, MemoryDenyWriteExecute,
      LockPersonality).
    - `deploy/README.md` with both container + systemd recipes.
- **CI** : `vet + test` on linux/amd64, cross-compile to linux/arm64+amd64,
  docker image smoke build on every push to `main`.
- **Release workflow** : tag-driven (`v*`) multi-arch GHCR publish
  (linux/amd64+arm64), `workflow_dispatch` for retry-from-ref.
- End-to-end gRPC integration test : spins up the real server on lo:0,
  dials it with a real client, exercises one mutation + one list per
  domain ; catches proto-wire / status-code-propagation regressions
  unit tests miss.

### Notes

- Backing stores are memory-only by default ; pass `--etcd <endpoints>` for
  persistence. The webui's live-first pattern (`wclient.IsUnimplemented`) lets
  the dashboard degrade gracefully to its mock store while individual RPCs are
  rolled out.
- The proto comment refers to LB data plane as "Envoy" ; the actual data
  plane is Caddy embedded in `weft-agent` (see
  [project_reverse_proxy_caddy](https://github.com/openweft/weft/blob/main/agent/proxy/doc.go)).
  The proto message shape (mode / address / port / backends / controller)
  still fits without a wire change.
