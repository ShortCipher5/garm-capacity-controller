#!/usr/bin/env python3
"""Disposable hosted-only image test, never an installed deployment command."""
import hashlib,json,os,re,shutil,subprocess,sys,tempfile,time,uuid
from pathlib import Path
import fixture,prepare

HERE=Path(__file__).resolve().parent
MARKER='PROFILE_DISPOSABLE_LAB=generated-image-qualification/v1'
PROBE='/probe/profile-probe'
DOCKER_CONFIG_PATH=None
class Failed(Exception):pass
PROBE_KINDS={'ok','unknown-authority','hostname','missing-loader','asset-mismatch','identity-mismatch','body-mismatch','missing-library','unsupported-elf','artifact-mismatch','http-status','response-too-large','invalid-input','environment-override','transport','filesystem','permission-denied','network-mismatch','unapproved-context'}
class ProbeMismatch(Failed):
 def __init__(self,wanted,actual,exit):
  super().__init__('probe-outcome-mismatch')
  self.wanted=wanted if wanted in PROBE_KINDS else 'unrecognized'
  self.actual=actual if actual in PROBE_KINDS else ('invalid-response' if actual=='invalid-response' else 'unrecognized')
  self.exit=str(exit) if type(exit)is int and -255<=exit<=255 else 'invalid'
 def safe(self):return 'probe expected='+self.wanted+' actual='+self.actual+' exit='+self.exit
class LoaderMismatch(Failed):
 def __init__(self,error):
  super().__init__('loader-rejection-mismatch')
  self.category='other'
  if type(error)is not str:self.category='non-string'
  elif not error:self.category='empty'
  elif 'exec user process caused: no such file or directory' in error:self.category='user-exec-enoent'
  elif 'exec:' in error and '/usr/local/bin/garm' in error and 'no such file or directory' in error:self.category='target-stat-enoent'
 def safe(self):return 'loader rejection category='+self.category

def need(ok):
 if not ok:raise Failed('disposable image test failed')

def hosted(env):
 need(env.get('GITHUB_ACTIONS')=='true' and env.get('RUNNER_ENVIRONMENT')=='github-hosted');need(not any(k in env for k in ('DOCKER_HOST','DOCKER_CONTEXT','DOCKER_TLS_VERIFY','DOCKER_CERT_PATH','BUILDKIT_HOST','BUILDX_BUILDER')))
 temp=Path(env.get('RUNNER_TEMP',''));need(temp.is_absolute() and temp.is_dir() and not temp.is_symlink());return temp

def command(args,timeout=180):
 env=dict(os.environ)
 if args[0]=='docker':
  need(DOCKER_CONFIG_PATH is not None and DOCKER_CONFIG_PATH.is_dir())
  env={'PATH':os.environ.get('PATH','/usr/bin:/bin'),'HOME':str(DOCKER_CONFIG_PATH.parent/'docker-home'),'DOCKER_CONFIG':str(DOCKER_CONFIG_PATH),'BUILDX_CONFIG':str(DOCKER_CONFIG_PATH/'buildx')}
  args=['docker','--host','unix:///var/run/docker.sock','--config',str(DOCKER_CONFIG_PATH),*args[1:]]
 if args[0]=='sudo':env={'PATH':os.environ.get('PATH','/usr/bin:/bin'),'HOME':'/nonexistent'}
 if args[0]=='git':
  env={'PATH':os.environ.get('PATH','/usr/bin:/bin'),'HOME':'/nonexistent','GIT_CONFIG_NOSYSTEM':'1','GIT_CONFIG_GLOBAL':'/dev/null','GIT_NO_REPLACE_OBJECTS':'1','GIT_TERMINAL_PROMPT':'0'}
 return subprocess.run(args,env=env,capture_output=True,timeout=timeout)

def data(args):
 r=command(args);need(r.returncode==0);return r.stdout

def digest(path):return hashlib.sha256(path.read_bytes()).hexdigest()

def result_kind(result,wanted,code):
 try:x=json.loads(result.stdout)
 except Exception:x=None
 valid=type(x)is dict and set(x)=={'kind','test_probe_only','installed_stack_qualified','runtime_profile_qualified','startup_authority','deployment_authorized'}
 valid=valid and x['test_probe_only'] is True and all(x[k] is False for k in ('installed_stack_qualified','runtime_profile_qualified','startup_authority','deployment_authorized'))
 if not valid:raise ProbeMismatch(wanted,'invalid-response',result.returncode)
 actual=x['kind'] if type(x['kind'])is str else 'unrecognized'
 if type(result.returncode)is not int or result.returncode!=code or actual!=wanted:raise ProbeMismatch(wanted,actual,result.returncode)

INSPECT='{"id":{{json .Id}},"image":{{json .Image}},"lab":{{json (index .Config.Labels "disposable-profile-run")}},"user":{{json .Config.User}},"entrypoint":{{json .Config.Entrypoint}},"volumes":{{json .Config.Volumes}},"network":{{json .HostConfig.NetworkMode}},"pid":{{json .HostConfig.PidMode}},"privileged":{{json .HostConfig.Privileged}},"readonly":{{json .HostConfig.ReadonlyRootfs}},"caps_add":{{json .HostConfig.CapAdd}},"caps_drop":{{json .HostConfig.CapDrop}},"security":{{json .HostConfig.SecurityOpt}},"ports":{{json .HostConfig.PortBindings}},"restart":{{json .HostConfig.RestartPolicy.Name}},"memory":{{json .HostConfig.Memory}},"cpus":{{json .HostConfig.NanoCpus}},"pids":{{json .HostConfig.PidsLimit}},"tmpfs":{{json .HostConfig.Tmpfs}},"mounts":{{json .Mounts}},"status":{{json .State.Status}},"running":{{json .State.Running}},"exit":{{json .State.ExitCode}},"oom":{{json .State.OOMKilled}}}'

def topology(x,lab,image,uid,network,mounts,tmpfs):
 need(x['lab']==lab and x['image']==image and x['user']==f'{uid}:{uid}')
 need(x['network']==network and x['pid']=='' and x['privileged'] is False and x['readonly'] is True and not x['caps_add'] and x['caps_drop']==['ALL'])
 need(type(x['security'])is list and len(x['security'])==1 and x['security'][0] in ('no-new-privileges','no-new-privileges:true') and not x['ports'] and x['restart']=='no' and not x['volumes'])
 need(all(type(x[k])is int for k in ('memory','cpus','pids')) and x['memory']==512*1024*1024 and x['cpus']==1000000000 and x['pids']==128 and set(x['tmpfs'])==set(tmpfs))
 for destination,options in x['tmpfs'].items():
  values={}
  for option in options.split(','):
   key,sep,value=option.partition('=');need(key not in values);values[key]=value if sep else None
  need(set(values)=={'rw','noexec','nosuid','nodev','size','uid','gid','mode'} and all(values[k] is None for k in ('rw','noexec','nosuid','nodev')))
  size=values['size'];multiplier=1024*1024 if size.lower().endswith('m') else 1;number=size[:-1] if multiplier>1 else size
  need(number.isdigit() and int(number)*multiplier==(8 if destination=='/run/garm-cli-home' else 16)*1024*1024)
  need(values['uid']==str(uid) and values['gid']==str(uid) and int(values['mode'],8)==0o700)
 binds=[p for p in x['mounts'] if p['Type']=='bind'];actual={p['Destination']:(p['Type'],p['Source'],p['RW']) for p in binds};need(len(actual)==len(binds))
 need(actual=={d:('bind',str(s),rw) for s,d,rw in mounts});need(all(p['Type'] in ('bind','tmpfs') for p in x['mounts']));need({p['Destination'] for p in x['mounts'] if p['Type']=='tmpfs'}<=set(tmpfs))

class Lab:
 def __init__(self,work):self.work=work;self.run=uuid.uuid4().hex;self.names=[];self.images={};self.created_images=[];self.counter=0
 def inspect(self,name):return json.loads(data(['docker','inspect','--format',INSPECT,name]))
 def mount_args(self,mounts):
  out=[]
  for source,dest,rw in mounts:
   need(',' not in str(source) and source.is_relative_to(self.work) and not source.is_symlink() and dest.startswith('/'))
   out+=['--mount',f'type=bind,src={source},dst={dest}'+('' if rw else ',readonly')]
  return out
 def create(self,tag,uid,network,entry,args=(),mounts=(),home=False):
  name='profile-lab-'+self.run+'-'+str(self.counter);self.counter+=1;tmpfs=['/tmp']+(['/run/garm-cli-home'] if home else [])
  cmd=['docker','create','--name',name,'--label','disposable-profile-run='+self.run,'--network',network,'--user',f'{uid}:{uid}','--read-only','--cap-drop','ALL','--security-opt','no-new-privileges','--pids-limit','128','--cpus','1','--memory','512m','--tmpfs',f'/tmp:rw,noexec,nosuid,nodev,size=16m,uid={uid},gid={uid},mode=0700']
  if home:cmd+=['--tmpfs',f'/run/garm-cli-home:rw,noexec,nosuid,nodev,size=8m,uid={uid},gid={uid},mode=0700']
  cmd+=self.mount_args(mounts);cmd+=['--env',MARKER,'--entrypoint',entry,tag,*map(str,args)]
  r=command(cmd);need(r.returncode==0);container=r.stdout.decode().strip();need(re.fullmatch('[0-9a-f]{64}',container) is not None)
  self.names.append(name);x=self.inspect(name);need(x['entrypoint']==[entry]);topology(x,self.run,self.images[tag],uid,network,mounts,tmpfs);return name
 def once(self,*args,**kwargs):
  name=self.create(*args,**kwargs);r=command(['docker','start','-a',name]);x=self.inspect(name);need(x['status']=='exited' and not x['running'] and not x['oom'] and r.returncode in (0,x['exit']));r.returncode=x['exit'];need(command(['docker','rm',name]).returncode==0);self.names.remove(name);return r
 def start(self,*args,**kwargs):
  name=self.create(*args,**kwargs);need(command(['docker','start',name]).returncode==0);need(self.inspect(name)['running']);return name
 def stop(self,name):
  need(command(['docker','stop','--time','30',name]).returncode==0);x=self.inspect(name);need(not x['running'] and x['exit']==0 and not x['oom'])
  need(command(['docker','rm',name]).returncode==0);self.names.remove(name)
 def cleanup(self):
  clean=True
  # ONLY names created in this NEW lab, with matching run label and image ID.
  for name in list(self.names):
   try:
    x=self.inspect(name)
    if x['lab']!=self.run or x['image'] not in self.images.values():clean=False;continue
    if command(['docker','rm','-f',name]).returncode!=0:clean=False;continue
    self.names.remove(name)
   except Exception:clean=False
  for tag in list(self.created_images):
   try:
    actual=data(['docker','image','inspect','--format','{{.Id}}',tag]).decode().strip()
    if actual!=self.images[tag]:clean=False;continue
    if command(['docker','image','rm',tag]).returncode!=0:clean=False;continue
    self.created_images.remove(tag)
   except Exception:clean=False
  return clean

 def build(self,context,target,variant):
  tag='profile-lab-'+self.run+':'+target+'-'+variant
  r=command(['docker','build','--platform','linux/amd64','--target',target,'--build-arg','PROFILE_VARIANT='+variant,'-t',tag,str(context)],timeout=900);need(r.returncode==0)
  x=json.loads(data(['docker','image','inspect','--format','{"id":{{json .Id}},"os":{{json .Os}},"arch":{{json .Architecture}},"user":{{json .Config.User}},"entrypoint":{{json .Config.Entrypoint}},"cmd":{{json .Config.Cmd}},"volumes":{{json .Config.Volumes}}}',tag]));need(re.fullmatch('sha256:[0-9a-f]{64}',x['id']) is not None);self.images[tag]=x['id'];self.created_images.append(tag);need(x['os']=='linux' and x['arch']=='amd64')
  expected=('65533:65533',['/usr/local/bin/garm-cli'],['--help']) if target=='tools' else ('65531:65531',['/usr/local/bin/garm'],['--version'])
  need((x['user'],x['entrypoint'],x['cmd'])==expected and not x['volumes'])
  return tag
 def export(self,context,variant):
  out=self.work/('artifacts-'+variant);need(not out.exists())
  r=command(['docker','build','--platform','linux/amd64','--target','probes','--build-arg','PROFILE_VARIANT='+variant,'--output','type=local,dest='+str(out),str(context)],timeout=900);need(r.returncode==0)
  need({p.name for p in out.iterdir()}=={'garm','garm-cli','capacity-provider','profile-probe','tls-anchor','public-assets.json','omitted.json'});need(all(p.is_file() and not p.is_symlink() and p.stat().st_size<128*1024*1024 for p in out.iterdir()));return out

def public_sources(work,repo):
 # Fetch only official anonymous public source into NEW Git object stores; no
 # existing installed path or credential file. No source working tree is used.
 source=work/'official';source.mkdir()
 data(['git','init','--quiet',str(source)]);data(['git','-C',str(source),'fetch','--quiet','--depth=1','--no-tags','https://github.com/cloudbase/garm.git',prepare.GARM])
 # Current checkout must already contain the immutable public ancestor; no
 # fallback private remote or unreviewed auth mechanism if it doesn't.
 need(data(['git','--no-replace-objects','-C',str(repo),'cat-file','-t',prepare.CONTROLLER]).strip()==b'commit')
 return prepare.context(work,repo,source)

def run():
 if not __debug__:raise Failed('assertion-enabled runtime required')
 temp=hosted(os.environ);repo=Path(data(['git','rev-parse','--show-toplevel']).decode().strip())
 results=repo/'qualification-results-release-profile';need(not results.exists() and not results.is_symlink());results.mkdir() # NEW, before build/network.
 work=Path(tempfile.mkdtemp(prefix='profile-lab-',dir=temp));global DOCKER_CONFIG_PATH;DOCKER_CONFIG_PATH=work/'docker-config';DOCKER_CONFIG_PATH.mkdir(mode=0o700);(work/'docker-home').mkdir(mode=0o700);lab=Lab(work);phase='context'
 try:
  context,source=public_sources(work,repo)
  phase='build';daemon=lab.build(context,'daemon','baseline');tools=lab.build(context,'tools','baseline');missing=lab.build(context,'daemon','missing-js');broken=lab.build(context,'missing-loader','baseline')
  artifacts=lab.export(context,'baseline');negative=lab.export(context,'missing-js')
  need((artifacts/'public-assets.json').read_bytes()==(negative/'public-assets.json').read_bytes());need(json.loads((negative/'omitted.json').read_text())['omitted'].endswith('.js'))
  hashes={n:digest(artifacts/n) for n in ('garm','garm-cli','capacity-provider','profile-probe','tls-anchor')}
  phase='content'
  for image,names in [(daemon,('garm','capacity-provider')),(tools,('garm-cli',))]:
   uid=65531 if image==daemon else 65533
   for name in names:
    r=lab.once(image,uid,'none','/usr/bin/sha256sum',['/usr/local/bin/'+name]);need(r.returncode==0 and r.stdout.decode().split()[0]==hashes[name])
   r=lab.once(image,uid,'none','/usr/bin/sha256sum',['/etc/ssl/certs/ca-certificates.crt']);need(r.returncode==0 and r.stdout.decode().split()[0]==prepare.CA)
  # All image/context builds finished BEFORE any generated key/config/state.
  phase='fixture';root=work/'generated';nonce=fixture.prepare(root,(context/'public-ca.pem').read_bytes(),change_owner=True)
  probe=[(artifacts/'profile-probe',PROBE,False)];inventory=[(artifacts/'public-assets.json','/probe/public-assets.json',False)]
  anchor=lab.start(daemon,65530,'none','/probe/tls-anchor',mounts=[(artifacts/'tls-anchor','/probe/tls-anchor',False),(root/'anchor','/run/anchor-private',False)])
  net='container:'+lab.inspect(anchor)['id']
  def attempt(image,uid,action,extra=(),trust='positive',additional=(),home=False):
   mounts=[*probe,*inventory,(root/'trust'/(trust+'.pem'),'/etc/ssl/certs/ca-certificates.crt',False),*additional]
   return lab.once(image,uid,net,PROBE,['-action',action,*extra],mounts=mounts,home=home)
  def check(image,uid,action,extra=(),trust='positive',additional=(),expected='ok',code=0,home=False):
   result_kind(attempt(image,uid,action,extra,trust,additional,home),expected,code)
  def wait_expected(image,uid,action,extra,expected='ok',code=0):
   for _ in range(120):
    r=attempt(image,uid,action,extra);x=json.loads(r.stdout)
    if x.get('kind')=='transport' and r.returncode==65:
     result_kind(r,'transport',65);time.sleep(.5);continue
    result_kind(r,expected,code);return
   raise Failed('generated service unavailable')
  phase='readiness'
  wait_expected(daemon,65531,'health',['-port','9444','-nonce',nonce])
  old_netns=data(['docker','exec',anchor,'/usr/bin/readlink','/proc/self/ns/net']).decode().strip()
  for image,uid in ((daemon,65531),(tools,65533)):check(image,uid,'network',['-netns',old_netns])
  phase='ca-path'
  for image,uid in ((daemon,65531),(tools,65533)):
   check(image,uid,'health',['-port','9444','-nonce',nonce])
   for trust in ('empty','unrelated'):check(image,uid,'health',['-port','9444','-nonce',nonce],trust,expected='unknown-authority',code=42)
   check(image,uid,'health',['-port','9444','-nonce',nonce,'-wrong-name'],expected='hostname',code=43)
   check(image,uid,'health',['-port','9444','-nonce',nonce])
  check(tools,65533,'identity',['-role','tools'])
  phase='dac'
  for image,uid,other,role in ((daemon,65531,65533,'garm'),(tools,65533,65531,'tools')):
   canary=[(root/(role+'-canary'),'/probe/canary',True)]
   check(image,uid,'canary',['-nonce',nonce],additional=canary)
   check(image,other,'canary',['-nonce',nonce],additional=canary,expected='permission-denied',code=53)
   check(image,uid,'canary',['-nonce',nonce],additional=canary)
  phase='linkage'
  for image,uid,names in ((daemon,65531,('garm','capacity-provider')),(tools,65533,('garm-cli',))):
   for name in names:
    phase='linkage-'+name;check(image,uid,'elf',['-binary',name,'-sha256',hashes[name]])
  phase='linkage-missing-loader-probe'
  check(broken,65531,'elf',['-binary','garm','-sha256',hashes['garm']],expected='missing-loader',code=44)
  phase='linkage-missing-loader-exec'
  failed=lab.create(broken,65531,'none','/usr/local/bin/garm',['--version']);r=command(['docker','start','-a',failed]);need(r.returncode!=0)
  # This deliberately keyless scratch container is the only case reading Error.
  err=json.loads(data(['docker','inspect','--format','{{json .State.Error}}',failed]));
  if type(err)is not str or 'exec /usr/local/bin/garm: no such file or directory' not in err:raise LoaderMismatch(err)
  phase='discovery'
  r=lab.once(daemon,65531,'none','/usr/local/bin/garm',['--version']);need(r.returncode==0 and b'v0.2.1+capacity.87e0d61751d4' in r.stdout)
  r=lab.once(tools,65533,'none','/usr/local/bin/garm-cli',['--help'],home=True);need(r.returncode==0 and r.stdout.startswith(b'CLI for the github self hosted runners manager.'))
  for command_name,expected in [('GetVersion',b'garm-capacity-controller-experimental\n'),('GetSupportedInterfaceVersions',b'["v0.1.1"]\n')]:
   r=lab.once(daemon,65531,'none','/bin/sh',['-ec','GARM_COMMAND='+command_name+' exec /usr/local/bin/capacity-provider']);need(r.returncode==0 and r.stdout==expected)
  r=lab.once(tools,65533,'none','/bin/sh',['-ec','/usr/local/bin/garm-cli version; test -z "$(find /run/garm-cli-home -type f -print -quit)"'],home=True);need(r.returncode==0 and r.stdout==b'garm-cli: v0.2.1+capacity.87e0d61751d4\ngarm server: v0.0.0-unknown\n')
  phase='garm-ui'
  def start_garm(image,state):return lab.start(image,65531,net,'/bin/sh',['-ec','umask 077; exec /usr/local/bin/garm -config /run/garm-private/config.toml'],mounts=[*probe,*inventory,(root/'garm','/run/garm-private',False),(state,'/var/lib/garm',True),(root/'trust/positive.pem','/etc/ssl/certs/ca-certificates.crt',False)])
  def ui_ready(image):wait_expected(image,65531,'assets',['-port','9998'])
  def state_stat():
   r=lab.once(daemon,65531,'none','/usr/bin/stat',['--format=%d:%i:%a:%u:%g','/var/lib/garm/controller.db','/var/lib/garm/blob-controller.db'],mounts=[(root/'state','/var/lib/garm',False)])
   need(r.returncode==0);lines=r.stdout.decode().splitlines();need(len(lines)==2)
   for line in lines:
    fields=line.split(':');need(len(fields)==5 and fields[2:]==['600','65531','65531'])
   return lines
  running=start_garm(daemon,root/'state');ui_ready(daemon)
  result_kind(command(['docker','exec','--user','65531:65531','--env',MARKER,running,PROBE,'-action','identity','-role','garm']), 'ok',0)
  need(command(['docker','exec','--user','65531:65531',running,'/bin/sh','-ec','test -f /var/lib/garm/controller.db && test -f /var/lib/garm/blob-controller.db']).returncode==0)
  lab.stop(running);initial_state=state_stat()
  bad=start_garm(missing,root/'bad-state')
  wait_expected(missing,65531,'assets',['-port','9998'],'asset-mismatch',45)
  lab.stop(bad)
  running=start_garm(daemon,root/'state');ui_ready(daemon);lab.stop(running);need(state_stat()==initial_state)
  phase='recreate'
  lab.stop(anchor)
  anchor=lab.start(daemon,65530,'none','/probe/tls-anchor',mounts=[(artifacts/'tls-anchor','/probe/tls-anchor',False),(root/'anchor','/run/anchor-private',False)])
  net='container:'+lab.inspect(anchor)['id'];wait_expected(daemon,65531,'health',['-port','9444','-nonce',nonce])
  new_netns=data(['docker','exec',anchor,'/usr/bin/readlink','/proc/self/ns/net']).decode().strip();need(new_netns!=old_netns)
  for image,uid in ((daemon,65531),(tools,65533)):check(image,uid,'network',['-netns',new_netns])
  running=start_garm(daemon,root/'state');ui_ready(daemon)
  result_kind(command(['docker','exec','--user','65531:65531','--env',MARKER,running,PROBE,'-action','identity','-role','garm']), 'ok',0)
  lab.stop(running);need(state_stat()==initial_state);lab.stop(anchor)
  phase='result'
  record={'schema':'disposable-historical-image-profile/v1','source':source,'image_ids':{'daemon':lab.images[daemon],'tools':lab.images[tools],'missing_js':lab.images[missing],'missing_loader':lab.images[broken]},'artifact_hashes':hashes,'actual_default_go_ca_path_both_images':True,'actual_garm_ui_and_missing_js_negative':True,'actual_elf_structure_and_missing_loader_negative':True,'actual_missing_loader_exec_attributed':True,'actual_role_dac_positive_negative':True,'actual_loopback_namespace_recreation':True,'actual_garm_pid1_identity':True,'generated_pair_inode_mode_continuity':True,'fixture_only_umask077':True,'provider_discovery_only':True,'tools_default_uid_boundary_not_direct_cli_pid1':True,'system_trust_changed_on_host':False,'real_issuer_qualified':False,'provider_gateway_runtime_qualified':False,'browser_functionality_qualified':False,'latest_controller_qualified':False,'jit_or_job_lifecycle_qualified':False,'native_physical_scope_qualified':False,'signed_release':False,'installed_stack_qualified':False,'startup_authority':False,'recovery_authority':False,'deployment_authorized':False}
  phase='cleanup';need(lab.cleanup());need(command(['sudo','-n','rm','-rf','--',str(work)]).returncode==0)
  with (results/'result.json').open('x') as f:json.dump(record,f,sort_keys=True,indent=2)
  print('PASS disposable historical image/profile test only; all deployment authorities false')
 except Exception as error:
  print('Disposable image/profile test failed at '+phase,file=sys.stderr)
  if isinstance(error,(ProbeMismatch,LoaderMismatch)):print(error.safe(),file=sys.stderr)
  raise Failed('bounded test failed') from None
 finally:
  lab.cleanup()
  # New owned lab tree only. Credentials/state are never artifacts; no repair or
  # deletion of any source/installed path. sudo handles generated mapped UID files.
  command(['sudo','-n','rm','-rf','--',str(work)])

if __name__=='__main__':
 try:
  need(len(sys.argv)==1);run()
 except Exception:raise SystemExit(1)
