# Disposable TEST image only. Context is generated public source before fixtures.
FROM golang:1.26.10-bookworm AS build
WORKDIR /src
COPY source/ ./
COPY store.go /lab/store.go
ENV GOTOOLCHAIN=local
RUN go mod verify && CGO_ENABLED=1 go build -mod=vendor -trimpath -buildvcs=false -o /out/garm ./cmd/garm && CGO_ENABLED=1 go build -mod=vendor -trimpath -buildvcs=false -o /out/garm-migration-lab /lab/store.go
FROM debian:bookworm-slim AS lab
COPY --from=build --chmod=0555 /out/garm /usr/local/bin/garm
COPY --from=build --chmod=0555 /out/garm-migration-lab /usr/local/bin/garm-migration-lab
# No installed provider/config/secrets; caller must supply explicit lab restrictions.
USER 65531:65531
ENTRYPOINT ["/usr/local/bin/garm"]
CMD ["--version"]
