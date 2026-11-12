# syntax=docker/dockerfile:1
# Reduced experimental paired artifact, NOT an installed-stack replacement.
# Context is ONLY fetched public source, public probe source and provider binary.
FROM golang:1.26.10-bookworm AS build
WORKDIR /src
COPY source/ ./
COPY probe.go /lab/probe.go
ENV GOTOOLCHAIN=local
RUN go mod verify && CGO_ENABLED=1 go build -mod=vendor -trimpath -buildvcs=false -ldflags='-X github.com/cloudbase/garm/util/appdefaults.Version=v0.2.1+capacity.87e0d61751d4' -o /out/garm ./cmd/garm && CGO_ENABLED=1 go build -mod=vendor -trimpath -buildvcs=false -o /out/pair-probe /lab/probe.go
FROM scratch AS pair-probe-artifact
COPY --from=build /out/pair-probe /pair-probe
FROM debian:bookworm-slim AS paired-garm
COPY --from=build --chmod=0555 /out/garm /usr/local/bin/garm
COPY --chmod=0555 capacity-provider /usr/local/bin/capacity-provider
USER 65531:65531
WORKDIR /
ENTRYPOINT ["/usr/local/bin/garm"]
CMD ["--version"]
