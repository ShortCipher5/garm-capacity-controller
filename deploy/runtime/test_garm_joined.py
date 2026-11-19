"""Offline generated-config and fixed probe contracts; never launch Docker."""
import importlib.util
import json
from pathlib import Path
import re
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
            joined.prepare(root / 'joined', root / 'stock/private')
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
            keys = [(root / 'stock/private/server.key').read_bytes(), (base / 'gateway-private/server.key').read_bytes(), (base / 'ingress-private/server.key').read_bytes()]
            self.assertEqual(len(set(keys)), 3)

    def test_probe_demands_unready_and_real_forward_result(self):
        view = {'admission_ready':False, 'experimental_provisioning_enabled':False, 'management_enabled':False, 'garm_management_available':False}
        def request(port,path,stock=False,authenticated=False):
            if port==9444:
                return (200,json.dumps(view).encode()) if authenticated else (401,b'')
            if path=='/api/v1/controller':
                return (409,b'init_required') if port==9998 else (404,b'')
            return 409,b'init_required'
        with patch.object(joined,'request',side_effect=request), patch.object(joined.socket,'create_connection',side_effect=ConnectionRefusedError):
            joined.probe()
            view['admission_ready']=True
            with self.assertRaises(AssertionError): joined.probe()

    def test_reachable_wrong_ingress_status_fails_without_readiness_retry(self):
        view = {'admission_ready':False, 'experimental_provisioning_enabled':False, 'management_enabled':False, 'garm_management_available':False}
        for status in (200, 403, 502):
            with patch.object(joined, 'request', side_effect=[(200,json.dumps(view).encode()),(status,b'')]) as requests, patch.object(joined.time, 'sleep') as sleep:
                with self.assertRaises(AssertionError): joined.probe()
                self.assertEqual(requests.call_count, 2)
                sleep.assert_not_called()

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

if __name__=='__main__': unittest.main()
