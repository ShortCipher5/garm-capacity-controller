"""Fresh public object archives + explicit NEW fixture-code allowlist only."""
import hashlib,json,os,re,subprocess,tarfile
from pathlib import Path,PurePosixPath

HERE=Path(__file__).resolve().parent
GARM='154638445c3949c1958b01812f69d9a1e4d82684';CONTROLLER='e44d7a9ff11f70e52eded5b1d706db7cd0c8e707';TREE='a322259d30a04927bccb63e2ac9c40e3274c1ac8'
PATCH='87e0d61751d4b9ffa15322cff09557f0a1094fdd68464fe840a12ffe01bb0660';CA='427c064216761a1e72a8b30f0d954d08cb6e4435a82091fb865afd20c8497285';LOCK='e9e29855fdfea8369c0e1fa76d5ff376666ced43aff04b15ee86c63150193c88'
FILES=('testsrc/go.mod','testsrc/probe/main.go','testsrc/anchor/main.go','inventory.mjs','public-ca.pem')
class Rejected(Exception):pass

def need(ok):
 if not ok:raise Rejected('disposable profile prerequisite rejected')

def sha(data):return hashlib.sha256(data).hexdigest()
def pin(x):return sha(json.dumps(x,sort_keys=True,separators=(',',':'),allow_nan=False).encode())

def render():
 lock=json.loads((HERE/'lock.json').read_text());need(pin(lock)==LOCK)
 text=(HERE/'Dockerfile.in').read_text();refs={k:v['repository']+'@'+v['linux_amd64_digest'] for k,v in lock['images'].items()}
 for name,ref in refs.items():text=text.replace('{{'+name+'}}',ref)
 need('{{' not in text);check_recipe(text,refs);return text,lock

def check_recipe(text,refs):
 need(type(text)is str and len(text)<65536 and all(c=='\n' or 32<=ord(c)<=126 for c in text));need('\\' not in text and '`' not in text and '<<' not in text)
 lines=text.split('\n');need(lines[0]=='# syntax='+refs['frontend'])
 expected={'web-build':refs['node'],'go-build':refs['go'],'probes':'scratch','daemon':refs['runtime'],'tools':refs['runtime'],'missing-loader':'scratch'};seen=set();args=[]
 for n,line in enumerate(lines):
  if not line.strip():continue
  if line.lstrip().startswith('#'):need(n==0 or '=' not in line);continue
  m=re.fullmatch(r'([A-Z]+) (.+)',line);need(m is not None and m[1] in {'FROM','RUN','COPY','WORKDIR','ENV','USER','ENTRYPOINT','CMD','ARG'})
  if m[1]=='FROM':
   f=re.fullmatch(r'FROM (\S+) AS ([a-z0-9-]+)',line);need(f is not None and f[2] in expected and f[2] not in seen and f[1]==expected[f[2]]);seen.add(f[2])
  if m[1]=='COPY' and '--from' in line:
   flags=[token for token in line.split() if token.startswith('--from')];need(len(flags)==1);f=re.fullmatch(r'--from=([a-z0-9-]+)',flags[0]);need(f is not None and f[1] in seen and f[1] in ('web-build','go-build'))
  if m[1]=='RUN':need('--mount' not in line and not m[2].startswith('--'))
  if m[1]=='ARG':args.append(line)
 need(seen==set(expected) and args==['ARG PROFILE_VARIANT=baseline']);return True

def command(args,cwd=None):
 env={'PATH':os.environ.get('PATH','/usr/bin:/bin'),'HOME':str(HERE/'tmp'),'GIT_CONFIG_NOSYSTEM':'1','GIT_CONFIG_GLOBAL':'/dev/null','GIT_NO_REPLACE_OBJECTS':'1','GIT_TERMINAL_PROMPT':'0'}
 return subprocess.run(args,cwd=cwd,env=env,check=True,capture_output=True,timeout=120).stdout

def archive(repo,commit,dest):
 need(not dest.exists());dest.mkdir()
 output=dest.parent/(dest.name+'.tar')
 command(['git','--no-replace-objects','-C',str(repo),'archive','--format=tar','--output',str(output),commit])
 need(output.stat().st_size<256*1024*1024)
 with tarfile.open(output) as tar:
  members=tar.getmembers();need(len(members)<30000)
  for member in members:
   p=PurePosixPath(member.name);need(not p.is_absolute() and '..' not in p.parts and (member.isdir() or member.isfile()) and member.size<32*1024*1024)
  tar.extractall(dest,filter='data')
 output.unlink()

def context(work,controller_repo,garm_repo):
 root=work/'context';root.mkdir() # NEW only: no adopter/updater.
 tree=command(['git','--no-replace-objects','-C',str(controller_repo),'rev-parse',CONTROLLER+'^{tree}']).decode().strip();need(tree==TREE)
 archive(controller_repo,CONTROLLER,root/'controller');archive(garm_repo,GARM,root/'garm')
 patch=root/'controller/integration/garm-v0.2.1/admission-preflight.patch';need(sha(patch.read_bytes())==PATCH)
 command(['git','apply','--check',str(patch)],root/'garm');command(['git','apply',str(patch)],root/'garm')
 for name in FILES:
  source=HERE/name;need(source.is_file() and not source.is_symlink());p=root/name;p.parent.mkdir(parents=True,exist_ok=True)
  with p.open('xb') as f:f.write(source.read_bytes())
 need(sha((root/'public-ca.pem').read_bytes())==CA)
 text,lock=render();(root/'Dockerfile').write_text(text)
 record={'schema':'fresh-public-profile-context/v1','garm_source':GARM,'controller_source':CONTROLLER,'controller_tree':TREE,'patch_sha256':PATCH,'lock_sha256':LOCK,'recipe_sha256':sha(text.encode()),'dependency_locks':{n:sha((root/n).read_bytes()) for n in ('garm/go.mod','garm/go.sum','garm/vendor/modules.txt','garm/webapp/package-lock.json','controller/go.mod','controller/go.sum')},'test_code':{n:sha((root/n).read_bytes()) for n in FILES},'private_fixture_created':False,'application_inputs_public_archives_only':True}
 (work/'context-manifest.json').write_text(json.dumps(record,sort_keys=True));return root,record
