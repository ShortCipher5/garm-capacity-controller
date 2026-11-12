#!/usr/bin/env bash
# Hosted disposable proof only; no installed config, volumes, endpoints or credentials.
set -euo pipefail
cd "$(dirname "$0")/../.."
: "${RUNNER_TEMP:?A disposable GitHub-hosted runner workspace is required}"
[[ ${GITHUB_ACTIONS:-} = true && ${RUNNER_ENVIRONMENT:-} = github-hosted ]] || { echo 'Refusing non-hosted execution' >&2; exit 1; }
image=ghcr.io/cloudbase/garm:v0.2.1@sha256:5ccdc29c31084ed4f30ec85e09a57b5105cf3dcc5fa8c1aba9e68de461e7e635
python_image=python:3.13-alpine@sha256:7984cf56ceaddebe4fefeb75d64000b9f45d8b77b11a09b24524da6cdc3af0c8
name="garm-identity-lab-${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}"
work=$(mktemp -d "$RUNNER_TEMP/garm-identity.XXXXXXXX")
results=$PWD/qualification-results
mkdir -p "$results"
cleanup() {
  local outcome=$?
  if [[ $outcome != 0 ]]; then
    docker inspect "$name" 2>/dev/null | python3 -c 'import json,sys;x=json.load(sys.stdin)[0];print(json.dumps({"status":x["State"]["Status"],"exit":x["State"]["ExitCode"],"oom":x["State"]["OOMKilled"]}))' || true
  fi
  docker rm -f "$name" >/dev/null 2>&1 || true
  sudo rm -rf -- "$work"
  return "$outcome"
}
trap cleanup EXIT

docker pull "$image"
docker pull "$python_image"
python3 deploy/runtime/garm-identity.py prepare "$work/fixture"
sudo chown -R 65531:65531 "$work/fixture/private" "$work/fixture/state"
sudo chown -R 65532:65532 "$work/fixture/gateway-canary"
base=(--read-only --cap-drop ALL --security-opt no-new-privileges --pids-limit 256 --cpus 1 --memory 512m)
# Only harmless discovery, with no provider config or network, from the exact image.
docker run --rm --network none "${base[@]}" --user 65531:65531 --env GARM_COMMAND=GetVersion --entrypoint /opt/garm/providers.d/garm-provider-incus "$image" >/dev/null
# Positive owner-read controls prove the paths/mounts work before denial tests.
docker run --rm --network none "${base[@]}" --user 65532:65532 --mount "type=bind,src=$work/fixture/gateway-canary,dst=/other-role,readonly" --entrypoint /bin/sh "$image" -ec 'cat /other-role/canary >/dev/null 2>&1'
docker run --rm --network none "${base[@]}" --user 65531:65531 --mount "type=bind,src=$work/fixture/private,dst=/other-role,readonly" --entrypoint /bin/sh "$image" -ec 'cat /other-role/config.toml >/dev/null 2>&1'
# Even an accidentally mounted other-role directory must be unreadable by DAC.
# No canary bytes are printed, including on an unexpected permission failure.
docker run --rm --network none "${base[@]}" --user 65531:65531 --mount "type=bind,src=$work/fixture/gateway-canary,dst=/other-role,readonly" --entrypoint /bin/sh "$image" -ec '! cat /other-role/canary >/dev/null 2>&1'
docker run --rm --network none "${base[@]}" --user 65532:65532 --mount "type=bind,src=$work/fixture/private,dst=/other-role,readonly" --entrypoint /bin/sh "$image" -ec '! cat /other-role/config.toml >/dev/null 2>&1'

start_garm() {
  # umask is explicit lab launcher setup, not an edit to the upstream image/live stack.
  # No bootstrap admin, repository, pool, credential or provider is configured.
  docker run -d --name "$name" --network none "${base[@]}" --user 65531:65531 \
    --tmpfs /tmp:rw,noexec,nosuid,nodev,size=16m,uid=65531,gid=65531,mode=0700 \
    --mount "type=bind,src=$work/fixture/private,dst=/run/garm-private,readonly" \
    --mount "type=bind,src=$work/fixture/state,dst=/var/lib/garm" \
    --entrypoint /bin/sh "$image" -ec 'umask 077; exec /bin/garm -config /run/garm-private/config.toml' >/dev/null
}
probe() {
  docker run --rm --network "container:$name" "${base[@]}" --user 65531:65531 \
    --mount "type=bind,src=$PWD/deploy/runtime/garm-identity.py,dst=/probe.py,readonly" \
    --mount "type=bind,src=$work/fixture/private/ca.pem,dst=/lab-ca.pem,readonly" \
    "$python_image" python /probe.py probe
}
verify_boundary() {
  docker inspect "$name" | python3 -c '
import json,sys
x=json.load(sys.stdin)[0];h=x["HostConfig"]
assert x["Config"]["User"]=="65531:65531"
assert h["ReadonlyRootfs"] and not h["Privileged"] and h["CapDrop"]==["ALL"] and not h["CapAdd"]
assert h["NetworkMode"]=="none" and not h["PortBindings"] and h["PidMode"]==""
assert h["RestartPolicy"]["Name"]=="no" and h["Memory"]>0 and h["PidsLimit"]==256 and h["NanoCpus"]>0
assert any(v in h["SecurityOpt"] for v in ("no-new-privileges", "no-new-privileges:true"))
assert all(p["Type"] in ("bind","tmpfs") for p in x["Mounts"])
assert set(h["Tmpfs"])=={"/tmp"}
m={p["Destination"]:p for p in x["Mounts"] if p["Type"]=="bind"}
assert set(m)=={"/run/garm-private","/var/lib/garm"}
assert not m["/run/garm-private"]["RW"] and m["/var/lib/garm"]["RW"]
print("PASS exact lab container user/capabilities/NNP/network/mount isolation")'
  # The shell launcher execs Garm. Check the actual daemon, not merely a helper UID.
  docker exec --user 65531:65531 "$name" /bin/sh -ec 'test "$(tr "\\000" " " </proc/1/cmdline)" = "/bin/garm -config /run/garm-private/config.toml "; cat /proc/1/status' | python3 -c '
import sys
fields=dict(line.split(":",1) for line in sys.stdin.read().splitlines() if ":" in line)
assert fields["Uid"].split()==["65531"]*4 and fields["Gid"].split()==["65531"]*4
for key in ("CapInh","CapPrm","CapEff","CapBnd","CapAmb"): assert int(fields[key].strip(),16)==0
assert fields["NoNewPrivs"].strip()=="1"
print("PASS actual Garm PID1 is UID/GID65531, all capability sets zero, NNP enabled")'
  docker exec --user 65531:65531 "$name" /bin/sh -ec '! touch /must-not-be-writable 2>/dev/null; test ! -e /var/run/docker.sock; test ! -e /var/lib/capacity-gateway; test ! -e /run/capacity-private'
}

start_garm
probe
verify_boundary
# Stop the ONLY disposable writer before inspecting Garm's own database.
docker stop --time 60 "$name" >/dev/null
[[ $(docker inspect --format '{{.State.ExitCode}}' "$name") = 0 ]]
sudo python3 - "$work/fixture/state" "$work/before.json" <<'PY'
import json,pathlib,sqlite3,sys
path=pathlib.Path(sys.argv[1])/'controller.db'
assert path.stat().st_uid==65531 and path.stat().st_mode&0o077==0
for child in path.parent.rglob('*'):
 assert not child.is_symlink() and child.stat().st_uid==65531 and child.stat().st_mode&0o077==0
connection=sqlite3.connect('file:'+str(path)+'?mode=ro',uri=True)
assert connection.execute('pragma integrity_check').fetchone()[0]=='ok'
rows=connection.execute('select controller_id from controller_infos').fetchall()
assert len(rows)==1
for table in ('repositories','organizations','enterprises','pools','instances','users'):
 assert connection.execute('select count(*) from '+table).fetchone()[0]==0
connection.close()
pathlib.Path(sys.argv[2]).write_text(json.dumps({'controller':rows[0][0],'inode':path.stat().st_ino}))
print('PASS NEW empty Garm state is private and contains no user/repository/pool/instance')
PY
# Recreate only the disposable container, never regenerate/adopt its database.
docker rm "$name" >/dev/null
start_garm
probe
verify_boundary
docker stop --time 60 "$name" >/dev/null
[[ $(docker inspect --format '{{.State.ExitCode}}' "$name") = 0 ]]
sudo python3 - "$work/fixture/state" "$work/before.json" <<'PY'
import json,pathlib,sqlite3,sys
path=pathlib.Path(sys.argv[1])/'controller.db';before=json.loads(pathlib.Path(sys.argv[2]).read_text())
connection=sqlite3.connect('file:'+str(path)+'?mode=ro',uri=True)
assert connection.execute('select controller_id from controller_infos').fetchone()[0]==before['controller']
assert path.stat().st_ino==before['inode']
connection.close()
print('PASS same Garm database/controller identity survives non-root container recreation')
PY
python3 - "$image" "$results/garm-nonroot-lab.json" <<'PY'
import json,pathlib,subprocess,sys
image,output=sys.argv[1:]
info=json.loads(subprocess.check_output(['docker','image','inspect',image]))[0]
record={'source_commit':subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip(),'image':image,'os':info['Os'],'architecture':info['Architecture'],'lab_uid':65531,'all_capability_sets_zero':True,'read_only_root':True,'network':'none','fresh_empty_state_startup_and_recreation_passed':True,'installed_stack_qualified':False,'deployment_authorized':False,'scope':'Stock public Garm image with generated empty state only; no live volumes, credentials, providers, jobs or migration.'}
pathlib.Path(output).write_text(json.dumps(record,indent=2))
print('PASS disposable non-root lab only; installed-stack qualification remains BLOCKED')
PY
