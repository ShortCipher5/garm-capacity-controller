import importlib.util
import pathlib
import unittest

spec = importlib.util.spec_from_file_location('native', pathlib.Path(__file__).with_name('garm-native-executable.py'))
native = importlib.util.module_from_spec(spec)
spec.loader.exec_module(native)


class Discovery(unittest.TestCase):
    reference = 'ghcr.io/cloudbase/garm:v0.2.1@sha256:' + 'a' * 64

    def fixture(self):
        return [{'Id': 'sha256:' + 'b' * 64, 'Os': 'linux', 'Architecture': 'amd64',
                 'RepoDigests': ['ghcr.io/cloudbase/garm@sha256:' + 'a' * 64],
                 'Config': {'Entrypoint': ['/bin/garm', '-config', '/etc/garm/config.toml'],
                            'Env': ['PATH=/bin'], 'WorkingDir': '', 'Cmd': None}}]

    def test_native_path_is_discovered_not_fixed(self):
        for path in ['/bin/garm', '/opt/stock/garm']:
            image = self.fixture()
            image[0]['Config']['Entrypoint'][0] = path
            result = native.discover(image, self.reference)
            self.assertEqual(result['executable'], path)
            self.assertEqual(result['image_id'], image[0]['Id'])
            self.assertFalse(result['production_binding'])

    def test_ambiguous_execution_is_refused(self):
        for entry in [None, [], ['/bin/sh', '-c', 'garm'], ['garm', '-config', '/etc/garm/config.toml'],
                      ['/bin/../bin/garm', '-config', '/etc/config'], ['/bin//garm', '-config', '/etc/config'],
                      ['/bin/garm\n', '-config', '/etc/config'], ['/bin/garm'],
                      ['/bin/garm', '-config', '/etc/config', '--debug']]:
            image = self.fixture()
            image[0]['Config']['Entrypoint'] = entry
            with self.assertRaises(ValueError):
                native.discover(image, self.reference)

    def test_identity_and_hidden_execution_are_refused(self):
        for field, value in [('Id', 'moving'), ('RepoDigests', []), ('Architecture', 'unknown'), ('Os', 'windows')]:
            image = self.fixture()
            image[0][field] = value
            with self.assertRaises(ValueError):
                native.discover(image, self.reference)
        for field, value in [('Cmd', ['extra']), ('OnBuild', ['RUN something']), ('Volumes', {'/state': {}})]:
            image = self.fixture()
            image[0]['Config'][field] = value
            with self.assertRaises(ValueError):
                native.discover(image, self.reference)


if __name__ == '__main__':
    unittest.main()
