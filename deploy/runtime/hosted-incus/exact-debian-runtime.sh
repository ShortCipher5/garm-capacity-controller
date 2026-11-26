#!/usr/bin/env bash
# Experimental, secretless GitHub-hosted amd64 runner only. Never on a server.
set -euo pipefail
export LC_ALL=C
[[ ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted ]]
[[ ${ImageOS:-} == ubuntu24 && ${RUNNER_ARCH:-} == X64 && $(uname -m) == x86_64 ]]
[[ ${GITHUB_EVENT_NAME:-} == pull_request || ${GITHUB_EVENT_NAME:-} == workflow_dispatch ]]
[[ ${GITHUB_RUN_ID:-} =~ ^[0-9]+$ && ${GITHUB_RUN_ATTEMPT:-} =~ ^[0-9]+$ ]]
[[ $# == 1 && $1 == /* && -x $1 && -f $1 && ! -L $1 ]]
: "${RUNNER_TEMP:?need disposable hosted workspace}"
test_binary=$1
cd "$(dirname "$0")/../../.."
[[ -z ${INCUS_DIR:-} && -z ${INCUS_CONF:-} && -z ${INCUS_REMOTE:-} ]]
if command -v incus >/dev/null || [[ -e /var/lib/incus || -L /var/lib/incus ]]; then
  echo 'Refusing preexisting Incus state or binary' >&2; exit 1
fi
sudo timeout 150 apt-get update -qq
sudo timeout 30 apt-get -s --no-install-recommends install incus > "$RUNNER_TEMP/gcc-exact-incus-apt-plan.txt"
if grep -Eq '^(Remv|Purg) ' "$RUNNER_TEMP/gcc-exact-incus-apt-plan.txt"; then
  echo 'Outer Incus install would remove an existing package' >&2; exit 1
fi
sudo timeout 210 apt-get --no-remove --no-install-recommends install -y incus
sudo timeout 30 systemctl start incus.socket
sudo timeout 35 incus admin waitready --timeout 25
outer() { sudo timeout --kill-after=10s 60s incus "$@"; }
[[ $(outer remote get-default) == local ]]
work=$(mktemp -d "$RUNNER_TEMP/gcc-exact-incus.XXXXXXXX")
suffix=${work##*.}; suffix=${suffix,,}
pool="gcce-dir-$suffix"; outer_guest="gcce-debian-$suffix"; alias="gcce-image-$suffix"
guest_owned=false; pool_owned=false; image_owned=false; alpine_owned=false
alpine_alias="gcce-alpine-$suffix"
cleanup() {
  local rc=$?
  trap - EXIT; set +e
  if [[ $guest_owned == true ]]; then
    outer delete "$outer_guest" --force >/dev/null || rc=1
    outer list --format=json | python3 -c 'import json,sys; sys.exit(any(x["name"]==sys.argv[1] for x in json.load(sys.stdin)))' "$outer_guest" || rc=1
  fi
  if [[ $alpine_owned == true ]]; then outer image delete "$alpine_alias" >/dev/null || rc=1; fi
  if [[ $image_owned == true ]]; then outer image delete "$alias" >/dev/null || rc=1; fi
  if [[ $pool_owned == true ]]; then outer storage delete "$pool" >/dev/null || rc=1; fi
  rm -rf -- "$work" || rc=1
  if [[ $rc == 0 ]]; then echo 'PASS exact Incus disposable resources removed'; fi
  exit "$rc"
}
trap cleanup EXIT; trap 'exit 130' INT; trap 'exit 143' TERM
outer storage create "$pool" dir >/dev/null; pool_owned=true
# Require the fixed public HTTPS simplestreams origin, not an alternate remote.
outer remote list --format=csv | python3 -c '
import csv, sys
rows = [r for r in csv.reader(sys.stdin) if r and r[0] == "images"]
if len(rows) != 1 or rows[0][1].rstrip("/") != "https://images.linuxcontainers.org" or rows[0][2:5] != ["simplestreams", "none", "YES"]:
    raise SystemExit("unexpected image origin")'
fingerprint=$(outer image info images:debian/13 | awk '/^Fingerprint: / {print $2}')
[[ $fingerprint =~ ^[a-f0-9]{64}$ ]]
sudo timeout --kill-after=10s 180s incus image copy "images:$fingerprint" local: --alias "$alias" >/dev/null
image_owned=true
outer init "$fingerprint" "$outer_guest" --storage "$pool" --no-profiles \
  -c security.privileged=false -c security.nesting=true -c limits.cpu=2 -c limits.memory=2GiB >/dev/null
guest_owned=true
[[ $(outer config get "$outer_guest" security.privileged) == false ]]
[[ $(outer config get "$outer_guest" security.nesting) == true ]]
outer query "/1.0/instances/$outer_guest" | python3 -c '
import json, sys
x = json.load(sys.stdin)
devices = list(x["expanded_devices"].values())
if x["type"] != "container" or x["profiles"] or len(devices) != 1:
    raise SystemExit("expected a NIC-less and profile-less Debian container")
d = devices[0]
if (d.get("type"), d.get("path"), d.get("pool")) != ("disk", "/", sys.argv[1]) or d.get("source"):
    raise SystemExit("unexpected Debian root device")' "$pool"
outer start "$outer_guest"
outer file push --uid=0 --gid=0 --mode=0755 "$test_binary" "$outer_guest/root/controller-incus.test"
# Resolve Debian dependencies outside the NIC-less guest using its actual
# package status. The helper authenticates the signed Debian repository.
mkdir -m 0700 "$work/bundle"
outer file pull "$outer_guest/var/lib/dpkg/status" "$work/guest.status"
[[ -s $work/guest.status ]]
timeout --kill-after=15s 450s bash deploy/runtime/hosted-incus/stage-debian-runtime-702.sh "$work/guest.status" "$work/bundle"
shopt -s nullglob
debs=("$work"/bundle/*.deb)
(( ${#debs[@]} > 0 ))
[[ -f $work/bundle/SHA256SUMS && ! -L $work/bundle/SHA256SUMS ]]
for deb in "${debs[@]}"; do [[ -f $deb && ! -L $deb ]]; done

# Resolve the public images: simple streams trust over HTTPS, then use the
# published full fingerprint. Incus split fingerprints hash metadata+rootfs.
# This is a per-run image pin, not a separately signed or perpetual image pin.
alpine_fingerprint=$(outer image info images:alpine/3.22 | awk '/^Fingerprint: / {print $2}')
[[ $alpine_fingerprint =~ ^[a-f0-9]{64}$ ]]
sudo timeout --kill-after=10s 150s incus image copy "images:$alpine_fingerprint" local: --alias "$alpine_alias" >/dev/null
alpine_owned=true
sudo timeout --kill-after=10s 120s incus image export "$alpine_fingerprint" "$work/alpine.meta" >/dev/null
[[ -s $work/alpine.meta && -s $work/alpine.meta.root ]]
[[ $(cat "$work/alpine.meta" "$work/alpine.meta.root" | sha256sum | cut -d' ' -f1) == "$alpine_fingerprint" ]]
printf '%s\n' "$alpine_fingerprint" > "$work/alpine.fingerprint"
outer exec "$outer_guest" -- install -d -m 0700 /root/gcc-offline /root/gcc-offline/debs
outer file push --uid=0 --gid=0 --mode=0600 "${debs[@]}" "$work/bundle/SHA256SUMS" "$outer_guest/root/gcc-offline/debs/"
outer file push --uid=0 --gid=0 --mode=0600 "$work/alpine.meta" "$work/alpine.meta.root" "$work/alpine.fingerprint" "$outer_guest/root/gcc-offline/"
sudo timeout --kill-after=15s 650s incus exec "$outer_guest" \
  --env "GITHUB_ACTIONS=$GITHUB_ACTIONS" --env "RUNNER_ENVIRONMENT=$RUNNER_ENVIRONMENT" \
  -- /bin/bash -se <<'INNER'
set -euo pipefail
[[ ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted ]]
umask 077
export DEBIAN_FRONTEND=noninteractive
. /etc/os-release
[[ $ID == debian && $VERSION_CODENAME == trixie && $(dpkg --print-architecture) == amd64 ]]
# The package's service waits for network-online, but this disposable container
# intentionally has no NIC. Skip only its wait-online helper for this boot.
[[ $(systemd-detect-virt --container) == lxc ]]
links=(/sys/class/net/*)
[[ ${#links[@]} -eq 1 && ${links[0]} == /sys/class/net/lo ]]
wait_unit=/run/systemd/system/systemd-networkd-wait-online.service.d
install -d -m 0755 "$wait_unit"
: >/run/gcc-ci-intentionally-networkless
printf '[Unit]\nConditionPathExists=!/run/gcc-ci-intentionally-networkless\n' \
  >"$wait_unit/90-gcc-offline-fixture.conf"
timeout 10 systemctl daemon-reload
[[ -r /usr/share/keyrings/debian-archive-keyring.gpg ]]
[[ ! -e /var/lib/incus && ! -L /var/lib/incus ]]
command -v openssl >/dev/null || { echo "Debian guest lacks fixture TLS tooling" >&2; exit 1; }
v=7.0.2-3~bpo13+1
bundle=/root/gcc-offline/debs
# Accept only a complete manifest of local regular .debs, with no paths,
# and then verify actual transferred bytes.
shopt -s nullglob
debs=("$bundle"/*.deb)
(( ${#debs[@]} > 0 && ${#debs[@]} <= 128 ))
for deb in "${debs[@]}"; do [[ -f $deb && ! -L $deb ]]; done
(
  cd "$bundle"
  awk '
    NF!=2 || length($1)!=64 || $1 !~ /^[0-9a-f]+$/ ||
    $2 !~ /^[0-9A-Za-z][0-9A-Za-z._+~%:-]*[.]deb$/ || seen[$2]++ {exit 1}
    END {if (!NR) exit 1}
  ' SHA256SUMS
  diff -u <(awk '{print $2}' SHA256SUMS | LC_ALL=C sort) \
    <(printf '%s\n' *.deb | LC_ALL=C sort)
  sha256sum --strict --check SHA256SUMS >/dev/null
)
f=$(cat /root/gcc-offline/alpine.fingerprint)
[[ $f =~ ^[a-f0-9]{64}$ ]]
[[ $(cat /root/gcc-offline/alpine.meta /root/gcc-offline/alpine.meta.root | sha256sum | cut -d' ' -f1) == "$f" ]]
# Incus may auto-start on package install. Deposit a deliberate one-job TLS
# identity before that first start; standard peer verification must accept IP SAN.
install -d -m 0700 /var/lib/incus
openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 1 -subj /CN=localhost \
  -addext subjectAltName=IP:127.0.0.1 -keyout /var/lib/incus/server.key -out /var/lib/incus/server.crt
for name in incus-base incus-client; do
  n=0
  for deb in "${debs[@]}"; do
    if [[ $(dpkg-deb -f "$deb" Package) == "$name" ]]; then
      [[ $(dpkg-deb -f "$deb" Version) == "$v" && $(dpkg-deb -f "$deb" Architecture) == amd64 ]]
      n=$((n + 1))
    fi
  done
  [[ $n == 1 ]]
done
# APT's --no-download also suppresses acquisition of the explicit local
# archives. Allow its local archive methods only, with no repository sources or
# cached indexes. Bundle provenance and exact contents were verified above.
apt_work=$(mktemp -d /var/tmp/gcc-offline-apt.XXXXXXXX)
chmod 0755 "$apt_work"
mkdir -m 0755 "$apt_work/sources" "$apt_work/lists" "$apt_work/methods" "$apt_work/debs"
for method in copy file store; do
  [[ -x /usr/lib/apt/methods/$method ]]
  ln -s "/usr/lib/apt/methods/$method" "$apt_work/methods/$method"
done
install -m 0644 -- "${debs[@]}" "$apt_work/debs/"
(cd "$apt_work/debs" && sha256sum --strict --check "$bundle/SHA256SUMS" >/dev/null)
echo "Installing ${#debs[@]} verified Debian archives with local-only APT methods"
timeout 270 apt-get \
  -o "Dir::Etc::sourcelist=/dev/null" \
  -o "Dir::Etc::sourceparts=$apt_work/sources" \
  -o "Dir::State::lists=$apt_work/lists" \
  -o "Dir::Bin::Methods=$apt_work/methods" \
  -o 'Dir::Cache::pkgcache=' -o 'Dir::Cache::srcpkgcache=' \
  --no-remove --no-install-recommends install -y "$apt_work"/debs/*.deb
rm -rf -- "$apt_work"
echo 'Verified local package installation complete'
[[ $(dpkg-query -W -f='${Version}' incus-base) == "$v" ]]
[[ $(dpkg-query -W -f='${Version}' incus-client) == "$v" ]]
# These diagnostics run only inside the disposable NIC-less Debian guest.
startup_diagnostics() {
  timeout 6 systemctl --no-pager list-jobs || true
  timeout 6 systemctl --no-pager show \
    -p Id -p ActiveState -p SubState -p Result -p Requires -p Wants -p After \
    incus.socket incus.service incus-startup.service lxcfs.service network-online.target \
    systemd-networkd-wait-online.service || true
  timeout 8 journalctl --boot --no-pager -n 60 -o short-monotonic \
    -u incus.socket -u incus.service -u incus-startup.service -u lxcfs.service \
    -u systemd-networkd-wait-online.service || true
}
startup_phase() {
  local phase=$1 rc
  shift
  printf 'Starting fixture phase: %s\n' "$phase"
  if "$@"; then
    printf 'Completed fixture phase: %s\n' "$phase"
  else
    rc=$?
    printf 'Fixture phase failed: %s (exit %d)\n' "$phase" "$rc" >&2
    startup_diagnostics
    return "$rc"
  fi
}
startup_phase incus-socket timeout --kill-after=5s 30s systemctl start incus.socket
startup_phase incus-daemon-ready timeout --kill-after=5s 35s incus admin waitready --timeout 25
# Some Incus CLI commands treat non-terminal stdin as YAML. Never give the
# CLI the remaining guest shell heredoc; no command below requires stdin.
inner() { timeout --kill-after=5s 45s incus "$@" </dev/null; }
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
echo 'Preparing isolated Incus project, storage and image'
inner project create "$GCC_HOSTED_INCUS_AUTH_PROJECT" -c features.images=false >/dev/null
inner storage create gcc-dir dir >/dev/null
inner image import /root/gcc-offline/alpine.meta /root/gcc-offline/alpine.meta.root local: --alias gcc-source >/dev/null
[[ $(inner image info gcc-source | awk '/^Fingerprint: / {print $2}') == "$f" ]]
inner init "$f" "$GCC_HOSTED_INCUS_AUTH_GUEST" --project "$GCC_HOSTED_INCUS_AUTH_PROJECT" --storage gcc-dir --no-profiles \
  -c user.runner-controller-id=11111111-1111-4111-8111-111111111111 \
  -c user.runner-pool-id=22222222-2222-4222-8222-222222222222 >/dev/null
echo 'Starting the nested fixture container'
inner start "$GCC_HOSTED_INCUS_AUTH_GUEST" --project "$GCC_HOSTED_INCUS_AUTH_PROJECT"
inner config trust add-certificate "$GCC_HOSTED_INCUS_AUTH_DIR/allowed.pem" --restricted --projects "$GCC_HOSTED_INCUS_AUTH_PROJECT"
inner config set core.https_address 127.0.0.1:8443
timeout 10 openssl s_client -verify_return_error -verify_ip 127.0.0.1 \
  -CAfile "$GCC_HOSTED_INCUS_AUTH_DIR/daemon.pem" -connect 127.0.0.1:8443 </dev/null >/dev/null 2>&1
inner query "/1.0/instances/$GCC_HOSTED_INCUS_AUTH_GUEST?project=$GCC_HOSTED_INCUS_AUTH_PROJECT" > "$GCC_HOSTED_INCUS_RUNTIME_METADATA"
chmod 0600 "$GCC_HOSTED_INCUS_RUNTIME_METADATA"
echo 'Running the authenticated Incus 7.0.2 runtime reader'
timeout 90 /root/controller-incus.test -test.run '^TestHostedIncusRuntimeHealth702$' -test.v | tee "$RUNNER_TEMP/reader.log"
grep -Eq '^--- PASS: TestHostedIncusRuntimeHealth702 \(' "$RUNNER_TEMP/reader.log"
inner delete "$GCC_HOSTED_INCUS_AUTH_GUEST" --project "$GCC_HOSTED_INCUS_AUTH_PROJECT" --force >/dev/null
INNER
