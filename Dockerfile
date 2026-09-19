# konstellationd container image — for tests/e2e (interchaintest) and local
# multi-node runs. NOT the release artifact: release binaries come from CI,
# reproducibly built, with published checksums (ENGINEERING.md §2.6), and
# validators run them under cosmovisor (infra/).
#
#   docker build -t konstellation:e2e .

FROM golang:1.26-bookworm AS builder
# The official image sets GOTOOLCHAIN=local. `auto` lets go.mod's `toolchain`
# line win, so a Go bump there does not break this build while `make build`
# keeps working (PR #11 review).
ENV GOTOOLCHAIN=auto
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
# CGO on (secp256k1, pebble); same flags as a developer's `make build`.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    make build

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl jq \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd -g 1025 konstellation \
    && useradd -m -u 1025 -g konstellation konstellation
COPY --from=builder /src/build/konstellationd /usr/local/bin/konstellationd
# p2p, rpc, api, grpc, evm json-rpc, evm ws, metrics
EXPOSE 26656 26657 1317 9090 8545 8546 26660
USER konstellation
WORKDIR /home/konstellation
ENTRYPOINT ["konstellationd"]
