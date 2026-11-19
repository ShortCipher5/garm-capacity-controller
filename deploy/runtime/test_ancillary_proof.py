import base64
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch

HERE=Path(__file__).resolve().parent
spec=importlib.util.spec_from_file_location('p',HERE/'ancillary-proof.py')
p=importlib.util.module_from_spec(spec);spec.loader.exec_module(p)


class Adapter(unittest.TestCase):
    def fixture(self,path):
        files={}
        def blob(value):
            raw=json.dumps(value).encode();digest='sha256:'+hashlib.sha256(raw).hexdigest()
            files['blobs/sha256/'+digest[7:]]=raw
            return digest,raw
        identity,config=blob({'os':'linux','config':{'Env':['PRIVATE_SYNTHETIC_VALUE']}})
        layer='sha256:'+'e'*64
        manifest,raw=blob({'config':{'digest':identity},'layers':[{'digest':layer}]})
        aux,aux_raw=blob({'id':'f'*64,'created':'epoch'})
        files.update({'manifest.json':json.dumps([{'Config':'blobs/sha256/'+identity[7:],'RepoTags':None,'Layers':['blobs/sha256/'+layer[7:]]}]).encode(),
          'index.json':json.dumps({'manifests':[{'mediaType':'application/vnd.oci.image.manifest.v1+json','digest':manifest,'size':len(raw)}]}).encode(),
          'oci-layout':b'{}','blobs/sha256/'+layer[7:]:b'synthetic layer not consumed by ancillary proof'})
        with tarfile.open(path,'w') as out:
            for name,raw in files.items():
                info=tarfile.TarInfo(name);info.size=len(raw);out.addfile(info,io.BytesIO(raw))
        return identity,config,aux,aux_raw

    def test_private_bytes_consumed_and_only_closed_result_returned(self):
        with tempfile.TemporaryDirectory() as root:
            path=Path(root)/'image.tar';identity,config,aux,aux_raw=self.fixture(path)
            result={'schema':'moby-v28-ancillary-proof/v1','derived_nodes':1,'matched_nodes':1,
                    'ancillary_derivation_verified':True,'archive_accepted':False,'deployment_authorized':False}
            def helper(args,**kwargs):
                self.assertEqual(args,['/reviewed/helper'])
                self.assertEqual(kwargs['timeout'],30)
                self.assertIs(kwargs['stderr'],subprocess.DEVNULL)
                payload=json.loads(kwargs['input'])
                self.assertEqual(base64.b64decode(payload['config']),config)
                self.assertEqual(payload['nodes'],{aux:base64.b64encode(aux_raw).decode()})
                return subprocess.CompletedProcess(args,0,json.dumps(result).encode())
            with patch.object(p.subprocess,'run',side_effect=helper):
                self.assertEqual(p.prove(path,{'gateway':identity},{},'/reviewed/helper'),result)
            self.assertEqual([v.name for v in Path(root).iterdir()],['image.tar'])
            self.assertNotIn('PRIVATE_SYNTHETIC',json.dumps(result))

    def test_error_raw_or_authority_output_refused(self):
        with tempfile.TemporaryDirectory() as root:
            path=Path(root)/'image.tar';identity,*_=self.fixture(path)
            for value in (b'private error',b'{"secret":"PRIVATE_SYNTHETIC"}',b'x'*4097):
                with patch.object(p.subprocess,'run',return_value=subprocess.CompletedProcess([],0,value)):
                    with self.assertRaises((ValueError,TypeError)):
                        p.prove(path,{'gateway':identity},{},'/reviewed/helper')

    def test_wrong_identity_stops_before_helper(self):
        with tempfile.TemporaryDirectory() as root:
            path=Path(root)/'image.tar';self.fixture(path)
            with patch.object(p.subprocess,'run',side_effect=AssertionError('helper called')):
                with self.assertRaises(ValueError):p.prove(path,{'gateway':'sha256:'+'0'*64},{},'/helper')

    def test_opt_in_only_consumption(self):
        shell=(HERE/'test.sh').read_text()
        opt=shell.index('if [[ ${GARM_ARCHIVE_FORMAT_PROBE:-false} = true ]]')
        build=shell.index('--target ancillary-proof-artifact')
        proof=shell.index('export GARM_ANCILLARY_PROOF=')
        self.assertLess(opt,build);self.assertLess(build,proof)
        self.assertLess(proof,shell.index('timeout --kill-after=10s 360s python3 deploy/runtime/probe-image-save-format.py'))
        dockerfile=(HERE.parents[1]/'Dockerfile').read_text()
        self.assertLess(dockerfile.index('AS ancillary-proof-artifact'),dockerfile.index('FROM runtime-base AS gateway'))
        copies=[line for line in dockerfile.splitlines() if line.startswith('COPY --from=ancillary-proof-source ')]
        names=['neutral.go','neutral_test.go','final_v28.go','final_v28_test.go','main.go','main_test.go','LICENSE','NOTICE']
        self.assertEqual(copies,['COPY --from=ancillary-proof-source /'+name+' /src/ancillary-proof/'+name for name in names])
        self.assertNotIn('!deploy/',(HERE.parents[1]/'.dockerignore').read_text())
        self.assertIn('--build-context "ancillary-proof-source=$work/ancillary-proof-source"',shell)

    def test_helper_context_is_exact_and_excludes_private_siblings(self):
        shell=(HERE/'test.sh').read_text()
        start=shell.index('  mkdir -m 0700 "$work/ancillary-proof-source"')
        end=shell.index('  timeout --kill-after=10s 180s docker build',start)
        source_names=['neutral.go','neutral_test.go','final_v28.go','final_v28_test.go','main.go','main_test.go','LICENSE','NOTICE']
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp); source=root/'deploy/runtime/ancillary-proof';source.mkdir(parents=True)
            work=root/'work';work.mkdir()
            for name in source_names: (source/name).write_text(name)
            for name in ['private.token','fixture.json','extra.go']: (source/name).write_text('PRIVATE')
            (source.parent/'private.key').write_text('PRIVATE')
            subprocess.run(['bash','-euc',shell[start:end]],cwd=root,env={'PATH':'/usr/bin:/bin','work':str(work)},check=True)
            context=work/'ancillary-proof-source'
            self.assertEqual(sorted(v.name for v in context.iterdir()),sorted(source_names))
            for name in source_names:self.assertEqual((context/name).read_text(),name)
            # A selected symlink must be refused, not copied into the context.
            import shutil
            shutil.rmtree(context)
            (source/'main.go').unlink();(source/'main.go').symlink_to(source/'private.token')
            result=subprocess.run(['bash','-euc',shell[start:end]],cwd=root,env={'PATH':'/usr/bin:/bin','work':str(work)})
            self.assertNotEqual(result.returncode,0)

    def test_all_froms_still_use_existing_reviewed_bases(self):
        import re
        text=(HERE.parents[1]/'Dockerfile').read_text()
        aliases=set();external=set()
        for line in text.splitlines():
            if not re.match(r'(?i)^\s*from(?:\s|$)',line):continue
            match=re.fullmatch(r'FROM (\S+) AS ([a-z0-9-]+)',line)
            self.assertIsNotNone(match)
            image,alias=match.groups()
            if image not in aliases and image!='scratch':external.add(image)
            aliases.add(alias)
        self.assertEqual(external,{'node:22.23.4-bookworm-slim','golang:1.26.10-bookworm','debian:bookworm-slim'})


if __name__=='__main__':unittest.main()
