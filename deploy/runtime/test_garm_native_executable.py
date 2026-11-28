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

    def test_double_dash_only_on_pinned_upgrade_artifact(self):
        ref = 'ghcr.io/cloudbase/garm:v0.2.2@sha256:5e1943ecc0c6e30e62228072f7c303c6be62875dc3e9514b4f677b37c6c39bde'
        image = self.fixture()
        image[0]['Config']['Entrypoint'][1] = '--config'
        with self.assertRaises(ValueError):
            native.discover(image, self.reference)
        image[0]['RepoDigests'] = ['ghcr.io/cloudbase/garm@' + ref.split('@')[1]]
        self.assertEqual(native.discover(image, ref)['config_flag'], '--config')
        image[0]['Config']['Entrypoint'][1] = '-config'
        with self.assertRaises(ValueError):
            native.discover(image, ref)

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

    def test_only_absent_null_or_explicitly_disabled_healthchecks_are_allowed(self):
        for disabled in (None, {'Test': ['NONE']}):
            image = self.fixture()
            image[0]['Config']['Healthcheck'] = disabled
            with self.subTest(disabled=disabled):
                self.assertEqual(native.discover(image, self.reference)['executable'], '/bin/garm')

        for unsafe in ({'Test': ['CMD', '/bin/true']}, {'Test': ['CMD-SHELL', 'true']},
                       {}, {'Test': []}, {'Test': 'NONE'}, {'Test': ['NONE', 'extra']},
                       {'Test': ['NONE'], 'Interval': 1_000_000_000}, ['NONE']):
            image = self.fixture()
            image[0]['Config']['Healthcheck'] = unsafe
            with self.subTest(unsafe=unsafe):
                with self.assertRaisesRegex(ValueError, 'implicit healthcheck refused'):
                    native.discover(image, self.reference)


if __name__ == '__main__':
    unittest.main()
