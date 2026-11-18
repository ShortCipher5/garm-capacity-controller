import importlib.util, pathlib, subprocess, sys, unittest, types, io, json
from unittest.mock import patch
from contextlib import nullcontext
spec=importlib.util.spec_from_file_location('upload',pathlib.Path(__file__).with_name('upload.py'))
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
class OutputTests(unittest.TestCase):
    def runchild(self,source):
        child=subprocess.Popen([sys.executable,'-c',source],stdout=subprocess.PIPE)
        try:return m.bounded_output(child)
        finally:
            if child.poll() is None:child.kill()
            child.wait();child.stdout.close()
    def test_success(self):self.assertEqual(self.runchild("print('receipt')"),b'receipt\n')
    def test_overflow(self):
        with self.assertRaises(ValueError):self.runchild("print('x'*20000)")
    def test_failure(self):
        with self.assertRaises(ValueError):self.runchild("raise SystemExit(1)")
class SpaceTests(unittest.TestCase):
    def fs(self,free=10<<30,inodes=1000,block=4096,flags=0):
        return types.SimpleNamespace(f_frsize=block,f_bavail=free//block if block else 0,f_blocks=100<<20,f_favail=inodes,f_flag=flags)
    def test_same_filesystem_counts_both_copies(self):
        need=100<<20
        with patch.object(m.os,'stat',return_value=types.SimpleNamespace(st_dev=1)),patch.object(m.os,'statvfs',return_value=self.fs(m.DISK_HEADROOM+need+m.METADATA_ALLOWANCE)):
            with self.assertRaises(ValueError):m.check_space(need,need)
        with patch.object(m.os,'stat',return_value=types.SimpleNamespace(st_dev=1)),patch.object(m.os,'statvfs',return_value=self.fs(m.DISK_HEADROOM+2*need+2*m.METADATA_ALLOWANCE)):
            m.check_space(need,need)
    def test_separate_filesystems_have_independent_budgets(self):
        with patch.object(m.os,'stat',side_effect=[types.SimpleNamespace(st_dev=1),types.SimpleNamespace(st_dev=2)]),patch.object(m.os,'statvfs',side_effect=[self.fs(m.DISK_HEADROOM+(100<<20)+m.METADATA_ALLOWANCE),self.fs(m.DISK_HEADROOM+(50<<20)+m.METADATA_ALLOWANCE)]):m.check_space(100<<20,50<<20)
        with patch.object(m.os,'stat',side_effect=[types.SimpleNamespace(st_dev=1),types.SimpleNamespace(st_dev=2)]),patch.object(m.os,'statvfs',side_effect=[self.fs(),self.fs(0)]):
            with self.assertRaises(ValueError):m.check_space(100<<20,50<<20)
    def test_unknown_readonly_inode_or_reserved_space_refused(self):
        for v in [self.fs(0),self.fs(inodes=79),self.fs(block=0),self.fs(flags=m.os.ST_RDONLY),self.fs(block=1<<20)]:
            with self.subTest(v=v),patch.object(m.os,'statvfs',return_value=v):
                with self.assertRaises(ValueError):m.available(m.BASE,1)
        with patch.object(m.os,'statvfs',side_effect=OSError('unavailable')):
            with self.assertRaises(OSError):m.available(m.BASE,1)
    def header(self,mode):
        files=[dict(name=n,size=64<<20 if n=='runtime.raw' else 64,sha256='a'*64) for n in [m.HELPER]+m.NAMES]
        manifest=dict(version='native-runtime-assets/v1',id='b'*64,architecture='amd64',files={f['name']:dict(size=f['size'],sha256=f['sha256']) for f in files[1:]})
        raw=json.dumps(manifest)
        return json.dumps(dict(mode=mode,manifest=manifest,manifest_json=raw,manifest_digest=m.sha(raw.encode()),files=files)).encode()+b'\n'
    def test_preflight_before_receiving_payload_or_creating_id(self):
        raw=self.header('prepare');stdin=types.SimpleNamespace(buffer=io.BytesIO(raw+b'payload'))
        with patch.object(m.os,'geteuid',return_value=0),patch.object(m.sys,'stdin',stdin),patch.object(m,'staging_lock',return_value=nullcontext()),patch.object(m,'check_space',side_effect=ValueError('no space')) as check,patch.object(m.os,'mkdir') as mkdir:
            with self.assertRaises(ValueError):m.run()
            self.assertEqual(stdin.buffer.tell(),len(raw));mkdir.assert_not_called();check.assert_called_once()
    def test_inspection_remains_read_only_without_disk_budget(self):
        stdin=types.SimpleNamespace(buffer=io.BytesIO(self.header('inspect')))
        with patch.object(m.os,'geteuid',return_value=0),patch.object(m.sys,'stdin',stdin),patch.object(m,'staging_lock') as lock,patch.object(m,'check_space') as check,patch.object(m,'controlled',side_effect=ValueError('stop before fixture file read')):
            with self.assertRaises(ValueError):m.run()
            lock.assert_not_called();check.assert_not_called()
    def test_falling_space_stops_stream_before_helper(self):
        header=json.loads(self.header('prepare'));header['files'][0]['sha256']=m.sha(bytes(64));raw=json.dumps(header).encode()+b'\n'
        class Input:
            def readline(self,_):return raw
            def read(self,n):return bytes(n)
        checks=[]
        def falling(up,final):
            checks.append((up,final))
            if len(checks)>1:raise ValueError('headroom fell')
        with patch.object(m.os,'geteuid',return_value=0),patch.object(m.sys,'stdin',types.SimpleNamespace(buffer=Input())),patch.object(m,'staging_lock',return_value=nullcontext()),patch.object(m,'check_space',side_effect=falling),patch.object(m,'write'),patch.object(m,'syncdir'),patch.object(m.os,'mkdir'),patch.object(m.os,'chmod'),patch.object(m.os,'open',return_value=3),patch.object(m.os,'fdopen',side_effect=lambda *_a,**_k:io.BytesIO()),patch.object(m.os,'fchmod'),patch.object(m.os,'fsync'),patch.object(m.os,'close'),patch.object(m.subprocess,'Popen') as helper:
            with self.assertRaises(ValueError):m.run()
            helper.assert_not_called()
        self.assertEqual(len(checks),2);self.assertGreaterEqual(checks[0][0]-checks[1][0],m.CHECK_INTERVAL);self.assertLess(checks[0][0]-checks[1][0],m.CHECK_INTERVAL+65536);self.assertEqual(checks[0][1],checks[1][1])
    def test_parallel_staging_lock_refuses_and_closes(self):
        st=types.SimpleNamespace(st_mode=m.stat.S_IFREG|0o600,st_uid=0,st_nlink=1)
        with patch.object(m,'fixedparent'),patch.object(m.os,'open',return_value=3),patch.object(m.os,'fstat',return_value=st),patch.object(m.fcntl,'flock',side_effect=BlockingIOError()),patch.object(m.os,'close') as close:
            with self.assertRaises(BlockingIOError):
                with m.staging_lock():self.fail('entered locked staging')
            close.assert_called_once_with(3)
if __name__=='__main__':unittest.main()
