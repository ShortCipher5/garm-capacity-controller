#!/usr/bin/env bash
# Host-local disposable smoke. Refuse self-hosted runners and preexisting daemons.
set -euo pipefail
[[ ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted ]]
[[ ${ImageOS:-} == ubuntu24 && ${RUNNER_ARCH:-} == X64 ]]
[[ ${GITHUB_EVENT_NAME:-} == pull_request || ${GITHUB_EVENT_NAME:-} == workflow_dispatch ]]
[[ ${GITHUB_RUN_ID:-} =~ ^[0-9]+$ && ${GITHUB_RUN_ATTEMPT:-} =~ ^[0-9]+$ ]]
[[ -z ${DOCKER_HOST:-} && -z ${DOCKER_CONTEXT:-} ]]
: "${RUNNER_TEMP:?need disposable runner temporary directory}"
cd "$(dirname "$0")/../../.."

# Do not repurpose an existing Incus or LXD daemon or remove Docker. The
# package-manager simulation below also rejects removals of any package.
if command -v incus >/dev/null || [[ -e /var/lib/incus ]]; then
  echo 'Refusing to take over a preexisting Incus installation' >&2
  exit 1
fi
docker info --format '{{.ServerVersion}}' >/dev/null
[[ $(docker context show) == default ]]
[[ $(docker context inspect default --format '{{.Endpoints.docker.Host}}') == unix:///var/run/docker.sock ]]
sudo timeout 150 apt-get update -qq
# The Ubuntu archive is signature-verified by apt. Record the installed
# security-patched Ubuntu package version instead of pinning a superseded .deb.
sudo timeout 30 apt-get -s --no-install-recommends install incus > "$RUNNER_TEMP/incus-apt-plan.txt"
if grep -Eq '^Remv |^Purg ' "$RUNNER_TEMP/incus-apt-plan.txt"; then
  echo 'Incus install would remove an existing package; coexistence not proven' >&2
  exit 1
fi
sudo timeout 180 apt-get --no-remove --no-install-recommends install -y incus
dpkg-query -W -f='${Package} ${Version}\n' incus
docker info --format '{{.ServerVersion}}' >/dev/null

work=$(mktemp -d "$RUNNER_TEMP/garm-hosted-incus.XXXXXXXX")
suffix=${work##*.}
guest="garm-ci-${suffix,,}"
pool="garm-ci-dir-${suffix,,}"
alias="garm-ci-image-${suffix,,}"
incus() { sudo timeout --kill-after=10s 45s incus "$@"; }
owned_guest=false
owned_pool=false
owned_alias=false
cleanup() {
  local status=$?
  trap - EXIT
  set +e
  if [[ $owned_guest == true ]]; then
    incus delete "$guest" --force >/dev/null || status=1
    incus list --format=json | python3 -c '
import json, sys
if any(row.get("name") == sys.argv[1] for row in json.load(sys.stdin)):
    raise SystemExit("disposable guest still present")' "$guest" || status=1
  fi
  if [[ $owned_alias == true ]]; then
    incus image delete "$alias" >/dev/null || status=1
  fi
  if [[ $owned_pool == true ]]; then
    incus storage delete "$pool" >/dev/null || status=1
  fi
  rm -rf -- "$work" || status=1
  if [[ $status == 0 ]]; then echo 'PASS disposable Incus guest and storage removed'; fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

sudo timeout 30 systemctl start incus.socket
sudo timeout 30 incus admin waitready --timeout 20
# Explicit storage and no guest profiles or NICs. No daemon HTTPS listener,
# host mounts inside the guest, or forwarding/firewall modifications.
incus storage create "$pool" dir >/dev/null
owned_pool=true
incus query "/1.0/storage-pools/$pool" | python3 -c '
import json, sys
if json.load(sys.stdin).get("driver") != "dir":
    raise SystemExit("expected disposable directory storage")'
# Resolve a public image once, copy by immutable fingerprint, and use only
# that immutable fingerprint to start the guest.
fingerprint=$(incus image info images:alpine/3.22 | awk '/^Fingerprint: / { print $2 }')
[[ $fingerprint =~ ^[0-9a-f]{64}$ ]]
sudo timeout --kill-after=10s 150s incus image copy "images:$fingerprint" local: --alias "$alias" >/dev/null
owned_alias=true
incus init "$fingerprint" "$guest" --storage "$pool" --no-profiles \
  -c limits.cpu=1 -c limits.memory=256MiB >/dev/null
owned_guest=true
incus query "/1.0/instances/$guest" | python3 -c '
import json, sys
instance = json.load(sys.stdin)
devices = instance["expanded_devices"]
expected = {"type": "disk", "path": "/", "pool": sys.argv[1]}
if instance["type"] != "container" or instance["profiles"] or len(devices) != 1:
    raise SystemExit("unexpected guest type, profiles or extra devices")
root = next(iter(devices.values()))
if any(root.get(key) != value for key, value in expected.items()) or root.get("source"):
    raise SystemExit("expected only a pooled root disk")' "$pool"
incus start "$guest"
incus exec "$guest" -- /bin/sh -ec 'test -r /proc/1/status'
incus query "/1.0/instances/$guest/state" > "$work/before.json"
python3 deploy/runtime/hosted-incus/check-state.py "$work/before.json"
# Existing packaging harness launches pinned, unmodified stock GARM and the
# gateway together with no external credentials. Run it with the guest up.
timeout --kill-after=20s 1200s deploy/runtime/test.sh
incus query "/1.0/instances/$guest/state" > "$work/after.json"
python3 deploy/runtime/hosted-incus/check-state.py "$work/before.json" "$work/after.json"
echo 'PASS isolated stock GARM/gateway specimen and one real Incus guest coexist'
