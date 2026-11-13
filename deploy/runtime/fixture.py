#!/usr/bin/env python3
"""Disposable packaging-only HTTPS fixtures. Never use real credentials/data."""
import hashlib
import http.client
import http.server
import json
import os
from pathlib import Path
import secrets
import socket
import ssl
import subprocess
import sys
import time


def write(path, value):
    path.write_text(value)
    path.chmod(0o600)


def prepare(root):
    root.mkdir(mode=0o700, parents=False, exist_ok=False)
    for name in ('gateway-private', 'fixture-private', 'state', 'backups', 'bad-private', 'init-private', 'empty-state', 'other-state', 'archive-state', 'restored-state'):
        (root / name).mkdir(mode=0o700)
    work = root / 'fixture-private'
    def openssl(*args):
        subprocess.run(['openssl', *args], cwd=work, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    openssl('req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256', '-nodes', '-days', '1', '-subj', '/CN=packaging-test-ca', '-addext', 'basicConstraints=critical,CA:TRUE', '-addext', 'keyUsage=critical,keyCertSign,cRLSign', '-addext', 'subjectKeyIdentifier=hash', '-keyout', 'ca.key', '-out', 'ca.pem')
    for name, purpose in [('api', 'serverAuth'), ('server', 'serverAuth'), ('client', 'clientAuth'), ('provider', 'clientAuth'), ('wrong', 'clientAuth'), ('control', 'clientAuth')]:
        write(work / 'ext.cnf', f'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage={purpose}\nsubjectAltName=IP:127.0.0.1\nsubjectKeyIdentifier=hash\nauthorityKeyIdentifier=keyid,issuer\n')
        openssl('req', '-new', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256', '-nodes', '-subj', f'/CN=packaging-{name}', '-keyout', f'{name}.key', '-out', f'{name}.csr')
        openssl('x509', '-req', '-in', f'{name}.csr', '-CA', 'ca.pem', '-CAkey', 'ca.key', '-CAcreateserial', '-days', '1', '-extfile', 'ext.cnf', '-out', f'{name}.pem')
    openssl('req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256', '-nodes', '-days', '1', '-subj', '/CN=packaging-untrusted', '-addext', 'extendedKeyUsage=clientAuth', '-keyout', 'untrusted.key', '-out', 'untrusted.pem')
    # Remove signing material. Only public CA trust and ephemeral leaf keys remain.
    for path in list(work.glob('*.csr')) + [work / 'ca.key', work / 'ca.srl', work / 'ext.cnf']:
        path.unlink()
    for name in ('ca.pem', 'server.pem', 'server.key', 'client.pem', 'client.key'):
        write(root / 'gateway-private' / name, (work / name).read_text())
    # The namespace anchor must NOT retain gateway server/upstream-client keys.
    # Its API test server has a different TLS leaf/key from the real gateway.
    for name in ('server.pem', 'server.key', 'client.pem', 'client.key'):
        (work / name).unlink()
    gateway_keys = {p.read_bytes() for p in (root / 'gateway-private').glob('*.key')}
    anchor_keys = {p.read_bytes() for p in work.glob('*.key')}
    assert gateway_keys and anchor_keys and gateway_keys.isdisjoint(anchor_keys)
    admin, inventory = secrets.token_hex(32), secrets.token_hex(32)
    for directory in (root / 'gateway-private', work):
        write(directory / 'admin.token', admin)
        write(directory / 'inventory.token', inventory)
    cert_der = ssl.PEM_cert_to_DER_cert((work / 'provider.pem').read_text())
    tls = lambda name: {'ca': '/run/capacity-private/ca.pem', 'certificate': f'/run/capacity-private/{name}.pem', 'key': f'/run/capacity-private/{name}.key'}
    budget = {'cpu_millis': 2000, 'ram_mib': 4096, 'runners': 2}
    config = {'listen': '127.0.0.1:9443', 'controller': 'packaging-only', 'database': '/var/lib/capacity-gateway/ledger.db', 'identity_file': '/run/capacity-private/ledger.identity',
      'server_tls': tls('server'), 'provider_fingerprints': [hashlib.sha256(cert_der).hexdigest()],
      'initial_policy': {'global': budget, 'hosts': {'testhost': {'budget': budget, 'mode': 'enabled', 'healthy': False, 'observed_at': '2000-01-01T00:00:00Z'}}, 'pools': {'testpool': {'budget': budget, 'hosts': ['testhost']}}, 'max_observation_age_ns': 3000000000},
      'hosts': {'testhost': {'endpoint': 'https://127.0.0.1:9553', 'project': 'runners', 'tls': tls('client')}},
      'pools': {'testpool': {'host': 'testhost', 'cpu_millis': 1000, 'ram_mib': 1024, 'flavor': 'small', 'image': 'linux', 'image_fingerprint': 'a' * 64, 'profiles': ['base'], 'type': 'container', 'arch': 'amd64'}},
      'garm': {'endpoint': 'https://127.0.0.1:9553', 'token_file': '/run/capacity-private/inventory.token', 'tls': tls('client'), 'providers': ['capacity']},
      'admin': {'listen': '127.0.0.1:9444', 'tls': tls('server'), 'token_file': '/run/capacity-private/admin.token', 'assets': '/usr/local/share/capacity-admin'}}
    write(root / 'init-private' / 'initial-policy.json', json.dumps(config['initial_policy']))
    write(root / 'gateway-private' / 'gateway.local.json', json.dumps(config))
    write(root / 'bad-private' / 'gateway.local.json', json.dumps(config))
    for path in work.iterdir():
        path.chmod(0o600)
    print('Prepared ephemeral packaging fixtures; no secrets printed')


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass
    def reply(self, value, status=200):
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.end_headers()
        self.wfile.write(json.dumps(value).encode())
    def do_GET(self):
        from urllib.parse import urlsplit
        path = urlsplit(self.path).path
        if path == '/api/v1/instances':
            if self.headers.get('Authorization') != 'Bearer ' + fixture('inventory.token').read_text():
                return self.reply({}, 401)
            return self.reply([])
        if path == '/1.0':
            cert = fixture('api.pem').read_text()
            metadata = {'api_version': '1.0', 'auth': 'trusted', 'api_extensions': ['instance_create_start'], 'environment': {'server_clustered': False, 'server_name': 'packaging-node', 'server_version': '6.0.4', 'certificate': cert, 'certificate_fingerprint': hashlib.sha256(ssl.PEM_cert_to_DER_cert(cert)).hexdigest()}}
        elif path == '/1.0/instances':
            metadata = []
        elif path == '/1.0/operations':
            metadata = {}
        else:
            return self.reply({}, 404)
        self.reply({'type': 'sync', 'status_code': 200, 'metadata': metadata})
    def do_POST(self):
        # No write endpoint exists, even in these disposable fixtures.
        self.reply({}, 405)


def fixture(name):
    return Path(os.environ.get('FIXTURE_PRIVATE_DIR', '/fixture-private')) / name


def serve():
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.minimum_version = ssl.TLSVersion.TLSv1_3
    context.verify_flags |= ssl.VERIFY_X509_STRICT
    context.load_cert_chain(str(fixture('api.pem')), str(fixture('api.key')))
    context.load_verify_locations(str(fixture('ca.pem')))
    context.verify_mode = ssl.CERT_REQUIRED
    server = http.server.ThreadingHTTPServer(('127.0.0.1', 9553), Handler)
    server.socket = context.wrap_socket(server.socket, server_side=True)
    # A harmless reachable control makes bridge-negative tests distinguish
    # loopback isolation from absent routes/DNS or a dead namespace anchor.
    import threading
    control = http.server.ThreadingHTTPServer(('0.0.0.0', 9554), Handler)
    threading.Thread(target=control.serve_forever, daemon=True).start()
    server.serve_forever()


def request(port, path, method='GET', cert=None, token=None, body=None):
    context = ssl.create_default_context(cafile=str(fixture('ca.pem')))
    context.minimum_version = ssl.TLSVersion.TLSv1_3
    context.verify_flags |= ssl.VERIFY_X509_STRICT
    if cert:
        context.load_cert_chain(str(fixture(f'{cert}.pem')), str(fixture(f'{cert}.key')))
    client = http.client.HTTPSConnection('127.0.0.1', port, context=context, timeout=2)
    headers = {'Content-Type': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    try:
        client.request(method, path, body=body, headers=headers)
        response = client.getresponse()
        return response.status, response.read()
    finally:
        client.close()


def ready():
    # Fixture-only CA-auth liveness; not evidence of real upstream role authority.
    for attempt in range(60):
        try:
            status, data = request(9553, '/1.0', cert='control')
            if status == 200 and json.loads(data)['metadata']['auth'] == 'trusted':
                print('PASS synthetic authenticated API readiness')
                return
        except (OSError, ssl.SSLError, http.client.HTTPException):
            pass
        time.sleep(0.5)
    raise RuntimeError('synthetic API did not become available')


def probe():
    for forbidden in ('server.key', 'client.key', 'ledger.identity'):
        assert not fixture(forbidden).exists(), 'gateway-only key retained by synthetic anchor'
    token = fixture('admin.token').read_text()
    for attempt in range(60):
        try:
            status, data = request(9444, '/admin/api/v1/overview', token=token)
            if status == 200:
                break
        except (OSError, ssl.SSLError):
            pass
        time.sleep(0.5)
    else:
        raise RuntimeError('gateway admin did not become available')
    overview = json.loads(data)
    assert overview['experimental_provisioning_enabled'] is False
    assert overview['management_enabled'] is False
    assert overview['garm_management_available'] is False
    assert request(9444, '/admin/api/v1/overview')[0] == 401
    assert request(9444, '/admin/api/v1/policy', 'POST', token=token, body='{}')[0] in (401, 404)
    assert request(9444, '/')[0] == 200
    status, data = request(9443, '/v1/list', 'POST', 'provider', body='{"name":"","pool_id":"testpool"}')
    assert status == 200, 'authorized list failed'
    assert request(9443, '/v1/admit', 'POST', 'provider', body='{}')[0] == 403
    assert request(9443, '/v1/list', 'POST', 'wrong', body='{}')[0] == 403
    for rejected_client in (None, 'untrusted'):
        try:
            request(9443, '/v1/list', 'POST', rejected_client, body='{}')
        except (OSError, ssl.SSLError, http.client.HTTPException):
            pass
        else:
            raise AssertionError('provider listener accepted unauthenticated/untrusted TLS')
    # Actual network namespace socket inventory, not EXPOSE metadata.
    listens = []
    for table in ('/proc/net/tcp', '/proc/net/tcp6'):
        for line in Path(table).read_text().splitlines()[1:]:
            fields = line.split()
            address, port = fields[1].split(':')
            if int(port, 16) in (9443, 9444) and fields[3] == '0A':
                assert address == '0100007F', 'listener escaped IPv4 loopback'
                listens.append(int(port, 16))
    assert sorted(listens) == [9443, 9444]
    print('PASS actual gateway TLS, exact-leaf rejection, default-off roles, UI assets and loopback listeners')


def peer():
    address = socket.gethostbyname('garm')  # DNS failure is not proof of isolation.
    with socket.create_connection((address, 9554), timeout=2):
        pass  # Positive routing/liveness control must succeed first.
    for port in (9443, 9444):
        try:
            with socket.create_connection((address, port), timeout=2):
                raise AssertionError('bridge peer reached loopback listener')
        except OSError:
            pass
    print('PASS bridge peer cannot reach loopback listeners')


if __name__ == '__main__':
    try:
        {'prepare': lambda: prepare(Path(sys.argv[2])), 'serve': serve, 'ready': ready, 'probe': probe, 'peer': peer}[sys.argv[1]]()
    except Exception as error:
        # Never dump request bodies, credentials, config or operator paths.
        print('Packaging fixture check failed:', type(error).__name__, file=sys.stderr)
        raise SystemExit(1)
