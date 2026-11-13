#!/usr/bin/env python3
"""Generated pair fixture ONLY. No installed config, identity or real issuer."""
import hashlib
import importlib.util
import json
from pathlib import Path
import shutil
import ssl
import sys

HERE = Path(__file__).resolve().parent
ROOT = '/lab/garm-pair-runtime'


def load(name, path):
    sys.path.insert(0, str(HERE.parent))
    spec = importlib.util.spec_from_file_location(name, path)
    value = importlib.util.module_from_spec(spec); spec.loader.exec_module(value)
    return value


def write(path, value):
    path.write_text(value); path.chmod(0o600)


def prepare(root):
    root.mkdir(mode=0o700)
    load('base_fixture', HERE.parent / 'fixture.py').prepare(root / 'fake')
    load('identity_fixture', HERE.parent / 'garm-identity.py').prepare(root / 'garm-pair-runtime')
    private = root / 'garm-pair-runtime' / 'private'
    config = private / 'config.toml'
    write(config, config.read_text().replace('/run/garm-private', ROOT + '/private').replace('/var/lib/garm', ROOT + '/state'))
    write(private / '.synthetic-only', 'NEW GENERATED FIXTURE ONLY\n')


def configure(root):
    private = root / 'garm-pair-runtime' / 'private'
    api = root / 'fake' / 'fixture-private'
    gateway = root / 'fake' / 'gateway-private'
    controller = (private / 'controller.id').read_text()
    assert len(controller) == 36
    for role, source in [('provider', 'provider'), ('source', 'wrong')]:
        for extension in ('pem', 'key'):
            write(private / f'{role}.{extension}', (api / f'{source}.{extension}').read_text())
            (api / f'{source}.{extension}').unlink()
    write(private / 'provider-ca.pem', (api / 'ca.pem').read_text())
    tls = lambda role, ca='provider-ca.pem': {'ca': f'{ROOT}/private/{ca}', 'certificate': f'{ROOT}/private/{role}.pem', 'key': f'{ROOT}/private/{role}.key'}
    provider = {'controller': controller, 'gateway': 'https://127.0.0.1:9443', 'tls': tls('provider'), 'source_tls': tls('source'), 'bootstrap_mode': 'gateway-owned-default-cloudinit/v1'}
    write(private / 'provider.json', json.dumps(provider))
    provider['tls'] = tls('provider', 'ca.pem')  # unrelated Garm-server CA, not a TLS bypass
    write(private / 'bad-ca.json', json.dumps(provider))
    config = private / 'config.toml'
    write(config, config.read_text() + f'''\n[[provider]]
name = "capacity"
provider_type = "external"
[provider.external]
provider_executable = "/usr/local/bin/capacity-provider"
config_file = "{ROOT}/private/provider.json"
interface_version = "v0.1.1"
admission_enabled = true
capacity_bootstrap_mode = "gateway-owned-default-cloudinit/v1"
environment_variables = []
''')
    cfgpath = gateway / 'gateway.local.json'
    cfg = json.loads(cfgpath.read_text())
    cfg['controller'] = controller
    cfg['bootstrap_mode'] = 'gateway-owned-default-cloudinit/v1'
    cfg['source_fingerprints'] = [hashlib.sha256(ssl.PEM_cert_to_DER_cert((private / 'source.pem').read_text())).hexdigest()]
    cfg['garm']['providers'] = ['capacity']
    write(cfgpath, json.dumps(cfg))
    keys = [{p.read_bytes() for p in directory.glob('*.key')} for directory in (private, gateway, api)]
    assert all(keys) and all(keys[i].isdisjoint(keys[j]) for i in range(3) for j in range(i))
    assert not (api / 'provider.key').exists() and not (api / 'wrong.key').exists()
    print('PASS Garm/provider/source, gateway and synthetic API private keys are role-disjoint')


def probe():
    # This process sees ONLY synthetic API/control material, not Garm/provider or
    # gateway private keys. No raw response/token/config content is printed.
    fixture = load('base_fixture', HERE.parent / 'fixture.py')
    status, data = fixture.request(9444, '/admin/api/v1/overview', token=fixture.fixture('admin.token').read_text())
    assert status == 200
    overview = json.loads(data)
    assert overview['experimental_provisioning_enabled'] is False and overview['management_enabled'] is False
    assert overview['garm_management_available'] is False
    assert fixture.request(9444, '/')[0] == 200
    for key in ('provider.key', 'wrong.key', 'server.key', 'client.key', 'ledger.identity'):
        assert not fixture.fixture(key).exists()
    listeners = []
    for table in ('/proc/net/tcp', '/proc/net/tcp6'):
        for line in Path(table).read_text().splitlines()[1:]:
            fields = line.split(); address, port = fields[1].split(':')
            if int(port, 16) in (9443, 9444) and fields[3] == '0A':
                assert address == '0100007F'
                listeners.append(int(port, 16))
    assert sorted(listeners) == [9443, 9444]
    print('PASS paired gateway uses actual production assets with provisioning/management OFF')


if __name__ == '__main__':
    try:
        {'prepare': lambda: prepare(Path(sys.argv[2])), 'configure': lambda: configure(Path(sys.argv[2])), 'probe': probe}[sys.argv[1]]()
    except Exception as error:
        print('Synthetic pair fixture failed:', type(error).__name__, file=sys.stderr)
        raise SystemExit(1)
