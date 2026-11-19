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
RUN CGO_ENABLED=1 go build -trimpath -buildvcs=false -o /out/capacity-native-bundle ./cmd/capacity-native-bundle

# Owner and helper artifacts are exported, never executed in the build or by an
# image ENTRYPOINT. CGO stays enabled for the shared canonical SQLite dependency.
# No Incus, LXC, ZFS, nspawn, credentials or runtime image is synthesized here.
FROM go-build AS native-owner-build
RUN mkdir -p /native-out && for name in \
    capacity-native-domain capacity-native-prepare capacity-capsule-launch capacity-native-stage \
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

# Offline bundle builder export; never runs artifacts or contacts a host.
FROM scratch AS native-bundle-tool
COPY --from=go-build /out/capacity-native-bundle /capacity-native-bundle

# Export only this binary to a reviewed Garm image. This is not a runnable image.
FROM scratch AS provider-artifact
COPY --from=go-build /out/capacity-provider /capacity-provider

# Optional source-pinned, stdlib-only archive metadata proof helper. No service,
# host installation or default target depends on it.
FROM go-build AS ancillary-proof-build
COPY --from=ancillary-proof-source /neutral.go /src/ancillary-proof/neutral.go
COPY --from=ancillary-proof-source /neutral_test.go /src/ancillary-proof/neutral_test.go
COPY --from=ancillary-proof-source /final_v28.go /src/ancillary-proof/final_v28.go
COPY --from=ancillary-proof-source /final_v28_test.go /src/ancillary-proof/final_v28_test.go
COPY --from=ancillary-proof-source /main.go /src/ancillary-proof/main.go
COPY --from=ancillary-proof-source /main_test.go /src/ancillary-proof/main_test.go
COPY --from=ancillary-proof-source /LICENSE /src/ancillary-proof/LICENSE
COPY --from=ancillary-proof-source /NOTICE /src/ancillary-proof/NOTICE
RUN CGO_ENABLED=0 go test -p 1 ./ancillary-proof && \
    CGO_ENABLED=0 go build -trimpath -buildvcs=false -o /ancillary-proof ./ancillary-proof
FROM scratch AS ancillary-proof-artifact
COPY --from=ancillary-proof-build --chmod=0555 /ancillary-proof /ancillary-proof

# Optional build-only export for the separately reviewed read-only pilot command.
# No ENTRYPOINT, host installation, qualification run or default workflow uses it.
# Requires the checkout containing cmd/capacity-stock-read-pilot; the normal
# gateway target does not depend on this stage. Runtime/host compatibility is
# deliberately not inferred from the build or checksum manifest.
FROM go-build AS stock-read-pilot-build
RUN test "$(go env GOOS)/$(go env GOARCH)" = linux/amd64 && \
    mkdir -p /stock-read-pilot-out && \
    CGO_ENABLED=1 go build -trimpath -buildvcs=false -o /stock-read-pilot-out/capacity-stock-read-pilot ./cmd/capacity-stock-read-pilot && \
    cd /stock-read-pilot-out && \
    sha256sum capacity-stock-read-pilot > SHA256SUMS && \
    chmod 0555 capacity-stock-read-pilot && chmod 0444 SHA256SUMS
FROM scratch AS stock-read-pilot-artifact
COPY --from=stock-read-pilot-build --chmod=0555 /stock-read-pilot-out/capacity-stock-read-pilot /capacity-stock-read-pilot
COPY --from=stock-read-pilot-build --chmod=0444 /stock-read-pilot-out/SHA256SUMS /SHA256SUMS

FROM debian:bookworm-slim AS runtime-base
USER 65532:65532
WORKDIR /

# No credentials/configuration, server, automatic backup or background process.
FROM runtime-base AS ledger-tool
COPY --from=go-build --chmod=0555 /out/capacity-ledger /usr/local/bin/capacity-ledger
ENTRYPOINT ["/usr/local/bin/capacity-ledger"]
CMD ["--help"]

# Separate offline runtime-asset preparation helper. This does not expand the
# existing native-owner flat bundle or install any system tool/runtime image.
FROM go-build AS native-runtime-assets-build
RUN mkdir -p /runtime-assets-out && \
    CGO_ENABLED=1 go build -trimpath -buildvcs=false -o /runtime-assets-out/capacity-native-assets ./cmd/capacity-native-assets && \
    cd /runtime-assets-out && sha256sum capacity-native-assets > SHA256SUMS && \
    chmod 0555 capacity-native-assets && chmod 0444 SHA256SUMS
FROM scratch AS native-runtime-assets-helper
COPY --from=native-runtime-assets-build /runtime-assets-out/ /


# Explicit disposable-qualification target. This is not the default image and
# supplies no credentials, config, ports, volumes or automatic server command.
FROM go-build AS stock-ingress-build
RUN CGO_ENABLED=1 go build -trimpath -buildvcs=false -o /out/capacity-stock-ingress ./cmd/capacity-stock-ingress
FROM runtime-base AS stock-ingress
USER 65531:65531
WORKDIR /
ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
COPY --from=stock-ingress-build --chmod=0555 /out/capacity-stock-ingress /usr/local/bin/capacity-stock-ingress
ENTRYPOINT []
CMD []


FROM runtime-base AS gateway
COPY --from=go-build --chmod=0555 /out/capacity-gateway /usr/local/bin/capacity-gateway
COPY --from=web-build /src/web/dist /usr/local/share/capacity-admin
ENTRYPOINT ["/usr/local/bin/capacity-gateway"]
CMD ["-config", "/run/capacity-private/gateway.local.json"]
