# syntax=docker/dockerfile:1
# Pin reviewed image digests in a private deployment lock before deployment.
FROM node:22.23.4-bookworm-slim AS web-build
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --ignore-scripts
COPY web/index.html ./
COPY web/src ./src
RUN npm test && npm run build

FROM golang:1.26.10-bookworm AS go-build
WORKDIR /src
ENV GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd ./cmd
COPY internal ./internal
# The gateway's actual SQLite implementation requires CGO. Never disable it.
RUN CGO_ENABLED=1 go build -trimpath -buildvcs=false -o /out/capacity-gateway ./cmd/capacity-gateway
# Provider currently imports SQLite-backed packages transitively. It also needs
# CGO; install only into a separately qualified, libc-compatible Garm image.
RUN CGO_ENABLED=1 go build -trimpath -buildvcs=false -o /out/capacity-provider ./cmd/capacity-provider
# Local recovery tooling is packaged separately, never started with the gateway.
RUN CGO_ENABLED=1 go build -trimpath -buildvcs=false -o /out/capacity-ledger ./cmd/capacity-ledger

# Owner and helper artifacts are exported, never executed in the build or by an
# image ENTRYPOINT. CGO stays enabled for the shared canonical SQLite dependency.
# No Incus, LXC, ZFS, nspawn, credentials or runtime image is synthesized here.
FROM go-build AS native-owner-build
RUN mkdir -p /native-out && for name in \
    capacity-native-domain capacity-capsule-launch capacity-native-stage \
    capacity-native-image capacity-native-zfs-exec capacity-native-egress \
    capacity-native-preflight capacity-native-memory-plan \
    capacity-quota-read capacity-protected-boot capacity-runner-init \
    capacity-stock-ingress capacity-managed-garm capacity-managed-writer; do \
      CGO_ENABLED=1 go build -trimpath -buildvcs=false -o /native-out/$name ./cmd/$name || exit 1; \
    done && \
    cp /out/capacity-gateway /out/capacity-provider /out/capacity-ledger /native-out/ && \
    cd /native-out && sha256sum capacity-* > SHA256SUMS && chmod 0555 capacity-* && chmod 0444 SHA256SUMS

# Export with --output type=local,dest=... . This stage is deliberately not a
# runnable service image. The startup guard remains compiled into the owner.
FROM scratch AS native-owner-artifacts
COPY --from=native-owner-build /native-out/ /

# Export only this binary to a reviewed Garm image. This is not a runnable image.
FROM scratch AS provider-artifact
COPY --from=go-build /out/capacity-provider /capacity-provider

FROM debian:bookworm-slim AS runtime-base
USER 65532:65532
WORKDIR /

# No credentials/configuration, server, automatic backup or background process.
FROM runtime-base AS ledger-tool
COPY --from=go-build --chmod=0555 /out/capacity-ledger /usr/local/bin/capacity-ledger
ENTRYPOINT ["/usr/local/bin/capacity-ledger"]
CMD ["--help"]

FROM runtime-base AS gateway
COPY --from=go-build --chmod=0555 /out/capacity-gateway /usr/local/bin/capacity-gateway
COPY --from=web-build /src/web/dist /usr/local/share/capacity-admin
ENTRYPOINT ["/usr/local/bin/capacity-gateway"]
CMD ["-config", "/run/capacity-private/gateway.local.json"]
