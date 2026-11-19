# Sourced only by the guarded hosted stock specimen, while its verified stock
# container is running. No standalone entrypoint or caller-selected paths.
[[ $joined = true && $lab_uid = 65530 && $specimen = prospective-service ]]
[[ $GITHUB_ACTIONS = true && $RUNNER_ENVIRONMENT = github-hosted ]]
[[ $image = sha256:cb7c076ddbf24a67a9aef4a4e61ace495e9ffc7bcb69df3bf5e89275a26b79f6 ]]
[[ $garm_sha = 1fd77ef68b67f7afb45208473bcbb984ceb3d25e50721ed5104fcb20c930b95d ]]
joined_gateway="${name}-gateway"
joined_ingress="${name}-ingress"
python3 deploy/runtime/garm-joined.py prepare "$work/joined" "$work/stock-public"
sudo chown -R 65532:65532 "$work/joined/gateway-private" "$work/joined/state" "$work/joined/init-private" "$work/joined/probe-private"
sudo chown -R 65531:65531 "$work/joined/ingress-private"
# Existing explicit disposable ledger initializer; no TLS keys in this process.
docker run --rm --network none "${base[@]}" --user 65532:65532 \
  --mount "type=bind,src=$work/joined/init-private,dst=/identity" \
  --mount "type=bind,src=$work/joined/state,dst=/state" \
  "$ledger_image" initialize --destination /state/ledger.db --identity-file /identity/ledger.identity --policy /identity/initial-policy.json >/dev/null
sudo install -o 65532 -g 65532 -m 0600 "$work/joined/init-private/ledger.identity" "$work/joined/gateway-private/ledger.identity"
docker run -d --name "$joined_ingress" --network "container:$name" "${base[@]}" --user 65531:65531 --workdir / \
  --mount "type=bind,src=$work/joined/ingress-private,dst=/run/joined-ingress,readonly" \
  "$ingress_image" /usr/local/bin/capacity-stock-ingress /run/joined-ingress/ingress.json >/dev/null
docker run -d --name "$joined_gateway" --network "container:$name" "${base[@]}" --user 65532:65532 --workdir / \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=16m,uid=65532,gid=65532,mode=0700 \
  --mount "type=bind,src=$work/joined/gateway-private,dst=/run/capacity-private,readonly" \
  --mount "type=bind,src=$work/joined/state,dst=/var/lib/capacity-gateway" \
  "$gateway_image" >/dev/null
probe_mount=(--mount "type=bind,src=$PWD/deploy/runtime/garm-joined.py,dst=/joined.py,readonly")
docker run --rm --network "container:$name" "${base[@]}" --user 65532:65532 "${probe_mount[@]}" \
  --mount "type=bind,src=$work/joined/probe-private,dst=/joined-probe,readonly" \
  "$python_image" python /joined.py probe
docker run --rm --network none "${base[@]}" --user 65532:65532 "${probe_mount[@]}" "$python_image" python /joined.py outside
# Exact actual container identities/mounts; no ENV, private content or process IDs printed.
docker inspect "$name" "$joined_ingress" "$joined_gateway" | python3 -c '
import json,sys
xs=json.load(sys.stdin); assert len(xs)==3
assert len({x["State"]["Pid"] for x in xs})==3
for i,x in enumerate(xs):
 h=x["HostConfig"]
 assert x["State"]["Running"] and x["Config"]["User"]==["65530:65530","65531:65531","65532:65532"][i]
 assert x["Config"]["WorkingDir"]=="/" and h["ReadonlyRootfs"] and not h["Privileged"]
 assert h["CapDrop"]==["ALL"] and not h["CapAdd"] and not h["Devices"] and not h["PidMode"]
 assert not h["PortBindings"] and h["RestartPolicy"]["Name"]=="no"
 assert h["NetworkMode"]==("none" if i==0 else "container:"+xs[0]["Id"])
 assert any(v in h["SecurityOpt"] for v in ("no-new-privileges","no-new-privileges:true"))
 if i:
  mounts={m["Destination"]:m["RW"] for m in x["Mounts"] if m["Type"]=="bind"}
  assert mounts==({"/run/joined-ingress":False} if i==1 else {"/run/capacity-private":False,"/var/lib/capacity-gateway":True})
print("PASS actual distinct three-role containers share only isolated network")'
# Closed diagnostic vocabulary only. Never print sampled identities or names.
joined_check() {
  case "$1" in stock|ingress|gateway) ;; *) return 1 ;; esac
  case "$2" in network-read|network-equality|pid-read|pid-private|process-checks|stop|exit-contract|remove) ;; *) return 1 ;; esac
  printf 'Joined check: %s %s\n' "$1" "$2" >&2
}
# Inspect actual PID1 namespace/credentials, not only Docker configuration.
joined_check stock network-read
stock_net=$(docker exec --user 65530:65530 "$name" readlink /proc/1/ns/net)
joined_check stock pid-read
stock_pid=$(docker exec --user 65530:65530 "$name" readlink /proc/1/ns/pid)
previous_pid=$stock_pid
for role in ingress gateway; do
  child=$joined_ingress; uid=65531; exe=/usr/local/bin/capacity-stock-ingress
  if [[ $role = gateway ]]; then child=$joined_gateway; uid=65532; exe=/usr/local/bin/capacity-gateway; fi
  joined_check "$role" network-equality
  [[ $(docker exec --user "$uid:$uid" "$child" readlink /proc/1/ns/net) = "$stock_net" ]]
  joined_check "$role" pid-read
  child_pid=$(docker exec --user "$uid:$uid" "$child" readlink /proc/1/ns/pid)
  joined_check "$role" pid-private
  [[ $child_pid != "$stock_pid" && $child_pid != "$previous_pid" ]]
  previous_pid=$child_pid
  joined_check "$role" process-checks
  docker exec --user "$uid:$uid" "$child" /bin/sh -ec '
    case "$2" in ingress|gateway) ;; *) exit 1 ;; esac
    check() {
      case "$1" in executable|cwd|readonly-root|no-docker-socket|status-read) ;; *) exit 1 ;; esac
      printf "Joined process check: %s %s\n" "$2" "$1" >&2
    }
    check executable "$2"
    test "$(readlink /proc/1/exe)" = "$1"
    check cwd "$2"
    test "$(readlink /proc/1/cwd)" = /
    check readonly-root "$2"
    if touch /must-not-be-writable 2>/dev/null; then exit 1; fi
    check no-docker-socket "$2"
    test ! -e /var/run/docker.sock
    check status-read "$2"
    cat /proc/1/status' probe "$exe" "$role" | python3 -c '
import sys
fields=dict(line.split(":",1) for line in sys.stdin.read().splitlines() if ":" in line)
uid,role=sys.argv[1:]
assert role in ("ingress","gateway")
print("Joined process check:",role,"uid-gid",file=sys.stderr)
assert fields["Uid"].split()==[uid]*4 and fields["Gid"].split()==[uid]*4
print("Joined process check:",role,"capabilities",file=sys.stderr)
assert all(int(fields[k].strip(),16)==0 for k in ("CapInh","CapPrm","CapEff","CapBnd","CapAmb"))
print("Joined process check:",role,"no-new-privileges",file=sys.stderr)
assert fields["NoNewPrivs"].strip()=="1"' "$uid" "$role"
done
# Retire the children before stock recreation; never retain stale namespace users.
for role in gateway ingress; do
  child=$joined_gateway
  if [[ $role = ingress ]]; then child=$joined_ingress; fi
  joined_check "$role" stop
  docker stop --time 60 "$child" >/dev/null
  joined_check "$role" exit-contract
  [[ $(docker inspect --format '{{.State.ExitCode}}' "$child") = 0 ]] || [[ $child = "$joined_ingress" && $(docker inspect --format '{{.State.ExitCode}}' "$child") = 143 ]]
  joined_check "$role" remove
  docker rm "$child" >/dev/null
done
joined_gateway=; joined_ingress=
python3 - "$results/garm-three-service-preboot.json" "$image" "$gateway_image" "$ingress_image" <<'PY'
import json,pathlib,sys
pathlib.Path(sys.argv[1]).write_text(json.dumps({'schema':'disposable-three-service-preboot/v1',
 'stock_image':sys.argv[2], 'gateway_image':sys.argv[3], 'ingress_image':sys.argv[4],
 'actual_three_binaries_started':True,'shared_isolated_network':True,'host_ports_published':False,
 'stock_uninitialized':True,'supervisor_present':False,'admission_ready':False,
 'upstream_inventory_integrated':False,'managed_topology_qualified':False,
 'startup_authority':False,'deployment_authorized':False},indent=2))
PY
