"""Offline archive fixtures only. No Docker or GitHub execution."""
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('probe', HERE / 'probe-image-save-format.py')
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)


class FormatProbe(unittest.TestCase):
    def setUp(self):
        self.configs = {r: json.dumps({'os': 'linux', 'architecture': 'amd64',
                         'rootfs': {'type': 'layers', 'diff_ids': []}, 'fixture': r}).encode()
                        for r in probe.pack.ROLES}
        self.ids = {r: 'sha256:'+hashlib.sha256(v).hexdigest() for r,v in self.configs.items()}
        self.patch = patch.object(probe.pack, 'STOCK_ID', self.ids['stock'])
        self.patch.start()
        self.addCleanup(self.patch.stop)
        self.record = {'schema':'disposable-three-service-preboot/v2',
                       'source_commit':'a'*40, 'run_id':'123', 'run_attempt':'1',
                       **{r+'_image':v for r,v in self.ids.items()},
                       **{k:True for k in probe.pack.TRUE},
                       **{k:False for k in probe.pack.FALSE},
                       **{k:'e'*64 for k in probe.pack.EXECUTABLE_HASHES}}

    def save(self, path, ids, modern=False):
        self.assertEqual(ids, self.ids)
        with tarfile.open(path, 'w') as out:
            def add(name, data):
                info=tarfile.TarInfo(name);info.size=len(data)
                out.addfile(info, io.BytesIO(data))
            rows=[]
            for role in ids:
                name=ids[role][7:]+'.json'
                add(name,self.configs[role])
                rows.append({'Config':name, 'RepoTags':None, 'Layers':[]})
            add('manifest.json',json.dumps(rows).encode())
            if modern:
                add('index.json',b'{"manifests":[]}')
                add('oci-layout',b'{"imageLayoutVersion":"1.0.0"}')

    def run_probe(self, parent, save):
        return probe.probe(json.dumps(self.record).encode(), parent, 'a'*40, '123', '1', save)

    def test_legacy_identity_and_cleanup(self):
        with tempfile.TemporaryDirectory() as parent:
            result=self.run_probe(parent, self.save)
            self.assertEqual(list(Path(parent).iterdir()), [])
            self.assertEqual(result['layout'], 'docker')
            self.assertEqual(result['validator_result'], 'accepted')
            self.assertTrue(result['archive_identity_verified'])
            self.assertFalse(result['deployment_authorized'])
            self.assertEqual(result['files'], 5)
            self.assertEqual(result['directories'], 0)
            self.assertNotIn(parent, json.dumps(result))
            self.assertEqual(set(result), {'schema','layout','files','directories','archive_bytes',
                             'archive_sha256','validator_result','archive_identity_verified','deployment_authorized'})

    def test_modern_layout_measured_not_accepted(self):
        with tempfile.TemporaryDirectory() as parent:
            result=self.run_probe(parent, lambda p,i:self.save(p,i,True))
            self.assertEqual(result['layout'], 'docker-and-oci')
            self.assertEqual(result['validator_result'], 'refused')
            self.assertFalse(result['archive_identity_verified'])
            self.assertEqual(result['files'], 7)
            self.assertEqual(list(Path(parent).iterdir()), [])

    def test_optional_single_role_samples_share_budget_and_cleanup(self):
        samples=[]
        with tempfile.TemporaryDirectory() as parent:
            def one(path, ids, max_bytes):
                # Multi archive and previous role sample have already been erased.
                self.assertEqual(len(list(Path(parent).iterdir())), 1)
                self.assertEqual(len(ids), 1)
                samples.append((next(iter(ids)), max_bytes))
                # Existing mock checks whole ID mapping; construct matching one-role tar.
                with tarfile.open(path, 'w') as out:
                    for name, data in [('manifest.json',b'[]'), ('index.json',b'{"manifests":[]}'),
                                       ('oci-layout',b'{"imageLayoutVersion":"1.0.0"}')]:
                        info=tarfile.TarInfo(name);info.size=len(data);out.addfile(info,io.BytesIO(data))
            result=probe.probe(json.dumps(self.record).encode(), parent, 'a'*40, '123', '1',
                               lambda p,i:self.save(p,i,True), sample_roles=True, role_save=one)
            self.assertEqual([r for r,_ in samples],list(probe.pack.ROLES))
            self.assertTrue(all(samples[i][1] > samples[i+1][1] for i in range(3)))
            self.assertLess(samples[0][1],probe.pack.MAX_ARCHIVE)
            self.assertEqual(set(result['per_role_structure']),set(probe.pack.ROLES))
            self.assertEqual(list(Path(parent).iterdir()), [])
            self.assertFalse(result['archive_identity_verified'])
            self.assertTrue(all(v['structure']['diagnostic_only'] for v in result['per_role_structure'].values()))

    def test_failed_save_or_invalid_tar_is_cleaned(self):
        for invalid_tar in (False,True):
            def broken(path, ids):
                path.write_bytes(b'not a tar')
                if not invalid_tar:
                    raise ValueError('mock save failure')
            with tempfile.TemporaryDirectory() as parent:
                with self.assertRaises((ValueError, tarfile.TarError)):
                    self.run_probe(parent, broken)
                self.assertEqual(list(Path(parent).iterdir()), [])

    def test_wrong_run_refused_before_save(self):
        self.record['run_attempt']='2'
        with tempfile.TemporaryDirectory() as parent:
            with self.assertRaises(ValueError):
                self.run_probe(parent, lambda *_: self.fail('save called'))
            self.assertEqual(list(Path(parent).iterdir()), [])

    def test_probe_precedes_outer_image_cleanup(self):
        shell=(HERE/'test.sh').read_text()
        joined=shell.index('deploy/runtime/garm-least-privilege.sh --joined-service')
        probe_call=shell.index('timeout --kill-after=10s 360s python3 deploy/runtime/probe-image-save-format.py')
        self.assertLess(joined, probe_call)
        self.assertLess(shell.index('trap cleanup EXIT'), joined)
        self.assertIn('docker image rm "$ingress_tag"', shell[:joined])
        self.assertNotIn('docker image rm', shell[joined:])
        self.assertNotIn('trap - EXIT', shell)
        self.assertIn('if [[ ${GARM_ARCHIVE_FORMAT_PROBE:-false} = true ]]; then', shell[joined:])
        self.assertIn('[[ ${GITHUB_EVENT_NAME:-} = workflow_dispatch ]]', shell[joined:])
        workflow=(HERE.parents[1]/'.github/workflows/packaging.yml').read_text()
        self.assertNotIn('run: python3 deploy/runtime/probe-image-save-format.py', workflow)
        build=workflow.split('- name: Build digest-locked artifacts',1)[1].split('- name:',1)[0]
        self.assertIn('GARM_ARCHIVE_FORMAT_PROBE:', build)
        self.assertIn('run: deploy/runtime/test.sh', build)

    def test_default_off_and_pr_cannot_enable(self):
        for env in ({}, {'GITHUB_ACTIONS':'true','RUNNER_ENVIRONMENT':'github-hosted',
                        'GITHUB_EVENT_NAME':'pull_request','GARM_ARCHIVE_FORMAT_PROBE':'true'}):
            with patch.dict(probe.os.environ,env,clear=True):
                with patch.object(probe.subprocess,'check_output',side_effect=AssertionError('unexpected command')):
                    with self.assertRaises(ValueError):
                        probe.main()
        workflow=(HERE.parents[1]/'.github/workflows/packaging.yml').read_text()
        self.assertIn('default: false',workflow)
        self.assertIn("github.event_name == 'workflow_dispatch' && inputs.archive_format_probe == true",workflow)
        self.assertNotIn('upload-artifact', (HERE/'probe-image-save-format.py').read_text())


if __name__ == '__main__':
    unittest.main()
