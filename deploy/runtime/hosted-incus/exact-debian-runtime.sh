#!/usr/bin/env bash
# Experimental, secretless GitHub-hosted amd64 runner only. Never on a server.
set -euo pipefail
[[ ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted ]]
[[ ${ImageOS:-} == ubuntu24 && ${RUNNER_ARCH:-} == X64 && $(uname -m) == x86_64 ]]
[[ ${GITHUB_EVENT_NAME:-} == pull_request || ${GITHUB_EVENT_NAME:-} == workflow_dispatch ]]
[[ ${GITHUB_RUN_ID:-} =~ ^[0-9]+$ && ${GITHUB_RUN_ATTEMPT:-} =~ ^[0-9]+$ ]]
[[ $# == 1 && $1 == /* && -x $1 && -f $1 && ! -L $1 ]]
: "${RUNNER_TEMP:?need disposable hosted workspace}"
test_binary=$1
[[ -z ${INCUS_DIR:-} && -z ${INCUS_CONF:-} && -z ${INCUS_REMOTE:-} ]]
if command -v incus >/dev/null || [[ -e /var/lib/incus || -L /var/lib/incus ]]; then
  echo 'Refusing preexisting Incus state or binary' >&2; exit 1
fi
sudo timeout 150 apt-get update -qq
sudo timeout 30 apt-get -s --no-install-recommends install incus dnsmasq-base > "$RUNNER_TEMP/gcc-exact-incus-apt-plan.txt"
if grep -Eq '^(Remv|Purg) ' "$RUNNER_TEMP/gcc-exact-incus-apt-plan.txt"; then
  echo 'Outer Incus install would remove an existing package' >&2; exit 1
fi
sudo timeout 210 apt-get --no-remove --no-install-recommends install -y incus dnsmasq-base
sudo timeout 30 systemctl start incus.socket
sudo timeout 35 incus admin waitready --timeout 25
outer() { sudo timeout --kill-after=10s 60s incus "$@"; }
[[ $(outer remote get-default) == local ]]
work=$(mktemp -d "$RUNNER_TEMP/gcc-exact-incus.XXXXXXXX")
suffix=${work##*.}; suffix=${suffix,,}
pool="gcce-dir-$suffix"; ip_bridge="gcce-$suffix"; outer_guest="gcce-debian-$suffix"; alias="gcce-image-$suffix"
guest_owned=false; network_owned=false; pool_owned=false; image_owned=false
cleanup() {
  local rc=$?
  trap - EXIT; set +e
  if [[ $guest_owned == true ]]; then
    outer delete "$outer_guest" --force >/dev/null || rc=1
    outer list --format=json | python3 -c 'import json,sys; sys.exit(any(x["name"]==sys.argv[1] for x in json.load(sys.stdin)))' "$outer_guest" || rc=1
  fi
  if [[ $network_owned == true ]]; then outer network delete "$ip_bridge" >/dev/null || rc=1; fi
  if [[ $image_owned == true ]]; then outer image delete "$alias" >/dev/null || rc=1; fi
  if [[ $pool_owned == true ]]; then outer storage delete "$pool" >/dev/null || rc=1; fi
  rm -rf -- "$work" || rc=1
  if [[ $rc == 0 ]]; then echo 'PASS exact Incus disposable resources removed'; fi
  exit "$rc"
}
trap cleanup EXIT; trap 'exit 130' INT; trap 'exit 143' TERM
outer storage create "$pool" dir >/dev/null; pool_owned=true
# The guest uses this dedicated NAT bridge for package/image retrieval. No host port
# publishes, proxy devices, raw LXC settings, or host firewall overrides.
outer network create "$ip_bridge" ipv4.address=auto ipv4.nat=true ipv6.address=none >/dev/null
network_owned=true
fingerprint=$(outer image info images:debian/13 | awk '/^Fingerprint: / {print $2}')
[[ $fingerprint =~ ^[a-f0-9]{64}$ ]]
sudo timeout --kill-after=10s 180s incus image copy "images:$fingerprint" local: --alias "$alias" >/dev/null
image_owned=true
outer init "$fingerprint" "$outer_guest" --storage "$pool" --no-profiles --network "$ip_bridge" \
  -c security.privileged=false -c security.nesting=true -c limits.cpu=2 -c limits.memory=2GiB >/dev/null
guest_owned=true
[[ $(outer config get "$outer_guest" security.privileged) == false ]]
[[ $(outer config get "$outer_guest" security.nesting) == true ]]
outer start "$outer_guest"
outer file push --uid=0 --gid=0 --mode=0755 "$test_binary" "$outer_guest/root/controller-incus.test"
sudo timeout --kill-after=15s 1050s incus exec "$outer_guest" -- /bin/bash -se <<'INNER'
set -euo pipefail
umask 077
export DEBIAN_FRONTEND=noninteractive
. /etc/os-release
[[ $ID == debian && $VERSION_CODENAME == trixie && $(dpkg --print-architecture) == amd64 ]]
[[ -r /usr/share/keyrings/debian-archive-keyring.gpg ]]
[[ ! -e /var/lib/incus && ! -L /var/lib/incus ]]
v=7.0.2-3~bpo13+1
timeout 150 apt-get update -qq
timeout 100 apt-get --no-remove --no-install-recommends install -y openssl ca-certificates
cat >/etc/apt/sources.list.d/gcc-exact-incus.sources <<'SOURCES'
Types: deb
URIs: https://snapshot.debian.org/archive/debian/20261104T143429Z/
Suites: trixie-backports
Components: main
# This pinned historical index is expired; APT still verifies Debian signatures.
Check-Valid-Until: no
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg
SOURCES
timeout 150 apt-get update -qq
# Incus may auto-start on package install. Deposit a deliberate one-job TLS
# identity before that first start; standard peer verification must accept IP SAN.
install -d -m 0700 /var/lib/incus
openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 1 -subj /CN=localhost \
  -addext subjectAltName=IP:127.0.0.1 -keyout /var/lib/incus/server.key -out /var/lib/incus/server.crt
timeout 270 apt-get --no-remove --no-install-recommends install -y "incus-base=$v" "incus-client=$v"
[[ $(dpkg-query -W -f='${Version}' incus-base) == "$v" ]]
[[ $(dpkg-query -W -f='${Version}' incus-client) == "$v" ]]
timeout 30 systemctl start incus.socket
timeout 35 incus admin waitready --timeout 25
inner() { timeout --kill-after=5s 45s incus "$@"; }
[[ -z ${INCUS_REMOTE:-} && $(inner remote get-default) == local ]]
[[ $(inner version | awk '$1=="Server" && $2=="version:" {print $3}') == 7.0.2 ]]
export RUNNER_TEMP=/root/gcc-runtime
install -d -m 0700 "$RUNNER_TEMP"
export GCC_HOSTED_INCUS_AUTH_DIR="$RUNNER_TEMP/auth"
install -d -m 0700 "$GCC_HOSTED_INCUS_AUTH_DIR"
install -m 0600 /var/lib/incus/server.crt "$GCC_HOSTED_INCUS_AUTH_DIR/daemon.pem"
openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 1 -subj /CN=gcc-disposable-reader \
  -keyout "$GCC_HOSTED_INCUS_AUTH_DIR/allowed.key" -out "$GCC_HOSTED_INCUS_AUTH_DIR/allowed.pem"
export GCC_HOSTED_INCUS_AUTH_PROJECT=garm-ci-project-runtime
export GCC_HOSTED_INCUS_AUTH_GUEST=garm-ci-runtime
export GCC_HOSTED_INCUS_AUTH_ENDPOINT=https://127.0.0.1:8443
export GCC_HOSTED_INCUS_RUNTIME_METADATA=$GCC_HOSTED_INCUS_AUTH_DIR/runtime-instance.json
inner project create "$GCC_HOSTED_INCUS_AUTH_PROJECT" -c features.images=false >/dev/null
inner storage create gcc-dir dir >/dev/null
f=$(inner image info images:alpine/3.22 | awk '/^Fingerprint: / {print $2}')
[[ $f =~ ^[a-f0-9]{64}$ ]]
timeout 150 incus image copy "images:$f" local: --alias gcc-source >/dev/null
inner init "$f" "$GCC_HOSTED_INCUS_AUTH_GUEST" --project "$GCC_HOSTED_INCUS_AUTH_PROJECT" --storage gcc-dir --no-profiles \
  -c user.runner-controller-id=11111111-1111-4111-8111-111111111111 \
  -c user.runner-pool-id=22222222-2222-4222-8222-222222222222 >/dev/null
inner start "$GCC_HOSTED_INCUS_AUTH_GUEST" --project "$GCC_HOSTED_INCUS_AUTH_PROJECT"
inner config trust add-certificate "$GCC_HOSTED_INCUS_AUTH_DIR/allowed.pem" --restricted --projects "$GCC_HOSTED_INCUS_AUTH_PROJECT"
inner config set core.https_address 127.0.0.1:8443
timeout 10 openssl s_client -verify_return_error -verify_ip 127.0.0.1 \
  -CAfile "$GCC_HOSTED_INCUS_AUTH_DIR/daemon.pem" -connect 127.0.0.1:8443 </dev/null >/dev/null 2>&1
inner query "/1.0/instances/$GCC_HOSTED_INCUS_AUTH_GUEST?project=$GCC_HOSTED_INCUS_AUTH_PROJECT" > "$GCC_HOSTED_INCUS_RUNTIME_METADATA"
chmod 0600 "$GCC_HOSTED_INCUS_RUNTIME_METADATA"
timeout 90 /root/controller-incus.test -test.run '^TestHostedIncusRuntimeHealth702$' -test.v | tee "$RUNNER_TEMP/reader.log"
grep -Eq '^--- PASS: TestHostedIncusRuntimeHealth702 \(' "$RUNNER_TEMP/reader.log"
inner delete "$GCC_HOSTED_INCUS_AUTH_GUEST" --project "$GCC_HOSTED_INCUS_AUTH_PROJECT" --force >/dev/null
INNER
