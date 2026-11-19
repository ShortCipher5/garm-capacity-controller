# Embedded read-only diagnostic recipe. No request controls a command or path.
import hashlib,json,os,stat,subprocess,sys,selectors,time
PARENT='/var/lib/garm-capacity-prepared'
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
    if set(req)!={'sha256','nonce','receipt'} or any(not isinstance(req[k],str) or len(req[k])!=64 or any(c not in '0123456789abcdef' for c in req[k]) for k in ('sha256','nonce')):
        raise RuntimeError('exact diagnostic recipe required')
    receipt=req['receipt']
    if not isinstance(receipt,dict) or set(receipt)!={'version','id','state','manifest_digest','unit_digest','activation_authorized'} or receipt['version']!='native-owner-preparation/v1' or receipt['state']!='prepared_not_activated' or receipt['activation_authorized'] is not False:
        raise RuntimeError('completed preparation required')
    for key in ('id','manifest_digest','unit_digest'):
        value=receipt[key]
        if not isinstance(value,str) or len(value)!=64 or any(c not in '0123456789abcdef' for c in value): raise RuntimeError('invalid receipt')
    dest=PARENT+'/'+receipt['id']
    for directory in (dest,dest+'/bin',dest+'/private'):
        info=protected(directory)
        if not stat.S_ISDIR(info.st_mode) or stat.S_IMODE(info.st_mode)!=0o700: raise RuntimeError('invalid prepared directory')
    def read_metadata(name):
        path=dest+'/'+name
        original=protected(path)
        fd=os.open(path,os.O_RDONLY|os.O_CLOEXEC|os.O_NOFOLLOW|os.O_NONBLOCK)
        try:
            info=os.fstat(fd)
            if (info.st_dev,info.st_ino)!=(original.st_dev,original.st_ino) or not stat.S_ISREG(info.st_mode) or info.st_nlink!=1 or stat.S_IMODE(info.st_mode)!=0o600 or info.st_mode&0o7000 or not 0<info.st_size<=16384: raise RuntimeError('invalid preparation metadata')
            return os.read(fd,16385)
        finally: os.close(fd)
    if json.loads(read_metadata('receipt.json'))!=receipt: raise RuntimeError('receipt mismatch')
    manifest=read_metadata('manifest.json')
    if hashlib.sha256(manifest).hexdigest()!=receipt['manifest_digest']: raise RuntimeError('manifest mismatch')
    m=json.loads(manifest)
    if m.get('id')!=receipt['id'] or m.get('files',{}).get('capacity-native-preflight')!=req['sha256']: raise RuntimeError('artifact association mismatch')
    if json.loads(read_metadata('intent.json'))!={'state':'preparing','manifest_digest':receipt['manifest_digest']}: raise RuntimeError('intent mismatch')
    if hashlib.sha256(read_metadata('garm-capacity-native-domain.service.staged')).hexdigest()!=receipt['unit_digest']: raise RuntimeError('unit mismatch')
    PATH=dest+'/bin/capacity-native-preflight'
    original=protected(PATH)
    fd=os.open(PATH,os.O_RDONLY|os.O_CLOEXEC|os.O_NOFOLLOW|os.O_NONBLOCK)
    try:
        s=os.fstat(fd)
        if (s.st_dev,s.st_ino)!=(original.st_dev,original.st_ino) or not stat.S_ISREG(s.st_mode) or s.st_size<1 or s.st_size>64*1024*1024 or s.st_mode&0o7000 or s.st_nlink!=1 or stat.S_IMODE(s.st_mode)!=0o555:
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
        print('Prepared diagnostic unavailable: verify exact completed bundle, protected paths and existing SSH/Python/sudo prerequisites.',file=sys.stderr)
        sys.exit(1)
