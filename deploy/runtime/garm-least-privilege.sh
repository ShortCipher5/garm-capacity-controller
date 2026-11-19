#!/usr/bin/env bash
# Hosted disposable proof only; no installed config, volumes, endpoints or credentials.
set -euo pipefail
cd "$(dirname "$0")/../.."
: "${RUNNER_TEMP:?A disposable GitHub-hosted runner workspace is required}"
[[ ${GITHUB_ACTIONS:-} = true && ${RUNNER_ENVIRONMENT:-} = github-hosted ]] || { echo 'Refusing non-hosted execution' >&2; exit 1; }
# Closed test variants only; no caller-supplied UID/path/runtime configuration.
joined=false
joined_gateway=
joined_ingress=
lab_uid=65531
specimen=standalone
workdir_args=()
result_name=garm-nonroot-lab.json
case "${1:-}" in
  "") [[ $# = 0 ]] || exit 1 ;;
  --prospective-service)
    [[ $# = 1 ]] || exit 1
    lab_uid=65530
    specimen=prospective-service
    workdir_args=(--workdir /)
    result_name=garm-prospective-service-lab.json
    ;;
  --joined-service)
    [[ $# = 4 ]] || exit 1
    joined=true
    gateway_image=$2; ingress_image=$3; ledger_image=$4
    for pinned in "$gateway_image" "$ingress_image" "$ledger_image"; do
      [[ $pinned =~ ^sha256:[0-9a-f]{64}$ ]] || exit 1
    done
    lab_uid=65530
    specimen=prospective-service
    workdir_args=(--workdir /)
    result_name=garm-joined-stock-lab.json
    ;;
  *) echo 'Unknown disposable specimen' >&2; exit 1 ;;
esac
image=ghcr.io/cloudbase/garm:v0.2.1@sha256:5ccdc29c31084ed4f30ec85e09a57b5105cf3dcc5fa8c1aba9e68de461e7e635
python_image=python:3.13-alpine@sha256:7984cf56ceaddebe4fefeb75d64000b9f45d8b77b11a09b24524da6cdc3af0c8
name="garm-identity-lab-${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}-${specimen}"
work=$(mktemp -d "$RUNNER_TEMP/garm-identity.XXXXXXXX")
results=$PWD/qualification-results
mkdir -p "$results"
if [[ $joined = true ]]; then
  # Never leave a previous joined success artifact after a failed rerun.
  rm -f -- "$results/garm-three-service-preboot.json"
  joined_source_commit=$(git rev-parse HEAD)
fi
cleanup() {
  local outcome=$?
  if [[ $outcome != 0 ]]; then
    docker inspect "$name" 2>/dev/null | python3 -c 'import json,sys;items=json.load(sys.stdin);print(json.dumps({"container_not_created":True} if not items else {"status":items[0]["State"]["Status"],"exit":items[0]["State"]["ExitCode"],"oom":items[0]["State"]["OOMKilled"]}))' || true
  fi
  for child in "$joined_gateway" "$joined_ingress"; do
    if [[ -n $child ]]; then docker rm -f "$child" >/dev/null 2>&1 || true; fi
  done
  docker rm -f "$name" >/dev/null 2>&1 || true
  sudo rm -rf -- "$work"
  return "$outcome"
}
trap cleanup EXIT

python3 deploy/runtime/test_garm_native_executable.py
python3 deploy/runtime/test_garm_lifetime_evidence.py
python3 deploy/runtime/test_garm_service_specimen.py
python3 deploy/runtime/test_garm_joined.py
python3 deploy/runtime/test_garm_joined_evidence.py
docker pull "$image"
docker pull "$python_image"
# Validate the pulled platform metadata once; all execution below uses its ID.
stock_reference=$image
docker image inspect "$image" > "$work/stock-image.json"
python3 deploy/runtime/garm-native-executable.py "$work/stock-image.json" "$image" > "$results/garm-native-executable.json"
garm_executable=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["executable"])' "$results/garm-native-executable.json")
image=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["image_id"])' "$results/garm-native-executable.json")
python3 deploy/runtime/garm-identity.py prepare "$work/fixture"
# Snapshot ONLY public certificates while still runner-owned. Never read the
# role-owned private directory again when preparing joined roles.
if [[ $joined = true ]]; then
  python3 deploy/runtime/garm-joined.py snapshot-public "$work/fixture/private" "$work/stock-public"
fi
sudo chown -R "$lab_uid:$lab_uid" "$work/fixture/private" "$work/fixture/state"
sudo chown -R 65532:65532 "$work/fixture/gateway-canary"
base=(--read-only --cap-drop ALL --security-opt no-new-privileges --pids-limit 256 --cpus 1 --memory 512m)
# Garm version exits before config loading. Never construct an Incus provider
# merely for discovery: bundled providers may require config before GetVersion.
# Hash only a bounded regular native executable, refusing symlink path components.
garm_sha=$(docker run --rm --network none "${base[@]}" --user "$lab_uid:$lab_uid" --entrypoint /bin/sh "$image" -ec '
 p=$1; while test "$p" != /; do test ! -L "$p"; p=${p%/*}; test -n "$p" || p=/; done
 test -f "$1" && test -x "$1"
 size=$(stat -c %s "$1"); test "$size" -gt 0 && test "$size" -le 134217728
 test "$(stat -c %h "$1")" = 1
 sha256sum "$1"' probe "$garm_executable" | cut -d' ' -f1)
[[ $garm_sha =~ ^[0-9a-f]{64}$ ]]
docker run --rm --network none "${base[@]}" --user "$lab_uid:$lab_uid" --entrypoint "$garm_executable" "$image" --version >/dev/null
# Positive owner-read controls prove the paths/mounts work before denial tests.
docker run --rm --network none "${base[@]}" --user 65532:65532 --mount "type=bind,src=$work/fixture/gateway-canary,dst=/other-role,readonly" --entrypoint /bin/sh "$image" -ec 'cat /other-role/canary >/dev/null 2>&1'
docker run --rm --network none "${base[@]}" --user "$lab_uid:$lab_uid" --mount "type=bind,src=$work/fixture/private,dst=/other-role,readonly" --entrypoint /bin/sh "$image" -ec 'cat /other-role/config.toml >/dev/null 2>&1'
# Even an accidentally mounted other-role directory must be unreadable by DAC.
# No canary bytes are printed, including on an unexpected permission failure.
docker run --rm --network none "${base[@]}" --user "$lab_uid:$lab_uid" --mount "type=bind,src=$work/fixture/gateway-canary,dst=/other-role,readonly" --entrypoint /bin/sh "$image" -ec '! cat /other-role/canary >/dev/null 2>&1'
docker run --rm --network none "${base[@]}" --user 65532:65532 --mount "type=bind,src=$work/fixture/private,dst=/other-role,readonly" --entrypoint /bin/sh "$image" -ec '! cat /other-role/config.toml >/dev/null 2>&1'

start_garm() {
  # umask is explicit lab launcher setup, not an edit to the upstream image/live stack.
  # No bootstrap admin, repository, pool, credential or provider is configured.
  docker run -d --name "$name" --network none "${base[@]}" --user "$lab_uid:$lab_uid" "${workdir_args[@]}" \
    --tmpfs /tmp:rw,noexec,nosuid,nodev,size=16m,uid=$lab_uid,gid=$lab_uid,mode=0700 \
    --mount "type=bind,src=$work/fixture/private,dst=/run/garm-private,readonly" \
    --mount "type=bind,src=$work/fixture/state,dst=/var/lib/garm" \
    --entrypoint /bin/sh "$image" -ec 'umask 077; exec "$1" -config /run/garm-private/config.toml' probe "$garm_executable" >/dev/null
}
probe() {
  docker run --rm --network "container:$name" "${base[@]}" --user "$lab_uid:$lab_uid" \
    --mount "type=bind,src=$PWD/deploy/runtime/garm-identity.py,dst=/probe.py,readonly" \
    --mount "type=bind,src=$work/fixture/private/ca.pem,dst=/lab-ca.pem,readonly" \
    "$python_image" python /probe.py probe
}
# Exact NUL-delimited argv digest; space-rendered text alone is ambiguous.
expected_command_sha=$(python3 - "$garm_executable" <<'PYARGV'
import hashlib,sys
print(hashlib.sha256(b''.join(v.encode()+b'\0' for v in [sys.argv[1],'-config','/run/garm-private/config.toml'])).hexdigest())
PYARGV
)
verify_boundary() {
  docker inspect "$name" | python3 -c '
import json,sys
x=json.load(sys.stdin)[0];h=x["HostConfig"]
assert x["Config"]["User"]==sys.argv[1]+":"+sys.argv[1]
if sys.argv[2]=="prospective-service": assert x["Config"]["WorkingDir"]=="/"
assert h["ReadonlyRootfs"] and not h["Privileged"] and h["CapDrop"]==["ALL"] and not h["CapAdd"]
assert h["NetworkMode"]=="none" and not h["PortBindings"] and h["PidMode"]==""
assert h["RestartPolicy"]["Name"]=="no" and h["Memory"]>0 and h["PidsLimit"]==256 and h["NanoCpus"]>0
assert any(v in h["SecurityOpt"] for v in ("no-new-privileges", "no-new-privileges:true"))
assert all(p["Type"] in ("bind","tmpfs") for p in x["Mounts"])
assert set(h["Tmpfs"])=={"/tmp"}
m={p["Destination"]:p for p in x["Mounts"] if p["Type"]=="bind"}
assert set(m)=={"/run/garm-private","/var/lib/garm"}
assert not m["/run/garm-private"]["RW"] and m["/var/lib/garm"]["RW"]
print("PASS exact lab container user/capabilities/NNP/network/mount isolation")' "$lab_uid" "$specimen"
  # The shell launcher execs Garm. Check the actual daemon, not merely a helper UID.
  docker exec --user "$lab_uid:$lab_uid" "$name" /bin/sh -ec '
 lifetime() {
   raw=$(head -c 8193 /proc/1/stat) || return 1
   test "${#raw}" -le 8192 || return 1
   case "$raw" in "1 ("*") "*) ;; *) return 1;; esac
   tail=${raw##*) }; start=$(printf "%s" "$tail" | cut -d " " -f 20)
   case "$start" in ""|*[!0-9]*) return 1;; esac
   test "$start" -gt 0 || return 1; printf "%s" "$start"
 }
 before=$(lifetime)
 if test "$4" = prospective-service; then test "$(readlink /proc/1/cwd)" = /; fi
 test "$(sha256sum /proc/1/cmdline | cut -d" " -f1)" = "$3"
 test "$(tr "\\000" " " </proc/1/cmdline)" = "$1 -config /run/garm-private/config.toml "
 test "$(readlink /proc/1/exe)" = "$1"
 test "$(stat -Lc %d:%i /proc/1/exe)" = "$(stat -c %d:%i "$1")"
 test "$(sha256sum /proc/1/exe | cut -d" " -f1)" = "$2"
 status=$(cat /proc/1/status)
 after=$(lifetime); test "$before" = "$after"
 printf "%s\n" "$status"' probe "$garm_executable" "$garm_sha" "$expected_command_sha" "$specimen" | python3 -c '
import sys
fields=dict(line.split(":",1) for line in sys.stdin.read().splitlines() if ":" in line)
assert fields["Uid"].split()==[sys.argv[1]]*4 and fields["Gid"].split()==[sys.argv[1]]*4
for key in ("CapInh","CapPrm","CapEff","CapBnd","CapAmb"): assert int(fields[key].strip(),16)==0
assert fields["NoNewPrivs"].strip()=="1"
print("PASS actual Garm PID1 has expected UID/GID, all capability sets zero, NNP enabled")' "$lab_uid"
  docker exec --user "$lab_uid:$lab_uid" "$name" /bin/sh -ec '! touch /must-not-be-writable 2>/dev/null; test ! -e /var/run/docker.sock; test ! -e /var/lib/capacity-gateway; test ! -e /run/capacity-private'
}

start_garm
probe
verify_boundary
if [[ $joined = true ]]; then source deploy/runtime/garm-joined.sh; fi
# Stop the ONLY disposable writer before inspecting Garm's own database.
docker stop --time 60 "$name" >/dev/null
[[ $(docker inspect --format '{{.State.ExitCode}}' "$name") = 0 ]]
sudo python3 - "$work/fixture/state" "$work/before.json" "$lab_uid" <<'PY'
import json,pathlib,sqlite3,sys
path=pathlib.Path(sys.argv[1])/'controller.db'
assert path.stat().st_uid==int(sys.argv[3]) and path.stat().st_mode&0o077==0
for child in path.parent.rglob('*'):
 assert not child.is_symlink() and child.stat().st_uid==int(sys.argv[3]) and child.stat().st_mode&0o077==0
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
sudo python3 - "$work/fixture/state" "$work/before.json" "$lab_uid" <<'PY'
import json,pathlib,sqlite3,sys
path=pathlib.Path(sys.argv[1])/'controller.db';before=json.loads(pathlib.Path(sys.argv[2]).read_text())
for child in path.parent.rglob('*'):
 assert not child.is_symlink() and child.stat().st_uid==int(sys.argv[3]) and child.stat().st_mode&0o077==0
connection=sqlite3.connect('file:'+str(path)+'?mode=ro',uri=True)
assert connection.execute('select controller_id from controller_infos').fetchone()[0]==before['controller']
assert path.stat().st_ino==before['inode']
connection.close()
print('PASS same Garm database/controller identity survives non-root container recreation')
PY
python3 - "$image" "$results/$result_name" "$stock_reference" "$garm_executable" "$garm_sha" "$lab_uid" "$specimen" <<'PY'
import hashlib,json,pathlib,subprocess,sys
image,output,reference,executable,sha,uid,specimen=sys.argv[1:]
info=json.loads(subprocess.check_output(['docker','image','inspect',image]))[0]
record={'source_commit':subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip(),'image':image,'pinned_stock_reference':reference,'native_executable':executable,'executable_sha256':sha,'runtime_executable_identity_verified':True,'os':info['Os'],'architecture':info['Architecture'],'lab_uid':int(uid),'all_capability_sets_zero':True,'read_only_root':True,'network':'none','fresh_empty_state_startup_and_recreation_passed':True,'installed_stack_qualified':False,'deployment_authorized':False,'scope':'Stock public Garm image with generated empty state only; no live volumes, credentials, providers, jobs or migration.'}
# Hash argv as exact UTF-8 arguments, each terminated by NUL. Do not expose
# private fixture paths or raw PID/inode/start-time values in public artifacts.
native=info['Config']['Entrypoint']
assert len(native)==3 and native[0]==executable and native[1]=='-config'
observed=[executable,'-config','/run/garm-private/config.toml']
def argv_hash(argv):
 return hashlib.sha256(b''.join(v.encode('utf-8')+b'\0' for v in argv)).hexdigest()
record.update(native_image_entrypoint_sha256=argv_hash(native),
 observed_test_command_sha256=argv_hash(observed),
 command_hash_encoding='utf8-argv-nul-terminated',
 observed_test_command_verified=True,
 native_image_entrypoint_is_test_command=(native==observed),
 process_lifetime_consistent_during_checks=True,
 process_lifetime_scope='container-local samples within each startup; not continuity across recreation',
 native_default_config_startup_verified=False,
 specimen=specimen,
 explicit_working_directory_verified=(specimen=='prospective-service'),
 private_created_state_modes_verified=True,
 fixture_launcher_umask_077=True,
 direct_executable_launch_qualified=False,
 managed_topology_qualified=False,
 startup_authority=False)
pathlib.Path(output).write_text(json.dumps(record,indent=2))
print('PASS disposable non-root lab only; installed-stack qualification remains BLOCKED')
PY

if [[ $joined = true ]]; then
  # Joined evidence is success-only: recreation and final removal must complete.
  docker rm "$name" >/dev/null
  sudo rm -rf -- "$work"
  trap - EXIT
  [[ $(git rev-parse HEAD) = "$joined_source_commit" ]]
  python3 deploy/runtime/garm-joined-evidence.py "$results/garm-three-service-preboot.json" \
    "$joined_source_commit" "$GITHUB_RUN_ID" "$GITHUB_RUN_ATTEMPT" \
    "$image" "$gateway_image" "$ingress_image" "$ledger_image" \
    "$gateway_executable_sha" "$ingress_executable_sha"
fi
