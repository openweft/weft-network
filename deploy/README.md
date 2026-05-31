# Deploying weft-network

Two supported patterns. Pick whichever fits the host topology.

## Container (Docker / Podman / Kubernetes)

Build the image — the Dockerfile lives at the repo root :

```sh
docker build \
  --build-arg VERSION=$(git describe --tags --always --dirty) \
  --build-arg COMMIT=$(git rev-parse --short HEAD) \
  --build-arg DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
  -t ghcr.io/openweft/weft-network:dev .
```

Run it :

```sh
docker run --rm -p 7700:7700 \
  -e WEFT_NETWORK_LOG=info \
  ghcr.io/openweft/weft-network:dev
```

Behind the scenes the daemon listens on `tcp::7700`. Override the
listen address by passing flags after the image — for instance to
expose a unix socket from a bind-mounted host directory :

```sh
docker run --rm \
  -v /run/weft-network:/run/weft-network \
  ghcr.io/openweft/weft-network:dev \
    --listen "unix:///run/weft-network/weft-network.sock" \
    --log-level info
```

In Kubernetes (one Deployment, three replicas, etcd-backed) the same
image fronts an etcd Service ; once the etcd backend lands point
`--etcd` at it. Until then memory-mode is fine for a single replica.

## systemd (bare metal / weft infra microVM)

For the production-mode infra microVMs `weft infra bootstrap` brings
up (one weft-network per DC), use the systemd unit at
`deploy/systemd/weft-network.service`.

```sh
sudo install -m 0755 ./weft-network /usr/local/bin/weft-network
sudo install -m 0644 ./deploy/systemd/weft-network.service \
                     /etc/systemd/system/weft-network.service
sudo useradd --system --no-create-home --shell /usr/sbin/nologin weft
sudo systemctl daemon-reload
sudo systemctl enable --now weft-network
```

Tune flags via `/etc/default/weft-network` (sourced as an
EnvironmentFile) without editing the unit :

```
WEFT_NETWORK_LISTEN=unix:///run/weft-network/weft-network.sock
WEFT_NETWORK_ETCD=https://etcd-1.weft.internal:2379,https://etcd-2.weft.internal:2379,https://etcd-3.weft.internal:2379
WEFT_NETWORK_LOG=info
```

The unit ships hardened — `NoNewPrivileges`, `ProtectSystem=strict`,
seccomp `@system-service` filter. Loosen only when you wire a
local-fs backend that needs broader file access.

## Pointing weft-webui at the daemon

Whichever transport you pick, the dashboard discovers the daemon via :

```
weft-webui --weft-network-socket <addr>
```

where `<addr>` is the same shape as `--listen` (`unix:///…` or
`tcp:host:port`). When set, the dashboard's Networking panels
(Routers, Load Balancers, DNS zones / records, Scheduling Rules)
swap their mock fallback for live RPCs against this daemon.

If the daemon is unreachable, the dashboard transparently falls back
to mock state — no hard error, the panels degrade gracefully (live-
first pattern, see `weft-webui/internal/server/api_networking.go`).
