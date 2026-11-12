#!/usr/bin/env bash
# Disposable GitHub-hosted qualification only. Never run against a live stack.
set -euo pipefail
cd "$(dirname "$0")/../.."
repo=$PWD
command -v docker >/dev/null
docker compose version >/dev/null
: "${RUNNER_TEMP:?This harness requires a disposable hosted runner workspace}"
work=$(mktemp -d "$RUNNER_TEMP/capacity-packaging.XXXXXXXX")
results="$repo/qualification-results"
mkdir -p "$results"
project="capacity-packaging-${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-1}"
export CAPACITY_GATEWAY_IMAGE=capacity-gateway:qualification
export CAPACITY_PRIVATE_DIR="$work/fixtures/gateway-private"
export CAPACITY_STATE_DIR="$work/fixtures/state"
export PACKAGING_FIXTURE_PRIVATE="$work/fixtures/fixture-private"
export PACKAGING_FIXTURE_SCRIPT="$repo/deploy/runtime/fixture.py"
compose=(docker compose -p "$project" -f deploy/runtime/compose.yaml -f deploy/compose.gateway.yaml --profile capacity-preview)
cleanup() {
  # Only our generated project. No existing stack/volumes and no global prune.
  "${compose[@]}" down --remove-orphans >/dev/null 2>&1 || true
  sudo rm -rf -- "$work"
}
trap cleanup EXIT

# Resolve every moving production base to a digest before building. No login or secrets.
python3 - "$work/locked.Dockerfile" "$results/base-images.json" <<'PY'
import json, pathlib, re, subprocess, sys
text = pathlib.Path('Dockerfile').read_text()
locks = {}
reviewed = {'node:22.23.4-bookworm-slim', 'golang:1.26.10-bookworm', 'debian:bookworm-slim'}
aliases, external = set(), set()
for line in text.splitlines():
    if not re.match(r'(?i)^\s*from(?:\s|$)', line):
        continue
    match = re.fullmatch(r'FROM (\S+) AS ([a-z0-9-]+)', line)
    assert match, 'unreviewed FROM syntax'
    image, alias = match.groups()
    if image not in aliases and image != 'scratch':
        assert image in reviewed, 'unreviewed external base image'
        external.add(image)
    aliases.add(alias)
assert external == reviewed, 'review base image additions/removals explicitly'
for image in sorted(external):
    subprocess.run(['docker', 'pull', image], check=True)
    info = json.loads(subprocess.check_output(['docker', 'image', 'inspect', image]))[0]
    digest = info['RepoDigests'][0]
    assert '@sha256:' in digest
    locks[image] = digest
    assert text.count('FROM ' + image + ' AS ') == 1, 'base replacement is ambiguous'
    text = text.replace('FROM ' + image + ' AS ', 'FROM ' + digest + ' AS ')
pathlib.Path(sys.argv[1]).write_text(text)
pathlib.Path(sys.argv[2]).write_text(json.dumps(locks, indent=2))
PY
for target in gateway ledger-tool; do
  tag=capacity-gateway:qualification
  if [[ $target = ledger-tool ]]; then tag=capacity-ledger:qualification; fi
  docker build -f "$work/locked.Dockerfile" --target "$target" -t "$tag" .
done
docker build -f "$work/locked.Dockerfile" --target provider-artifact --output "type=local,dest=$work/provider" .

restricted=(--rm --network none --read-only --cap-drop ALL --security-opt no-new-privileges --pids-limit 128 --cpus 1 --memory 512m --user 65532:65532)
docker run "${restricted[@]}" capacity-ledger:qualification --help >/dev/null
expect_exit() {
  local expected=$1; shift
  local got=0
  "$@" >/dev/null 2>&1 || got=$?
  [[ $got = "$expected" ]] || { echo "Unexpected disposable exit status $got, wanted $expected" >&2; return 1; }
}
expect_exit 2 docker run "${restricted[@]}" capacity-ledger:qualification restore --source /absent/source.db --destination /absent/restore.db
expect_exit 2 docker run "${restricted[@]}" capacity-ledger:qualification invalid
expect_exit 1 docker run "${restricted[@]}" capacity-gateway:qualification
# Verify image user, capabilities, NNP and rootfs behavior rather than just YAML.
docker run "${restricted[@]}" --entrypoint /bin/sh capacity-gateway:qualification -ec '
  test "$(id -u)" = 65532
  grep -Eq "^CapEff:[[:space:]]+0+$" /proc/self/status
  grep -Eq "^NoNewPrivs:[[:space:]]+1$" /proc/self/status
  ! touch /must-not-be-writable 2>/dev/null
  test ! -e /usr/local/bin/capacity-ledger
  test -f /usr/local/share/capacity-admin/index.html
'
python3 deploy/runtime/fixture.py prepare "$work/fixtures"
# Numeric ownership changes apply ONLY to generated disposable fixture directories.
sudo chown -R 65532:65532 "$work/fixtures/gateway-private" "$work/fixtures/state" "$work/fixtures/backups" "$work/fixtures/bad-private" "$work/fixtures/init-private" "$work/fixtures/empty-state" "$work/fixtures/other-state" "$work/fixtures/archive-state" "$work/fixtures/restored-state"
sudo chown -R 65531:65531 "$work/fixtures/fixture-private"
sudo chmod 0444 "$work/fixtures/bad-private/gateway.local.json"
expect_exit 1 docker run "${restricted[@]}" --mount "type=bind,src=$work/fixtures/bad-private,dst=/run/capacity-private,readonly" capacity-gateway:qualification
expect_exit 1 docker run "${restricted[@]}" --user 65531:65531 --mount "type=bind,src=$CAPACITY_PRIVATE_DIR,dst=/run/capacity-private,readonly" capacity-gateway:qualification

# Explicit NEW-only initialization is test setup, never a startup/restart hook.
# The initializer sees only standalone policy/identity output, not TLS credentials.
docker run "${restricted[@]}" --mount "type=bind,src=$work/fixtures/init-private,dst=/identity" --mount "type=bind,src=$CAPACITY_STATE_DIR,dst=/state" capacity-ledger:qualification initialize --destination /state/ledger.db --identity-file /identity/ledger.identity --policy /identity/initial-policy.json
sudo install -o 65532 -g 65532 -m 0600 "$work/fixtures/init-private/ledger.identity" "$CAPACITY_PRIVATE_DIR/ledger.identity"
expect_exit 1 docker run "${restricted[@]}" --mount "type=bind,src=$work/fixtures/init-private,dst=/identity" --mount "type=bind,src=$CAPACITY_STATE_DIR,dst=/state" capacity-ledger:qualification initialize --destination /state/ledger.db --identity-file /identity/ledger.identity --policy /identity/initial-policy.json
# A second independently initialized ledger must not match the first identity.
docker run "${restricted[@]}" --mount "type=bind,src=$work/fixtures/init-private,dst=/identity" --mount "type=bind,src=$work/fixtures/other-state,dst=/state" capacity-ledger:qualification initialize --destination /state/ledger.db --identity-file /identity/other.identity --policy /identity/initial-policy.json
for rejected in empty-state other-state; do
  expect_exit 1 docker run "${restricted[@]}" --mount "type=bind,src=$CAPACITY_PRIVATE_DIR,dst=/run/capacity-private,readonly" --mount "type=bind,src=$work/fixtures/$rejected,dst=/var/lib/capacity-gateway" capacity-gateway:qualification
done
sudo python3 - "$work/fixtures" <<'PYVERIFY'
from pathlib import Path
import sys
root=Path(sys.argv[1])
assert list((root/'empty-state').iterdir())==[]
assert sorted(p.name for p in (root/'other-state').iterdir())==['ledger.db']
print('PASS missing/wrong initialized ledger rejected without WAL or auto-initialization')
PYVERIFY

# No output of rendered Compose config, inspect ENV, config, keys or private fixture files.
"${compose[@]}" config --quiet
"${compose[@]}" up -d garm
"${compose[@]}" up -d capacity-gateway
"${compose[@]}" exec -T garm python /fixture.py probe
"${compose[@]}" run --rm peer
verify_topology() {
  local anchor gateway
  anchor=$("${compose[@]}" ps -q garm)
  gateway=$("${compose[@]}" ps -q capacity-gateway)
  docker inspect "$anchor" "$gateway" | python3 -c '
import json,sys
anchor,gateway=json.load(sys.stdin)
for item,uid in ((anchor,"65531:65531"),(gateway,"65532:65532")):
 h=item["HostConfig"]
 assert item["Config"]["User"]==uid and h["ReadonlyRootfs"] and not h["Privileged"]
 assert h["CapDrop"]==["ALL"] and not h["CapAdd"]
 assert "no-new-privileges:true" in h["SecurityOpt"] or "no-new-privileges" in h["SecurityOpt"]
 assert not h["PortBindings"] and h["PidMode"]=="" and h["RestartPolicy"]["Name"]=="no"
 assert h["PidsLimit"]==128 and h["Memory"]>0 and h["NanoCpus"]>0
assert gateway["HostConfig"]["NetworkMode"]=="container:"+anchor["Id"]
mounts={m["Destination"]:m for m in gateway["Mounts"]}
assert set(mounts)=={"/run/capacity-private","/var/lib/capacity-gateway"}
assert not mounts["/run/capacity-private"]["RW"] and mounts["/var/lib/capacity-gateway"]["RW"]
assert all(m["Type"]=="bind" for m in mounts.values())
print("PASS actual separate UID/capability/mount/network namespace configuration")'
}
verify_topology
# Select an exact committed audit witness absent from the immutable main file.
# This test-only immutable read never supplies allocation/admission decisions.
sudo python3 - "$CAPACITY_STATE_DIR" "$work/wal-witness.json" <<'PYVERIFY'
import json,pathlib,sqlite3,sys
path=pathlib.Path(sys.argv[1])/'ledger.db'
assert pathlib.Path(str(path)+'-wal').stat().st_size>0
main=sqlite3.connect('file:'+str(path)+'?mode=ro&immutable=1',uri=True)
main_sequence=main.execute('select coalesce(max(sequence),0) from audit').fetchone()[0]
main.close()
current=sqlite3.connect('file:'+str(path)+'?mode=ro',uri=True)
witness=current.execute('select sequence,kind,request_key,recorded_at from audit where sequence>? order by sequence limit 1',(main_sequence,)).fetchone()
assert witness is not None, 'no committed WAL-only audit witness established'
current.close()
pathlib.Path(sys.argv[2]).write_text(json.dumps(witness))
print('PASS committed runtime audit witness is present in WAL but absent from main DB')
PYVERIFY
# Actual CLI online backup sees the whole source directory, including live WAL/SHM.
docker run "${restricted[@]}" \
  --mount "type=bind,src=$CAPACITY_STATE_DIR,dst=/source,readonly" \
  --mount "type=bind,src=$work/fixtures/archive-state,dst=/destination" \
  capacity-ledger:qualification backup --source /source/ledger.db --destination /destination/ledger.db
# Close the checkpoint race: witness must STILL be absent from main immediately
# after successful backup. Append-only audit makes a main-only copy insufficient.
sudo python3 - "$CAPACITY_STATE_DIR" "$work/wal-witness.json" <<'PYVERIFY'
import json,pathlib,sqlite3,sys
path=pathlib.Path(sys.argv[1])/'ledger.db';witness=json.loads(pathlib.Path(sys.argv[2]).read_text())
main=sqlite3.connect('file:'+str(path)+'?mode=ro&immutable=1',uri=True)
assert main.execute('select coalesce(max(sequence),0) from audit').fetchone()[0]<witness[0], 'WAL witness checkpointed before backup proof completed'
main.close()
print('PASS post-backup main still excludes the committed WAL witness')
PYVERIFY
expect_exit 1 docker run "${restricted[@]}" --mount "type=bind,src=$CAPACITY_STATE_DIR,dst=/source,readonly" --mount "type=bind,src=$work/fixtures/archive-state,dst=/destination" capacity-ledger:qualification backup --source /source/ledger.db --destination /destination/ledger.db
# Fencing is simulated only by stopping the disposable writer. Never a live claim.
"${compose[@]}" stop capacity-gateway
docker run "${restricted[@]}" --mount "type=bind,src=$work/fixtures/archive-state,dst=/source,readonly" --mount "type=bind,src=$work/fixtures/restored-state,dst=/destination" capacity-ledger:qualification restore --source /source/ledger.db --destination /destination/ledger.db --old-writers-fenced
sudo python3 - "$work/fixtures" "$work/wal-witness.json" <<'PYVERIFY'
import json,pathlib,sqlite3,sys
root=pathlib.Path(sys.argv[1]);witness=tuple(json.loads(pathlib.Path(sys.argv[2]).read_text()))
for directory,kind in (('archive-state','archive'),('restored-state','restored')):
 path=root/directory/'ledger.db'
 assert path.stat().st_uid==65532 and path.stat().st_mode&0o077==0
 db=sqlite3.connect('file:'+str(path)+'?mode=ro',uri=True)
 assert db.execute('pragma integrity_check').fetchone()[0]=='ok'
 assert db.execute('select ready from metadata').fetchone()[0]==0
 assert db.execute('select runtime_kind from ledger_lineage').fetchone()[0]==kind
 assert db.execute('select sequence,kind,request_key,recorded_at from audit where sequence=?',(witness[0],)).fetchone()==witness
 db.close()
print('PASS actual container online backup/restore integrity and permanent runtime-disabled kind')
PYVERIFY
for rejected in archive-state restored-state; do
  expect_exit 1 docker run "${restricted[@]}" --mount "type=bind,src=$CAPACITY_PRIVATE_DIR,dst=/run/capacity-private,readonly" --mount "type=bind,src=$work/fixtures/$rejected,dst=/var/lib/capacity-gateway" capacity-gateway:qualification
done
sudo python3 - "$work/fixtures" <<'PYVERIFY'
from pathlib import Path
import sys
root=Path(sys.argv[1])
for directory in ('archive-state','restored-state'):
 assert sorted(p.name for p in (root/directory).iterdir())==['ledger.db']
print('PASS matching-lineage archives/restores cannot start gateway or create WAL/SHM')
PYVERIFY
sudo python3 - "$CAPACITY_STATE_DIR" "$work/before.json" <<'PYVERIFY'
import json,pathlib,sqlite3,sys
path=pathlib.Path(sys.argv[1])/'ledger.db'
db=sqlite3.connect('file:'+str(path)+'?mode=ro',uri=True)
record={'lineage':db.execute('select lineage_id from ledger_lineage').fetchone()[0],'audit':db.execute('select count(*) from audit').fetchone()[0],'inode':path.stat().st_ino}
pathlib.Path(sys.argv[2]).write_text(json.dumps(record))
PYVERIFY
old_anchor=$("${compose[@]}" ps -q garm)
"${compose[@]}" up -d --force-recreate garm
"${compose[@]}" up -d --force-recreate capacity-gateway
[[ $("${compose[@]}" ps -q garm) != "$old_anchor" ]]
"${compose[@]}" exec -T garm python /fixture.py probe
verify_topology
sudo python3 - "$CAPACITY_STATE_DIR" "$work/before.json" <<'PYVERIFY'
import json,pathlib,sqlite3,sys
path=pathlib.Path(sys.argv[1])/'ledger.db';before=json.loads(pathlib.Path(sys.argv[2]).read_text())
db=sqlite3.connect('file:'+str(path)+'?mode=ro',uri=True)
assert db.execute('pragma journal_mode').fetchone()[0]=='wal'
assert db.execute('select lineage_id,runtime_kind from ledger_lineage').fetchone()==(before['lineage'],'active')
assert db.execute('select count(*) from audit').fetchone()[0]>=before['audit']
assert path.stat().st_ino==before['inode']
print('PASS original initialized ledger identity/file/audit survives namespace recreation')
PYVERIFY

# Probe exact PUBLIC upstream image without starting Garm or using any credentials.
# This does not copy a private installed configuration into CI.
export PACKAGING_GARM_IMAGE=ghcr.io/cloudbase/garm:v0.2.1@sha256:5ccdc29c31084ed4f30ec85e09a57b5105cf3dcc5fa8c1aba9e68de461e7e635
docker pull "$PACKAGING_GARM_IMAGE"
python3 - "$work/provider/capacity-provider" "$results" <<'PY'
import hashlib,json,os,pathlib,re,subprocess,sys
provider,results=map(pathlib.Path,sys.argv[1:]); image=os.environ['PACKAGING_GARM_IMAGE']
info=json.loads(subprocess.check_output(['docker','image','inspect',image]))[0]
run=['docker','run','--rm','--network','none','--read-only','--cap-drop','ALL','--security-opt','no-new-privileges','--pids-limit','64','--memory','128m','--cpus','1']
uid=subprocess.run(run+['--entrypoint','/bin/sh',image,'-c','id -u'],capture_output=True,text=True,timeout=30)
assert uid.returncode==0 and uid.stdout.strip().isdigit(), 'exact-image default UID probe failed'
probe=subprocess.run(run+['--user','65531:65531','--mount',f'type=bind,src={provider},dst=/capacity-provider,readonly','--env','GARM_COMMAND=GetSupportedInterfaceVersions','--entrypoint','/capacity-provider',image],capture_output=True,text=True,timeout=30)
compatible=probe.returncode==0 and json.loads(probe.stdout)==['v0.1.1']
elf=subprocess.check_output(['readelf','-l',str(provider)],text=True)
match=re.search(r'Requesting program interpreter: ([^\]]+)',elf)
assert match, 'expected CGO provider interpreter metadata'
interpreter=match.group(1)
loader=subprocess.run(run+['--entrypoint','/bin/sh',image,'-c','test -e "$1"','sh',interpreter],capture_output=True,timeout=30)
record={'image':image,'os':info['Os'],'architecture':info['Architecture'],'image_default_uid':int(uid.stdout.strip()),'provider_interpreter':interpreter,'provider_interpreter_present':loader.returncode==0,'provider_interface_executable':compatible,'provider_exit_status':probe.returncode,'installed_stack_qualified':False,'reason':'Synthetic checks cannot qualify existing UID/capability isolation, exclusive physical scope, writer fencing, lineage or recovery.'}
(results/'garm-compatibility.json').write_text(json.dumps(record,indent=2))
(results/'generic-runtime.json').write_text(json.dumps({'source_commit':subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip(),'gateway_image_id':json.loads(subprocess.check_output(['docker','image','inspect','capacity-gateway:qualification']))[0]['Id'],'ledger_tool_image_id':json.loads(subprocess.check_output(['docker','image','inspect','capacity-ledger:qualification']))[0]['Id'],'provider_sha256':hashlib.sha256(provider.read_bytes()).hexdigest(),'synthetic_gateway_checks_passed':True,'live_stack_changed':False,'deployment_authorized':False},indent=2))
print('Exact-image provider executable:',compatible,'; image default UID:',record['image_default_uid'])
print('Installed-stack qualification: BLOCKED; no provider or runtime change attempted')
PY
