#!/usr/bin/env bash
# Exact-image packaging fixture on GitHub-hosted Ubuntu only. No live state.
set -euo pipefail
umask 077
[[ $# == 0 && ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted ]]
[[ ${ImageOS:-} == ubuntu24 && ${RUNNER_ARCH:-} == X64 ]]
[[ ${GITHUB_RUN_ID:-} =~ ^[0-9]+$ && ${GITHUB_RUN_ATTEMPT:-} =~ ^[0-9]+$ ]]
[[ -z ${DOCKER_HOST:-} && -z ${DOCKER_CONTEXT:-} && $(docker context show) == default ]]
[[ $(docker context inspect default --format '{{.Endpoints.docker.Host}}') == unix:///var/run/docker.sock ]]
cd "$(dirname "$0")/../../.."
base=$PWD/deploy/runtime
work=$(mktemp -d "${RUNNER_TEMP:?}/stock-reader.XXXXXXXX")
project="stock-reader-${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}-${work##*.}"
project=${project,,}
names=()
cleanup() {
  local rc=$?
  trap - EXIT
  set +e
  if [[ ${#names[@]} != 0 ]]; then
    for ((i=${#names[@]}-1;i>=0;i--)); do
      if docker container inspect "${names[i]}" >/dev/null 2>&1; then
        timeout 20 docker rm -f "${names[i]}" >/dev/null || rc=1
      fi
    done
  fi
  timeout 20 sudo rm -rf -- "$work" || rc=1
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
stock=ghcr.io/cloudbase/garm:v0.2.1@sha256:5ccdc29c31084ed4f30ec85e09a57b5105cf3dcc5fa8c1aba9e68de461e7e635
python=python:3.13-alpine@sha256:7984cf56ceaddebe4fefeb75d64000b9f45d8b77b11a09b24524da6cdc3af0c8
docker pull "$stock" >/dev/null
docker pull "$python" >/dev/null
export CAPACITY_STOCK_BRIDGE_IMAGE="$project:caddy"
export CAPACITY_STOCK_READER_IMAGE="$project:reader"
export CAPACITY_GATEWAY_IMAGE="$project:gateway"
mkdir -p "$work/caddy"
archive=caddy_2.11.9_linux_amd64.tar.gz
curl --fail --location --silent --show-error --max-time 60 "https://github.com/caddyserver/caddy/releases/download/v2.11.9/$archive" --output "$work/$archive"
echo "5b8cb7faf8726a2028ad96a66e54e6e81e8f361bc6175e5d22bbd997cc5177fb  $work/$archive" | sha256sum -c --status
tar -xzf "$work/$archive" -C "$work/caddy" caddy
chmod 0555 "$work/caddy/caddy"
printf 'FROM %s\nCOPY --chmod=0555 caddy /usr/local/bin/caddy\n' "$python" > "$work/caddy/Dockerfile"
docker build --network none -t "$CAPACITY_STOCK_BRIDGE_IMAGE" "$work/caddy"
docker build --target stock-reader -t "$CAPACITY_STOCK_READER_IMAGE" .
docker build --target gateway -t "$CAPACITY_GATEWAY_IMAGE" .
docker build --target ledger-tool -t "$project:ledger" .
docker image inspect "$stock" > "$work/stock-image.json"
python3 "$base/garm-native-executable.py" "$work/stock-image.json" "$stock" > "$work/stock-discovered.json"
read -r stock_id executable < <(python3 -c 'import json,sys;x=json.load(open(sys.argv[1]));print(x["image_id"],x["executable"])' "$work/stock-discovered.json")
python3 -B "$base/stock-reader-bridge/fixture.py" prepare "$work"
sudo chown -R 65530:65530 "$work/stock/private" "$work/stock/state"
sudo chown -R 65528:65528 "$work/bridge"
sudo chown -R 65529:65529 "$work/reader"
sudo chown -R 65532:65532 "$work/companion/gateway-private" "$work/companion/state" "$work/companion/init-private"
sudo chown -R 65535:65535 "$work/probe" "$work/companion/fixture-private"
export CAPACITY_STOCK_BRIDGE_DIR="$work/bridge" CAPACITY_STOCK_READER_DIR="$work/reader"
export CAPACITY_PRIVATE_DIR="$work/companion/gateway-private" CAPACITY_STATE_DIR="$work/companion/state"
export CAPACITY_STOCK_BRIDGE_PORT=9461 CAPACITY_STOCK_HTTP_PORT=9998
restricted=(--read-only --cap-drop ALL --security-opt=no-new-privileges --pids-limit 96 --cpus 1 --memory 512m)
register() {
  ! docker container inspect "$1" >/dev/null 2>&1 || return 1
  names+=("$1")
}
create_stock() {
  local name="$1"
  register "$name"
  docker run -d --name "$name" --network none "${restricted[@]}" --user 65530:65530 --workdir / \
    --tmpfs /tmp:rw,noexec,nosuid,nodev,size=16m,uid=65530,gid=65530,mode=0700 \
    --mount "type=bind,src=$work/stock/private,dst=/run/garm-private,readonly" \
    --mount "type=bind,src=$work/stock/state,dst=/var/lib/garm" \
    --entrypoint "$executable" "$stock_id" -config /run/garm-private/config.toml > "$work/stock-id"
  CAPACITY_STOCK_CONTAINER_ID=$(<"$work/stock-id")
  export CAPACITY_STOCK_CONTAINER_ID
  [[ $CAPACITY_STOCK_CONTAINER_ID =~ ^[a-f0-9]{64}$ ]]
}
probe() {
  local who="$1" network="$2" mode="$3"; shift 3
  docker run --rm --network "$network" "${restricted[@]}" --user "$who:$who" \
    --mount "type=bind,src=$base/stock-reader-bridge/fixture.py,dst=/fixture.py,readonly" \
    "$@" "$python" python -B /fixture.py "$mode"
}
create_stock "$project-stock"
probe 65529 "container:$CAPACITY_STOCK_CONTAINER_ID" bootstrap --mount "type=bind,src=$work/reader,dst=/run/capacity-stock-reader"
docker run --rm --network none "${restricted[@]}" --user 65532:65532 \
  --mount "type=bind,src=$work/companion/init-private,dst=/initial,readonly" \
  --mount "type=bind,src=$CAPACITY_PRIVATE_DIR,dst=/identity" \
  --mount "type=bind,src=$CAPACITY_STATE_DIR,dst=/state" \
  "$project:ledger" initialize --destination /state/ledger.db --identity-file /identity/ledger.identity --policy /initial/initial-policy.json
compose=(docker compose --project-name "$project" --profile capacity-preview -f deploy/compose.stock-reader.yaml -f deploy/compose.stock-reader-http-bridge.yaml)
services=(capacity-stock-http-bridge capacity-stock-reader capacity-gateway)
for service in "${services[@]}"; do register "$project-$service-1"; done
for role in 1 2; do
  incus="$project-synthetic-incus-$role"
  register "$incus"
  docker run -d --name "$incus" --network "container:$CAPACITY_STOCK_CONTAINER_ID" "${restricted[@]}" --user 65535:65535 \
    --mount "type=bind,src=$base/fixture.py,dst=/synthetic.py,readonly" \
    --mount "type=bind,src=$work/companion/fixture-private,dst=/fixture-private,readonly" \
    "$python" python -B /synthetic.py serve >/dev/null
  probe 65535 "container:$CAPACITY_STOCK_CONTAINER_ID" incus-ready \
    --mount "type=bind,src=$work/probe,dst=/probe,readonly"
  for service in "${services[@]}"; do
    "${compose[@]}" up -d --no-deps --no-build --pull never "$service"
    current=$("${compose[@]}" ps -q "$service" | head -1)
    [[ $current =~ ^[a-f0-9]{64}$ ]]
    # Allow only exactly generated files and no stock mounts or host privileges.
    docker inspect "$current" | python3 -c '
import json,sys
service, anchor=sys.argv[1:]
[x]=json.load(sys.stdin)
h=x["HostConfig"]
assert h["NetworkMode"]=="container:"+anchor and not h["PortBindings"] and not h["Privileged"]
assert h["ReadonlyRootfs"] and not h["CapAdd"] and h["CapDrop"]==["ALL"] and not h["PidMode"]
uid,expected={
 "capacity-stock-http-bridge":("65528:65528",{"/run/capacity-stock-http-bridge":False}),
 "capacity-stock-reader":("65529:65529",{"/run/capacity-stock-reader":False}),
 "capacity-gateway":("65532:65532",{"/run/capacity-private":False,"/var/lib/capacity-gateway":True}),
}[service]
assert x["Config"]["User"]==uid
assert {m["Destination"]:m["RW"] for m in x["Mounts"] if m["Type"]=="bind"}==expected
' "$service" "$CAPACITY_STOCK_CONTAINER_ID"
    if [[ $service != capacity-gateway ]]; then
      # The reader verifies upstream during startup, so prove bridge readiness before it starts.
      for attempt in {1..20}; do
        running=$(docker inspect --format '{{.State.Running}}' "$current")
        [[ $running == true ]] && break
        sleep .25
      done
      [[ $running == true ]]
      if [[ $service == capacity-stock-http-bridge ]]; then
        ready=bridge-ready
      else
        ready=reader-ready
      fi
      probe 65535 "container:$CAPACITY_STOCK_CONTAINER_ID" "$ready" \
        --mount "type=bind,src=$work/probe,dst=/probe,readonly"
    fi
  done
  probe 65535 "container:$CAPACITY_STOCK_CONTAINER_ID" verify --mount "type=bind,src=$work/probe,dst=/probe,readonly"
  probe 65535 none outer
  # Stop and remove only the three companion fixtures; stock continues serving.
  "${compose[@]}" rm -fs "${services[@]}"
  probe 65535 "container:$CAPACITY_STOCK_CONTAINER_ID" stock-health
  docker rm -f "$incus" >/dev/null
  if [[ $role == 1 ]]; then
    old_stock=$CAPACITY_STOCK_CONTAINER_ID
    docker stop --time 25 "$old_stock" >/dev/null
    docker rm "$old_stock" >/dev/null
    create_stock "$project-stock-recreated"
    [[ $CAPACITY_STOCK_CONTAINER_ID != "$old_stock" ]]
    probe 65535 "container:$CAPACITY_STOCK_CONTAINER_ID" stock-health
  fi
done
echo 'PASS disposable stock/companion image namespace checks and explicit stock replacement recovery'
