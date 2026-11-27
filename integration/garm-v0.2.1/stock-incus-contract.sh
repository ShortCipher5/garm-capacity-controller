#!/usr/bin/env bash
# Check the official plugin's startup contract without an Incus daemon or credentials.
set -euo pipefail
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
test "$(uname -m)" = x86_64
work=$(mktemp -d)
tag="gcc-stock-incus-contract:${work##*/}"
cleanup() {
  docker image rm "$tag" >/dev/null 2>&1 || true
  rm -rf -- "$work"
}
trap cleanup EXIT
mkdir "$work/context"
interface=$(python3 - "$here/stock-incus-contract.toml" "$work/context/provider.toml" <<'PY'
import pathlib
import sys
import tomllib

with open(sys.argv[1], "rb") as source:
    fixture = tomllib.load(source)
interface = fixture["provider"]["interface_version"]
kind = fixture["provider"]["instance_type"]
minutes = fixture["pool"]["runner_bootstrap_timeout"]
if interface not in ("v0.1.0", "v0.1.1"):
    sys.exit("Unexpected external provider protocol")
if kind != "container" or type(minutes) is not int or minutes != 3:
    sys.exit("Expected a container and a stock bootstrap timeout of three minutes")
pathlib.Path(sys.argv[2]).write_text(
    'unix_socket_path = "/fixture/not-a-daemon"\n'
    'project_name = "offline-fixture"\n'
    f'instance_type = "{kind}"\n'
)
print(interface)
PY
)
touch "$work/context/not-a-daemon"
archive=garm-provider-incus-linux-amd64.tgz
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --max-time 90 \
  "https://github.com/cloudbase/garm-provider-incus/releases/download/v0.1.6/$archive" -o "$work/$archive"
(cd "$work"; printf '%s  %s\n' cffc18c62fe6a909703680671b8a92870c1ee67ee72a5054b9ecee23eadf75f1 "$archive" | sha256sum -c -)
tar -xOzf "$work/$archive" -- garm-provider-incus >"$work/context/garm-provider-incus"
chmod 0555 "$work/context/garm-provider-incus"
cat >"$work/context/Dockerfile" <<'DOCKERFILE'
FROM scratch
COPY garm-provider-incus /garm-provider-incus
COPY provider.toml not-a-daemon /fixture/
USER 65534:65534
ENTRYPOINT ["/garm-provider-incus"]
DOCKERFILE
docker build --network none --tag "$tag" "$work/context"
run_version() {
  docker run --rm --network none --read-only --cap-drop ALL --security-opt no-new-privileges \
    --pids-limit 64 --memory 128m \
    -e GARM_COMMAND=GetVersion -e GARM_CONTROLLER_ID=00000000-0000-4000-8000-000000000001 \
    -e GARM_PROVIDER_CONFIG_FILE=/fixture/provider.toml -e "GARM_INTERFACE_VERSION=$1" "$tag"
}
version=$(run_version "$interface")
[[ "$version" == v0.1.6 ]]
if run_version v0.1.1 >"$work/incompatible.log" 2>&1; then
  echo "Unexpectedly accepted incompatible external provider protocol" >&2
  exit 1
fi
grep -Fq 'provider does not implement v0.1.1 ExternalProvider' "$work/incompatible.log"
echo 'Verified the exact stock Incus binary protocol. No Incus daemon or runner was contacted.'
