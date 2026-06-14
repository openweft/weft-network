# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project aims to adhere to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.3.2] - 2026-06-14

### Added

- **Seed-at-startup + 30 s safety-net poller** (`internal/fips/poller.go`).
  Closes the failure model where a freshly-restarted weft-network
  had no events to replay and would briefly withdraw every active
  /32 from upstream BGP on the first publish. The Poller now hydrates
  `fips.Index` synchronously from `weft.ListFloatingIPs` via the new
  `(*lifecycle.WeftClient).ListFloatingIPs(ctx)` BEFORE the
  Subscriber goes live, then ticks every 30 s as a safety net
  against missed NATS events. Every tick uses `Index.ReplaceAll`
  (new) for an atomic full-state swap and dedups onChange per
  affected network so a 5-FIP reshuffle on network N republishes
  that router exactly once. 5 new tests, commit `965c778`.

## [0.3.1] - 2026-06-14

### Added

- **Floating IPs announced as /32 BGP prefixes** via the existing
  per-tenant `weft-router` microVM. Closes the loop between weft's
  host-side nftables NAT and the public Internet : any mapped FIP
  now ALSO travels as a single-host BGP UPDATE to the upstream peer,
  alongside the operator-typed Prefixes. weft-router itself needed
  zero changes — GoBGP accepts /32 + /128 via the existing
  `bgp.ApplyPrefixes` path.
- **New package `internal/fips/`** : thread-safe per-network
  active-FIP index (`Index`) + NATS wildcard subscriber on
  `weft.events.floating_ip.>` (`Subscriber`). Reacts to
  allocated/mapped/unmapped/released ; surfaces only Mapped entries
  to `ActiveFIPsInNetworks` so available-but-unmapped FIPs don't
  pollute the BGP announce set. Decode failures + unknown kinds
  log + drop ; the next event self-heals.
- **`publisher.FIPLookup` interface** + `(*publisher.NATS).SetFIPLookup`.
  `StateFor` now appends every active FIP in the router's stitched
  networks as a /32 (v4) or /128 (v6) `PrefixAdvertisement`. `nil` /
  `NoopFIPLookup` keep the legacy "operator-typed Prefixes only"
  behaviour so pre-existing scaffold callers compile without churn.
- **`cmd/weft-network` wiring** : when `--nats` is set, spin a
  dedicated `fip-subscriber` NATS connection, hand the Index to
  the publisher via `SetFIPLookup`, and on every FIP mutation
  re-Publish every router stitching the affected network so
  GoBGP picks up the new announce set within one round-trip.

13 new tests (6 index + 3 subscriber + 4 publisher), full repo
suite green. Commit `71dd435`.

## [0.2.0] - 2026-06-02

v0.2.0-track work since `v0.1.0` (`cc3b880`).

### Added

- **Publisher → router NATS pipeline** : DesiredState push to
  `weft-router` on every Router CRUD (`11631eb`), with OTel spans
  around `Publish` / `Withdraw` (`fdd8cc7`).
- **Server startup resync** + integration tests for the publisher
  wiring (`ca0c811`).
- **Status receiver** : closes the observability loop from
  `weft-router` back into `weft-network` (`74bc068`), with
  `Prefixes` threaded from the proto through the store and the
  publisher (`9baf487`).
- **Lifecycle seam** for orchestrating the weft-router micro-VM
  (`4d86cc3`), with the real `WeftClient` impl calling
  `weft.RegisterMicroVM` with the configured `image=` (`a28881b`).
- **Reproducible build + supply chain** : `SOURCE_DATE_EPOCH`-pinned
  bit-reproducible OCI image (`b28e689`).

### Changed

- **Stores coverage** lifted from ~40 % → 65–87 % across the four
  domains (scheduling rules, DNS zones, DNS records, routers, load
  balancers). Replicated the v0.1.0 etcd / memory parity tests under
  a shared `etcdtest` helper so a backend bug fails both targets.
- **`etcdtest` helper** : reusable harness that spins an
  `embed.Etcd` in `t.TempDir()` and tears it down on `t.Cleanup`.
  Adopted by every `*_etcd_test.go` in the repo ; consumers outside
  this module can vendor it for their own etcd-backed packages.

### Fixed

- **DNS zone updates** (real bug) : zone `Put` against etcd is now
  retried with a CAS loop so two concurrent updates can't clobber
  each other (`8acb9e3`).
- **`weftclient.Ensure`** (real bug) : must `StartVM` after
  `RegisterMicroVM`, otherwise the router VM never boots (`98dcc29`).

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
