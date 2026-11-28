#!/usr/bin/env bash
# Native isolated Incus 7.0.2 smoke; run only after the reviewed package installer.
set -euo pipefail
export LC_ALL=C
umask 077
[[ ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted ]]
[[ ${ImageOS:-} == ubuntu24 && ${RUNNER_ARCH:-} == X64 && $(uname -m) == x86_64 ]]
[[ ${GITHUB_EVENT_NAME:-} == pull_request || ${GITHUB_EVENT_NAME:-} == workflow_dispatch ]]
[[ ${GITHUB_RUN_ID:-} =~ ^[0-9]+$ && ${GITHUB_RUN_ATTEMPT:-} =~ ^[0-9]+$ ]]
[[ $# == 1 && $1 == /* && -x $1 && -f $1 && ! -L $1 && $(id -u) != 0 ]]
[[ -z ${INCUS_DIR:-} && -z ${INCUS_CONF:-} && -z ${INCUS_REMOTE:-} ]]
: "${RUNNER_TEMP:?need disposable GitHub runner temporary directory}"
[[ $RUNNER_TEMP == /* && -d $RUNNER_TEMP && ! -L $RUNNER_TEMP ]]
test_binary=$1
cd "$(dirname "$0")"
bash ./install-noble-incus-702.sh
inc() { sudo timeout --kill-after=10s 65s incus "$@" </dev/null; }
[[ $(inc remote get-default) == local ]]
[[ $(inc version | awk '$1=="Server" && $2=="version:" {print $3}') == 7.0.2 ]]
[[ -z $(inc config get core.https_address) ]]
work=$(mktemp -d "$RUNNER_TEMP/gcc-702-auth.XXXXXXXX")
suffix=${work##*.}; suffix=${suffix,,}
export GCC_HOSTED_INCUS_AUTH_DIR=$work
export GCC_HOSTED_INCUS_AUTH_PROJECT="garm-ci-project-$suffix"
export GCC_HOSTED_INCUS_AUTH_GUEST="garm-ci-$suffix"
export GCC_HOSTED_INCUS_AUTH_ENDPOINT=https://127.0.0.1:8443
export GCC_HOSTED_INCUS_RUNTIME_METADATA="$work/runtime-instance.json"
project=$GCC_HOSTED_INCUS_AUTH_PROJECT
guest=$GCC_HOSTED_INCUS_AUTH_GUEST
pool="garm-ci-dir-$suffix"
alias="garm-ci-image-$suffix"
guest_owned=false; project_owned=false; pool_owned=false; image_owned=false
trust_owned=false; listener_owned=false
alternate_project="garm-ci-metrics-alt-$suffix"
alternate_guest="garm-ci-metrics-alt-$suffix"
alternate_project_owned=false; alternate_guest_owned=false
metrics_fingerprints=()
cert_fingerprint=
cleanup() {
  local rc=$? cleanup_failed=0
  trap - EXIT; set +e
  if [[ $listener_owned == true ]]; then
    inc config unset core.https_address || cleanup_failed=1
    [[ -z $(inc config get core.https_address) ]] || cleanup_failed=1
  fi
  local fingerprint
  for fingerprint in "${metrics_fingerprints[@]}"; do inc config trust remove "$fingerprint" || cleanup_failed=1; done
  if [[ $trust_owned == true ]]; then inc config trust remove "$cert_fingerprint" || cleanup_failed=1; fi
  if [[ $alternate_guest_owned == true ]]; then inc delete "$alternate_guest" --project "$alternate_project" --force >/dev/null || cleanup_failed=1; fi
  if [[ $alternate_project_owned == true ]]; then inc project delete "$alternate_project" >/dev/null || cleanup_failed=1; fi
  if [[ $guest_owned == true ]]; then
    inc delete "$guest" --project "$project" --force >/dev/null || cleanup_failed=1
    inc list --project "$project" --format=json |
      python3 -c 'import json,sys; sys.exit(any(x["name"]==sys.argv[1] for x in json.load(sys.stdin)))' "$guest" || cleanup_failed=1
  fi
  if [[ $project_owned == true ]]; then inc project delete "$project" >/dev/null || cleanup_failed=1; fi
  if [[ $image_owned == true ]]; then inc image delete "$alias" >/dev/null || cleanup_failed=1; fi
  if [[ $pool_owned == true ]]; then inc storage delete "$pool" >/dev/null || cleanup_failed=1; fi
  rm -rf -- "$work" || cleanup_failed=1
  if (( cleanup_failed )); then echo 'Native Incus fixture cleanup failed' >&2; fi
  if (( rc != 0 )); then exit "$rc"; fi
  if (( cleanup_failed )); then exit 1; fi
  echo 'PASS exact Incus native reader fixture and resource cleanup'
}
trap cleanup EXIT; trap 'exit 130' INT; trap 'exit 143' TERM

# Assembled by the fresh daemon installer; do not accept unsafe TLS or rotate
# an already running daemon's identity here.
sudo cat /var/lib/incus/server.crt > "$work/daemon.pem"
if ! openssl verify -CAfile "$work/daemon.pem" -verify_ip 127.0.0.1 "$work/daemon.pem" >/dev/null; then
  echo 'Native Incus certificate requires a trusted loopback IP SAN before startup' >&2; exit 1
fi
openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 1 -subj /CN=gcc-disposable-reader \
  -keyout "$work/allowed.key" -out "$work/allowed.pem" 2>/dev/null
cert_fingerprint=$(openssl x509 -in "$work/allowed.pem" -outform DER | sha256sum | cut -d' ' -f1)
[[ $cert_fingerprint =~ ^[a-f0-9]{64}$ ]]

inc remote list --format=csv | python3 -c '
import csv, sys
rows = [r for r in csv.reader(sys.stdin) if r and r[0] == "images"]
if len(rows) != 1 or rows[0][1].rstrip("/") != "https://images.linuxcontainers.org" or rows[0][2:5] != ["simplestreams", "none", "YES"]:
    raise SystemExit("unexpected image origin")'
f=$(inc image info images:alpine/3.22 | awk '/^Fingerprint: / {print $2}')
[[ $f =~ ^[a-f0-9]{64}$ ]]
sudo timeout --kill-after=10s 170s incus image copy "images:$f" local: --alias "$alias" >/dev/null
image_owned=true
[[ $(inc image info "$alias" | awk '/^Fingerprint: / {print $2}') == "$f" ]]
echo "Native Incus public image fingerprint: $f"

inc storage create "$pool" dir >/dev/null
pool_owned=true
inc project create "$project" -c features.images=false >/dev/null
project_owned=true
inc init "$f" "$guest" --project "$project" --storage "$pool" --no-profiles \
  -c security.privileged=false -c limits.cpu=1 -c limits.memory=256MiB \
  -c user.runner-controller-id=11111111-1111-4111-8111-111111111111 \
  -c user.runner-pool-id=22222222-2222-4222-8222-222222222222 >/dev/null
guest_owned=true
inc query "/1.0/instances/$guest?project=$project" | python3 -c '
import json, sys
x = json.load(sys.stdin)
devices = list(x["expanded_devices"].values())
if x["type"] != "container" or x["profiles"] or len(devices) != 1:
    raise SystemExit("expected a NIC-less and profile-less fixture")
d = devices[0]
if (d.get("type"), d.get("path"), d.get("pool")) != ("disk", "/", sys.argv[1]) or d.get("source"):
    raise SystemExit("unexpected fixture root device")
if x["expanded_config"].get("security.privileged") != "false":
    raise SystemExit("fixture is not explicitly unprivileged")' "$pool"
# Incus 7.0.2 reconstructs process startup from whole-second boot time
# and scheduler ticks. Leave a gap after creation so truncation cannot make
# a healthy fixture look like its process predates the container.
sleep 2
if inc start "$guest" --project "$project"; then
  echo 'Native fixture container started'
else
  launch_rc=$?
  inc info --show-log "$guest" --project "$project" 2>&1 | tail -n 120 >&2 || true
  exit "$launch_rc"
fi
inc config trust add-certificate "$work/allowed.pem" --restricted --projects "$project"
trust_owned=true
inc config set core.https_address 127.0.0.1:8443
listener_owned=true
tls_ready=false
for attempt in 1 2 3 4 5; do
  if timeout 3 openssl s_client -verify_return_error -verify_ip 127.0.0.1 \
    -CAfile "$work/daemon.pem" -connect 127.0.0.1:8443 </dev/null >/dev/null 2>&1; then
    tls_ready=true; break
  fi
  if [[ $attempt != 5 ]]; then sleep 1; fi
done
[[ $tls_ready == true ]] || { echo 'Pinned fixture loopback TLS not reachable' >&2; exit 1; }
inc query "/1.0/instances/$guest?project=$project" > "$GCC_HOSTED_INCUS_RUNTIME_METADATA"
[[ $(stat -c '%a' "$GCC_HOSTED_INCUS_RUNTIME_METADATA") == 600 ]]
if timeout 90 "$test_binary" -test.run '^TestHostedIncusRuntimeHealth702$' -test.v | tee "$work/reader.log"; then
  :
else
  test_rc=$?
  # Capture only disposable, loopback fixture timing and counter summaries.
  if inc query "/1.0/instances/$guest/state?project=$project" > "$work/state-at-failure.json"; then
    jq -cr '{status, status_code, pid, started_at, cpu_usage: .cpu.usage,
      memory_current: .memory.usage, memory_peak: .memory.usage_peak}' \
      "$work/state-at-failure.json" >&2 || true
    jq -cr '{created_at}' "$GCC_HOSTED_INCUS_RUNTIME_METADATA" >&2 || true
  fi
  exit "$test_rc"
fi
grep -Eq '^--- PASS: TestHostedIncusRuntimeHealth702 \(' "$work/reader.log"

# Independently qualify Incus's built-in metrics identity on this same
# disposable daemon. It is not the authenticated inventory role.
[[ $(inc config get core.metrics_authentication) != false ]]
inc project create "$alternate_project" -c features.images=false >/dev/null
alternate_project_owned=true
inc init "$f" "$alternate_guest" --project "$alternate_project" --storage "$pool" --no-profiles \
  -c security.privileged=false -c limits.cpu=1 -c limits.memory=256MiB >/dev/null
alternate_guest_owned=true
inc start "$alternate_guest" --project "$alternate_project"
enroll_metrics_fixture() {
  local role=$1 allowed_project=$2 fingerprint
  openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 1 -subj "/CN=gcc-disposable-$role" \
    -keyout "$work/$role.key" -out "$work/$role.pem" 2>/dev/null
  fingerprint=$(openssl x509 -in "$work/$role.pem" -outform DER | sha256sum | cut -d' ' -f1)
  [[ $fingerprint =~ ^[a-f0-9]{64}$ && $fingerprint != "$cert_fingerprint" ]]
  inc config trust add-certificate "$work/$role.pem" --type=metrics --restricted --projects "$allowed_project"
  metrics_fingerprints+=("$fingerprint")
}
enroll_metrics_fixture metrics "$project"
enroll_metrics_fixture metrics-control "$alternate_project"
curl_fixture=(curl --disable --noproxy '*' --silent --show-error --connect-timeout 3 --max-time 20
  --proto '=https' --cacert "$work/daemon.pem")
metrics_identity=(--cert "$work/metrics.pem" --key "$work/metrics.key")
endpoint=$GCC_HOSTED_INCUS_AUTH_ENDPOINT
[[ $("${curl_fixture[@]}" -o /dev/null -w '%{http_code}' "$endpoint/1.0/metrics?project=$project") == 403 ]]
"${curl_fixture[@]}" "${metrics_identity[@]}" --fail --output "$work/telemetry" "$endpoint/1.0/metrics?project=$project"
"${curl_fixture[@]}" "${metrics_identity[@]}" --fail --output "$work/telemetry-default" "$endpoint/1.0/metrics"
"${curl_fixture[@]}" "${metrics_identity[@]}" --fail --output "$work/telemetry-alternate" "$endpoint/1.0/metrics?project=$alternate_project"
"${curl_fixture[@]}" --cert "$work/metrics-control.pem" --key "$work/metrics-control.key" \
  --fail --output "$work/telemetry-control" "$endpoint/1.0/metrics?project=$alternate_project"
python3 - "$work" "$project" "$guest" "$alternate_project" "$alternate_guest" <<'PY'
import math, re, sys
from pathlib import Path
root, project, guest, alternate_project, alternate_guest = sys.argv[1:]
expected = {"incus_cpu_seconds_total", "incus_memory_MemTotal_bytes", "incus_memory_MemFree_bytes"}

def check(filename, allowed, forbidden, required):
    families = set()
    with (Path(root) / filename).open(encoding="utf-8") as scrape:
        for line in scrape:
            m = re.fullmatch(r"([a-zA-Z0-9_:]+)\{([^}]+)\} (\S+)\s*", line)
            if not m:
                continue
            labels = dict(re.findall(r'(\w+)="([^"]*)"', m[2]))
            if labels.get("project") == forbidden:
                raise SystemExit(f"{filename}: foreign project was disclosed")
            if (labels.get("project"), labels.get("name")) == allowed and m[1] in expected:
                value = float(m[3])
                if math.isfinite(value) and value >= 0:
                    families.add(m[1])
    if required and families != expected:
        raise SystemExit(f"{filename}: fixture telemetry is incomplete")
    if not required and families:
        raise SystemExit(f"{filename}: wrong project selector returned guest counters")

check("telemetry", (project, guest), alternate_project, True)
check("telemetry-default", (project, guest), alternate_project, True)
check("telemetry-alternate", (project, guest), alternate_project, False)
check("telemetry-control", (alternate_project, alternate_guest), project, True)
PY
[[ $("${curl_fixture[@]}" "${metrics_identity[@]}" -o /dev/null -w '%{http_code}' "$endpoint/1.0/instances?project=$project&recursion=1") == 403 ]]
[[ $("${curl_fixture[@]}" "${metrics_identity[@]}" -o /dev/null -w '%{http_code}' "$endpoint/1.0/operations?project=$project&recursion=1") == 403 ]]
echo 'PASS restricted native metrics identity: guest telemetry scoped, inventory and operations denied'
timeout 90 "$test_binary" -test.run '^TestHostedIncusMetricsObserver702$' -test.v | tee "$work/metrics-reader.log"
grep -Eq '^--- PASS: TestHostedIncusMetricsObserver702 \(' "$work/metrics-reader.log"
