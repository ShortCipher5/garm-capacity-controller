import copy,json,os,subprocess,tempfile,unittest
from pathlib import Path
from unittest.mock import patch
import prepare,run,fixture

class RunnerTest(unittest.TestCase):
 def test_actual_rendered_recipe_uses_retained_pins_and_known_graph(self):
  text,lock=prepare.render();self.assertNotIn('{{',text);self.assertNotIn('latest',text)
  self.assertEqual(sum(l.startswith('FROM ') for l in text.splitlines()),6)
  self.assertIn('USER 65533:65533',text);self.assertIn('public-ca.pem /etc/ssl/certs/ca-certificates.crt',text)
 def test_hidden_external_sources_and_parser_bypasses_refused(self):
  text,lock=prepare.render();refs={k:v['repository']+'@'+v['linux_amd64_digest'] for k,v in lock['images'].items()}
  for altered in [text.replace('COPY --from=go-build','COPY --from=ubuntu',1),text.replace('COPY --from=go-build','COPY --from=go-build --from=ubuntu',1),text+'\nRUN --mount=from=ubuntu echo hidden\n',text+'\nARG BUILDKIT_SYNTAX=unrecognized\n',text.replace('\nFROM ', '\n# escape=`\nFR`\nOM ubuntu:latest AS hidden\nFROM ',1),text.replace('FROM scratch AS missing-loader','FROM ubuntu:latest AS missing-loader')]:
   with self.assertRaises(prepare.Rejected):prepare.check_recipe(altered,refs)
 def test_not_hosted_refuses_before_any_command_or_engine(self):
  with patch.dict(os.environ,{},clear=True),patch.object(run,'command',side_effect=AssertionError('must never execute')) as commands:
   with self.assertRaises(run.Failed):run.run()
   commands.assert_not_called()
 def test_remote_engine_environment_refused_and_no_default_client(self):
  with tempfile.TemporaryDirectory(dir=Path(__file__).parent) as temp:
   base={'GITHUB_ACTIONS':'true','RUNNER_ENVIRONMENT':'github-hosted','RUNNER_TEMP':temp}
   for key in ('DOCKER_HOST','DOCKER_CONTEXT','BUILDKIT_HOST','BUILDX_BUILDER'):
    with self.assertRaises(run.Failed):run.hosted({**base,key:'unapproved'})
  with patch.object(run,'DOCKER_CONFIG_PATH',None),patch.object(run.subprocess,'run',side_effect=AssertionError('must not call client')) as child:
   with self.assertRaises(run.Failed):run.command(['docker','version'])
   child.assert_not_called()
 def test_docker_command_uses_only_local_socket_and_empty_auth_config(self):
  with tempfile.TemporaryDirectory(dir=Path(__file__).parent) as temp:
   config=Path(temp)/'docker-config';config.mkdir()
   with patch.object(run,'DOCKER_CONFIG_PATH',config),patch.dict(os.environ,{'DOCKER_HOST':'never-contact','GITHUB_TOKEN':'do-not-inherit'}),patch.object(run.subprocess,'run',return_value=subprocess.CompletedProcess([],0,b'',b'')) as child:
    run.command(['docker','version'])
    args=child.call_args.args[0];env=child.call_args.kwargs['env']
    self.assertEqual(args[:5],['docker','--host','unix:///var/run/docker.sock','--config',str(config)])
    self.assertNotIn('DOCKER_HOST',env);self.assertNotIn('GITHUB_TOKEN',env)
 def test_sudo_cleanup_does_not_inherit_workflow_credentials(self):
  with patch.dict(os.environ,{'GITHUB_TOKEN':'do-not-inherit'}),patch.object(run.subprocess,'run',return_value=subprocess.CompletedProcess([],0,b'',b'')) as child:
   run.command(['sudo','-n','rm','-rf','--','/never-executed-unit-path'])
   self.assertEqual(set(child.call_args.kwargs['env']),{'PATH','HOME'})
 def test_result_requires_exact_kind_exit_and_no_claims(self):
  value={'kind':'unknown-authority','test_probe_only':True,'installed_stack_qualified':False,'runtime_profile_qualified':False,'startup_authority':False,'deployment_authorized':False}
  result=subprocess.CompletedProcess([],42,json.dumps(value).encode(),b'');run.result_kind(result,'unknown-authority',42)
  for code,field,new in [(65,'kind','transport'),(42,'deployment_authorized',True),(42,'runtime_profile_qualified',0)]:
   x=dict(value);x[field]=new
   with self.assertRaises(run.Failed):run.result_kind(subprocess.CompletedProcess([],code,json.dumps(x).encode(),b''),'unknown-authority',42)
 def record(self):return {'lab':'generated','image':'sha256:test','user':'65531:65531','network':'none','pid':'','privileged':False,'readonly':True,'caps_add':None,'caps_drop':['ALL'],'security':['no-new-privileges'],'ports':{},'restart':'no','volumes':None,'memory':512*1024*1024,'cpus':1000000000,'pids':128,'tmpfs':{'/tmp':'rw,noexec,nosuid,nodev,size=16m,uid=65531,gid=65531,mode=0700'},'mounts':[]}
 def test_topology_unsafe_controls_rejected(self):
  good=self.record();run.topology(good,'generated','sha256:test',65531,'none',[],['/tmp'])
  for key,value in [('user','0:0'),('network','host'),('pid','host'),('privileged',True),('readonly',False),('caps_add',['SYS_ADMIN']),('restart','unless-stopped'),('volumes',{'/var/lib/garm':{}}),('ports',{'9444/tcp':[{}]})]:
   x=copy.deepcopy(good);x[key]=value
   with self.assertRaises(run.Failed):run.topology(x,'generated','sha256:test',65531,'none',[],['/tmp'])
 def test_new_generated_fixture_has_disjoint_keys_and_no_owner_mutation(self):
  with tempfile.TemporaryDirectory(dir=Path(__file__).parent) as t:
   root=Path(t)/'generated'
   with patch.object(os,'chown',side_effect=AssertionError('not allowed')),patch.object(fixture.subprocess,'run',wraps=subprocess.run) as commands:
    nonce=fixture.prepare(root,b'public fixture prefix',False)
    self.assertTrue(all(call.args[0][0]=='openssl' for call in commands.call_args_list))
   self.assertEqual(len(nonce),32);self.assertNotEqual((root/'garm/server.key').read_bytes(),(root/'anchor/server.key').read_bytes())
   self.assertIn('enable=true',(root/'garm/config.toml').read_text());self.assertEqual(list((root/'state').iterdir()),[])
   with self.assertRaises(RuntimeError):fixture.prepare(root,b'',False)
 def test_private_parent_chown_last_without_privileged_execution(self):
  with tempfile.TemporaryDirectory(dir=Path(__file__).parent) as t:
   root=Path(t)/'generated';sealed=set();order=[];sentinel=Path(t)/'preexisting';sentinel.write_bytes(b'unchanged')
   roles={'anchor':65530,'garm':65531,'state':65531,'bad-state':65531,'garm-canary':65531,'tools-canary':65533}
   real_run=subprocess.run;real_symlink=Path.is_symlink
   def guarded_metadata(path):
    if path.parent in sealed:raise PermissionError('simulated private mapped owner')
    return real_symlink(path)
   def no_privilege(args,**kwargs):
    if args[0]=='sudo':
     self.assertEqual(args[1:3],['-n','chown'])
     path=Path(args[-1]);relative=path.relative_to(root);role=relative.parts[0]
     self.assertIn(role,roles);self.assertLessEqual(len(relative.parts),2);self.assertEqual(args[3],str(roles[role])+':'+str(roles[role]));order.append(path)
     if path.is_dir():sealed.add(path)
     return subprocess.CompletedProcess(args,0,b'',b'')
    self.assertEqual(args[0],'openssl');return real_run(args,**kwargs)
   with patch.object(Path,'is_symlink',guarded_metadata),patch.object(fixture.subprocess,'run',side_effect=no_privilege):
    fixture.prepare(root,b'public fixture prefix',True)
    before=list(order)
    with self.assertRaises(RuntimeError):fixture.prepare(root,b'',True)
    self.assertEqual(before,order)
   self.assertEqual(sealed,{root/name for name in roles});self.assertEqual(sentinel.read_bytes(),b'unchanged')
   expected={p for name in roles for p in [root/name,*(root/name).iterdir()]};self.assertEqual(set(order),expected);self.assertEqual(len(order),len(expected))
   for name in roles:
    parent=root/name;positions=[n for n,p in enumerate(order) if p==parent or p.parent==parent]
    self.assertTrue(positions);self.assertEqual(order[max(positions)],parent)
 def test_monotonic_names_do_not_reuse_removed_container_suffix(self):
  with tempfile.TemporaryDirectory(dir=Path(__file__).parent) as t:
   lab=run.Lab(Path(t));lab.images['image']='sha256:test';names=[]
   def fake(args,timeout=180):
    if args[:2]==['docker','create']:names.append(args[args.index('--name')+1]);return subprocess.CompletedProcess(args,0,b'a'*64+b'\n',b'')
    return subprocess.CompletedProcess(args,0,b'',b'')
   with patch.object(run,'command',side_effect=fake),patch.object(lab,'inspect',return_value={'entrypoint':['/probe']}),patch.object(run,'topology'):
    a=lab.create('image',65531,'none','/probe');lab.names.remove(a);b=lab.create('image',65531,'none','/probe')
   self.assertNotEqual(a,b);self.assertEqual(len(set(names)),2)

if __name__=='__main__':unittest.main()
