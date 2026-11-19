"""Synthetic bytes consumed by the real pinned Go checker, never mocked proof.
Run with GARM_TEST_ANCILLARY_PROOF pointing to the locally built reviewed helper.
"""
import base64
import hashlib
import importlib.util
import io
import json
import os
import subprocess
from unittest.mock import patch
from pathlib import Path
import tarfile
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('verify', HERE/'verify-single-role-archive.py')
v = importlib.util.module_from_spec(spec); spec.loader.exec_module(v)
META = {'server_version':'28.0.4','api_version':'1.48','engine_git_commit':'6430e49',
        'platform':'linux/amd64','storage_driver':'overlay2','image_store':None}

@unittest.skipUnless(os.environ.get('GARM_TEST_ANCILLARY_PROOF'), 'requires real reviewed Go helper; no mocked success')
class Modern(unittest.TestCase):
    def fixture(self, path, change=lambda files:None):
        fixture=json.loads((HERE/'synthetic-ancillary-fixture.json').read_text())
        files={}
        def blob(raw, media):
            digest='sha256:'+hashlib.sha256(raw).hexdigest()
            files['blobs/sha256/'+digest[7:]]=raw
            return {'mediaType':media,'digest':digest,'size':len(raw)}
        layer=blob(b'synthetic uncompressed layer bytes',v.LAYER)
        config=blob(base64.b64decode(fixture['config']),v.CONFIG)
        manifest=blob(json.dumps({'schemaVersion':2,'mediaType':v.MANIFEST,'config':config,'layers':[layer]}).encode(),v.MANIFEST)
        files['index.json']=json.dumps({'schemaVersion':2,'mediaType':v.INDEX,'manifests':[manifest]}).encode()
        files['oci-layout']=b'{"imageLayoutVersion":"1.0.0"}'
        files['manifest.json']=json.dumps([{'Config':'blobs/sha256/'+config['digest'][7:],'RepoTags':None,
            'Layers':['blobs/sha256/'+layer['digest'][7:]],'LayerSources':{layer['digest']:layer}}]).encode()
        for node in fixture['nodes']:files['blobs/sha256/'+node['BlobDigest'][7:]]=base64.b64decode(node['Bytes'])
        change(files)
        with tarfile.open(path,'w',format=tarfile.USTAR_FORMAT) as out:
            for name,raw in files.items():
                info=tarfile.TarInfo(name);info.size=len(raw);out.addfile(info,io.BytesIO(raw))
        return config['digest']

    def verify(self,path,identity,meta=META):
        return v.verify_modern_single_role_archive(path,identity,meta,os.environ['GARM_TEST_ANCILLARY_PROOF'])

    def test_full_closure_and_actual_derivation_without_authority(self):
        with tempfile.TemporaryDirectory() as root:
            path=Path(root)/'image.tar';identity=self.fixture(path)
            result=self.verify(path,identity)
            self.assertTrue(result['closure_verified']);self.assertTrue(result['ancillary_derivation_verified'])
            self.assertEqual(result['ancillary_nodes'],1)
            self.assertFalse(result['archive_accepted']);self.assertFalse(result['deployment_authorized'])
            self.assertEqual([p.name for p in Path(root).iterdir()],['image.tar'])
            with self.assertRaises(ValueError):v.verify_single_role_archive(path,identity)

    def test_archive_opened_once_and_in_place_changes_refused(self):
        with tempfile.TemporaryDirectory() as root:
            path=Path(root)/'image.tar';identity=self.fixture(path)
            real_open=os.open
            opens=[]
            def observed_open(*args,**kwargs):
                if str(args[0])==str(path):opens.append(args[0])
                return real_open(*args,**kwargs)
            with patch.object(os,'open',side_effect=observed_open):self.verify(path,identity)
            self.assertEqual(len(opens),1)
            real_run=subprocess.run
            def change_after_actual_proof(*args,**kwargs):
                result=real_run(*args,**kwargs)
                with path.open('ab') as out:out.write(bytes(512))
                return result
            with patch.object(subprocess,'run',side_effect=change_after_actual_proof):
                with self.assertRaises(ValueError):self.verify(path,identity)

    def test_exact_auxiliary_bytes_and_complete_set_required(self):
        node=json.loads((HERE/'synthetic-ancillary-fixture.json').read_text())['nodes'][0]
        name='blobs/sha256/'+node['BlobDigest'][7:]
        def rehash_changed(files):
            raw=files.pop(name)+b' '
            files['blobs/sha256/'+hashlib.sha256(raw).hexdigest()]=raw
        for change in [lambda f:f.pop(name),lambda f:f.update({name:b'changed'}),rehash_changed,
                       lambda f:f.update({'blobs/sha256/'+hashlib.sha256(b'{}').hexdigest():b'{}'}),
                       lambda f:f.update({'private.token':b'SYNTHETIC_NOT_SECRET'})]:
            with self.subTest(change=change),tempfile.TemporaryDirectory() as root:
                path=Path(root)/'image.tar';identity=self.fixture(path,change)
                with self.assertRaises(ValueError):self.verify(path,identity)

    def test_combined_tags_layersources_and_pin_refused(self):
        def change_row(files, fn):
            value=json.loads(files['manifest.json']);fn(value);files['manifest.json']=json.dumps(value).encode()
        for fn in [lambda x:x.append(dict(x[0])),lambda x:x[0].update(RepoTags=['unapproved:tag']),
                   lambda x:x[0].update(LayerSources={}),lambda x:x[0].update(Layers=[])]:
            with tempfile.TemporaryDirectory() as root:
                path=Path(root)/'image.tar';identity=self.fixture(path,lambda f:change_row(f,fn))
                with self.assertRaises(ValueError):self.verify(path,identity)
        with tempfile.TemporaryDirectory() as root:
            path=Path(root)/'image.tar';identity=self.fixture(path)
            for meta in [dict(META,server_version='29.0.0'),dict(META,engine_git_commit='unknown'),dict(META,image_store='containerd')]:
                with self.assertRaises(ValueError):self.verify(path,identity,meta)
            with self.assertRaises(ValueError):self.verify(path,'sha256:'+'f'*64)

if __name__=='__main__':unittest.main()
