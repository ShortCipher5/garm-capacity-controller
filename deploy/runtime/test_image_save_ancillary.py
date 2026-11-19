import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

spec=importlib.util.spec_from_file_location('a',Path(__file__).with_name('image-save-ancillary.py'))
a=importlib.util.module_from_spec(spec);spec.loader.exec_module(a)


class Ancillary(unittest.TestCase):
    def fixture(self,path,change=lambda nodes:None,tamper=False):
        config={'created':'synthetic-time','os':'linux','architecture':'amd64','config':{'Env':['DO_NOT_EMIT']}}
        raw=json.dumps(config).encode();identity='sha256:'+hashlib.sha256(raw).hexdigest()
        config_path=a.blob_path(identity)
        nodes=[{'id':'a'*64,'created':'epoch','container_config':{},'os':'linux'},
               {'id':'b'*64,'parent':'a'*64,'container_config':{},**config}]
        change(nodes)
        files={config_path:raw,'manifest.json':json.dumps([{'Config':config_path,'Layers':[]}]).encode(),
               'index.json':b'{"manifests":[]}','oci-layout':b'{}'}
        for node in nodes:
            raw=json.dumps(node).encode();digest=hashlib.sha256(raw).hexdigest()
            files['blobs/sha256/'+digest]=raw+(b' ' if tamper else b'')
        with tarfile.open(path,'w') as out:
            for name,raw in files.items():
                info=tarfile.TarInfo(name);info.size=len(raw);out.addfile(info,io.BytesIO(raw))
        return {'gateway':identity}

    def test_closed_shape_and_shared_relation_never_derivation(self):
        with tempfile.TemporaryDirectory() as root:
            path=Path(root)/'a.tar';ids=self.fixture(path)
            value=a.describe(path,ids)
            self.assertTrue(value['single_chain_shape'])
            self.assertEqual(value['extra_blob_hash_matches'],2)
            self.assertEqual(value['terminal_shared_config_matches'],1)
            self.assertFalse(value['derivation_verified'])
            self.assertTrue(value['diagnostic_only'])
            for private in ('DO_NOT_EMIT','synthetic-time','a'*64,root):
                self.assertNotIn(private,json.dumps(value))

    def test_drift_unknown_missing_duplicate_cycles_are_visible(self):
        cases=[
            (lambda n:n[1].update(parent='f'*64),'missing_parents'),
            (lambda n:n[0].update(parent='b'*64),'cycle_nodes'),
            (lambda n:n[1].update(id='a'*64),'duplicate_ids'),
            (lambda n:n[0].update(secret='DO_NOT_EMIT'),'unknown_shape_nodes'),
            (lambda n:n[0].update(id='wrong'),'invalid_identity_nodes'),
        ]
        for change,field in cases:
            with self.subTest(field=field),tempfile.TemporaryDirectory() as root:
                path=Path(root)/'a.tar';ids=self.fixture(path,change)
                value=a.describe(path,ids)
                self.assertGreater(value[field],0)
                self.assertFalse(value['single_chain_shape'])
                self.assertNotIn('DO_NOT_EMIT',json.dumps(value))

    def test_hash_and_metadata_bounds_fail_closed(self):
        with tempfile.TemporaryDirectory() as root:
            path=Path(root)/'a.tar';ids=self.fixture(path,tamper=True)
            with self.assertRaises(ValueError):a.describe(path,ids)
        with tempfile.TemporaryDirectory() as root:
            path=Path(root)/'a.tar';ids=self.fixture(path,lambda n:n[0].update(comment='x'*(256*1024)))
            with self.assertRaises(ValueError):a.describe(path,ids)

    def test_duplicate_keys_and_node_count_bound(self):
        with self.assertRaises(ValueError):
            a.unique([('id','a'*64),('id','b'*64)])
        with tempfile.TemporaryDirectory() as root:
            path=Path(root)/'a.tar'
            def many(nodes):
                nodes[:]=[{'id':format(i,'064x'),'created':'epoch','container_config':{},'os':'linux'} for i in range(257)]
            ids=self.fixture(path,many)
            with self.assertRaises(ValueError):a.describe(path,ids)

    def test_final_relation_mismatch_does_not_imply_derivation(self):
        with tempfile.TemporaryDirectory() as root:
            path=Path(root)/'a.tar';ids=self.fixture(path,lambda n:n[1].update(architecture='arm64'))
            value=a.describe(path,ids)
            self.assertTrue(value['single_chain_shape'])
            self.assertEqual(value['terminal_shared_config_matches'],0)
            self.assertFalse(value['derivation_verified'])


if __name__=='__main__':unittest.main()
