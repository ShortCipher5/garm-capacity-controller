"""Offline checks only: never execute Docker, sudo or the hosted launcher."""
import ast
import pathlib
import re
import subprocess
import unittest

SOURCE = pathlib.Path(__file__).with_name('garm-least-privilege.sh').read_text()


class ServiceSpecimen(unittest.TestCase):
    def selection(self, args):
        selector = SOURCE[SOURCE.index('lab_uid='):SOURCE.index('image=ghcr.io/')]
        return subprocess.run(['bash', '-c', selector + '\nprintf "%s|%s|%s|%s" "$lab_uid" "$specimen" "$result_name" "${workdir_args[*]}"', 'test', *args], capture_output=True, text=True)

    def test_closed_variants(self):
        self.assertEqual(self.selection([]).stdout, '65531|standalone|garm-nonroot-lab.json|')
        self.assertEqual(self.selection(['--prospective-service']).stdout, '65530|prospective-service|garm-prospective-service-lab.json|--workdir /')
        for args in [['--uid', '0'], ['--prospective-service', 'extra'], ['', 'extra']]:
            self.assertNotEqual(self.selection(args).returncode, 0)

    def test_all_embedded_python_compiles(self):
        chunks = re.findall(r"python3 -c '([^']*)'", SOURCE)
        chunks += re.findall(r"<<'PY(?:ARGV)?'\n(.*?)\nPY(?:ARGV)?\n", SOURCE, re.S)
        self.assertGreaterEqual(len(chunks), 7)
        for chunk in chunks:
            ast.parse(chunk)

    def test_runtime_checks_are_not_only_configuration(self):
        self.assertIn('readlink /proc/1/cwd', SOURCE)
        self.assertIn('st_uid==int(sys.argv[3])', SOURCE)
        self.assertIn('st_mode&0o077==0', SOURCE)
        self.assertIn('direct_executable_launch_qualified=False', SOURCE)
        self.assertIn('umask 077; exec', SOURCE)
        self.assertIn('managed_topology_qualified=False', SOURCE)


if __name__ == '__main__':
    unittest.main()
