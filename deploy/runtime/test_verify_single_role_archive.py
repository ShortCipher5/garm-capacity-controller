"""Pure synthetic tar fixtures; no Docker, host reads, extraction or import."""
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

spec=importlib.util.spec_from_file_location('verify',Path(__file__).with_name('verify-single-role-archive.py'))
verify=importlib.util.module_from_spec(spec);spec.loader.exec_module(verify)


class Closure(unittest.TestCase):
    def fixture(self, path, target='', change=lambda x:None):
        files={}
        def blob(value, media, raw=False):
            data=value if raw else json.dumps(value).encode()
            digest='sha256:'+hashlib.sha256(data).hexdigest()
            files['blobs/sha256/'+digest[7:]]=data
            return {'mediaType':media,'digest':digest,'size':len(data)}
        layer=blob(b'synthetic uncompressed layer bytes',verify.LAYER,True)
        config={'os':'linux','architecture':'amd64','rootfs':{'type':'layers','diff_ids':[layer['digest']]}}
        if target=='config':change(config)
        cfg=blob(config,verify.CONFIG)
        manifest={'schemaVersion':2,'mediaType':verify.MANIFEST,'config':cfg,'layers':[layer]}
        if target=='manifest':change(manifest)
        desc=blob(manifest,verify.MANIFEST)
        index={'schemaVersion':2,'mediaType':verify.INDEX,'manifests':[desc]}
        if target=='index':change(index)
        docker=[{'Config':'blobs/sha256/'+cfg['digest'][7:],'RepoTags':None,
                 'Layers':['blobs/sha256/'+layer['digest'][7:]],'LayerSources':{layer['digest']:layer}}]
        if target=='docker':change(docker)
        files.update({'oci-layout':b'{"imageLayoutVersion":"1.0.0"}',
                      'index.json':json.dumps(index).encode(),'manifest.json':json.dumps(docker).encode()})
        if target=='files':change(files)
        with tarfile.open(path,'w',format=tarfile.USTAR_FORMAT) as out:
            for name in ('blobs','blobs/sha256'):
                info=tarfile.TarInfo(name);info.type=tarfile.DIRTYPE;out.addfile(info)
            for name,data in files.items():
                info=tarfile.TarInfo(name);info.size=len(data);out.addfile(info,io.BytesIO(data))
            if target=='tar':change(out)
        return cfg['digest']

    def test_exact_single_role_closure(self):
        with tempfile.TemporaryDirectory() as root:
            path=Path(root)/'image.tar';identity=self.fixture(path)
            result=verify.verify_single_role_archive(path,identity)
            self.assertEqual(result['image_id'],identity)
            self.assertTrue(result['closure_verified'])
            self.assertFalse(result['deployment_authorized'])
            self.assertEqual(result['layers'],1)
            self.assertEqual(sorted(p.name for p in Path(root).iterdir()),['image.tar'])

    def test_semantic_drift_is_rejected_even_with_rehashed_metadata(self):
        cases=[
            ('config',lambda x:x.update(architecture='arm64')),
            ('config',lambda x:x.update(os='windows')),
            ('config',lambda x:x['rootfs'].update(diff_ids=['sha256:'+'0'*64])),
            ('manifest',lambda x:x.update(schemaVersion=True)),
            ('manifest',lambda x:x.update(annotations={})),
            ('manifest',lambda x:x['layers'][0].update(mediaType='application/vnd.oci.image.layer.v1.tar+gzip')),
            ('manifest',lambda x:x['layers'][0].update(size=True)),
            ('manifest',lambda x:x['layers'][0].update(size=verify.MAX_ARCHIVE+1)),
            ('manifest',lambda x:x.update(layers=x['layers']*257)),
            ('index',lambda x:x.update(manifests=x['manifests']*2)),
            ('index',lambda x:x['manifests'][0].update(annotations={})),
            ('index',lambda x:x['manifests'][0].update(mediaType=verify.INDEX)),
            ('index',lambda x:x['manifests'][0].update(size=1)),
            ('docker',lambda x:x.append(dict(x[0]))),
            ('docker',lambda x:x[0].update(RepoTags=['unreviewed:tag'])),
            ('docker',lambda x:x[0].update(Parent='sha256:'+'0'*64)),
            ('docker',lambda x:x[0].update(Config='../config')),
            ('docker',lambda x:x[0].update(Layers=[])),
            ('docker',lambda x:x[0].update(LayerSources={})),
        ]
        for target,change in cases:
            with self.subTest(target=target), tempfile.TemporaryDirectory() as root:
                path=Path(root)/'image.tar';identity=self.fixture(path,target,change)
                with self.assertRaises(ValueError):verify.verify_single_role_archive(path,identity)

    def test_extra_legacy_or_unrelated_blobs_missing_content_and_duplicates_refused(self):
        cases=[
            lambda f:f.update({'blobs/sha256/'+'0'*64:b'{"id":"legacy-v1-config"}'}),
            lambda f:f.update({'private.db':b'unrelated'}),
            lambda f:f.update({'index.json':b'{"manifests":[],"manifests":[]}'}),
            lambda f:f.update({'index.json':b' '* (verify.MAX_METADATA+1)}),
            lambda f:f.update({'blobs/sha256/'+next(k.rsplit('/',1)[1] for k in f if k.startswith('blobs/')):b'changed'}),
            lambda f:f.pop('oci-layout'),
        ]
        for change in cases:
            with tempfile.TemporaryDirectory() as root:
                path=Path(root)/'image.tar';identity=self.fixture(path,'files',change)
                with self.assertRaises(ValueError):verify.verify_single_role_archive(path,identity)

    def test_tar_links_duplicate_members_and_trailing_data_refused(self):
        def extra(out, name, kind):
            info=tarfile.TarInfo(name);info.type=kind;info.linkname='/private'
            out.addfile(info)
        for name,kind in [('index.json',tarfile.REGTYPE),('blobs/extra',tarfile.SYMTYPE),
                          ('blobs/extra',tarfile.LNKTYPE),('../escape',tarfile.REGTYPE)]:
            with tempfile.TemporaryDirectory() as root:
                path=Path(root)/'image.tar'
                identity=self.fixture(path,'tar',lambda out:extra(out,name,kind))
                with self.assertRaises(ValueError):verify.verify_single_role_archive(path,identity)
        with tempfile.TemporaryDirectory() as root:
            path=Path(root)/'image.tar';identity=self.fixture(path)
            with path.open('ab') as out:out.write(b'hidden')
            with self.assertRaises(ValueError):verify.verify_single_role_archive(path,identity)

    def test_hidden_gnu_and_empty_pax_physical_records_refused(self):
        for kind,payload in [(tarfile.GNUTYPE_LONGNAME,b'blobs/\0'),
                             (tarfile.GNUTYPE_LONGLINK,b'\0'),
                             (tarfile.XHDTYPE,b''),(tarfile.XGLTYPE,b'')]:
            with self.subTest(kind=kind),tempfile.TemporaryDirectory() as root:
                path=Path(root)/'image.tar';identity=self.fixture(path)
                original=path.read_bytes()
                extension=tarfile.TarInfo('././@LongLink');extension.type=kind;extension.size=len(payload)
                physical=extension.tobuf(format=tarfile.USTAR_FORMAT)+payload
                physical+=bytes((-len(physical))%512)
                path.write_bytes(physical+original)
                # Demonstrate the abstraction gap: high-level iteration hides
                # the physical extension and returns the original member names.
                with tarfile.open(fileobj=io.BytesIO(original)) as before,tarfile.open(path) as after:
                    self.assertEqual(before.getnames(),after.getnames())
                with self.assertRaises(ValueError):verify.verify_single_role_archive(path,identity)

    def test_physical_padding_checksum_size_and_hidden_tail_refused(self):
        def checksum(header):
            header[148:156]=b'        '
            header[148:156]=('%06o\0 ' % sum(header)).encode()
        with tempfile.TemporaryDirectory() as root:
            path=Path(root)/'image.tar';identity=self.fixture(path)
            original=path.read_bytes()
            with tarfile.open(path) as archive:
                member=next(m for m in archive if m.isfile() and m.size%512)
                padding=member.offset_data+member.size
            changes=[]
            bad=bytearray(original);bad[padding]=1;changes.append(bad)
            bad=bytearray(original);bad[0]^=1;changes.append(bad)
            header=bytearray(original[:512]);header[124:136]=b'77777777777\0';checksum(header)
            changes.append(header+original[512:])
            header=bytearray(original[:512]);header[500]=1;checksum(header)
            changes.append(header+original[512:])
            changes.append(original[:-1]);changes.append(original+b'X'+bytes(511))
            changes.append(original+bytes(10240))
            for bad in changes:
                path.write_bytes(bad)
                with self.assertRaises(ValueError):verify.verify_single_role_archive(path,identity)

    def test_expected_pin_and_size_bound(self):
        with tempfile.TemporaryDirectory() as root:
            path=Path(root)/'image.tar';self.fixture(path)
            with self.assertRaises(ValueError):verify.verify_single_role_archive(path,'sha256:'+'f'*64)
            with self.assertRaises(ValueError):verify.verify_single_role_archive(path,'tag')


if __name__=='__main__':unittest.main()
