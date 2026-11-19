import importlib.util
import json
import re
import subprocess
from pathlib import Path
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('evidence', HERE / 'garm-joined-evidence.py')
evidence = importlib.util.module_from_spec(spec)
spec.loader.exec_module(evidence)


class JoinedEvidence(unittest.TestCase):
    def args(self):
        return ['a' * 40, '123', '2', evidence.STOCK_ID,
                'sha256:' + '1' * 64, 'sha256:' + '2' * 64, 'sha256:' + '3' * 64, '4' * 64, '5' * 64]

    def test_exact_closed_record(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'result.json'
            evidence.main([str(path), *self.args()])
            value = json.loads(path.read_text())
            self.assertEqual(value['schema'], 'disposable-three-service-preboot/v2')
            for key, expected in zip(('source_commit', 'run_id', 'run_attempt',
                                      'stock_image', 'gateway_image', 'ingress_image', 'ledger_image',
                                      'gateway_executable_sha256', 'ingress_executable_sha256'), self.args()):
                self.assertEqual(value[key], expected)
            true = {'actual_three_binaries_started', 'exact_four_ipv4_loopback_listeners_verified',
                    'shared_isolated_network', 'stock_uninitialized'}
            false = {'host_ports_published', 'supervisor_present', 'admission_ready',
                     'upstream_inventory_integrated', 'managed_topology_qualified',
                     'startup_authority', 'deployment_authorized'}
            self.assertEqual(set(value), true | false | {'schema', 'source_commit', 'run_id',
                             'run_attempt', 'stock_image', 'gateway_image', 'ingress_image', 'ledger_image',
                             'gateway_executable_sha256', 'ingress_executable_sha256'})
            for key in true:
                self.assertIs(value[key], True)
            for key in false:
                self.assertIs(value[key], False)
            with self.assertRaises(FileExistsError):
                evidence.main([str(path), *self.args()])

    def test_refuses_unbound_or_ambiguous_identity_before_write(self):
        for index, bad in [(0, 'refs/pull/1/merge'), (0, 'A'*40), (1, ''), (1, '0'),
                           (2, '1\n'), (3, 'sha256:'+'0'*64), (4, 'latest'),
                           (6, 'sha256:'+'1'*64), (7, ''), (7, 'sha256:'+'4'*64),
                           (8, 'A'*64), (8, '5'*64+'\n')]:
            with self.subTest(index=index, bad=bad), tempfile.TemporaryDirectory() as directory:
                args = self.args()
                args[index] = bad
                path = Path(directory) / 'absent.json'
                with self.assertRaises(ValueError):
                    evidence.main([str(path), *args])
                self.assertFalse(path.exists())

    def test_runtime_status_digest_is_closed(self):
        source = (HERE / 'garm-joined.sh').read_text()
        parser = next(code for code in re.findall(r"python3 -c '([^']*)'", source)
                      if 'ExecutableSHA256' in code)
        status = ('Uid: 65531 65531 65531 65531\nGid: 65531 65531 65531 65531\n'
                  'CapInh: 0\nCapPrm: 0\nCapEff: 0\nCapBnd: 0\nCapAmb: 0\n'
                  'NoNewPrivs: 1\nExecutableSHA256: ')
        for digest, success in [('a'*64, True), ('secret/path', False), ('A'*64, False), ('a'*63, False)]:
            result = subprocess.run(['python3', '-c', parser, '65531', 'ingress'],
                                    input=status+digest+'\n', capture_output=True, text=True)
            self.assertEqual(result.returncode == 0, success)
            self.assertEqual(result.stdout, digest+'\n' if success else '')
        self.assertIn('role_sha=$(timeout 30s docker exec', source)
        self.assertIn('test "$size" -gt 0 && test "$size" -le 134217728', source)
        self.assertIn('test "$(stat -Lc %d:%i /proc/1/exe)" = "$(stat -Lc %d:%i "$1")"', source)
        self.assertIn('test "$actual_sha" = "$(sha256sum "$1" | cut -d" " -f1)"', source)

    def test_success_emission_follows_recreation_and_cleanup(self):
        source = (HERE / 'garm-least-privilege.sh').read_text()
        emit = source.index('python3 deploy/runtime/garm-joined-evidence.py')
        recreation = source.index('PASS same Garm database/controller identity survives')
        cleanup = source.rindex('sudo rm -rf -- "$work"')
        removal = source.rindex('docker rm "$name"')
        self.assertLess(recreation, removal)
        self.assertLess(removal, cleanup)
        self.assertLess(cleanup, source.rindex('trap - EXIT'))
        self.assertLess(source.rindex('trap - EXIT'), emit)
        self.assertLess(source.index('rm -f -- "$results/garm-three-service-preboot.json"'),
                        source.index('start_garm\nprobe'))
        self.assertNotIn('garm-three-service-preboot.json', (HERE / 'garm-joined.sh').read_text())
        self.assertIn('joined_source_commit=$(git rev-parse HEAD)', source)
        self.assertIn('[[ $(git rev-parse HEAD) = "$joined_source_commit" ]]', source)


if __name__ == '__main__':
    unittest.main()
