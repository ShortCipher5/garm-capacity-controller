#!/usr/bin/env bash
# GitHub-hosted disposable paired artifact gate. NEVER a live install/release.
set -euo pipefail
cd "$(dirname "$0")/../../.."
: "${RUNNER_TEMP:?Disposable GitHub-hosted workspace required}"
[[ ${GITHUB_ACTIONS:-} = true && ${RUNNER_ENVIRONMENT:-} = github-hosted ]] || { echo 'Refusing non-hosted execution' >&2; exit 1; }
git diff --quiet && git diff --cached --quiet
repo=$PWD
work=$(mktemp -d "$RUNNER_TEMP/garm-pair.XXXXXXXX")
name="garm-pair-${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}"
garm="$name-garm:lab"; gateway="$name-gateway:lab"; ledger="$name-ledger:lab"
python_image=python:3.13-alpine@sha256:7984cf56ceaddebe4fefeb75d64000b9f45d8b77b11a09b24524da6cdc3af0c8
root=/lab/garm-pair-runtime
results="$repo/qualification-results"
cleanup() {
  local outcome=$?
  if [[ $outcome != 0 ]]; then
    docker inspect "$name-garm" "$name-gateway" "$name-api" 2>/dev/null | python3 -c 'import json,sys;print(json.dumps([{ "status":v["State"]["Status"],"exit":v["State"]["ExitCode"],"oom":v["State"]["OOMKilled"]} for v in json.load(sys.stdin)]))' || true
  fi
  docker rm -f "$name-gateway" "$name-api" "$name-garm" >/dev/null 2>&1 || true
  docker image rm "$garm" "$gateway" "$ledger" >/dev/null 2>&1 || true
  sudo rm -rf -- "$work"
  return "$outcome"
}
trap cleanup EXIT
# Exclusively NEW result directory; preexisting files/dirs/symlinks fail closed.
mkdir "$results"
mkdir -p "$work/build" "$work/controller"
git archive HEAD | tar -x -C "$work/controller"
# Exact fetched public source and fixed reviewed patch. No installed source/config.
git init --quiet "$work/build/source"
git -C "$work/build/source" remote add origin https://github.com/cloudbase/garm.git
git -C "$work/build/source" fetch --quiet --depth=1 origin 154638445c3949c1958b01812f69d9a1e4d82684
git -C "$work/build/source" checkout --quiet --detach FETCH_HEAD
[[ $(git -C "$work/build/source" rev-parse HEAD) = 154638445c3949c1958b01812f69d9a1e4d82684 ]]
[[ $(sha256sum integration/garm-v0.2.1/admission-preflight.patch | cut -d' ' -f1) = 87e0d61751d4b9ffa15322cff09557f0a1094fdd68464fe840a12ffe01bb0660 ]]
git -C "$work/build/source" apply --check "$repo/integration/garm-v0.2.1/admission-preflight.patch"
git -C "$work/build/source" apply "$repo/integration/garm-v0.2.1/admission-preflight.patch"
cp deploy/runtime/garm-pair/probe.go.txt "$work/build/probe.go"
printf 'source/.git\n' > "$work/build/.dockerignore"
python3 deploy/runtime/garm-pair/manifest.py lock "$work/controller.Dockerfile" "$work/build/Dockerfile" "$work/bases.json"
# Every build and public manifest is completed BEFORE synthetic private state exists.
docker build --platform linux/amd64 -f "$work/controller.Dockerfile" --target gateway -t "$gateway" "$work/controller"
docker build --platform linux/amd64 -f "$work/controller.Dockerfile" --target ledger-tool -t "$ledger" "$work/controller"
docker build --platform linux/amd64 -f "$work/controller.Dockerfile" --target provider-artifact --output "type=local,dest=$work/provider" "$work/controller"
cp "$work/provider/capacity-provider" "$work/build/capacity-provider"
docker build --platform linux/amd64 --target paired-garm -t "$garm" "$work/build"
docker build --platform linux/amd64 --target pair-probe-artifact --output "type=local,dest=$work/probe" "$work/build"
python3 deploy/runtime/garm-pair/manifest.py assemble "$work" "$garm" "$gateway" "$ledger" "$work/pair-manifest.json"
python3 deploy/runtime/garm-pair/manifest.py validate "$work/pair-manifest.json"
python3 - "$work/pair-manifest.json" <<'PY'
import importlib.util,json,pathlib,sys
spec=importlib.util.spec_from_file_location('manifest','deploy/runtime/garm-pair/manifest.py');m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
x=json.loads(pathlib.Path(sys.argv[1]).read_text());x['images']['gateway']='sha256:'+'0'*64
try:m.validate(x)
except AssertionError:pass
else:raise AssertionError('mixed image manifest accepted without its independently pinned bundle identity')
print('PASS public content identity rejects changed image pairing')
PY
base=(--read-only --cap-drop ALL --security-opt no-new-privileges --pids-limit 256 --cpus 1 --memory 512m)
docker run --rm --network none "${base[@]}" "$garm" --version | grep -F 'v0.2.1+capacity.87e0d61751d4' >/dev/null
docker run --rm --network none "${base[@]}" -e GARM_COMMAND=GetVersion --entrypoint /usr/local/bin/capacity-provider "$garm" | grep -Fx garm-capacity-controller-experimental >/dev/null
docker run --rm --network none "${base[@]}" -e GARM_COMMAND=GetSupportedInterfaceVersions --entrypoint /usr/local/bin/capacity-provider "$garm" | grep -Fx '["v0.1.1"]' >/dev/null
docker pull "$python_image"
python3 deploy/runtime/garm-pair/fixture.py prepare "$work/fixtures"
gp="$work/fixtures/garm-pair-runtime/private"; gs="$work/fixtures/garm-pair-runtime/state"
fp="$work/fixtures/fake/fixture-private"; gwp="$work/fixtures/fake/gateway-private"; gws="$work/fixtures/fake/state"
sudo chown -R 65531:65531 "$gp" "$gs"
# Explicit NEW-only helper invocation; probe is a read-only public test bind,
# never copied into the paired runtime image or automatically run by its entrypoint.
docker run --rm --network none "${base[@]}" --user 65531:65531 \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=16m,uid=65531,gid=65531,mode=0700 \
  --mount "type=bind,src=$work/probe/pair-probe,dst=/pair-probe,readonly" \
  --mount "type=bind,src=$gp,dst=$root/private" --mount "type=bind,src=$gs,dst=$root/state" \
  --entrypoint /pair-probe "$garm" seed "$root"
sudo python3 deploy/runtime/garm-pair/fixture.py configure "$work/fixtures"
sudo chown -R 65531:65531 "$gp" "$gs"
sudo chown -R 65530:65530 "$fp"
sudo chown -R 65532:65532 "$gwp" "$gws" "$work/fixtures/fake/init-private"
docker run --rm --network none "${base[@]}" --user 65532:65532 \
  --mount "type=bind,src=$work/fixtures/fake/init-private,dst=/identity" --mount "type=bind,src=$gws,dst=/state" \
  "$ledger" initialize --destination /state/ledger.db --identity-file /identity/ledger.identity --policy /identity/initial-policy.json
sudo install -o 65532 -g 65532 -m 0600 "$work/fixtures/fake/init-private/ledger.identity" "$gwp/ledger.identity"
start_garm() {
  docker run -d --name "$name-garm" --network none "${base[@]}" --user 65531:65531 \
    --tmpfs /tmp:rw,noexec,nosuid,nodev,size=16m,uid=65531,gid=65531,mode=0700 \
    --mount "type=bind,src=$work/probe/pair-probe,dst=/pair-probe,readonly" \
    --mount "type=bind,src=$gp,dst=$root/private,readonly" --mount "type=bind,src=$gs,dst=$root/state" \
    --entrypoint /bin/sh "$garm" -ec "umask 077; exec /usr/local/bin/garm -config $root/private/config.toml" >/dev/null
}
start_garm
# Fake upstream inventory/Incus API role is UID65530 with disjoint private keys.
docker run -d --name "$name-api" --network "container:$name-garm" "${base[@]}" --user 65530:65530 \
  --mount "type=bind,src=$repo/deploy/runtime/fixture.py,dst=/fixture.py,readonly" --mount "type=bind,src=$fp,dst=/fixture-private,readonly" \
  "$python_image" python /fixture.py serve >/dev/null
docker exec "$name-api" python /fixture.py ready
docker run -d --name "$name-gateway" --network "container:$name-garm" "${base[@]}" --user 65532:65532 \
  --mount "type=bind,src=$gwp,dst=/run/capacity-private,readonly" --mount "type=bind,src=$gws,dst=/var/lib/capacity-gateway" "$gateway" >/dev/null
# Native Garm TLS/API and actual daemon PID1/UID/capability masks.
docker run --rm --network "container:$name-garm" "${base[@]}" --user 65531:65531 \
  --mount "type=bind,src=$repo/deploy/runtime/garm-identity.py,dst=/probe.py,readonly" --mount "type=bind,src=$gp/ca.pem,dst=/lab-ca.pem,readonly" \
  "$python_image" python /probe.py probe >/dev/null
ready=false
for _ in {1..60}; do
  if docker run --rm --network "container:$name-garm" "${base[@]}" --user 65530:65530 \
    --mount "type=bind,src=$repo/deploy/runtime/fixture.py,dst=/fixture.py,readonly" \
    --mount "type=bind,src=$repo/deploy/runtime/garm-pair/fixture.py,dst=/garm-pair/fixture.py,readonly" \
    --mount "type=bind,src=$fp,dst=/fixture-private,readonly" "$python_image" python /garm-pair/fixture.py probe; then ready=true; break; fi
  sleep .5
done
[[ $ready = true ]]
# Mandatory facade positive; readiness timeouts never pass.
docker exec --user 65531:65531 "$name-garm" /pair-probe list "$root"
docker exec --user 65531:65531 "$name-garm" /pair-probe source-role "$root"
docker exec --user 65531:65531 "$name-garm" /pair-probe bad-ca "$root"
docker exec --user 65531:65531 "$name-garm" /pair-probe list "$root"
docker inspect "$name-garm" "$name-gateway" "$name-api" | python3 -c '
import json,sys
items=json.load(sys.stdin);g,gw,api=items
for x,uid in zip(items,("65531","65532","65530")):
 h=x["HostConfig"];assert x["Config"]["User"]==uid+":"+uid and h["ReadonlyRootfs"] and not h["Privileged"]
 assert h["CapDrop"]==["ALL"] and not h["CapAdd"] and h["PidMode"]=="" and not h["PortBindings"] and h["RestartPolicy"]["Name"]=="no"
 assert any(v in h["SecurityOpt"] for v in ("no-new-privileges","no-new-privileges:true"))
 assert h["PidsLimit"]==256 and h["Memory"]>0 and h["NanoCpus"]>0
assert g["HostConfig"]["NetworkMode"]=="none"
for x in (gw,api):assert x["HostConfig"]["NetworkMode"]=="container:"+g["Id"]
m={p["Destination"]:p for p in g["Mounts"] if p["Type"]=="bind"};root=sys.argv[1]
assert set(m)=={root+"/private",root+"/state","/pair-probe"} and not m[root+"/private"]["RW"] and m[root+"/state"]["RW"] and not m["/pair-probe"]["RW"]
m={p["Destination"]:p for p in gw["Mounts"]};assert set(m)=={"/run/capacity-private","/var/lib/capacity-gateway"} and not m["/run/capacity-private"]["RW"] and m["/var/lib/capacity-gateway"]["RW"]
print("PASS paired images use disjoint UIDs/mounts in actual Garm-owned loopback-only namespace")
' "$root"
docker exec "$name-garm" /bin/sh -ec "test \"\$(tr '\\000' ' ' </proc/1/cmdline)\" = '/usr/local/bin/garm -config $root/private/config.toml '"
for spec in "$name-garm 65531" "$name-gateway 65532"; do
  read -r container uid <<< "$spec"
  docker exec "$container" /bin/sh -ec 'cat /proc/1/status' | python3 -c '
import sys
f=dict(v.split(":",1) for v in sys.stdin.read().splitlines() if ":" in v)
assert f["Uid"].split()==[sys.argv[1]]*4 and f["Gid"].split()==[sys.argv[1]]*4
for k in ("CapInh","CapPrm","CapEff","CapBnd","CapAmb"):assert int(f[k],16)==0
assert f["NoNewPrivs"].strip()=="1"
' "$uid"
done
# Runtime role-boundary positive/negative controls, no key/config bytes printed.
for spec in '65531 garm yes' '65532 garm no' '65532 gateway yes' '65531 gateway no'; do
 read -r uid role expected <<< "$spec"; path="$gp"; file=provider.json
 if [[ $role = gateway ]]; then path="$gwp"; file=client.key; fi
 cmd="cat /other/$file >/dev/null 2>&1"; if [[ $expected = no ]]; then cmd="! $cmd"; fi
 docker run --rm --network none "${base[@]}" --user "$uid:$uid" --mount "type=bind,src=$path,dst=/other,readonly" --entrypoint /bin/sh "$garm" -ec "$cmd"
done
# Only sanitized public result is emitted after ALL assertions passed.
python3 - "$work/pair-manifest.json" "$results" <<'PY'
import hashlib,json,pathlib,sys
manifest=pathlib.Path(sys.argv[1]);directory=pathlib.Path(sys.argv[2]);x=json.loads(manifest.read_text())
result={'bundle_version':x['bundle_version'],'manifest_sha256':hashlib.sha256(manifest.read_bytes()).hexdigest(),'actual_pinned_facade_packaged_provider_gateway_list_passed':True,'distinct_source_leaf_rejected_for_list':True,'wrong_gateway_ca_rejected':True,'nonroot_private_loopback_pair_passed':True,'provisioning_enabled':False,'source_issuer_qualified':False,'jit_or_job_lifecycle_qualified':False,'signed_release':False,'installed_stack_qualified':False,'deployment_authorized':False,'scope':'Runner-local reduced candidate images; generated state/keys, actual read-only facade/provider/gateway path; synthetic upstream APIs, no real account/provider/host/job or image publication.'}
with (directory/'paired-artifacts.json').open('xb') as out:out.write(manifest.read_bytes())
with (directory/'paired-runtime.json').open('x') as out:out.write(json.dumps(result,indent=2))
print('PASS disposable paired artifact/read-only RPC gate; installed deployment and real issuer gates remain BLOCKED')
PY
