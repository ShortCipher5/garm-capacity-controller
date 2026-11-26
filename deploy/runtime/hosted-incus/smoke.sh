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
project="garm-ci-project-${suffix,,}"
endpoint=https://127.0.0.1:8443
incus() { sudo timeout --kill-after=10s 45s incus "$@"; }
owned_guest=false
owned_pool=false
owned_alias=false
owned_project=false
owned_listener=false
trusted_certs=()
cleanup() {
  local status=$?
  trap - EXIT
  set +e
  if [[ $owned_listener == true ]]; then
    incus config unset core.https_address >/dev/null || status=1
    [[ -z $(ss -Hlnt 'sport = :8443') ]] || status=1
  fi
  for cert in "${trusted_certs[@]}"; do incus config trust remove "$cert" >/dev/null || status=1; done
  if [[ $owned_guest == true ]]; then
    incus delete "$guest" --project "$project" --force >/dev/null || status=1
    incus list --project "$project" --format=json | python3 -c '
import json, sys
if any(row.get("name") == sys.argv[1] for row in json.load(sys.stdin)):
    raise SystemExit("disposable guest still present")' "$guest" || status=1
  fi
  if [[ $owned_project == true ]]; then
    incus project delete "$project" >/dev/null || status=1
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
# Explicit storage and no guest profiles, NICs, host mounts or forwarding.
# Only the CI runner loopback endpoint is opened, briefly, for the client test.
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
incus project create "$project" -c features.images=false >/dev/null
owned_project=true
incus init "$fingerprint" "$guest" --storage "$pool" --project "$project" --no-profiles \
  -c limits.cpu=1 -c limits.memory=256MiB >/dev/null
owned_guest=true
incus query "/1.0/instances/$guest?project=$project" | python3 -c '
import json, sys
instance = json.load(sys.stdin)
devices = instance["expanded_devices"]
expected = {"type": "disk", "path": "/", "pool": sys.argv[1]}
if instance["type"] != "container" or instance["profiles"] or len(devices) != 1:
    raise SystemExit("unexpected guest type, profiles or extra devices")
root = next(iter(devices.values()))
if any(root.get(key) != value for key, value in expected.items()) or root.get("source"):
    raise SystemExit("expected only a pooled root disk")' "$pool"
incus start "$guest" --project "$project"
incus exec "$guest" --project "$project" -- /bin/sh -ec 'test -r /proc/1/status'
incus query "/1.0/instances/$guest/state?project=$project" > "$work/before.json"
python3 deploy/runtime/hosted-incus/check-state.py "$work/before.json"

# One-use TLS keys in a private workspace. A native restricted TLS certificate
# is project-scoped, not intrinsically read-only; only GET client methods are
# invoked here. The Unix administrator is used solely to prepare/clean fixtures.
[[ -z $(incus config get core.https_address) && -z $(ss -Hlnt 'sport = :8443') ]]
chmod 700 "$work"
sudo timeout 20 install -m 0644 /var/lib/incus/server.crt "$work/daemon.pem"
for role in allowed wrong-project unknown; do
  (umask 077; openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 \
    -noenc -days 1 -subj "/CN=garm-hosted-$role-$suffix" -addext 'extendedKeyUsage=clientAuth' \
    -keyout "$work/$role.key" -out "$work/$role.pem" >/dev/null 2>&1)
done
trust_client() {
  local role=$1 target=$2 fingerprint
  fingerprint=$(openssl x509 -in "$work/$role.pem" -outform DER | sha256sum | awk '{print $1}')
  [[ $fingerprint =~ ^[a-f0-9]{64}$ ]]
  incus config trust add-certificate "$work/$role.pem" --name "garm-hosted-$role-$suffix" \
    --restricted --projects "$target" >/dev/null
  trusted_certs+=("$fingerprint")
}
trust_client allowed "$project"
trust_client wrong-project default
incus config set core.https_address 127.0.0.1:8443
owned_listener=true
[[ $(ss -Hlnt 'sport = :8443' | awk '{print $4}') == 127.0.0.1:8443 ]]
GCC_HOSTED_INCUS_AUTH_DIR="$work" GCC_HOSTED_INCUS_AUTH_ENDPOINT="$endpoint" \
  GCC_HOSTED_INCUS_AUTH_PROJECT="$project" GCC_HOSTED_INCUS_AUTH_GUEST="$guest" \
  go test -count=1 -timeout=75s ./deploy/runtime/hosted-incus -run '^TestHostedIncusAuthenticatedInventory$' -v | tee "$work/auth-result.log"
grep -Eq '^--- PASS: TestHostedIncusAuthenticatedInventory \(' "$work/auth-result.log"
incus config unset core.https_address
[[ -z $(ss -Hlnt 'sport = :8443') ]]
owned_listener=false
for cert in "${trusted_certs[@]}"; do incus config trust remove "$cert"; done
trusted_certs=()

python3 deploy/runtime/hosted-incus/capture-job-counters.py "$guest" "$project" "$work/job-counters.json"
GCC_HOSTED_INCUS_JOB_COUNTERS="$work/job-counters.json" \
  go test -count=1 -timeout=75s ./internal/cihealth -run '^TestHostedIncusExactJobCounters$' -v | tee "$work/counters-result.log"
grep -Eq '^--- PASS: TestHostedIncusExactJobCounters \(' "$work/counters-result.log"
# Existing packaging harness launches pinned, unmodified stock GARM and the
# gateway together with no external credentials. Run it with the guest up.
timeout --kill-after=20s 1200s deploy/runtime/test.sh
incus query "/1.0/instances/$guest/state?project=$project" > "$work/after.json"
python3 deploy/runtime/hosted-incus/check-state.py "$work/before.json" "$work/after.json"
echo 'PASS isolated stock GARM/gateway specimen and one real Incus guest coexist'
