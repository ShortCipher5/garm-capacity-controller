"""Mock image save only. Never invokes Docker, starts services or uploads."""
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

spec=importlib.util.spec_from_file_location('pack',Path(__file__).with_name('pack-pilot-images.py'))
pack=importlib.util.module_from_spec(spec);spec.loader.exec_module(pack)

class Images(unittest.TestCase):
    def setUp(self):
        self.configs={r:json.dumps({'os':'linux','architecture':'amd64','rootfs':{'type':'layers','diff_ids':[]},'config':{'Labels':{'fixture':r}}}).encode() for r in pack.ROLES}
        self.ids={r:'sha256:'+hashlib.sha256(v).hexdigest() for r,v in self.configs.items()}
        self.patch=patch.object(pack,'STOCK_ID',self.ids['stock']);self.patch.start();self.addCleanup(self.patch.stop)
        self.record={'schema':'disposable-three-service-preboot/v2','source_commit':'a'*40,'run_id':'1','run_attempt':'1',**{r+'_image':v for r,v in self.ids.items()},**{k:'e'*64 for k in pack.EXECUTABLE_HASHES},**{k:True for k in pack.TRUE},**{k:False for k in pack.FALSE}}
    def archive(self,path,ids,extra=False):
        with tarfile.open(path,'w') as out:
            def add(name,data):
                info=tarfile.TarInfo(name);info.size=len(data);out.addfile(info,io.BytesIO(data))
            rows=[]
            for role in ids:
                name=ids[role][7:]+'.json';add(name,self.configs[role]);rows.append({'Config':name,'RepoTags':None,'Layers':[]})
            add('manifest.json',json.dumps(rows).encode())
            if extra:add('private-state.db',b'not an image')
    def test_export_consumes_exact_ids_and_writes_bounded_manifest(self):
        with tempfile.TemporaryDirectory() as root:
            result=pack.pack(json.dumps(self.record).encode(),Path(root)/'new','a'*40,self.archive)
            self.assertEqual(result['roles'],self.ids);self.assertFalse(result['uploaded']);self.assertFalse(result['deployment_authorized'])
            self.assertEqual(result['observed_executable_sha256'], {'gateway':'e'*64,'ingress':'e'*64})
            self.assertNotIn(root,json.dumps(result));self.assertEqual(result['platform'],'linux/amd64')
            with self.assertRaises(FileExistsError):pack.pack(json.dumps(self.record).encode(),Path(root)/'new','a'*40,self.archive)
    def test_invalid_evidence_never_calls_save(self):
        for key,value in [('schema','disposable-three-service-preboot/v1'),('ledger_image','tag'),('source_commit','b'*40),('actual_three_binaries_started',1),('host_ports_published',True),('stock_image','sha256:'+'0'*64),('exact_four_ipv4_loopback_listeners_verified',False),('exact_four_ipv4_loopback_listeners_verified',1),('gateway_executable_sha256','e'*63),('gateway_executable_sha256','E'*64),('ingress_executable_sha256','sha256:'+'e'*64),('ingress_executable_sha256',None)]:
            record=dict(self.record);record[key]=value
            with tempfile.TemporaryDirectory() as root:
                with self.assertRaises(ValueError):pack.pack(json.dumps(record).encode(),Path(root)/'new','a'*40,lambda *_:self.fail('save called'))
                self.assertFalse((Path(root)/'new').exists())
    def test_missing_or_extra_fields_refused_before_save(self):
        for key in self.record:
            record=dict(self.record);del record[key]
            with self.subTest(key=key), tempfile.TemporaryDirectory() as root:
                with self.assertRaises(ValueError):
                    pack.pack(json.dumps(record).encode(),Path(root)/'new','a'*40,lambda *_:self.fail('save called'))
                self.assertFalse((Path(root)/'new').exists())
        record=dict(self.record);record['runtime_verified']=True
        with self.assertRaises(ValueError):pack.evidence(json.dumps(record).encode())

    def test_extra_state_refused_without_success_manifest(self):
        with tempfile.TemporaryDirectory() as root:
            dest=Path(root)/'new'
            with self.assertRaises(ValueError):pack.pack(json.dumps(self.record).encode(),dest,'a'*40,lambda p,i:self.archive(p,i,True))
            self.assertFalse((dest/'manifest.json').exists())
    def test_image_config_bytes_must_match_recorded_id(self):
        self.configs['gateway']=b'{"os":"linux","architecture":"amd64"}'
        with tempfile.TemporaryDirectory() as root:
            with self.assertRaises(ValueError):pack.pack(json.dumps(self.record).encode(),Path(root)/'new','a'*40,self.archive)

if __name__=='__main__':unittest.main()
