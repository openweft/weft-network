# weft-network production image.
#
# Two-stage build : a Go build stage produces a statically-linked
# binary, then we copy it into a scratch base. Image weighs in around
# 16 MB ; no shell, no package manager, no surface area beyond the
# daemon itself.
#
# The build EXPECTS a vendored module tree. weft-network-proto is a real
# published, tagged dependency now (go.mod has no `replace` directive —
# dropped in #2), so `go mod vendor` needs nothing but network access to
# the module proxy; the Dockerfile then builds with `-mod=vendor` so the
# actual docker build step itself needs no network access.
#
# Build args :
#   - VERSION : git describe output, stamped into the binary via
#     -ldflags so `weft-network --version` returns something useful.
#   - COMMIT  : short sha.
#   - DATE    : RFC-3339 UTC build timestamp.
#
# Pre-build + build sequence :
#   go mod vendor
#   docker build \
#     --build-arg VERSION=$(git describe --tags --always --dirty) \
#     --build-arg COMMIT=$(git rev-parse --short HEAD) \
#     --build-arg DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
#     -t ghcr.io/openweft/weft-network:dev .

ARG GO_VERSION=1.26

# ---- build stage --------------------------------------------------
# Pinned to --platform=$BUILDPLATFORM (the runner's own native arch, not the
# target one) so it cross-compiles via GOOS/GOARCH instead of running the Go
# toolchain itself under QEMU emulation for every target platform. This is
# required for linux/loong64: the official golang image publishes no
# linux/loong64 manifest at all, so a build stage tagged for the target
# platform directly could never pull it even with QEMU installed. The
# scratch final stage has no OS content of its own, so it never needs a
# base-image manifest either.
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
COPY vendor/ ./vendor/
COPY cmd/ ./cmd/
COPY internal/ ./internal/

ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
ARG TARGETOS
ARG TARGETARCH

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
      -mod=vendor \
      -trimpath \
      -ldflags "-s -w \
                -X main.version=${VERSION} \
                -X main.commit=${COMMIT} \
                -X main.date=${DATE}" \
      -o /out/weft-network \
      ./cmd/weft-network

# ---- runtime stage ------------------------------------------------
FROM scratch
COPY --from=build /out/weft-network /weft-network

# Default listen : tcp on :7700 inside the container. Override with
# the --listen flag at run time. Unix sockets require a host mount
# to be useful from a container.
EXPOSE 7700 9100
ENTRYPOINT ["/weft-network"]
CMD ["--listen", "tcp::7700", "--metrics-addr", ":9100"]
