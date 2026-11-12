# Commit response-loss component only: native test-cluster library, never daemon or LXC.
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
 && (cd "$(go env GOMODCACHE)/github.com/cowsql/go-cowsql@v1.22.0" && sha256sum -c /out/driver-source.sha256) \
 && go test -p 2 -race -c -o /native-wireloss.test ./internal/server/db \
 && go vet -p 2 ./internal/server/db \
 && go version > /out/go-version.txt
USER 65532:65532
ENV HOME=/tmp TMPDIR=/tmp
# No network capability is needed; native helper uses a private Unix socket.
RUN --network=none /native-wireloss.test -test.run '^TestQualification(NativeCommitResponseLoss|LossParser)$' -test.count=3 -test.timeout=120s -test.v > /out/test.txt \
 && printf '%s\n' 'native_commit_response_suppression_component=passed' 'native_test_cluster_started=true' 'successful_commit_response_withheld=true' 'independent_unwrapped_commit_observation=true' 'arbitrary_network_failure_qualified=false' 'incus_daemon_executed=false' 'guard_integration_qualified=false' 'physical_release_qualified=false' > /out/result.txt
FROM scratch AS evidence
COPY --from=component /out/ /
