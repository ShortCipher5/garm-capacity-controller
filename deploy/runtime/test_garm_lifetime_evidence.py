"""Offline regression for the exact shell lifetime parser embedded in the lab."""
import hashlib
import pathlib
import subprocess
import tempfile
import unittest


class Lifetime(unittest.TestCase):
    def parse(self, stat):
        source = pathlib.Path(__file__).with_name('garm-least-privilege.sh').read_text()
        function = source.split(' lifetime() {', 1)[1].split('\n }', 1)[0]
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'stat'
            path.write_text(stat)
            function = function.replace('/proc/1/stat', '"$STAT_FIXTURE"')
            return subprocess.run(['sh', '-c', 'lifetime() {' + function + '\n}; lifetime'],
                                  env={'STAT_FIXTURE': str(path), 'PATH': '/usr/bin:/bin'},
                                  capture_output=True, text=True)

    def test_field_after_command_with_spaces_and_parentheses(self):
        # Field 22 is the twentieth field after the final closing parenthesis.
        row = '1 (garm name ) extra) ' + ' '.join(['S'] + ['0'] * 18 + ['12345'] + ['0'] * 5)
        result = self.parse(row)
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout, '12345')

    def test_ambiguous_space_rendering_does_not_match_exact_command(self):
        expected = b'/bin/garm\0-config\0/run/garm-private/config.toml\0'
        ambiguous = b'/bin/garm -config\0/run/garm-private/config.toml\0'
        self.assertEqual(expected.replace(b'\0', b' '), ambiguous.replace(b'\0', b' '))
        self.assertNotEqual(hashlib.sha256(expected).digest(), hashlib.sha256(ambiguous).digest())
        source = pathlib.Path(__file__).with_name('garm-least-privilege.sh').read_text()
        self.assertIn('sha256sum /proc/1/cmdline', source)
        self.assertIn('"$expected_command_sha" | python3', source)

    def test_missing_malformed_zero_and_unbounded_fail(self):
        prefix = '1 (garm) ' + ' '.join(['S'] + ['0'] * 18) + ' '
        for value in ['', 'garbage', '2 (garm) S', prefix + '0', prefix + 'abc', prefix + '-1', 'x' * 8193]:
            with self.subTest(value=value[:60]):
                self.assertNotEqual(self.parse(value).returncode, 0)


if __name__ == '__main__':
    unittest.main()
