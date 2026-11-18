# Fixed privileged recipe: no caller command, archive or destination.
import os, sys, json, stat, hashlib, subprocess, selectors, time
BASE='/var/lib/garm-capacity-runtime-upload'
FINAL='/var/lib/garm-capacity/native-runtime-assets'
NAMES=['runtime.raw','systemd-nspawn','zfs','zpool','ip','nft']
HELPER='capacity-native-assets'
def require(v):
    if not v: raise ValueError('refused')
def sha(v): return hashlib.sha256(v).hexdigest()
def hex64(v): return isinstance(v,str) and len(v)==64 and all(c in '0123456789abcdef' for c in v)
def controlled(path):
    while True:
        s=os.lstat(path)
        require(stat.S_ISDIR(s.st_mode) and s.st_uid==0 and s.st_mode&0o022==0)
        if path=='/': break
        path=os.path.dirname(path)
def syncdir(path):
    fd=os.open(path,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
    try: os.fsync(fd)
    finally: os.close(fd)
def fixedparent(path):
    controlled(os.path.dirname(path))
    try: os.mkdir(path,0o700);os.chmod(path,0o700)
    except FileExistsError: pass
    controlled(path);syncdir(os.path.dirname(path))
def write(path,data,mode):
    fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,mode)
    try:
        os.fchmod(fd,mode)
        with os.fdopen(fd,'wb',closefd=False) as f: f.write(data);f.flush();os.fsync(fd)
    finally: os.close(fd)
def bounded_output(p):
    deadline=time.monotonic()+1800; output=bytearray()
    with selectors.DefaultSelector() as events:
        events.register(p.stdout,selectors.EVENT_READ)
        while events.get_map():
            remaining=deadline-time.monotonic();require(remaining>0)
            for event,_ in events.select(remaining):
                part=os.read(event.fileobj.fileno(),4096)
                if not part: events.unregister(event.fileobj);continue
                require(len(output)+len(part)<=16384);output.extend(part)
        require(p.wait(timeout=max(0.001,deadline-time.monotonic()))==0)
    return bytes(output)
def run():
    require(os.geteuid()==0)
    raw=sys.stdin.buffer.readline(65537);require(len(raw)<=65536 and raw.endswith(b'\n'))
    h=json.loads(raw);require(set(h)=={'mode','manifest','manifest_json','manifest_digest','files'})
    m=h['manifest'];require(set(m)=={'version','id','architecture','files'} and m['version']=='native-runtime-assets/v1' and hex64(m['id']) and m['architecture'] in ('amd64','arm64'))
    require(set(m['files'])==set(NAMES))
    require(isinstance(h['manifest_json'],str) and json.loads(h['manifest_json'])==m and sha(h['manifest_json'].encode())==h['manifest_digest'])
    fs=h['files'];require(isinstance(fs,list) and len(fs)==7 and [f['name'] for f in fs]==[HELPER]+NAMES)
    for f in fs:
        require(set(f)=={'name','size','sha256'} and type(f['size']) is int and hex64(f['sha256']))
        if f['name']=='runtime.raw': require(64<<20<=f['size']<=16<<30 and f['size']%(1<<20)==0)
        else: require(0<f['size']<=(64<<20 if f['name']==HELPER else 128<<20))
        if f['name']!=HELPER: require(m['files'][f['name']]=={'sha256':f['sha256'],'size':f['size']})
    require(h['mode'] in ('prepare','inspect'))
    dest=BASE+'/'+m['id']
    if h['mode']=='prepare':
        fixedparent(BASE);fixedparent('/var/lib/garm-capacity');fixedparent(FINAL)
        os.mkdir(dest,0o700);os.chmod(dest,0o700);syncdir(BASE)
        write(dest+'/upload-intent.json',raw,0o600);syncdir(dest)
        # Helper requires an exact flat inventory; keep upload intent outside bundle.
        bundle=dest+'/bundle';os.mkdir(bundle,0o700);os.chmod(bundle,0o700);syncdir(dest)
        write(bundle+'/manifest.json',h['manifest_json'].encode(),0o600)
        for entry in fs:
            name=entry['name'];path=(dest+'/'+name if name==HELPER else bundle+'/'+name);fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600)
            try:
                hasher=hashlib.sha256();remaining=entry['size']
                with os.fdopen(fd,'wb',closefd=False) as out:
                    while remaining:
                        chunk=sys.stdin.buffer.read(min(65536,remaining));require(chunk);remaining-=len(chunk);hasher.update(chunk);out.write(chunk)
                    out.flush();require(hasher.hexdigest()==entry['sha256']);os.fchmod(fd,0o555 if name!='runtime.raw' else 0o400);os.fsync(fd)
            finally: os.close(fd)
        require(sys.stdin.buffer.read(1)==b'');syncdir(bundle)
    else:
        require(sys.stdin.buffer.read(1)==b'')
        controlled(dest);bundle=dest+'/bundle';controlled(bundle)
        # Exact original header binds inspection to the upload that consumed the ID.
        intentfd=os.open(dest+'/upload-intent.json',os.O_RDONLY|os.O_NONBLOCK|os.O_NOFOLLOW)
        with os.fdopen(intentfd,'rb') as f:
            st=os.fstat(f.fileno());require(stat.S_ISREG(st.st_mode) and st.st_uid==0 and st.st_nlink==1 and stat.S_IMODE(st.st_mode)==0o600 and st.st_size<=65536)
            old=json.loads(f.read(65537))
        require(old==dict(h,mode='prepare'))
    helper=dest+'/'+HELPER;fd=os.open(helper,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
    try:
        s=os.fstat(fd);require(stat.S_ISREG(s.st_mode) and s.st_uid==0 and s.st_nlink==1 and stat.S_IMODE(s.st_mode)==0o555 and 0<s.st_size<=64<<20)
        hh=hashlib.sha256()
        while True:
            chunk=os.read(fd,65536)
            if not chunk: break
            hh.update(chunk)
        require(hh.hexdigest()==fs[0]['sha256'])
        try: require(not os.getxattr(fd,'security.capability'))
        except OSError as e:
            import errno
            require(e.errno in (errno.ENODATA,errno.ENOTSUP,errno.EOPNOTSUPP))
        # Pinned helper emits only bounded receipt/error. No caller text enters argv.
        arg=['--prepare-source='+bundle] if h['mode']=='prepare' else ['--inspect-id='+m['id']]
        arg+=['--expected-manifest-sha256='+h['manifest_digest']]
        child=subprocess.Popen(['/proc/self/fd/'+str(fd)]+arg,pass_fds=(fd,),close_fds=True,stdin=subprocess.DEVNULL,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,env={'PATH':'/usr/bin:/bin','LANG':'C'},cwd='/')
        try: output=bounded_output(child)
        finally:
            if child.poll() is None: child.kill()
            child.wait();child.stdout.close()
        sys.stdout.buffer.write(output)
    finally: os.close(fd)
if __name__=='__main__':
    try: run()
    except Exception:
        sys.stderr.write('runtime asset transfer unknown or refused; inspect the same approved ID\n');sys.exit(1)
