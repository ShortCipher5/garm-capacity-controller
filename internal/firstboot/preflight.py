# Embedded read-only diagnostic recipe. No request controls a command or path.
import hashlib,json,os,stat,subprocess,sys,selectors,time
PATH='/usr/local/libexec/capacity-native-preflight'
def protected(path):
    current='/'
    root=os.lstat(current)
    if root.st_uid!=0 or root.st_mode & 0o022 or not stat.S_ISDIR(root.st_mode):
        raise RuntimeError('untrusted root ancestor')
    for part in path.strip('/').split('/'):
        current=os.path.join(current,part)
        s=os.lstat(current)
        if s.st_uid!=0 or s.st_mode & 0o022 or stat.S_ISLNK(s.st_mode):
            raise RuntimeError('untrusted diagnostic path')
    return s

def bounded_output(p):
    deadline=time.monotonic()+20
    buffers={p.stdout:bytearray(),p.stderr:bytearray()}
    with selectors.DefaultSelector() as events:
        for pipe in buffers: events.register(pipe,selectors.EVENT_READ)
        while events.get_map():
            remaining=deadline-time.monotonic()
            if remaining<=0: raise RuntimeError('diagnostic timed out')
            for event,_ in events.select(remaining):
                part=os.read(event.fileobj.fileno(),4096)
                if not part: events.unregister(event.fileobj); continue
                if len(buffers[event.fileobj])+len(part)>65536: raise RuntimeError('diagnostic output exceeded limit')
                buffers[event.fileobj].extend(part)
        if p.wait(timeout=max(0.001,deadline-time.monotonic()))!=0: raise RuntimeError('diagnostic failed')
    return bytes(buffers[p.stdout])

def main():
    if os.geteuid()!=0: raise RuntimeError('separate administrator authority required')
    raw=sys.stdin.buffer.read(4097)
    if len(raw)>4096: raise RuntimeError('bounded request required')
    req=json.loads(raw)
    if set(req)!={'sha256','nonce'} or any(not isinstance(req[k],str) or len(req[k])!=64 or any(c not in '0123456789abcdef' for c in req[k]) for k in req):
        raise RuntimeError('exact diagnostic recipe required')
    original=protected(PATH)
    fd=os.open(PATH,os.O_RDONLY|os.O_CLOEXEC|os.O_NOFOLLOW)
    try:
        s=os.fstat(fd)
        if (s.st_dev,s.st_ino)!=(original.st_dev,original.st_ino) or not stat.S_ISREG(s.st_mode) or s.st_size<1 or s.st_size>64*1024*1024 or s.st_mode&0o7000 or not s.st_mode&0o111:
            raise RuntimeError('unsupported diagnostic artifact')
        try:
            if os.getxattr(fd,'security.capability'): raise RuntimeError('privileged diagnostic artifact')
        except OSError as e:
            import errno
            if e.errno not in (errno.ENODATA,errno.ENOTSUP): raise
        h=hashlib.sha256()
        while True:
            b=os.read(fd,65536)
            if not b: break
            h.update(b)
        if h.hexdigest()!=req['sha256']: raise RuntimeError('diagnostic artifact pin mismatch')
        p=subprocess.Popen(['/proc/self/fd/'+str(fd)],pass_fds=(fd,),close_fds=True,stdin=subprocess.DEVNULL,stdout=subprocess.PIPE,stderr=subprocess.PIPE,env={'PATH':'/usr/sbin:/usr/bin:/sbin:/bin','LANG':'C','LC_ALL':'C'})
        try: output=bounded_output(p)
        finally:
            if p.poll() is None: p.kill()
            p.wait()
            p.stdout.close();p.stderr.close()
        report=json.loads(output)
        if report.get('version')!='native-host-preflight/v1' or report.get('state')!='unqualified_no_activation': raise RuntimeError('unsupported diagnostic report')
        print(json.dumps({'nonce':req['nonce'],'report':report},separators=(',',':')))
    finally: os.close(fd)
if __name__=='__main__':
    try: main()
    except Exception:
        print('Read-only diagnostic unavailable: verify pinned preinstalled binary, protected path, Python/sudo access and supported host prerequisites.',file=sys.stderr)
        sys.exit(1)
