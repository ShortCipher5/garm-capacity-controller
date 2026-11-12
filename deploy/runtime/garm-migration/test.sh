#!/usr/bin/env bash
# Disposable GitHub-hosted TEST image/state only. Never an installed-state input.
set -euo pipefail
cd "$(dirname "$0")/../../.."
: "${RUNNER_TEMP:?A disposable GitHub-hosted workspace is required}"
[[ ${GITHUB_ACTIONS:-} = true && ${RUNNER_ENVIRONMENT:-} = github-hosted ]] || { echo 'Refusing non-hosted execution' >&2; exit 1; }
commit=154638445c3949c1958b01812f69d9a1e4d82684
patch=87e0d61751d4b9ffa15322cff09557f0a1094fdd68464fe840a12ffe01bb0660
python_image=python:3.13-alpine@sha256:7984cf56ceaddebe4fefeb75d64000b9f45d8b77b11a09b24524da6cdc3af0c8
name="garm-transfer-${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}"
image="$name:test-only"
work=$(mktemp -d "$RUNNER_TEMP/garm-transfer.XXXXXXXX")
legacy="$work/garm-synthetic-storage-legacy"
target="$work/garm-synthetic-storage-target"
root=/lab/garm-synthetic-storage-runtime
repo=$PWD
results="$repo/qualification-results"
mkdir -p "$results"
cleanup() {
  local outcome=$?
  if [[ $outcome != 0 ]]; then
    docker inspect "$name" "$name-negative" 2>/dev/null | python3 -c 'import json,sys;print(json.dumps([{ "status":x["State"]["Status"],"exit":x["State"]["ExitCode"],"oom":x["State"]["OOMKilled"]} for x in json.load(sys.stdin)]))' || true
  fi
  docker rm -f "$name" "$name-negative" >/dev/null 2>&1 || true
  docker image rm "$image" >/dev/null 2>&1 || true
  sudo rm -rf -- "$work"
  return "$outcome"
}
trap cleanup EXIT

# Build context exists BEFORE any synthetic credentials/data, from public source.
mkdir -p "$work/build"
git init --quiet "$work/build/source"
git -C "$work/build/source" remote add origin https://github.com/cloudbase/garm.git
git -C "$work/build/source" fetch --quiet --depth=1 origin "$commit"
git -C "$work/build/source" checkout --quiet --detach FETCH_HEAD
[[ $(git -C "$work/build/source" rev-parse HEAD) = "$commit" ]]
[[ $(sha256sum integration/garm-v0.2.1/admission-preflight.patch | cut -d' ' -f1) = "$patch" ]]
git -C "$work/build/source" apply --check "$repo/integration/garm-v0.2.1/admission-preflight.patch"
git -C "$work/build/source" apply "$repo/integration/garm-v0.2.1/admission-preflight.patch"
cp deploy/runtime/garm-migration/store.go.txt "$work/build/store.go"
printf 'source/.git\n' > "$work/build/.dockerignore"
# Strict reviewed FROM grammar; resolve each approved mutable base before build.
python3 - "$work/build/Dockerfile" "$work/bases.json" <<'PY'
import json,pathlib,re,subprocess,sys
text=pathlib.Path('deploy/runtime/garm-migration/Dockerfile').read_text()
allowed={'golang:1.26.10-bookworm','debian:bookworm-slim'};seen=set();locks={}
for line in text.splitlines():
 if not re.match(r'(?i)^\s*from(?:\s|$)',line): continue
 m=re.fullmatch(r'FROM (\S+) AS ([a-z0-9-]+)',line);assert m,'unsupported FROM grammar'
 image,_=m.groups();assert image in allowed and image not in seen;seen.add(image)
 subprocess.run(['docker','pull',image],check=True)
 info=json.loads(subprocess.check_output(['docker','image','inspect',image]))[0]
 digest=info['RepoDigests'][0];assert '@sha256:' in digest
 locks[image]=digest;text=text.replace('FROM '+image+' AS ','FROM '+digest+' AS ')
assert seen==allowed
pathlib.Path(sys.argv[1]).write_text(text);pathlib.Path(sys.argv[2]).write_text(json.dumps(locks))
PY
docker build --platform linux/amd64 -t "$image" "$work/build"
docker pull "$python_image"
# No build context/cache ever receives the following generated private fixture.
python3 deploy/runtime/garm-migration/state.py prepare "$legacy"
sudo chown -R 0:0 "$legacy/private" "$legacy/state"
sudo chown -R 65532:65532 "$legacy/roles/gateway-private"
base=(--network none --read-only --cap-drop ALL --security-opt no-new-privileges --pids-limit 256 --cpus 1 --memory 512m)
mounts() { private_source="$1/private"; state_source="$1/state"; }
helper() {
  local source=$1 uid=$2 action=$3
  mounts "$source"
  docker run --rm "${base[@]}" --user "$uid:$uid" --tmpfs "/tmp:rw,noexec,nosuid,nodev,size=16m,uid=$uid,gid=$uid,mode=0700" \
    --mount "type=bind,src=$private_source,dst=$root/private" --mount "type=bind,src=$state_source,dst=$root/state" \
    --entrypoint /usr/local/bin/garm-migration-lab "$image" "$action" "$root"
}
# Explicit NEW seeding is setup, never in the guarded startup/transfer path.
helper "$legacy" 0 seed
start() {
  local source=$1 uid=$2
  mounts "$source"
  docker run -d --name "$name" "${base[@]}" --user "$uid:$uid" \
    --tmpfs "/tmp:rw,noexec,nosuid,nodev,size=16m,uid=$uid,gid=$uid,mode=0700" \
    --mount "type=bind,src=$private_source,dst=$root/private,readonly" --mount "type=bind,src=$state_source,dst=$root/state" \
    --entrypoint /bin/sh "$image" -ec "umask 077; /usr/local/bin/garm-migration-lab guard $root; exec /usr/local/bin/garm -config $root/private/config.toml" >/dev/null
}
probe() {
  local source=$1 uid=$2
  docker run --rm --network "container:$name" --read-only --cap-drop ALL --security-opt no-new-privileges --pids-limit 128 --memory 128m --cpus 1 --user "$uid:$uid" \
    --mount "type=bind,src=$repo/deploy/runtime/garm-identity.py,dst=/probe.py,readonly" --mount "type=bind,src=$source/private/ca.pem,dst=/lab-ca.pem,readonly" \
    "$python_image" python /probe.py probe >/dev/null
}
boundary() {
  local uid=$1
  docker inspect "$name" | python3 -c '
import json,sys
x=json.load(sys.stdin)[0];h=x["HostConfig"];uid=sys.argv[1];root=sys.argv[2]
assert x["Config"]["User"]==uid+":"+uid and h["NetworkMode"]=="none"
assert h["ReadonlyRootfs"] and not h["Privileged"] and h["CapDrop"]==["ALL"] and not h["CapAdd"]
assert not h["PortBindings"] and h["PidMode"]=="" and h["RestartPolicy"]["Name"]=="no"
assert h["PidsLimit"]==256 and h["Memory"]>0 and h["NanoCpus"]>0
assert any(v in h["SecurityOpt"] for v in ("no-new-privileges","no-new-privileges:true"))
m={p["Destination"]:p for p in x["Mounts"] if p["Type"]=="bind"}
assert set(m)=={root+"/private",root+"/state"} and not m[root+"/private"]["RW"] and m[root+"/state"]["RW"]
assert all(p["Type"] in ("bind","tmpfs") for p in x["Mounts"]) and set(h["Tmpfs"])=={"/tmp"}
' "$uid" "$root"
  docker exec --user "$uid:$uid" "$name" /bin/sh -ec "test \"\$(tr '\\000' ' ' </proc/1/cmdline)\" = '/usr/local/bin/garm -config $root/private/config.toml '; cat /proc/1/status" | python3 -c '
import sys
f=dict(x.split(":",1) for x in sys.stdin.read().splitlines() if ":" in x)
assert f["Uid"].split()==[sys.argv[1]]*4 and f["Gid"].split()==[sys.argv[1]]*4
for k in ("CapInh","CapPrm","CapEff","CapBnd","CapAmb"): assert int(f[k],16)==0
assert f["NoNewPrivs"].strip()=="1"
' "$uid"
}
stop() {
  docker stop --time 60 "$name" >/dev/null
  [[ $(docker inspect --format '{{.State.ExitCode}}' "$name") = 0 ]]
  [[ $(docker inspect --format '{{.State.Running}}' "$name") = false ]]
  docker rm "$name" >/dev/null
}

start "$legacy" 0
probe "$legacy" 0
boundary 0
stop
helper "$legacy" 0 guard
# Every daemon and helper has exited, no restart policy/external writer. Freeze.
sudo python3 deploy/runtime/garm-migration/state.py freeze "$legacy" "$work/original.json"
sudo python3 deploy/runtime/garm-migration/state.py transfer "$legacy" "$target"
# Paired owner-positive / cross-role-negative read controls; no bytes logged.
for spec in '65532 gateway yes' '65531 gateway no' '65531 garm yes' '65532 garm no'; do
  read -r uid role expected <<< "$spec"
  path="$legacy/roles/gateway-private"; file=server.key
  if [[ $role = garm ]]; then path="$target/private"; file=config.toml; fi
  testcmd="cat /other-role/$file >/dev/null 2>&1"
  if [[ $expected = no ]]; then testcmd="! $testcmd"; fi
  docker run --rm "${base[@]}" --user "$uid:$uid" --mount "type=bind,src=$path,dst=/other-role,readonly" --entrypoint /bin/sh "$image" -ec "$testcmd"
done
start "$target" 65531
probe "$target" 65531
boundary 65531
stop
helper "$target" 65531 guard
start "$target" 65531
probe "$target" 65531
boundary 65531
stop
helper "$target" 65531 guard
# Guard negatives run the same guard+exec launcher. A false guard PASS launches
# Garm, and the non-exited container is a failure, never timeout-based success.
for kind in missing-main missing-blob mode owner symlink hardlink wrong-controller changed-config interrupted-copy wrong-passphrase wrong-jwt wrong-ca wrong-tls-key; do
  bad="$work/garm-synthetic-storage-negative-$kind"
  sudo python3 deploy/runtime/garm-migration/state.py negative "$target" "$bad" "$kind"
  docker run -d --name "$name-negative" "${base[@]}" --user 65531:65531 \
    --tmpfs /tmp:rw,noexec,nosuid,nodev,size=16m,uid=65531,gid=65531,mode=0700 \
    --mount "type=bind,src=$bad/private,dst=$root/private,readonly" --mount "type=bind,src=$bad/state,dst=$root/state" \
    --entrypoint /bin/sh "$image" -ec "umask 077; /usr/local/bin/garm-migration-lab guard $root; exec /usr/local/bin/garm -config $root/private/config.toml" >/dev/null
  for _ in {1..40}; do [[ $(docker inspect --format '{{.State.Running}}' "$name-negative") = false ]] && break; sleep .25; done
  [[ $(docker inspect --format '{{.State.Running}}' "$name-negative") = false ]]
  [[ $(docker inspect --format '{{.State.ExitCode}}' "$name-negative") = 1 ]]
  docker rm "$name-negative" >/dev/null
  if [[ $kind = missing-main ]]; then sudo test ! -e "$bad/state/controller.db"; fi
  if [[ $kind = missing-blob ]]; then sudo test ! -e "$bad/state/blob-controller.db"; fi
done
# Reversible LAB rollback only: new writer stopped; untouched original retained.
sudo python3 deploy/runtime/garm-migration/state.py unchanged "$legacy" "$work/original.json"
start "$legacy" 0
probe "$legacy" 0
boundary 0
stop
helper "$legacy" 0 guard
python3 - "$image" "$commit" "$patch" "$work/bases.json" "$results/garm-state-transfer.json" "$work/build/source" <<'PY'
import json,pathlib,subprocess,sys
image,commit,patch,bases,out,source=sys.argv[1:]
import hashlib
artifacts=subprocess.check_output(['docker','run','--rm','--network','none','--read-only','--cap-drop','ALL','--security-opt','no-new-privileges','--user','65531:65531','--pids-limit','64','--memory','128m','--cpus','1','--entrypoint','/bin/sh',image,'-ec','ldd /usr/local/bin/garm >/dev/null; sha256sum /usr/local/bin/garm /usr/local/bin/garm-migration-lab; getconf GNU_LIBC_VERSION'],text=True).splitlines()
assert len(artifacts)==3
source_hashes={n:hashlib.sha256((pathlib.Path(source)/n).read_bytes()).hexdigest() for n in ('go.mod','go.sum','vendor/modules.txt')}
info=json.loads(subprocess.check_output(['docker','image','inspect',image]))[0]
record={'controller_source':subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip(),'garm_source':commit,'source_patch_sha256':patch,'lab_image_id':info['Id'],'platform':info['Os']+'/'+info['Architecture'],'base_images':json.loads(pathlib.Path(bases).read_text()),'toolchain':'go1.26.10','garm_binary_sha256':artifacts[0].split()[0],'helper_binary_sha256':artifacts[1].split()[0],'runtime_libc':artifacts[2],'source_dependency_locks':source_hashes,'generated_populated_root_to_nonroot_transfer_passed':True,'both_databases_preserved':True,'private_bytes_preserved':True,'guard_negatives_passed':True,'lab_recreation_and_untouched_original_rollback_passed':True,'installed_stack_qualified':False,'deployment_authorized':False,'provider_integration_qualified':False,'availability_qualified':False,'scope':'Disposable patched-Garm TEST image, generated fake credentials/state only; no real account, provider, job, live migration or image publication.'}
pathlib.Path(out).write_text(json.dumps(record,indent=2))
print('PASS generated patched-Garm state transfer only; installed deployment and availability remain BLOCKED')
PY
