#!/usr/bin/env bash
# Stage APT-verified Debian archives for the disposable, offline Incus CI guest.
set -euo pipefail
umask 077
[[ ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted ]]
[[ ${ImageOS:-} == ubuntu24 && ${RUNNER_ARCH:-} == X64 ]]
die() { printf 'Incus CI package staging: %s\n' "$*" >&2; exit 1; }
[[ $# -eq 2 ]] || die 'usage: stage-debian-runtime-702.sh <guest-dpkg-status> <empty-output-dir>'
[[ "$(uname -m)" == x86_64 ]] || die 'requires a Linux amd64 runner'
[[ -f "$1" && ! -L "$1" && -s "$1" && -d "$2" && ! -L "$2" ]] ||
  die 'requires a regular, nonempty status file and an existing destination'
status="$(realpath -e -- "$1")"
out="$(realpath -e -- "$2")"
[[ "$(stat -c %s -- "$status")" -le 4194304 ]] || die 'unexpectedly large dpkg status'
[[ -z "$(find "$out" -mindepth 1 -maxdepth 1 -print -quit)" ]] ||
  die 'destination must be empty'
[[ "$status" != *','* && "$out" != *','* ]] || die 'invalid path'
ca=/etc/ssl/certs/ca-certificates.crt
[[ -s "$ca" ]] || die 'runner public CA bundle is missing'

# Docker Official Image, platform-specific manifest, verified in:
# https://raw.githubusercontent.com/docker-library/repo-info/master/repos/debian/remote/trixie-slim.md
resolver='docker.io/library/debian@sha256:ab3b3c8c5159eebfca4376430132d53c4d425f869c765ecbe47177a030da575d'
[[ -z "${DOCKER_HOST:-}" && -z "${DOCKER_CONTEXT:-}" &&
   -z "${DOCKER_TLS_VERIFY:-}" && -S /var/run/docker.sock ]] ||
  die 'expected the default local Docker socket, without a remote override'
[[ "$(docker context show)" == default ]] || die 'expected the default Docker context'
local_work="$(mktemp -d)"
trap 'rm -rf -- "$local_work"' EXIT
install -m 0644 -- "$status" "$local_work/status"
stage="$local_work/stage"
# The disposable resolver writes here; the host temp parent is private (0700).
install -d -m 0777 -- "$stage"
timeout --kill-after=10s 380s docker --host unix:///var/run/docker.sock run \
  --rm --platform linux/amd64 --interactive \
  --read-only --cap-drop=ALL --cap-add=CHOWN --cap-add=SETGID --cap-add=SETUID \
  --security-opt=no-new-privileges --pids-limit=128 \
  --tmpfs /tmp:rw,nosuid,nodev,size=768m \
  --mount "type=bind,src=$local_work/status,dst=/guest.status,readonly" \
  --mount "type=bind,src=$ca,dst=/runner-ca.crt,readonly" \
  --mount "type=bind,src=$stage,dst=/stage" \
  "$resolver" /bin/bash -s <<'RESOLVER'
set -euo pipefail
export LC_ALL=C
version='7.0.2-3~bpo13+1'
snapshot='20261104T143429Z'
limit_bytes=268435456
limit_packages=128
[[ "$(dpkg --print-architecture)" == amd64 ]]
. /etc/os-release
[[ "$ID" == debian && "$VERSION_CODENAME" == trixie ]]
# APT is isolated from the resolver image's state and any default Docker cleanup hooks.
# Copy only the status file so the _apt sandbox can read it regardless of host mode.
work="$(mktemp -d /tmp/apt-stage.XXXXXXXX)"
chmod 755 "$work"
install -m 0644 /guest.status "$work/status"
mkdir -p "$work/lists/partial" "$work/archives/partial"
chown _apt:root "$work/lists/partial" "$work/archives/partial"
keyring=/usr/share/keyrings/debian-archive-keyring.gpg
test -s "$keyring"
# The timestamp is fixed intentionally. Release signature and package checksum
# verification remain mandatory; only the dates of these historic archives are exempt.
cat >"$work/sources.list" <<EOF
deb [arch=amd64 signed-by=$keyring check-valid-until=no] https://snapshot.debian.org/archive/debian/$snapshot/ trixie main
deb [arch=amd64 signed-by=$keyring check-valid-until=no] https://snapshot.debian.org/archive/debian/$snapshot/ trixie-updates main
deb [arch=amd64 signed-by=$keyring check-valid-until=no] https://snapshot.debian.org/archive/debian-security/$snapshot/ trixie-security main
deb [arch=amd64 signed-by=$keyring check-valid-until=no] https://snapshot.debian.org/archive/debian/$snapshot/ trixie-backports main
EOF
cat >"$work/apt.conf" <<EOF
Dir::Etc::parts "-";
Dir::Etc::main "-";
Dir::Etc::sourcelist "$work/sources.list";
Dir::Etc::sourceparts "-";
Dir::Etc::preferences "/dev/null";
Dir::Etc::preferencesparts "-";
Dir::State::status "$work/status";
Dir::State::lists "$work/lists";
Dir::State::extended_states "$work/extended_states";
Dir::Cache::archives "$work/archives";
Dir::Cache::pkgcache "";
Dir::Cache::srcpkgcache "";
Acquire::Languages "none";
Acquire::Retries "2";
Acquire::https::Timeout "25";
Acquire::https::CAInfo "/runner-ca.crt";
Acquire::https::Verify-Peer "true";
Acquire::https::Verify-Host "true";
Acquire::AllowInsecureRepositories "false";
Acquire::AllowDowngradeToInsecureRepositories "false";
APT::Get::AllowUnauthenticated "false";
EOF
export APT_CONFIG="$work/apt.conf"
apt-get -q -o APT::Update::Error-Mode=any update
request=(install --yes --no-install-recommends --no-remove --download-only
  "incus-base=$version" "incus-client=$version" openssl ca-certificates)
# The same solver and options first give us the expected complete filename set
# and archive sizes, so a short download, duplicate, or over-budget plan fails.
apt-get --print-uris "${request[@]}" >"$work/plan"
awk -v max_files="$limit_packages" -v max_bytes="$limit_bytes" '
  substr($1,1,1) == sprintf("%c",39) {
    if (NF != 4 || $2 !~ /^[a-zA-Z0-9][a-zA-Z0-9.+_~%:-]*[.]deb$/ ||
        $3 !~ /^[0-9]+$/ || $3 == 0 || seen[$2]++) exit 1
    ++count; bytes += $3; print $2
  }
  END { if (!count || count > max_files || bytes > max_bytes) exit 1 }
' "$work/plan" | sort >"$work/expected"
apt-get -q "${request[@]}" >"$work/download.log" ||
  { printf 'APT could not download the authenticated dependency plan\n' >&2; exit 1; }
cd "$work/archives"
shopt -s nullglob
debs=(*.deb)
printf '%s\n' "${debs[@]}" | sort >"$work/actual"
cmp "$work/expected" "$work/actual"
total=0
base=0
client=0
for deb in "${debs[@]}"; do
  [[ -f "$deb" && ! -L "$deb" ]]
  ((total += $(stat -c %s -- "$deb")))
  case "$(dpkg-deb -f "$deb" Architecture)" in amd64|all) ;; *) exit 1;; esac
  case "$(dpkg-deb -f "$deb" Package)" in
    incus-base) [[ "$(dpkg-deb -f "$deb" Version)" == "$version" ]]; base=1;;
    incus-client) [[ "$(dpkg-deb -f "$deb" Version)" == "$version" ]]; client=1;;
  esac
done
[[ "$base" == 1 && "$client" == 1 && "$total" -le "$limit_bytes" ]]
install -m 0644 -- "${debs[@]}" /stage/
sha256sum -- "${debs[@]}" >/stage/SHA256SUMS
chmod 644 /stage/SHA256SUMS
printf 'Staged %s APT-verified Debian archives (%s bytes)\n' "${#debs[@]}" "$total"
RESOLVER

(
  cd "$stage"
  sha256sum --check --strict --status SHA256SUMS
)
cp -- "$stage"/*.deb "$out"/
cp -- "$stage"/SHA256SUMS "$out"/
