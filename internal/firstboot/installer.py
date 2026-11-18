# Fixed first-boot program, executed only under separately approved full-admin
# authority. No caller-provided command/source/path is ever executed.
import base64, hashlib, ipaddress, json, os, pwd, grp, socket, stat, struct
import subprocess, sys, tempfile, time, fcntl, signal

ROOT = '/opt/garm-inventory-listener'
STATE = '/var/lib/garm-inventory-listener'
CONFIG = '/etc/garm-inventory-listener'
USER = 'garm-inventory-listener'
UNIT = 'garm-inventory-listener.service'
UNIT_PATH = '/etc/systemd/system/' + UNIT
TABLE = 'garm_inventory_listener'
PORT = 19922
MAX = 24 * 1024 * 1024
class Refused(Exception): pass

def require(ok, reason):
    if not ok: raise Refused(reason)

def run(args, data=None, timeout=10, accepted=(0,)):
    result = subprocess.run(args, input=data, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, timeout=timeout,
                            env={'PATH':'/usr/sbin:/usr/bin:/sbin:/bin','LC_ALL':'C'})
    require(len(result.stdout) <= 1024*1024 and len(result.stderr) <= 65536, 'tool-output-too-large')
    require(result.returncode in accepted, 'fixed-tool-failed')
    return result

def protected(path, directory=False):
    path = os.path.abspath(path)
    here = '/'
    for part in path.split('/'):
        if not part: continue
        here = os.path.join(here, part)
        s = os.lstat(here)
        require(s.st_uid == 0 and not stat.S_ISLNK(s.st_mode) and not s.st_mode & 0o022,
                'untrusted-path')
        if here != path or directory: require(stat.S_ISDIR(s.st_mode), 'directory-required')
    return s

def trusted_existing(path):
    pending=path.split('/'); current='/'; hops=0
    while pending:
        part=pending.pop(0)
        if part in ('','.'):continue
        if part=='..':current=os.path.dirname(current);continue
        candidate=os.path.join(current,part); info=os.lstat(candidate)
        require(info.st_uid==0,'untrusted-tool-owner')
        if stat.S_ISLNK(info.st_mode):
            hops+=1;require(hops<=40,'tool-link-cycle');target=os.readlink(candidate)
            if target.startswith('/'):current='/'
            pending=target.split('/')+pending;continue
        require(not info.st_mode&0o022,'untrusted-tool-mode')
        if pending:require(stat.S_ISDIR(info.st_mode),'tool-parent-directory')
        current=candidate
    return current

def mkdir(path):
    protected(os.path.dirname(path), True)
    try: os.mkdir(path, 0o700)
    except FileExistsError: pass
    protected(path, True)
    syncdir(os.path.dirname(path))

def syncdir(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try: os.fsync(fd)
    finally: os.close(fd)

def immutable(path, data, mode):
    protected(os.path.dirname(path), True)
    if os.path.lexists(path):
        s = protected(path)
        require(stat.S_ISREG(s.st_mode) and stat.S_IMODE(s.st_mode)==mode, 'existing-file-mode')
        require(not os.listxattr(path) or 'security.capability' not in os.listxattr(path), 'file-capabilities')
        with open(path,'rb') as f: require(f.read(len(data)+1)==data,'existing-file-content')
        syncdir(os.path.dirname(path)); return
    fd, temp = tempfile.mkstemp(prefix='.firstboot-', dir=os.path.dirname(path))
    try:
        with os.fdopen(fd,'wb') as f:
            f.write(data); f.flush(); os.fchmod(f.fileno(),mode); os.fsync(f.fileno())
        os.link(temp,path); syncdir(os.path.dirname(path))
    finally: os.unlink(temp)

def ipv4(value):
    a=ipaddress.IPv4Address(value)
    require(str(a)==value and not (a.is_unspecified or a.is_loopback or a.is_multicast or a.is_reserved),'invalid-ipv4')
    return str(a)

def public_key(value):
    parts=value.strip().split()
    require(len(parts)==2 and parts[0]=='ssh-ed25519','plain-ed25519-key-required')
    raw=base64.b64decode(parts[1],validate=True)
    require(len(raw)==51 and raw[:4]==struct.pack('>I',11) and raw[4:15]==b'ssh-ed25519' and raw[15:19]==struct.pack('>I',32),'invalid-public-key')
    return ' '.join(parts)+'\n'

def validate(request):
    require(set(request)=={'plan','review_digest','artifact'},'request-shape')
    p=request['plan']; require(set(p)=={'base','listen_address','installer_sha256','enable_at_boot','inventory_address','inventory_port','manifest'},'plan-shape')
    require(type(p['enable_at_boot']) is bool,'boot-choice-required')
    manifest=p['manifest'];require(set(manifest)=={'profile','architecture','artifact_sha256','installer_sha256'},'manifest-shape')
    require(manifest['profile']=='ubuntu-24.04-systemd-nftables/v1','manifest-profile')
    base=p['base']; require(set(base)=={'address','port','admin_user','host_key','admin_key_fingerprint','controller_ip','inventory_key','artifact_sha256','architecture'},'base-shape')
    require(manifest['architecture']==base['architecture'] and manifest['artifact_sha256']==base['artifact_sha256'] and manifest['installer_sha256']==p['installer_sha256'],'manifest-mismatch')
    require(base['architecture'] in ('amd64','arm64'),'unsupported-architecture')
    ipv4(base['address']); ipv4(base['controller_ip']); ipv4(p['listen_address']);ipv4(p['inventory_address'])
    require(type(p['inventory_port']) is int and 1<=p['inventory_port']<=65535,'inventory-endpoint-port')
    require(type(base['port']) is int and 1<=base['port']<=65535 and base['port']!=PORT,'admin-port-conflict')
    public_key(base['host_key']); public_key(base['inventory_key'])
    require(len(p['installer_sha256'])==64,'installer-review-missing')
    digest=hashlib.sha256(json.dumps(p,sort_keys=True,separators=(',',':'),ensure_ascii=False).encode()).hexdigest()
    require(digest==request['review_digest'],'review-mismatch')
    artifact=base64.b64decode(request['artifact'],validate=True)
    require(0<len(artifact)<=16*1024*1024 and hashlib.sha256(artifact).hexdigest()==base['artifact_sha256'],'artifact-mismatch')
    require(artifact[:7]==b'\x7fELF\x02\x01\x01' and len(artifact)>=64,'static-linux-artifact-required')
    require(struct.unpack('<H',artifact[18:20])[0]==({'amd64':62,'arm64':183}[base['architecture']]),'artifact-architecture')
    return p,digest,artifact

def ssh_config(plan):
    source=ipv4(plan['base']['controller_ip']); listen=ipv4(plan['listen_address'])
    return ('''Port 19922
AddressFamily inet
ListenAddress %s
HostKey /etc/garm-inventory-listener/host_key
PidFile /run/garm-inventory-listener/sshd.pid
AuthorizedKeysFile /etc/garm-inventory-listener/authorized_keys
AuthorizedKeysCommand none
TrustedUserCAKeys none
AuthorizedPrincipalsCommand none
AllowUsers garm-inventory-listener@%s
AuthenticationMethods publickey
PubkeyAuthentication yes
PasswordAuthentication no
KbdInteractiveAuthentication no
GSSAPIAuthentication no
HostbasedAuthentication no
PermitRootLogin no
PermitEmptyPasswords no
UsePAM yes
UseDNS no
PermitUserEnvironment no
PermitUserRC no
DisableForwarding yes
PermitTTY no
PermitTunnel no
X11Forwarding no
ForceCommand garm-inventory-v1
MaxAuthTries 2
MaxSessions 1
MaxStartups 4:50:8
LoginGraceTime 10
ClientAliveInterval 10
ClientAliveCountMax 1
LogLevel ERROR
''' % (listen,source)).encode()

GUARD_PROGRAM="import json,os,stat,subprocess,sys\nTABLE='garm_inventory_listener'\ndef require(value):\n if not value:raise RuntimeError('guard refused')\ndef run(args,data=None):\n r=subprocess.run(args,input=data,stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=10,env={'PATH':'/usr/sbin:/usr/bin:/sbin:/bin','LC_ALL':'C'})\n require(len(r.stdout)<=1048576 and len(r.stderr)<=65536);return r\ndef safe(path):\n here='/'\n for part in path.split('/'):\n  if not part:continue\n  here=os.path.join(here,part);s=os.lstat(here);require(s.st_uid==0 and not stat.S_ISLNK(s.st_mode) and not s.st_mode&0o022)\ndef normalize(items):\n out=[]\n for item in items:\n  if 'metainfo' in item:continue\n  for value in item.values():\n   if isinstance(value,dict):value.pop('handle',None)\n  out.append(item)\n return out\ntry:\n require(os.geteuid()==0);safe('/etc/garm-inventory-listener/firewall.json')\n safe('/etc/garm-inventory-listener/host_identity');safe('/etc/garm-inventory-listener/host_key')\n with open('/etc/garm-inventory-listener/host_identity','rb') as f: expected_key=f.read(4096)\n actual_key=run(['/usr/bin/ssh-keygen','-y','-f','/etc/garm-inventory-listener/host_key']);require(actual_key.returncode==0 and actual_key.stdout.strip()==expected_key.strip())\n with open('/etc/garm-inventory-listener/firewall.json') as f: expected=json.load(f)['rules']\n for unit in ['ufw.service','firewalld.service','nftables.service']:\n  a=run(['/usr/bin/systemctl','is-active',unit]);e=run(['/usr/bin/systemctl','is-enabled',unit]);require(a.returncode in (3,4) and e.stdout.strip() in (b'disabled',b'masked',b'not-found',b''))\n r=run(['/usr/sbin/nft','-j','list','table','inet',TABLE])\n if r.returncode!=0:\n  tables=run(['/usr/sbin/nft','-j','list','tables']);require(tables.returncode==0 and not any(i.get('table',{}).get('name')==TABLE for i in json.loads(tables.stdout)['nftables']))\n  require(run(['/usr/sbin/nft','-j','-f','-'],json.dumps({'nftables':[{'add':i} for i in expected]}).encode()).returncode==0)\n r=run(['/usr/sbin/nft','-j','list','table','inet',TABLE]);require(r.returncode==0 and normalize(json.loads(r.stdout)['nftables'])==expected)\nexcept Exception:sys.exit(1)\n"

SERVICE=b'''[Unit]
Description=Isolated GARM read-only inventory SSH listener
After=network-online.target
Wants=network-online.target
[Service]
Type=notify
NotifyAccess=main
ExecStartPre=/usr/bin/python3 -I /opt/garm-inventory-listener/firewall_guard.py
ExecStart=/usr/sbin/sshd -D -e -f /etc/garm-inventory-listener/sshd_config
RuntimeDirectory=garm-inventory-listener
RuntimeDirectoryMode=0755
KillMode=control-group
TimeoutStartSec=15
TimeoutStopSec=10
Restart=no
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/run/garm-inventory-listener
RestrictAddressFamilies=AF_INET AF_UNIX AF_NETLINK
TasksMax=32
MemoryMax=128M
[Install]
WantedBy=multi-user.target
'''

# No ACCEPT rule: existing host policy can still block the approved source.
# Our early drop-only chain rejects every other source to this dedicated port.
def firewall(plan,digest):
    source=ipv4(plan['base']['controller_ip']); destination=ipv4(plan['listen_address'])
    return [
      {'table':{'family':'inet','name':TABLE,'comment':digest}},
      {'chain':{'family':'inet','table':TABLE,'name':'ingress','type':'filter','hook':'input','prio':-200,'policy':'accept'}},
      {'rule':{'family':'inet','table':TABLE,'chain':'ingress','expr':[
        {'match':{'op':'==','left':{'meta':{'key':'nfproto'}},'right':'ipv4'}},
        {'match':{'op':'==','left':{'payload':{'protocol':'ip','field':'daddr'}},'right':destination}},
        {'match':{'op':'==','left':{'meta':{'key':'l4proto'}},'right':'tcp'}},
        {'match':{'op':'==','left':{'payload':{'protocol':'tcp','field':'dport'}},'right':PORT}},
        {'match':{'op':'!=','left':{'payload':{'protocol':'ip','field':'saddr'}},'right':source}},
        {'drop':None}]}}
    ]

def normalize_nft(items):
    out=[]
    for item in items:
        if 'metainfo' in item: continue
        copy=json.loads(json.dumps(item))
        for value in copy.values():
            if isinstance(value,dict): value.pop('handle',None)
        out.append(copy)
    return out

def existing_firewall(expected):
    result=run(['/usr/sbin/nft','-j','list','table','inet',TABLE],accepted=(0,1))
    if result.returncode==1:
        # Confirm absence from a successful complete table list, rather than
        # interpreting an arbitrary nft error as absence.
        tables=json.loads(run(['/usr/sbin/nft','-j','list','tables']).stdout)['nftables']
        require(not any(i.get('table',{}).get('name')==TABLE for i in tables),'firewall-query-failed')
        return False
    current=json.loads(result.stdout)['nftables']
    require(normalize_nft(current)==expected,'foreign-firewall-state')
    return True

def socket_free(address):
    s=socket.socket(socket.AF_INET,socket.SOCK_STREAM)
    try:s.bind((address,PORT))
    finally:s.close()

def host_profile(plan):
    require(os.geteuid()==0,'full-administrator-required')
    releasepath=trusted_existing('/etc/os-release')
    with open(releasepath) as current:release=dict(line.rstrip().split('=',1) for line in current if '=' in line)
    require(release.get('ID','').strip('"')=='ubuntu' and release.get('VERSION_ID','').strip('"')=='24.04','unsupported-os-profile')
    require(os.path.isdir('/run/systemd/system'),'systemd-required')
    require(os.uname().machine=={'amd64':'x86_64','arm64':'aarch64'}[plan['base']['architecture']],'host-architecture')
    for tool in ['/usr/sbin/sshd','/usr/sbin/nft','/usr/sbin/useradd','/usr/bin/ssh-keygen','/usr/bin/systemctl','/usr/bin/sudo']:
        trusted_existing(tool)
    # A competing firewall manager could flush our owned chain after startup.
    # This profile does not disable, replace or take over such managers.
    for unit in ['ufw.service','firewalld.service','nftables.service']:
        active=run(['/usr/bin/systemctl','is-active',unit],accepted=(0,3,4))
        enabled=run(['/usr/bin/systemctl','is-enabled',unit],accepted=(0,1,3,4))
        require(active.returncode!=0 and enabled.stdout.strip() in (b'disabled',b'masked',b'not-found',b''),'competing-firewall-manager')
    run(['/usr/sbin/nft','-j','list','tables'])
    protected('/run/sshd',True)

def service_state():
    result=run(['/usr/bin/systemctl','show',UNIT,'--property=LoadState,FragmentPath,DropInPaths,ActiveState'],accepted=(0,1))
    state=dict(line.split('=',1) for line in result.stdout.decode().splitlines() if '=' in line)
    require(state.get('LoadState') in ('loaded','not-found'),'unsupported-service-state')
    require(not state.get('DropInPaths'),'service-dropins-unsupported')
    if state['LoadState']=='loaded':require(state.get('FragmentPath')==UNIT_PATH,'foreign-service-fragment')
    return state

def apply(request):
    plan,digest,artifact=validate(request)
    host_profile(plan)
    # Do not traverse distro symlinks into unreviewed locations. This profile
    # requires these explicit directories and never creates their ancestors.
    for p in ['/opt','/var/lib','/etc','/etc/systemd/system']:protected(p,True)
    fresh=not os.path.lexists(STATE+'/intent')
    if fresh:
        for path in [ROOT,STATE,CONFIG]:
            require(not os.path.exists(path) or not os.listdir(path),'foreign-installation-directory')
        require(not os.path.lexists(UNIT_PATH),'foreign-service')
        require(service_state()['LoadState']=='not-found','foreign-loaded-service')
        for directory in ['/etc/systemd/system/','/run/systemd/system/','/usr/lib/systemd/system/']:
            require(not os.path.exists(directory+UNIT+'.d'),'foreign-service-dropin')
        try:grp.getgrnam(USER)
        except KeyError:pass
        else:raise Refused('foreign-account-group')
        try:pwd.getpwnam(USER)
        except KeyError:pass
        else:raise Refused('foreign-account')
        require(not existing_firewall(firewall(plan,digest)),'foreign-firewall')
        socket_free(plan['listen_address'])
    for p in [ROOT,STATE,CONFIG]:mkdir(p)
    lock=os.open(STATE+'/lock',os.O_RDWR|os.O_CREAT|os.O_NOFOLLOW,0o600)
    protected(STATE+'/lock')
    fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
    immutable(STATE+'/intent',digest.encode()+b'\n',0o600)
    def phase(value):
        data=json.dumps({'review_digest':digest,'phase':value},separators=(',',':')).encode()
        path=STATE+'/phase.json'
        if os.path.lexists(path):
            info=protected(path);require(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode)==0o600,'foreign-phase')
            with open(path) as current:require(json.load(current).get('review_digest')==digest,'phase-identity')
        fd,temp=tempfile.mkstemp(prefix='.phase-',dir=STATE)
        try:
            with os.fdopen(fd,'wb') as f:f.write(data);f.flush();os.fsync(f.fileno())
            os.replace(temp,path);syncdir(STATE)
        finally:
            if os.path.exists(temp):os.unlink(temp)
    phase('install-artifact')
    immutable(ROOT+'/inventory-shell',artifact,0o755)
    os.chmod(ROOT,0o755)
    expected=firewall(plan,digest)
    owned_unit=False
    try:
        # Existing user is accepted only if explicitly tagged by this exact
        # attempt and all identity properties still match. Never adopt a user.
        phase('create-account')
        try: account=pwd.getpwnam(USER)
        except KeyError:
            run(['/usr/sbin/useradd','--system','--user-group','--no-create-home','--home-dir',STATE,'--shell',ROOT+'/inventory-shell','--password','!','--comment','garm-firstboot:'+digest,USER])
            account=pwd.getpwnam(USER)
        require(account.pw_uid!=0 and account.pw_gid!=0 and account.pw_dir==STATE and account.pw_shell==ROOT+'/inventory-shell' and account.pw_gecos=='garm-firstboot:'+digest,'foreign-account')
        require(set(os.getgrouplist(USER,account.pw_gid))=={account.pw_gid},'privileged-account-groups')
        sudo=run(['/usr/bin/sudo','-n','-l','-U',USER],accepted=(0,1))
        require(sudo.returncode==1 and (USER+' is not allowed to run sudo').encode() in sudo.stdout+sudo.stderr,'account-sudo-policy-unsupported')
        # sshd needs account traversal but not home writes. Root retains ownership.
        os.chmod(STATE,0o755)
        phase('install-key-and-config')
        os.chmod(CONFIG,0o755)
        key=public_key(plan['base']['inventory_key']).strip()
        immutable(CONFIG+'/authorized_keys',('restrict,from="'+plan['base']['controller_ip']+'" '+key+'\n').encode(),0o644)
        hostkey=CONFIG+'/host_key'
        if not os.path.lexists(hostkey):
            run(['/usr/bin/ssh-keygen','-q','-t','ed25519','-N','','-C','garm-inventory-listener','-f',hostkey])
        s=protected(hostkey);require(stat.S_ISREG(s.st_mode) and stat.S_IMODE(s.st_mode)==0o600,'host-key-mode')
        with open(hostkey,'rb') as durable_key:os.fsync(durable_key.fileno())
        syncdir(CONFIG)
        public=public_key(run(['/usr/bin/ssh-keygen','-y','-f',hostkey]).stdout.decode())
        immutable(CONFIG+'/host_identity',public.encode(),0o600)
        immutable(CONFIG+'/sshd_config',ssh_config(plan),0o600)
        run(['/usr/sbin/sshd','-t','-f',CONFIG+'/sshd_config'])
        phase('install-service-and-firewall')
        immutable(CONFIG+'/firewall.json',json.dumps({'rules':expected},separators=(',',':')).encode(),0o600)
        immutable(ROOT+'/firewall_guard.py',GUARD_PROGRAM.encode(),0o600)
        immutable(UNIT_PATH,SERVICE,0o644)
        owned_unit=service_state()['LoadState']=='loaded'
        active=run(['/usr/bin/systemctl','is-active',UNIT],accepted=(0,3,4)).returncode==0
        if not active:socket_free(plan['listen_address'])
        if not existing_firewall(expected):
            run(['/usr/sbin/nft','-j','-f','-'],json.dumps({'nftables':[{'add':item} for item in expected]}).encode())
        require(existing_firewall(expected),'firewall-not-installed')
        run(['/usr/bin/systemctl','daemon-reload'])
        require(service_state()['LoadState']=='loaded','service-not-loaded');owned_unit=True
        phase('start-listener')
        run(['/usr/bin/systemctl','start',UNIT],timeout=20)
        require(run(['/usr/bin/systemctl','is-active',UNIT],accepted=(0,3,4)).returncode==0,'listener-not-active')
        if plan['enable_at_boot']:run(['/usr/bin/systemctl','enable',UNIT])
        boot=run(['/usr/bin/systemctl','is-enabled',UNIT],accepted=(0,1,3,4))
        require((boot.returncode==0 and boot.stdout.strip()==b'enabled') if plan['enable_at_boot'] else (boot.returncode!=0 and boot.stdout.strip() in (b'disabled',b'not-found',b'')),'boot-enablement-mismatch')
        phase('installed')
        immutable(STATE+'/installed',digest.encode()+b'\n',0o600)
        return {'state':'installed','review_digest':digest,'listener_address':plan['listen_address'],'listener_port':PORT,'host_key':public,'boot_enabled':plan['enable_at_boot'],'inventory_address':plan['inventory_address'],'inventory_port':plan['inventory_port'],'network_reachability_verified':False}
    except Exception:
        journal_ok=True
        try:phase('rollback-stopping-listener')
        except Exception:journal_ok=False
        stopped=False;boot_disabled=False;listener_stopped=False
        if owned_unit:
            identity_ok=False
            try:identity_ok=service_state()['LoadState']=='loaded'
            except Exception:pass
            if identity_ok:
                try:
                    run(['/usr/bin/systemctl','disable',UNIT])
                    disabled=run(['/usr/bin/systemctl','is-enabled',UNIT],accepted=(0,1,3,4))
                    boot_disabled=disabled.returncode!=0 and disabled.stdout.strip() in (b'disabled',b'not-found',b'')
                except Exception:pass
                # Always attempt stop even if boot disable or journal writes failed.
                try:
                    run(['/usr/bin/systemctl','stop',UNIT],timeout=15)
                    socket_free(plan['listen_address']);listener_stopped=True
                except Exception:pass
            stopped=boot_disabled and listener_stopped
        if stopped:
            try:
                if existing_firewall(expected):run(['/usr/sbin/nft','delete','table','inet',TABLE])
            except Exception:pass
        try:phase('rollback-stopped' if stopped and journal_ok else 'rollback-unknown')
        except Exception:pass

        # Preserve protected intent/account/files for exact retry and inspection.
        # Never delete unknown service/firewall resources or claim complete rollback.
        raise

if __name__=='__main__':
    try:
        def timed_out(signum,frame):raise Refused('installer-deadline')
        signal.signal(signal.SIGALRM,timed_out);signal.alarm(150)
        data=sys.stdin.buffer.read(MAX+1);require(len(data)<=MAX,'request-too-large')
        def pairs(items):
            result={}
            for k,v in items:
                require(k not in result,'duplicate-json-field');result[k]=v
            return result
        result=apply(json.loads(data,object_pairs_hook=pairs))
        print(json.dumps(result,separators=(',',':')))
    except Exception:
        print('{"state":"unknown","reason":"setup-refused-or-partial; inspect protected intent and retry only the identical reviewed plan"}')
        sys.exit(1)
