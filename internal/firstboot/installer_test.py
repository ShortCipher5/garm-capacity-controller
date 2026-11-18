import base64,hashlib,importlib.util,json,os,struct,tempfile,types,unittest
from unittest.mock import patch
from pathlib import Path
spec=importlib.util.spec_from_file_location('installer',Path(__file__).with_name('installer.py'));m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
KEY='ssh-ed25519 '+base64.b64encode(struct.pack('>I',11)+b'ssh-ed25519'+struct.pack('>I',32)+bytes(range(32))).decode()+'\n'
def request():
 artifact=bytearray(64);artifact[:7]=b'\x7fELF\x02\x01\x01';artifact[18:20]=struct.pack('<H',62)
 p={'base':{'address':'192.0.2.20','port':22,'admin_user':'bootstrap','host_key':KEY,'admin_key_fingerprint':'SHA256:'+'a'*43,'controller_ip':'192.0.2.10','inventory_key':KEY,'artifact_sha256':hashlib.sha256(artifact).hexdigest(),'architecture':'amd64'},'listen_address':'192.0.2.20','inventory_address':'192.0.2.20','inventory_port':19922,'installer_sha256':'b'*64,'enable_at_boot':True}
 p['manifest']={'profile':'ubuntu-24.04-systemd-nftables/v1','architecture':'amd64','artifact_sha256':p['base']['artifact_sha256'],'installer_sha256':p['installer_sha256']}
 return {'plan':p,'review_digest':hashlib.sha256(json.dumps(p,sort_keys=True,separators=(',',':')).encode()).hexdigest(),'artifact':base64.b64encode(artifact).decode()}
class Validation(unittest.TestCase):
 def test_fixed_config_and_firewall(self):
  p,d,_=m.validate(request());config=m.ssh_config(p).decode();self.assertNotIn('Include ',config);self.assertIn('ForceCommand garm-inventory-v1',config);self.assertIn('DisableForwarding yes',config)
  rules=m.firewall(p,d);self.assertNotIn('"accept":',json.dumps(rules));self.assertEqual(rules[0]['table']['comment'],d);self.assertIn('ExecStartPre=',m.SERVICE.decode());self.assertIn({'match':{'op':'==','left':{'payload':{'protocol':'ip','field':'daddr'}},'right':p['listen_address']}},rules[2]['rule']['expr']);self.assertEqual(len(rules),3);compile(m.GUARD_PROGRAM,'guard','exec')
 def test_substitution_and_injection(self):
  for field,value in [('listen_address','192.0.2.1;id'),('inventory_port',0),('enable_at_boot','true')]:
   r=request();r['plan'][field]=value;r['review_digest']=hashlib.sha256(json.dumps(r['plan'],sort_keys=True,separators=(',',':')).encode()).hexdigest()
   with self.assertRaises(Exception):m.validate(r)
  r=request();r['artifact']=base64.b64encode(b'wrong').decode()
  with self.assertRaises(m.Refused):m.validate(r)
 def test_public_key_options_rejected(self):
  for key in ['command="id" '+KEY,KEY+KEY,'ssh-ed25519 AAAA']:
   with self.assertRaises(Exception):m.public_key(key)
class Lifecycle(unittest.TestCase):
 def setUp(self):
  self.tmp=tempfile.TemporaryDirectory();self.root=self.tmp.name;self.actions=[];self.account=None;self.active=False;self.rules=False;self.start_failure=False;self.enabled=False;self.public=KEY
  self.patches=[]
  for name,value in [('ROOT',self.root+'/bin'),('STATE',self.root+'/state'),('CONFIG',self.root+'/config'),('UNIT_PATH',self.root+'/listener.service')]:self.patches.append(patch.object(m,name,value))
  self.patches += [patch.object(m,'protected',lambda path,directory=False:os.lstat(path if path.startswith(self.root) else self.root)),patch.object(m,'host_profile',lambda p:None),patch.object(m,'socket_free',lambda p:None),patch.object(m,'service_state',self.service),patch.object(m,'run',self.fixed_run),patch.object(m,'existing_firewall',self.firewall),patch.object(m.pwd,'getpwnam',self.user),patch.object(m.grp,'getgrnam',side_effect=KeyError),patch.object(m.os,'getgrouplist',return_value=[1234])]
  for p in self.patches:p.start()
 def tearDown(self):
  for p in reversed(self.patches):p.stop()
  self.tmp.cleanup()
 def user(self,name):
  if self.account is None:raise KeyError(name)
  return self.account
 def service(self):return {'LoadState':'loaded' if os.path.exists(m.UNIT_PATH) else 'not-found','FragmentPath':m.UNIT_PATH,'DropInPaths':''}
 def firewall(self,expected):self.actions.append('verify-firewall');return self.rules
 def fixed_run(self,args,data=None,timeout=10,accepted=(0,)):
  self.actions.append(tuple(args));out=b'';code=0
  if args[0].endswith('useradd'):
   self.account=types.SimpleNamespace(pw_uid=1234,pw_gid=1234,pw_dir=m.STATE,pw_shell=m.ROOT+'/inventory-shell',pw_gecos=args[args.index('--comment')+1])
  elif args[0].endswith('sudo'):code=1;out=(m.USER+' is not allowed to run sudo on fixture.').encode()
  elif args[0].endswith('ssh-keygen'):
   if '-y' in args:out=self.public.encode()
   else:Path(args[-1]).write_bytes(b'private-fixture');os.chmod(args[-1],0o600)
  elif args[0].endswith('nft'):
   if '-f' in args:self.rules=True
   elif 'delete' in args:self.rules=False
  elif args[0].endswith('systemctl'):
   if args[1]=='is-active':code=0 if self.active else 3
   elif args[1]=='start':
    self.assertTrue(self.rules)
    if self.start_failure:raise m.Refused('fixture-start-failure')
    self.active=True
   elif args[1]=='stop':self.active=False
   elif args[1]=='enable':self.enabled=True
   elif args[1]=='disable':self.enabled=False
   elif args[1]=='is-enabled':code=0 if self.enabled else 1;out=b'enabled' if self.enabled else b'disabled'
  return types.SimpleNamespace(returncode=code,stdout=out,stderr=b'')
 def test_first_install_retry_and_digest_conflict(self):
  r=request();result=m.apply(r);self.assertEqual(result['host_key'],KEY);self.assertTrue(self.active);self.assertEqual(json.loads(Path(m.STATE+'/phase.json').read_text())['phase'],'installed')
  # Simulate a new process: release lock descriptors owned by this fixture before retry.
  self.close_locks()
  again=m.apply(r);self.assertEqual(result,again);self.assertEqual(sum(isinstance(a,tuple) and a[0].endswith('useradd') for a in self.actions),1)
  self.close_locks();r['plan']['enable_at_boot']=False;r['review_digest']=hashlib.sha256(json.dumps(r['plan'],sort_keys=True,separators=(',',':')).encode()).hexdigest()
  with self.assertRaises(m.Refused):m.apply(r)
 def close_locks(self):
  # Installer is a one-shot process; tests release its per-process open lock.
  for fd in os.listdir('/proc/self/fd'):
   try:
    if os.readlink('/proc/self/fd/'+fd)==m.STATE+'/lock':os.close(int(fd))
   except OSError:pass
 def test_start_failure_stops_owned_service_and_removes_only_owned_rule(self):
  self.start_failure=True
  with self.assertRaises(m.Refused):m.apply(request())
  self.assertFalse(self.active);self.assertFalse(self.enabled);self.assertFalse(self.rules);self.assertTrue(Path(m.STATE+'/intent').exists());self.assertEqual(json.loads(Path(m.STATE+'/phase.json').read_text())['phase'],'rollback-stopped');self.close_locks()
 def test_failure_after_enable_disables_owned_boot_activation(self):
  original=m.immutable
  def write(path,data,mode):
   if path==m.STATE+'/installed':raise m.Refused('fixture-commit-failure')
   return original(path,data,mode)
  with patch.object(m,'immutable',write):
   with self.assertRaises(m.Refused):m.apply(request())
  self.assertFalse(self.active);self.assertFalse(self.enabled);self.assertFalse(self.rules);self.close_locks()
 def test_changed_generated_identity_is_not_adopted(self):
  m.apply(request());self.close_locks();self.public=KEY.replace(KEY.split()[1],base64.b64encode(struct.pack('>I',11)+b'ssh-ed25519'+struct.pack('>I',32)+b'x'*32).decode())
  with self.assertRaises(m.Refused):m.apply(request())
  self.close_locks()
 def test_journal_failure_after_enable_does_not_skip_cleanup(self):
  replace=os.replace
  def fail_phase(src,dst):
   if self.enabled and dst==m.STATE+'/phase.json':raise OSError('fixture-full-disk')
   return replace(src,dst)
  with patch.object(m.os,'replace',fail_phase):
   with self.assertRaises(OSError):m.apply(request())
  self.assertFalse(self.active);self.assertFalse(self.enabled);self.assertFalse(self.rules);self.close_locks()
 def test_disable_failure_still_attempts_stop(self):
  fixed=self.fixed_run;self.start_failure=True
  def fail_disable(args,*a,**kw):
   if args[:2]==['/usr/bin/systemctl','disable']:raise m.Refused('fixture-disable-failure')
   return fixed(args,*a,**kw)
  with patch.object(m,'run',fail_disable):
   with self.assertRaises(m.Refused):m.apply(request())
  self.assertIn(('/usr/bin/systemctl','stop',m.UNIT),self.actions);self.assertTrue(self.rules);self.assertEqual(json.loads(Path(m.STATE+'/phase.json').read_text())['phase'],'rollback-unknown');self.close_locks()
 def test_preexisting_account_refused_without_mutation(self):
  self.account=types.SimpleNamespace()
  with self.assertRaises(m.Refused):m.apply(request())
  self.assertFalse(Path(m.STATE).exists())
if __name__=='__main__':unittest.main()
