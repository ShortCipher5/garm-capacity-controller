"""Synthetic metadata only; no Docker, layers, extraction or archive delivery."""
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

spec=importlib.util.spec_from_file_location('structure',Path(__file__).with_name('image-save-structure.py'))
structure=importlib.util.module_from_spec(spec);spec.loader.exec_module(structure)


class Structure(unittest.TestCase):
    def fixture(self, path, mutate=lambda files:None):
        self.ids={r:'sha256:'+c*64 for r,c in zip(('stock','gateway','ingress','ledger'),'abcd')}
        manifests=[]
        files={}
        for identity in self.ids.values():
            value={'schemaVersion':2,'mediaType':'application/vnd.oci.image.manifest.v1+json',
                   'config':{'mediaType':'application/vnd.oci.image.config.v1+json','digest':identity,'size':123},
                   'layers':[{'mediaType':'application/vnd.oci.image.layer.v1.tar','digest':'sha256:'+'e'*64,'size':456}]}
            raw=json.dumps(value).encode()
            digest=hashlib.sha256(raw).hexdigest()
            files['blobs/sha256/'+digest]=raw
            manifests.append({'mediaType':value['mediaType'],'size':len(raw),'digest':'sha256:'+digest})
        files['index.json']=json.dumps({'schemaVersion':2,'manifests':manifests}).encode()
        files['manifest.json']=json.dumps([{'Config':'ignored','RepoTags':None,'Layers':[],'LayerSources':{}}]*4).encode()
        mutate(files)
        with tarfile.open(path,'w') as out:
            for name, raw in files.items():
                info=tarfile.TarInfo(name);info.size=len(raw)
                out.addfile(info,io.BytesIO(raw))

    def test_closed_counts_not_qualification(self):
        with tempfile.TemporaryDirectory() as root:
            path=Path(root)/'a.tar';self.fixture(path)
            value=structure.describe(path,self.ids)
            self.assertEqual(value['referenced_role_configs'],4)
            self.assertEqual(value['root_manifest_digest_size_matches'],4)
            self.assertEqual(value['layer_media_counts'],dict(tar=4,gzip=0,zstd=0,docker_gzip=0,unknown=0))
            self.assertTrue(value['docker_layer_sources_present'])
            self.assertTrue(value['diagnostic_only'])
            self.assertNotIn('ignored',json.dumps(value))
            self.assertNotIn('qualified',value)
            self.assertNotIn(root,json.dumps(value))

    def test_partial_roots_and_drift_are_visible(self):
        def change(files):
            value=json.loads(files['index.json']);value['manifests']=value['manifests'][:1]
            files['index.json']=json.dumps(value).encode()
        with tempfile.TemporaryDirectory() as root:
            path=Path(root)/'a.tar';self.fixture(path,change)
            value=structure.describe(path,self.ids)
            self.assertEqual(value['index_roots'],1)
            self.assertEqual(value['referenced_role_configs'],1)
        def drift(files):
            key=next(k for k in files if k.startswith('blobs/'))
            files[key]+=b' '
        with tempfile.TemporaryDirectory() as root:
            path=Path(root)/'a.tar';self.fixture(path,drift)
            value=structure.describe(path,self.ids)
            self.assertEqual(value['root_missing_or_invalid_blobs'],1)
            self.assertEqual(value['referenced_role_configs'],3)

    def test_duplicate_json_and_bounds_fail_closed(self):
        variants=[b'{"manifests":[],"manifests":[]}',b' '*65537,
                  json.dumps({'manifests':[{}]*33}).encode()]
        for raw in variants:
            with tempfile.TemporaryDirectory() as root:
                path=Path(root)/'a.tar';self.fixture(path,lambda files:files.update({'index.json':raw}))
                with self.assertRaises(ValueError):structure.describe(path,self.ids)

    def test_unknown_metadata_never_echoed(self):
        def change(files):
            value=json.loads(files['index.json'])
            value['manifests'][0]={'mediaType':'sensitive-untrusted-string','secret-field':'do-not-emit'}
            files['index.json']=json.dumps(value).encode()
        with tempfile.TemporaryDirectory() as root:
            path=Path(root)/'a.tar';self.fixture(path,change)
            value=structure.describe(path,self.ids)
            self.assertEqual(value['root_unknown_media_types'],1)
            self.assertFalse(value['root_descriptors_standard_fields_only'])
            self.assertNotIn('sensitive',json.dumps(value))
            self.assertNotIn('do-not-emit',json.dumps(value))


if __name__=='__main__':unittest.main()
