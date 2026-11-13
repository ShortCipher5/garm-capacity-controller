# Disposable compile/test ONLY. Never execute the incusd test binary.
FROM docker.io/library/debian@sha256:7378260c0a098d27ac206225db5e7d92b8c7c84ea7afccc86eeb513756bbd392 AS build
ARG DEBIAN_FRONTEND=noninteractive
ENV GOTOOLCHAIN=local GOMAXPROCS=2
RUN . /etc/os-release && test "$ID" = debian && test "$VERSION_ID" = 13 \
 && printf '#!/bin/sh\nexit 101\n' > /usr/sbin/policy-rc.d && chmod 755 /usr/sbin/policy-rc.d \
 && apt-get update && apt-get install --no-install-recommends -y \
    ca-certificates gcc g++ make pkg-config golang-go \
    libacl1-dev libcap-dev libdbus-1-dev libcowsql-dev lxc-dev \
    libseccomp-dev libselinux1-dev libsqlite3-dev libudev-dev \
 && dpkg-query -W -f='${Package}\t${Version}\n' | sort > /native-package-versions.tsv \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY source/ /src/
COPY evidence/ /evidence/
RUN go mod verify && go mod download && go mod verify \
 && mkdir /out && cp /native-package-versions.tsv /out/ && cp /evidence/* /out/ \
 && go version > /out/go-version.txt \
 && go env GOOS GOARCH GOVERSION CGO_ENABLED > /out/go-build-env.txt \
 && go test -p 2 -c -o /out/incusd-compile-only.test ./cmd/incusd \
 && go test -p 2 -race -count=3 ./qualification/nativefrontier \
 && go vet -p 2 ./qualification/nativefrontier \
 && go test -p 2 -race -count=3 ./internal/server/db/cluster \
 && go vet -p 2 ./internal/server/db/cluster \
 && cd /out && sha256sum incusd-compile-only.test > incusd-compile-only.sha256 \
 && printf '%s\n' 'compile=passed' 'native_db_sqlite_tests=passed' 'qualification_fixtures=passed' 'daemon_executed=false' 'cowsql_runtime_qualified=false' 'deployment_qualified=false' 'physical_release_qualified=false' > result.txt
FROM scratch AS evidence
COPY --from=build /out/ /
