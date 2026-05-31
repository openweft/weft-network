# weft-network production image.
#
# Two-stage build : a Go build stage produces a statically-linked
# binary, then we copy it into a scratch base. Image weighs in around
# 16 MB ; no shell, no package manager, no surface area beyond the
# daemon itself.
#
# The build EXPECTS a vendored module tree. Run `go mod vendor`
# (pointing at a checked-out sibling weft-network-proto) before
# `docker build` ; the in-repo go.mod has a `replace ../weft-network-proto`
# directive that the vendor step resolves, and the Dockerfile then
# builds with `-mod=vendor` so no network access is needed.
#
# Build args :
#   - VERSION : git describe output, stamped into the binary via
#     -ldflags so `weft-network --version` returns something useful.
#   - COMMIT  : short sha.
#   - DATE    : RFC-3339 UTC build timestamp.
#
# Pre-build + build sequence :
#   git clone https://github.com/openweft/weft-network-proto ../weft-network-proto
#   go mod vendor
#   docker build \
#     --build-arg VERSION=$(git describe --tags --always --dirty) \
#     --build-arg COMMIT=$(git rev-parse --short HEAD) \
#     --build-arg DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
#     -t ghcr.io/openweft/weft-network:dev .

ARG GO_VERSION=1.26

# ---- build stage --------------------------------------------------
FROM golang:${GO_VERSION}-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
COPY vendor/ ./vendor/
COPY cmd/ ./cmd/
COPY internal/ ./internal/

ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

RUN CGO_ENABLED=0 GOOS=linux go build \
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
