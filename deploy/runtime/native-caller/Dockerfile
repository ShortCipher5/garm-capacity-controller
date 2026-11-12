# Component test only: native test-cluster library, never Incus daemon or LXC.
FROM docker.io/library/debian@sha256:7378260c0a098d27ac206225db5e7d92b8c7c84ea7afccc86eeb513756bbd392 AS component
ENV GOTOOLCHAIN=local GOMAXPROCS=2 DEBIAN_FRONTEND=noninteractive
RUN . /etc/os-release && test "$ID:$VERSION_ID" = debian:13 \
 && printf '#!/bin/sh\nexit 101\n' > /usr/sbin/policy-rc.d && chmod 755 /usr/sbin/policy-rc.d \
 && apt-get update && apt-get install --no-install-recommends -y ca-certificates gcc g++ pkg-config golang-go libacl1-dev libcap-dev libdbus-1-dev libcowsql-dev lxc-dev libseccomp-dev libselinux1-dev libsqlite3-dev libudev-dev \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir /out && chmod 777 /out \
 && dpkg-query -W -f='${Package}\t${Version}\n' | sort > /out/packages.tsv
WORKDIR /src
COPY source/ /src/
COPY evidence/ /out/
RUN go mod download && go mod verify \
 && go test -p 2 -race -c -o /native-caller.test ./internal/server/instance \
 && go vet -p 2 ./internal/server/instance \
 && go version > /out/go-version.txt
USER 65532:65532
ENV HOME=/tmp TMPDIR=/tmp
# No network capability is needed; native helper uses a private Unix socket.
RUN --network=none /native-caller.test -test.run '^TestQualificationActualCreateInternalDriverFailure$' -test.count=3 -test.timeout=120s -test.v > /out/test.txt \
 && printf '%s\n' 'actual_create_internal_component=passed' 'native_test_cluster_started=true' 'driver_replaced_by_sentinel=true' 'incus_daemon_executed=false' 'guard_integration_qualified=false' 'physical_release_qualified=false' > /out/result.txt
FROM scratch AS evidence
COPY --from=component /out/ /
