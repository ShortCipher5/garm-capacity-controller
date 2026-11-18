import base64,hashlib,json,os,struct,types,unittest
from pathlib import Path
from unittest.mock import patch
from installer_test import Lifecycle,request,KEY,m
exec(Path(__file__).with_name('rotation.py').read_text().split('\ntry:\n')[0],m.__dict__)
NEW='ssh-ed25519 '+base64.b64encode(struct.pack('>I',11)+b'ssh-ed25519'+struct.pack('>I',32)+b'n'*32).decode()+'\n'
class RotationLifecycle(Lifecycle):
 def setup_rotation(self):
  original=request();m.apply(original);self.close_locks();self.actions=[]
  plan={'request_id':'11111111-1111-4111-8111-111111111111','installation':original['plan'],'installation_digest':original['review_digest'],'listener_host_key':KEY,'old_key':KEY,'new_key':NEW,'program_sha256':'c'*64,'restart_inventory_sessions':True}
  return {'plan':plan,'review_digest':hashlib.sha256(json.dumps(plan,sort_keys=True,separators=(',',':')).encode()).hexdigest(),'artifact':original['artifact'],'phase':'stage'}
 def fixed_run(self,args,*a,**kw):
  if args[0].endswith('systemctl') and args[1]=='show':self.actions.append(tuple(args));return types.SimpleNamespace(stdout=b'KillMode=control-group\nSendSIGKILL=yes\n',stderr=b'',returncode=0)
  return super().fixed_run(args,*a,**kw)
 def rotate(self,r):
  try:return m.rotation_apply(r)
  finally:self.close_locks()
 def keys(self):return Path(m.CONFIG+'/authorized_keys').read_text()
 def test_rotation_add_before_remove_and_owned_session_restart(self):
  r=self.setup_rotation();result=self.rotate(r);self.assertEqual(result['state'],'staged');self.assertIn(KEY.strip(),self.keys());self.assertIn(NEW.strip(),self.keys());self.assertNotIn(('/usr/bin/systemctl','stop',m.UNIT),self.actions)
  r['phase']='commit';result=self.rotate(r);self.assertTrue(result['old_key_removed']);self.assertTrue(result['sessions_terminated']);self.assertNotIn(KEY.strip(),self.keys());self.assertIn(NEW.strip(),self.keys());self.assertLess(self.actions.index(('/usr/bin/systemctl','stop',m.UNIT)),self.actions.index(('/usr/bin/systemctl','start',m.UNIT)))
 def test_request_id_cannot_be_rebound_to_another_plan(self):
  r=self.setup_rotation();self.rotate(r);r['plan']['program_sha256']='d'*64;r['review_digest']=hashlib.sha256(json.dumps(r['plan'],sort_keys=True,separators=(',',':')).encode()).hexdigest()
  with self.assertRaises(m.Refused):self.rotate(r)
  self.assertIn(KEY.strip(),self.keys());self.assertIn(NEW.strip(),self.keys())
 def test_commit_before_stage_refused(self):
  r=self.setup_rotation();r['phase']='commit'
  with self.assertRaises(m.Refused):self.rotate(r)
  self.assertIn(KEY.strip(),self.keys());self.assertNotIn(NEW.strip(),self.keys())
 def test_failure_after_old_removal_recovers_stopped_listener(self):
  r=self.setup_rotation();self.rotate(r);r['phase']='commit';self.start_failure=True
  with self.assertRaises(m.Refused):self.rotate(r)
  self.assertFalse(self.active);self.assertNotIn(KEY.strip(),self.keys())
  self.start_failure=False;r['phase']='stage';self.rotate(r);self.assertTrue(self.active);r['phase']='commit';self.assertEqual(self.rotate(r)['state'],'revoked')
 def test_crash_after_new_key_publication_resumes_stage(self):
  r=self.setup_rotation();replace=os.replace;count=0
  def fail(src,dst):
   nonlocal count
   if dst.endswith('/phase'):
    count+=1
    if count==2:raise OSError('crash after keys published')
   return replace(src,dst)
  with patch.object(m.os,'replace',fail):
   with self.assertRaises(OSError):self.rotate(r)
  self.assertIn(NEW.strip(),self.keys());self.assertEqual(self.rotate(r)['state'],'staged');r['phase']='commit';self.assertEqual(self.rotate(r)['state'],'revoked')
 def test_phase_write_failure_never_removes_old_key(self):
  r=self.setup_rotation();self.rotate(r);r['phase']='commit';replace=os.replace
  def fail(src,dst):
   if dst.endswith('/phase'):raise OSError('full disk')
   return replace(src,dst)
  with patch.object(m.os,'replace',fail):
   with self.assertRaises(OSError):self.rotate(r)
  self.assertIn(KEY.strip(),self.keys());self.assertIn(NEW.strip(),self.keys())
 def test_foreign_key_source_unit_or_identity_refused(self):
  for suffix in ['sshd_config','host_identity']:
   # Each test starts from a valid installed host, but never repairs foreign data.
   r=self.setup_rotation();p=Path(m.CONFIG+'/'+suffix);before=p.read_bytes();p.write_bytes(before+b'foreign')
   with self.assertRaises(m.Refused):self.rotate(r)
   p.write_bytes(before)
 def test_revoked_old_key_reappearance_not_silently_adopted(self):
  r=self.setup_rotation();old=self.keys();self.rotate(r);r['phase']='commit';self.rotate(r);Path(m.CONFIG+'/authorized_keys').write_text(old);r['phase']='stage'
  with self.assertRaises(m.Refused):self.rotate(r)
if __name__=='__main__':unittest.main()
