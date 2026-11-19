"""Generated, empty-state three-service preboot fixture. No supervisor/auth bootstrap."""
import hashlib
import http.client
import json
from pathlib import Path
import shutil
import socket
import ssl
import subprocess
import sys
import time



def prepare(root, stock):
    import fixture
    fixture.prepare(root)
    gateway = root / 'gateway-private'
    seed = root / 'fixture-private'
    ingress = root / 'ingress-private'
    probe = root / 'probe-private'
    ingress.mkdir(mode=0o700)
    probe.mkdir(mode=0o700)
    for source, dest in [('ca.pem', 'ca.pem'), ('api.pem', 'server.pem'), ('api.key', 'server.key')]:
        fixture.write(ingress / dest, (seed / source).read_text())
    fixture.write(ingress / 'stock.pem', (stock / 'server.pem').read_text())
    stock_pin = hashlib.sha256(ssl.PEM_cert_to_DER_cert((stock / 'server.pem').read_text())).hexdigest()
    fixture.write(ingress / 'ingress.json', json.dumps({
        'Listen': '0.0.0.0:8443', 'Upstream': 'https://127.0.0.1:9998', 'Controller': '',
        'TLSCertificate': '/run/joined-ingress/server.pem', 'TLSKey': '/run/joined-ingress/server.key',
        'StockCertificate': '/run/joined-ingress/stock.pem', 'StockFingerprint': stock_pin}))
    config = json.loads((gateway / 'gateway.local.json').read_text())
    # No process owns this supervisor endpoint. A successful stock read is NOT
    # simulated: real reconciliation must fail and keep the ledger unready.
    public_key = subprocess.check_output(['openssl', 'x509', '-in', str(seed / 'api.pem'), '-pubkey', '-noout'])
    spki = subprocess.check_output(['openssl', 'pkey', '-pubin', '-outform', 'DER'], input=public_key)
    config['garm'] = {'managed_proxy': True, 'endpoint': 'https://127.0.0.1:9445',
        'proxy_server_key_pin': hashlib.sha256(spki).hexdigest(),
        'providers': ['capacity'], 'tls': config['hosts']['testhost']['tls']}
    fixture.write(gateway / 'gateway.local.json', json.dumps(config))
    (gateway / 'inventory.token').unlink()
    for name in ('ca.pem', 'admin.token'):
        fixture.write(probe / name, (gateway / name).read_text())
    fixture.write(probe / 'stock-ca.pem', (stock / 'ca.pem').read_text())
    # Keep only independently owned role material, initialization input and state.
    for name in ('fixture-private', 'bad-private', 'backups', 'empty-state', 'other-state', 'archive-state', 'restored-state'):
        shutil.rmtree(root / name)
    assert (gateway / 'server.key').read_bytes() != (ingress / 'server.key').read_bytes()
    assert (stock / 'server.key').read_bytes() not in ((gateway / 'server.key').read_bytes(), (ingress / 'server.key').read_bytes())


def request(port, path, stock=False, authenticated=False):
    root = Path('/joined-probe')
    context = ssl.create_default_context(cafile=str(root / ('stock-ca.pem' if stock else 'ca.pem')))
    context.minimum_version = ssl.TLSVersion.TLSv1_3
    context.verify_flags |= ssl.VERIFY_X509_STRICT
    conn = http.client.HTTPSConnection('127.0.0.1', port, context=context, timeout=2)
    headers = {}
    if authenticated:
        headers['Authorization'] = 'Bearer ' + (root / 'admin.token').read_text().strip()
    try:
        conn.request('GET', path, headers=headers)
        response = conn.getresponse()
        data = response.read(1048577)
        assert len(data) <= 1048576
        return response.status, data
    finally:
        conn.close()


def probe():
    for _ in range(90):
        try:
            code, raw = request(9444, '/admin/api/v1/overview', authenticated=True)
            ingress_code, _ = request(8443, '/api/v1/controller')
            # Readiness retries transport availability only. A reachable but
            # incorrect status is checked once below and fails immediately.
            break
        except (OSError, ssl.SSLError, http.client.HTTPException):
            pass
        time.sleep(0.5)
    else:
        raise RuntimeError('joined preboot unavailable')
    assert code == 200
    assert ingress_code == 404
    view = json.loads(raw)
    for field in ('admission_ready', 'experimental_provisioning_enabled', 'management_enabled', 'garm_management_available'):
        assert view[field] is False
    assert request(9444, '/admin/api/v1/overview')[0] == 401
    assert request(9998, '/api/v1/controller', stock=True)[0] == 409
    assert request(8443, '/api/v1/controller')[0] == 404
    # Fixed permitted GET reaches actual stock, with no credential or account.
    route = '/api/v1/metadata/system/service-name'
    direct = request(9998, route, stock=True)
    assert direct[0] in (401, 409)
    assert request(8443, route) == direct
    try:
        with socket.create_connection(('127.0.0.1', 9445), timeout=1):
            raise AssertionError('unexpected supervisor')
    except ConnectionRefusedError:
        pass
    print('PASS actual three-service preboot; supervisor absent, admission unready')


def outside():
    # Separate network=none namespace, positive inside checks ran first. This
    # checks only local namespace non-reachability; no external address is used.
    for port in (8443, 9443, 9444, 9998):
        try:
            with socket.create_connection(('127.0.0.1', port), timeout=1):
                raise AssertionError('joined listener escaped namespace')
        except ConnectionRefusedError:
            pass
    print('PASS separate networkless namespace cannot reach joined listeners')


def main(args):
    if args and args[0] == 'prepare' and len(args) == 3:
        prepare(Path(args[1]), Path(args[2]))
    elif args == ['probe']:
        probe()
    elif args == ['outside']:
        outside()
    else:
        raise ValueError('invalid joined fixture action')


if __name__ == '__main__':
    try:
        main(sys.argv[1:])
    except Exception:
        print('Joined preboot fixture unavailable', file=sys.stderr)
        raise SystemExit(1)
