# weft-network production image.
#
# Two-stage build : a Go build stage produces a statically-linked
# binary, then we copy it into a scratch base. Image weighs in around
# 16 MB ; no shell, no package manager, no surface area beyond the
# daemon itself.
#
# Build args :
#   - VERSION : git describe output, stamped into the binary via
#     -ldflags so `weft-network --version` returns something useful.
#   - COMMIT  : short sha.
#   - DATE    : RFC-3339 UTC build timestamp.
#
# Pass them at build time :
#   docker build \
#     --build-arg VERSION=$(git describe --tags --always --dirty) \
#     --build-arg COMMIT=$(git rev-parse --short HEAD) \
#     --build-arg DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
#     -t ghcr.io/openweft/weft-network:dev .

ARG GO_VERSION=1.26

# ---- build stage --------------------------------------------------
FROM golang:${GO_VERSION}-alpine AS build

# We pull the sibling weft-network-proto module from GitHub. The
# repo's go.mod has `replace ../weft-network-proto` for local dev ;
# we strip that replace so the docker build resolves the import via
# the module cache instead. ./vendor/ would be an alternative — pick
# this path so the host's go.sum stays authoritative.
WORKDIR /src
COPY go.mod go.sum ./
RUN sed -i '/^replace /d' go.mod && go mod download

COPY . .
# Drop the replace again after copying the rest of the source.
RUN sed -i '/^replace /d' go.mod

ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

RUN CGO_ENABLED=0 GOOS=linux go build \
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
# the --listen flag at run time (or with the WEFT_NETWORK_LISTEN env
# pattern the daemon may grow later). Unix sockets require a host
# mount to be useful from a container.
EXPOSE 7700
ENTRYPOINT ["/weft-network"]
CMD ["--listen", "tcp::7700"]
