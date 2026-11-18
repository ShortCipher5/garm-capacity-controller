# Appended only to the fixed embedded firstboot module under a non-main name.
# All functions below use its root-owned fixed-path/command helpers.
def rotation_apply(request):
    require(set(request)=={'plan','review_digest','artifact','phase'},'rotation-shape')
    plan=request['plan'];require(set(plan)=={'request_id','installation','installation_digest','listener_host_key','old_key','new_key','program_sha256','restart_inventory_sessions'},'rotation-plan')
    import re
    require(type(plan['request_id']) is str and re.fullmatch('[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}',plan['request_id']) is not None,'rotation-request-id')
    require(plan['restart_inventory_sessions'] is True,'explicit-listener-restart-required')
    require(request['phase'] in ('stage','commit'),'rotation-phase')
    digest=hashlib.sha256(json.dumps(plan,sort_keys=True,separators=(',',':')).encode()).hexdigest()
    require(request['review_digest']==digest,'rotation-review')
    install,install_digest,artifact=validate({'plan':plan['installation'],'review_digest':plan['installation_digest'],'artifact':request['artifact']})
    old=public_key(plan['old_key']);new=public_key(plan['new_key']);host=public_key(plan['listener_host_key'])
    require(old!=new,'distinct-key-required')
    host_profile(install)
    for path in [ROOT,STATE,CONFIG]:protected(path,True)
    lock_path=STATE+'/lock';info=protected(lock_path);require(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode)==0o600,'lock-mode')
    lock=os.open(lock_path,os.O_RDWR|os.O_NOFOLLOW);fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
    def exact(path,data,mode):
        info=protected(path);require(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode)==mode,'owned-file-mode')
        require('security.capability' not in os.listxattr(path),'file-capabilities')
        with open(path,'rb') as f:require(f.read(len(data)+1)==data,'owned-file-mismatch')
    exact(STATE+'/intent',install_digest.encode()+b'\n',0o600)
    exact(STATE+'/installed',install_digest.encode()+b'\n',0o600)
    exact(ROOT+'/inventory-shell',artifact,0o755)
    exact(CONFIG+'/sshd_config',ssh_config(install),0o600)
    exact(ROOT+'/firewall_guard.py',GUARD_PROGRAM.encode(),0o600)
    exact(UNIT_PATH,SERVICE,0o644)
    exact(CONFIG+'/host_identity',host.encode(),0o600)
    info=protected(CONFIG+'/host_key');require(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode)==0o600,'host-private-key-mode')
    require(public_key(run(['/usr/bin/ssh-keygen','-y','-f',CONFIG+'/host_key']).stdout.decode())==host,'listener-key-mismatch')
    require(service_state()['LoadState']=='loaded','owned-listener-required')
    require(existing_firewall(firewall(install,install_digest)),'owned-firewall-required')
    kill=run(['/usr/bin/systemctl','show',UNIT,'--property=KillMode,SendSIGKILL']).stdout.decode()
    require(set(kill.strip().splitlines())=={'KillMode=control-group','SendSIGKILL=yes'},'owned-session-termination-required')
    account=pwd.getpwnam(USER)
    require(account.pw_uid!=0 and account.pw_gid!=0 and account.pw_dir==STATE and account.pw_shell==ROOT+'/inventory-shell' and account.pw_gecos=='garm-firstboot:'+install_digest,'foreign-account')
    require(set(os.getgrouplist(USER,account.pw_gid))=={account.pw_gid},'privileged-account-groups')
    sudo=run(['/usr/bin/sudo','-n','-l','-U',USER],accepted=(0,1))
    require(sudo.returncode==1 and (USER+' is not allowed to run sudo').encode() in sudo.stdout+sudo.stderr,'account-sudo-policy-unsupported')
    prefix='restrict,from="'+install['base']['controller_ip']+'" '
    oldline=(prefix+old).encode();newline=(prefix+new).encode();both=oldline+newline
    auth=CONFIG+'/authorized_keys'
    info=protected(auth);require(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode)==0o644,'key-file-mode')
    with open(auth,'rb') as f:current=f.read(4097)
    journal=STATE+'/key-rotations';mkdir(journal)
    record=journal+'/'+plan['request_id']
    resumed=os.path.lexists(record)
    if not resumed:require(request['phase']=='stage' and current==oldline,'initial-key-set-mismatch')
    mkdir(record);immutable(record+'/intent',digest.encode()+b'\n',0o600)
    # Durable unknown always precedes key mutation. Never silently restore an old key.
    def publish(path,data,mode):
        if os.path.lexists(path):
            info=protected(path);require(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode)==mode,'publication-mode')
        fd,temp=tempfile.mkstemp(prefix='.rotate-',dir=os.path.dirname(path))
        try:
            with os.fdopen(fd,'wb') as f:f.write(data);f.flush();os.fchmod(f.fileno(),mode);os.fsync(f.fileno())
            os.replace(temp,path);syncdir(os.path.dirname(path))
        finally:
            if os.path.exists(temp):os.unlink(temp)
    phase=record+'/phase'
    previous=''
    if os.path.lexists(phase):
        info=protected(phase);require(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode)==0o600,'phase-mode')
        with open(phase) as f:previous=f.read(100)
    require(previous in ('','unknown-stage','staged','unknown-commit','revoked'),'rotation-journal')
    require(current in (oldline,both,newline),'foreign-key-set')
    require(current!=newline or previous in ('unknown-commit','revoked'),'new-key-without-commit-intent')
    require(previous!='revoked' or current==newline,'revoked-key-reappeared')
    if request['phase']=='stage':
        if current==both:
            require(previous in ('unknown-stage','staged','unknown-commit'),'staged-key-without-intent')
            if previous=='unknown-stage':publish(phase,b'staged',0o600)
        if current==newline and run(['/usr/bin/systemctl','is-active',UNIT],accepted=(0,3,4)).returncode!=0:
            run(['/usr/bin/systemctl','start',UNIT],timeout=20)
            require(run(['/usr/bin/systemctl','is-active',UNIT],accepted=(0,3,4)).returncode==0,'listener-recovery-failed')
        if current==oldline:
            require(previous in ('','unknown-stage'),'staged-or-revoked-key-set-changed')
            publish(phase,b'unknown-stage',0o600);publish(auth,both,0o644);publish(phase,b'staged',0o600)
        return {'state':'staged','review_digest':digest,'host_key':host,'old_key_removed':False,'sessions_terminated':False}
    require(resumed and current in (both,newline) and previous in ('staged','unknown-commit','revoked'),'stage-required')
    publish(phase,b'unknown-commit',0o600)
    if current==both:publish(auth,newline,0o644)
    # Stop/start (not reload) terminates existing authenticated inventory sessions.
    # Exact unit/config validation above excludes global/shared daemons.
    run(['/usr/bin/systemctl','stop',UNIT],timeout=15)
    socket_free(install['listen_address'])
    require(run(['/usr/bin/systemctl','is-active',UNIT],accepted=(0,3,4)).returncode!=0,'listener-still-active')
    run(['/usr/bin/systemctl','start',UNIT],timeout=20)
    require(run(['/usr/bin/systemctl','is-active',UNIT],accepted=(0,3,4)).returncode==0,'listener-not-active')
    exact(auth,newline,0o644)
    publish(phase,b'revoked',0o600)
    return {'state':'revoked','review_digest':digest,'host_key':host,'old_key_removed':True,'sessions_terminated':True}

try:
    def rotation_timeout(signum,frame):raise Refused('rotation-deadline')
    signal.signal(signal.SIGALRM,rotation_timeout);signal.alarm(150)
    data=sys.stdin.buffer.read(MAX+1);require(len(data)<=MAX,'request-too-large')
    def rotation_pairs(items):
        result={}
        for k,v in items:require(k not in result,'duplicate-json');result[k]=v
        return result
    print(json.dumps(rotation_apply(json.loads(data,object_pairs_hook=rotation_pairs)),separators=(',',':')))
except Exception:
    print('{"state":"unknown","reason":"rotation refused or partial; inspect owned journal; old key may remain, new key may be staged, listener may need recovery"}')
    sys.exit(1)
