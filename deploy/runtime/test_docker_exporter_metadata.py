import importlib.util
from pathlib import Path
import unittest
import io
from unittest.mock import patch

spec=importlib.util.spec_from_file_location('metadata',Path(__file__).with_name('docker-exporter-metadata.py'))
metadata=importlib.util.module_from_spec(spec);spec.loader.exec_module(metadata)


class Metadata(unittest.TestCase):
    def test_closed_positive(self):
        def fake(kind,template):
            self.assertEqual(template,metadata.VERSION_FORMAT if kind=='version' else metadata.INFO_FORMAT)
            return ({'version':'29.9.0','api':'1.54','git':'abcdef0','os':'linux','arch':'amd64'}
                    if kind=='version' else {'driver':'overlayfs','driver_types':['','io.containerd.snapshotter.v1']})
        self.assertEqual(metadata.collect(fake),{'server_version':'29.9.0','api_version':'1.54',
                         'engine_git_commit':'abcdef0','platform':'linux/amd64',
                         'storage_driver':'overlayfs','image_store':'containerd-snapshotter'})

    def test_unknown_and_errors_are_nullable_not_echoed(self):
        values=[None,{'secret':'never echo'},[],
                {'version':'private/path','api':'secret','git':'token','os':'private','arch':'private'}]
        for value in values:
            self.assertTrue(all(v is None for v in metadata.collect(lambda *_:value).values()))
        def failure(*_):raise OSError('private path and secret')
        self.assertTrue(all(v is None for v in metadata.collect(failure).values()))

    def test_missing_driver_type_does_not_assert_classic(self):
        value=metadata.collect(lambda kind,_: {'driver':'overlay2','driver_types':['']} if kind=='info' else None)
        self.assertEqual(value['storage_driver'],'overlay2')
        self.assertIsNone(value['image_store'])

    def test_transport_output_bound_and_redaction(self):
        for raw, expected in [(b'{"ok":true}', {'ok':True}), (b'x'*4097, None)]:
            class Fake:
                stdout=io.BytesIO(raw)
                stopped=False
                def poll(self): return 0 if self.stopped else None
                def kill(self): self.stopped=True
                def wait(self, timeout=None): self.stopped=True;return 0
            def start(args, **kwargs):
                self.assertEqual(args[:4],['docker','--host','unix:///var/run/docker.sock','version'])
                self.assertIs(kwargs['stderr'],metadata.subprocess.DEVNULL)
                return Fake()
            with patch.object(metadata.subprocess,'Popen',side_effect=start):
                self.assertEqual(metadata.query('version',metadata.VERSION_FORMAT),expected)

    def test_only_server_and_driver_fields_requested(self):
        self.assertIn('.Server.APIVersion',metadata.VERSION_FORMAT)
        self.assertNotIn('{{json .}}',metadata.VERSION_FORMAT)
        self.assertNotIn('{{json .}}',metadata.INFO_FORMAT)
        self.assertNotIn('Name',metadata.INFO_FORMAT)
        self.assertNotIn('DockerRootDir',metadata.INFO_FORMAT)


if __name__=='__main__':unittest.main()
