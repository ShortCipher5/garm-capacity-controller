#!/usr/bin/env bash
# Separate hosted fixture only; never mutate the existing empty-state specimen.
set -euo pipefail
umask 077
[[ $# = 0 && ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted ]]
[[ ${ImageOS:-} == ubuntu24 && ${RUNNER_ARCH:-} == X64 ]]
[[ ${GITHUB_RUN_ID:-} =~ ^[0-9]+$ && ${GITHUB_RUN_ATTEMPT:-} =~ ^[0-9]+$ ]]
[[ -z ${DOCKER_HOST:-} && -z ${DOCKER_CONTEXT:-} ]]
[[ $(docker context show) = default ]]
[[ $(docker context inspect default --format '{{.Endpoints.docker.Host}}') = unix:///var/run/docker.sock ]]
: "${RUNNER_TEMP:?disposable hosted runner temporary directory required}"
cd "$(dirname "$0")/../../.."

stock=ghcr.io/cloudbase/garm:v0.2.1@sha256:5ccdc29c31084ed4f30ec85e09a57b5105cf3dcc5fa8c1aba9e68de461e7e635
python_image=python:3.13-alpine@sha256:7984cf56ceaddebe4fefeb75d64000b9f45d8b77b11a09b24524da6cdc3af0c8
work=$(mktemp -d "$RUNNER_TEMP/garm-stock-inventory.XXXXXXXX")
name="garm-stock-inventory-${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}-${work##*.}"
probe_name="${name}-probe"
daemon_owned=false
probe_owned=false
cleanup() {
  local status=$?
  trap - EXIT
  set +e
  if [[ $probe_owned == true ]]; then
    timeout 20 docker rm -f "$probe_name" >/dev/null 2>&1 || true
  fi
  if [[ $daemon_owned == true ]]; then
    timeout 20 docker rm -f "$name" >/dev/null 2>&1 || true
  fi
  timeout 20 sudo rm -rf -- "$work" || status=1
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

timeout --kill-after=10s 120s docker pull "$stock" >/dev/null
timeout --kill-after=10s 120s docker pull "$python_image" >/dev/null
docker image inspect "$stock" > "$work/stock-image.json"
python3 deploy/runtime/garm-native-executable.py "$work/stock-image.json" "$stock" > "$work/stock-discovered.json"
read -r image executable < <(python3 -c 'import json,sys;x=json.load(open(sys.argv[1]));print(x["image_id"],x["executable"])' "$work/stock-discovered.json")
python3 deploy/runtime/garm-identity.py prepare "$work/fixture"
sudo install -m 0600 -o 65532 -g 65532 "$work/fixture/private/ca.pem" "$work/probe-ca.pem"
sudo chown -R 65530:65530 "$work/fixture/private" "$work/fixture/state"

base=(--read-only --cap-drop ALL --security-opt no-new-privileges --pids-limit 256 --cpus 1 --memory 512m)
! docker container inspect "$name" >/dev/null 2>&1
! docker container inspect "$probe_name" >/dev/null 2>&1
daemon_owned=true
docker run -d --name "$name" --network none "${base[@]}" --user 65530:65530 --workdir / \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=16m,uid=65530,gid=65530,mode=0700 \
  --mount "type=bind,src=$work/fixture/private,dst=/run/garm-private,readonly" \
  --mount "type=bind,src=$work/fixture/state,dst=/var/lib/garm" \
  --entrypoint /bin/sh "$image" -ec 'umask 077; exec "$1" -config /run/garm-private/config.toml' probe "$executable" >/dev/null
docker inspect "$name" | python3 -c '
import json,sys
[x]=json.load(sys.stdin)
h=x["HostConfig"]
assert x["State"]["Running"] and x["Image"]==sys.argv[1] and x["Config"]["User"]=="65530:65530"
assert h["NetworkMode"]=="none" and not h["PortBindings"] and not h["Privileged"] and not h["PidMode"]
assert h["ReadonlyRootfs"] and h["CapDrop"]==["ALL"] and not h["CapAdd"]
m={p["Destination"]:p["RW"] for p in x["Mounts"] if p["Type"]=="bind"}
assert m=={"/run/garm-private":False,"/var/lib/garm":True}
assert any(v in h["SecurityOpt"] for v in ("no-new-privileges", "no-new-privileges:true"))
' "$image"
probe_owned=true
timeout --kill-after=10s 90s docker run --name "$probe_name" --network "container:$name" "${base[@]}" --user 65532:65532 \
  --mount "type=bind,src=$PWD/deploy/runtime/stock-inventory/probe.py,dst=/inventory-probe.py,readonly" \
  --mount "type=bind,src=$work/probe-ca.pem,dst=/fixture-ca.pem,readonly" \
  "$python_image" python /inventory-probe.py
docker rm "$probe_name" >/dev/null
probe_owned=false
docker stop --time 30 "$name" >/dev/null
[[ $(docker inspect --format '{{.State.ExitCode}}' "$name") = 0 ]]
docker rm "$name" >/dev/null
daemon_owned=false
! docker container inspect "$name" >/dev/null 2>&1
! docker container inspect "$probe_name" >/dev/null 2>&1
sudo rm -rf -- "$work"
trap - EXIT
echo "PASS authenticated stock empty inventory fixture cleaned up; no enrolled runner or live qualification"
