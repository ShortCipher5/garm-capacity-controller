"""Local unprivileged parser/hash tests; no root helper or host qualification."""
import importlib.util,pathlib,unittest,tempfile,os,json,hashlib,io
from unittest import mock
spec=importlib.util.spec_from_file_location('diagnostic',pathlib.Path(__file__).with_name('diagnostic.py'))
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
class DiagnosticTests(unittest.TestCase):
    def fixture(self,root):
        ident='a'*64;dest=pathlib.Path(root)/ident;dest.mkdir(mode=0o700)
        (dest/'bin').mkdir(mode=0o700);(dest/'private').mkdir(mode=0o700)
        data=b'nonexecuted fixture bytes';sha=hashlib.sha256(data).hexdigest()
        binary=dest/'bin/capacity-native-preflight';binary.write_bytes(data);binary.chmod(0o555)
        manifest=json.dumps({'version':'native-owner-preparation/v1','id':ident,'files':{'capacity-native-preflight':sha}},separators=(',',':')).encode()
        md=hashlib.sha256(manifest).hexdigest();unit=b'staged unit fixture'
        receipt={'version':'native-owner-preparation/v1','id':ident,'state':'prepared_not_activated','manifest_digest':md,'unit_digest':hashlib.sha256(unit).hexdigest(),'activation_authorized':False}
        files={'manifest.json':manifest,'receipt.json':json.dumps(receipt).encode(),'intent.json':json.dumps({'state':'preparing','manifest_digest':md}).encode(),'garm-capacity-native-domain.service.staged':unit}
        for name,data in files.items():p=dest/name;p.write_bytes(data);p.chmod(0o600)
        return dest,{'sha256':sha,'nonce':'b'*64,'receipt':receipt}
    def invoke(self,root,req):
        stdin=mock.Mock();stdin.buffer=io.BytesIO(json.dumps(req).encode())
        with mock.patch.object(m,'PARENT',root),mock.patch.object(m.os,'geteuid',return_value=0),mock.patch.object(m,'protected',side_effect=os.lstat),mock.patch.object(m.sys,'stdin',stdin),mock.patch.object(m.subprocess,'Popen',side_effect=RuntimeError('reached pinned execution')):
            m.main()
    def test_valid_reaches_execution_without_executing(self):
        with tempfile.TemporaryDirectory() as root:
            _,req=self.fixture(root)
            with self.assertRaisesRegex(RuntimeError,'reached pinned execution'):self.invoke(root,req)
    def test_metadata_hash_mode_and_path_fail_before_execution(self):
        for mode in ('manifest','receipt','binary','fifo','symlink','hardlink','mode','id'):
            with self.subTest(mode=mode),tempfile.TemporaryDirectory() as root:
                dest,req=self.fixture(root);binary=dest/'bin/capacity-native-preflight'
                if mode=='manifest':(dest/'manifest.json').write_bytes(b'{}')
                if mode=='receipt':req['receipt']['manifest_digest']='c'*64
                if mode=='binary':binary.chmod(0o600);binary.write_bytes(b'wrong same length bytes');binary.chmod(0o555)
                if mode=='fifo':binary.unlink();os.mkfifo(binary)
                if mode=='symlink':binary.unlink();binary.symlink_to('/dev/null')
                if mode=='hardlink':os.link(binary,dest/'other')
                if mode=='mode':binary.chmod(0o755)
                if mode=='id':req['receipt']['id']='../outside'
                try:self.invoke(root,req)
                except Exception as e:self.assertNotIn('reached pinned execution',str(e))
                else:self.fail('invalid recipe reached success')
if __name__=='__main__':unittest.main()
