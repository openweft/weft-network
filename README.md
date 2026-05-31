# weft-network

The control-plane daemon that reconciles **Routers**, **Load Balancers**,
**DNS zones / records**, and **Scheduling Rules** from operator intent
into the data plane.

Status: **scaffolding**. The gRPC contract (`weft-network-proto`) is
complete ; this repo provides the server skeleton and progressively wires
each RPC to its backing store + reconciler.

## Architecture

- One process per DC, started as an infra microVM by `weft infra
  bootstrap`. Three replicas in production ; etcd elects a leader for
  the reconcile loop, followers serve read-only snapshots and forward
  writes.
- Source of truth lives in etcd under `/weft/network/*` :
    - `/weft/network/routers/<uuid>`
    - `/weft/network/loadbalancers/<uuid>`
    - `/weft/network/dns-zones/<uuid>`
    - `/weft/network/dns-records/<uuid>`
    - `/weft/network/scheduling-rules/<name>`
- Reconcilers watch their prefix and translate desired-state into :
    - **Routers** → WireGuard peer configs ; VyOS / FRR rules for
      BGP egress.
    - **Load Balancers** → JSON config POSTed to each `weft-agent`'s
      embedded Caddy admin socket (see `weft/agent/proxy/`).
    - **DNS zones / records** → CoreDNS RFC-2136 UPDATE against the
      per-DC CoreDNS microVMs (`weft/infra/coredns/`), or zone-file
      rendering for static deployments.
    - **Scheduling rules** → no direct data-plane action ; the agent's
      `FirstFitScheduler` (`weft/scheduler.go`) reads the rules from
      etcd at startup and on watch events.

## Building

```sh
task build              # produces ./weft-network
task test               # go test ./...
task vet                # go vet ./...
task check              # vet + test
```

Cross-compile (Linux is the production target ; darwin works for dev) :

```sh
GOOS=linux GOARCH=arm64 go build -o weft-network-linux-arm64 ./cmd/weft-network
GOOS=linux GOARCH=amd64 go build -o weft-network-linux-amd64 ./cmd/weft-network
```

## Running locally

```sh
./weft-network --listen unix:///tmp/weft-network.sock
```

Point `weft-webui --weft-network-socket=unix:///tmp/weft-network.sock` at
this and the dashboard's networking panels (Routers, Load Balancers, DNS,
Scheduling Rules) will dial here instead of falling back to their mock
stores.

## Implementation order

Tracked in `cmd/weft-network/server.go` — every RPC returns
`codes.Unimplemented` today. The webui's live-first pattern degrades
gracefully when an RPC returns Unimplemented, so the daemon can light up
one method at a time without breaking the dashboard.

Recommended order :

1. `ListSchedulingRules` / `CreateSchedulingRule` / `DeleteSchedulingRule` —
   smallest blast radius, no reconciler (the rules persist ; the agent
   reads them).
2. `ListDNSZones` / `CreateDNSZone` / `DeleteDNSZone` — simple
   key-value catalogue.
3. `ListDNSRecords` / `CreateDNSRecord` / `DeleteDNSRecord` — wired to
   CoreDNS via RFC-2136 (next milestone after the persistence shape
   settles).
4. Routers + Load Balancers — biggest reconcilers, last.
