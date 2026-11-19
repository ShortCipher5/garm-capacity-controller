"""Offline generated-config and fixed probe contracts; never launch Docker."""
import importlib.util
import json
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import ast

HERE = Path(__file__).parent

def load(name):
    spec = importlib.util.spec_from_file_location(name, HERE / (name + '.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module

joined = load('garm-joined')
stock = load('garm-identity')

class Joined(unittest.TestCase):
    def test_generated_real_role_configs_and_no_auth_bootstrap(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            stock.prepare(root / 'stock')
            joined.snapshot_public(root / 'stock/private', root / 'stock-public')
            public = root / 'stock-public'
            self.assertEqual(set(p.name for p in public.iterdir()), {'server.pem', 'ca.pem'})
            self.assertEqual(public.stat().st_mode & 0o777, 0o700)
            self.assertTrue(all(p.stat().st_mode & 0o777 == 0o600 for p in public.iterdir()))
            # Remove the entire source: prepare must work with ONLY public
            # certificate inputs, with no stock key/config/state accessible.
            shutil.rmtree(root / 'stock')
            joined.prepare(root / 'joined', public)
            base = root / 'joined'
            config = json.loads((base / 'gateway-private/gateway.local.json').read_text())
            self.assertTrue(config['garm']['managed_proxy'])
            self.assertEqual(config['garm']['endpoint'], 'https://127.0.0.1:9445')
            self.assertNotIn('token_file', config['garm'])
            self.assertFalse((base / 'gateway-private/inventory.token').exists())
            ingress = json.loads((base / 'ingress-private/ingress.json').read_text())
            self.assertEqual(ingress['Upstream'], 'https://127.0.0.1:9998')
            self.assertEqual(ingress['Controller'], '')
            self.assertFalse((base / 'fixture-private').exists())
            self.assertEqual(set(p.name for p in (base / 'probe-private').iterdir()), {'ca.pem','stock-ca.pem','admin.token'})
            for role in ('gateway-private','ingress-private','probe-private'):
                for file in (base / role).iterdir():
                    self.assertEqual(file.stat().st_mode & 0o777, 0o600)
            keys = [joined.certificate_spki(public / 'server.pem'), joined.certificate_spki(base / 'gateway-private/server.pem'), joined.certificate_spki(base / 'ingress-private/server.pem')]
            self.assertEqual(len(set(keys)), 3)

    def test_public_snapshot_precedes_ownership_transfer_and_labels_are_fixed(self):
        source = (HERE / 'garm-least-privilege.sh').read_text()
        self.assertLess(source.index('garm-joined.py snapshot-public'), source.index('sudo chown -R "$lab_uid:$lab_uid"'))
        self.assertIn('prepare "$work/joined" "$work/stock-public"', (HERE / 'garm-joined.sh').read_text())
        for name in joined.STAGES:
            with patch.object(joined, 'stage', name):
                self.assertEqual(joined.failure_message(), 'Joined preboot fixture unavailable at ' + name)
        with patch.object(joined, 'stage', '/private/path secret'):
            self.assertEqual(joined.failure_message(), 'Joined preboot fixture unavailable at joined-action')
        with tempfile.TemporaryDirectory() as directory:
            result = subprocess.run(['python3',str(HERE / 'garm-joined.py'), 'snapshot-public', directory+'/secret-missing', directory+'/public'], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(result.stderr, 'Joined preboot fixture unavailable at joined-snapshot-stock-public\n')
            self.assertNotIn(directory, result.stderr)

    def test_probe_demands_unready_and_real_forward_result(self):
        view = {'admission_ready':False, 'experimental_provisioning_enabled':False, 'management_enabled':False, 'garm_management_available':False}
        def request(port,path,stock=False,authenticated=False):
            if port==9444:
                return (200,json.dumps(view).encode()) if authenticated else (401,b'')
            if path=='/api/v1/controller':
                return (409,b'init_required') if port==9998 else (404,b'')
            return 409,b'init_required'
        with patch.object(joined,'request',side_effect=request), patch.object(joined.socket,'create_connection',side_effect=ConnectionRefusedError), patch.object(joined, 'verify_listener_tables') as listeners:
            joined.probe()
            listeners.assert_called_once_with()
            view['admission_ready']=True
            with self.assertRaises(AssertionError): joined.probe()

    def test_reachable_wrong_ingress_status_fails_without_readiness_retry(self):
        view = {'admission_ready':False, 'experimental_provisioning_enabled':False, 'management_enabled':False, 'garm_management_available':False}
        for status in (200, 403, 502):
            with patch.object(joined, 'request', side_effect=[(200,json.dumps(view).encode()),(status,b'')]) as requests, patch.object(joined.time, 'sleep') as sleep:
                with self.assertRaises(AssertionError): joined.probe()
                self.assertEqual(requests.call_count, 2)
                sleep.assert_not_called()

    def test_fixed_process_labels_and_readonly_failure_are_enforced(self):
        shell = (HERE / 'garm-joined.sh').read_text()
        function = re.search(r'joined_check\(\) \{.*?\n\}', shell, re.S).group(0)
        for role in ('stock', 'ingress', 'gateway'):
            result = subprocess.run(['bash', '-ec', function + '\njoined_check "$1" "$2"', 'test', role, 'pid-read'], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0)
            self.assertEqual(result.stderr, 'Joined check: ' + role + ' pid-read\n')
            self.assertEqual(result.stdout, '')
        for role, phase in [('private-container-name','pid-read'),('stock','raw-namespace-123')]:
            result = subprocess.run(['bash', '-ec', function + '\njoined_check "$1" "$2"', 'test', role, phase], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(result.stdout + result.stderr, '')
        statement = 'if touch /must-not-be-writable 2>/dev/null; then exit 1; fi'
        self.assertIn(statement, shell)
        self.assertNotIn('! touch /must-not-be-writable', shell)
        # Run the exact assertion with a harmless shell function, not touch or
        # Docker. A successful write must abort even under sh -e exemptions.
        for touch_status in (0, 1):
            program = 'touch() { return ' + str(touch_status) + '; };\n' + statement + '\nprintf reached'
            result = subprocess.run(['sh', '-ec', program], capture_output=True, text=True)
            self.assertEqual(result.returncode, 1 if touch_status == 0 else 0)
            self.assertEqual(result.stdout, '' if touch_status == 0 else 'reached')

    def test_stop_requires_clean_non_oom_exit_and_redacts_status(self):
        shell = (HERE / 'garm-joined.sh').read_text()
        code = re.search(r'docker inspect "\$child" \| python3 -c \'(.*?)\'', shell, re.S).group(1)
        for exit_code, status, oom, running, ok in ((0,'exited',False,False,True),(143,'exited',False,False,False),(0,'exited',True,False,False),(0,'private-name',False,False,False),(0,'running',False,True,False)):
            payload = [{'Name':'secret-container','Config':{'Env':['SECRET=hidden']},'State':{'ExitCode':exit_code,'Status':status,'OOMKilled':oom,'Running':running}}]
            result = subprocess.run(['python3','-c',code],input=json.dumps(payload),capture_output=True,text=True)
            self.assertEqual(result.returncode == 0, ok)
            self.assertEqual(result.stdout, '')
            if ok:
                self.assertEqual(result.stderr, '')
            else:
                value = json.loads(result.stderr)
                self.assertEqual(set(value), {'exit_code','status','oom'})
                self.assertNotIn('secret',result.stderr)
                self.assertNotIn('private-name',result.stderr)

    def test_closed_joined_selector_and_shell_python(self):
        source=(HERE/'garm-least-privilege.sh').read_text()
        selector=source[source.index('lab_uid='):source.index('image=ghcr.io/')]
        args=['sha256:'+'a'*64]*3
        result=subprocess.run(['bash','-c',selector+'\nprintf "%s|%s" "$joined" "$lab_uid"','test','--joined-service',*args],capture_output=True,text=True)
        self.assertEqual(result.stdout,'true|65530')
        for bad in ([],['tag',*args[1:]],args+['extra']):
            self.assertNotEqual(subprocess.run(['bash','-c',selector,'test','--joined-service',*bad],capture_output=True).returncode,0)
        shell=(HERE/'garm-joined.sh').read_text()
        for chunk in re.findall(r"python3 -c '([^']*)'",shell)+re.findall(r"<<'PY'\n(.*?)\nPY\n",shell,re.S): ast.parse(chunk)
        self.assertIn('source deploy/runtime/garm-joined.sh',source)
        self.assertIn('garm-least-privilege.sh --joined-service',(HERE/'test.sh').read_text())
        self.assertNotIn('--privileged',shell)



class ExactListeners(unittest.TestCase):
    header = ' sl local_address rem_address st'

    def rows(self, ports=(8443, 9443, 9444, 9998), address='0100007F'):
        return self.header + '\n' + '\n'.join(f'{i}: {address}:{p:04X} 00000000:0000 0A' for i, p in enumerate(ports))

    def test_exact_loopback_only(self):
        joined.validate_listener_tables(self.rows(), self.header + '\n')

    def test_wildcard_ipv6_extra_missing_duplicate_rejected(self):
        for tcp, tcp6 in [
            (self.rows(address='00000000'), self.header),
            (self.rows(), self.rows((8443,), '00000000000000000000000001000000')),
            (self.rows((8443, 9443, 9444, 9998, 9445)), self.header),
            (self.rows((8443, 9443, 9998)), self.header),
            (self.rows((8443, 9443, 9444, 9998, 9998)), self.header),
            (self.rows(address='0200007F'), self.header),
            ('', self.header), (self.header + '\nmalformed', self.header),
            ('x' * (4 * 1024 * 1024 + 1), self.header),
        ]:
            with self.subTest(tcp=tcp[:100], tcp6=tcp6[:100]):
                with self.assertRaises(ValueError):
                    joined.validate_listener_tables(tcp, tcp6)

if __name__ == '__main__':
    unittest.main()
