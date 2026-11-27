"""Scoped fixture for the disposable hosted stock reader verification, never live."""
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import secrets
import ssl
import subprocess
import sys
import time

URI = 'stock-read-bridge.invalid'
STOCK = 9998
BRIDGE = 9461
READER = 9462
ROOT = Path('/probe')
READ = Path('/run/capacity-stock-reader')
GW = Path('/run/capacity-private')
SCOPE = '11111111-1111-4111-8111-111111111111'
RENEWABLE = os.getenv('CAPACITY_STOCK_LOGIN_ENABLED', 'false') == 'true'


def private(path, text):
    path.write_text(text)
    path.chmod(0o600)


def openssl(*args, data=None):
    return subprocess.run(
        ['openssl', *map(str, args)], input=data, check=True,
        stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=10,
    ).stdout


def leaf(path):
    return hashlib.sha256(ssl.PEM_cert_to_DER_cert(path.read_text())).hexdigest()


def pin(path):
    public = openssl('x509', '-in', path, '-pubkey', '-noout')
    return hashlib.sha256(openssl('pkey', '-pubin', '-outform', 'DER', data=public)).hexdigest()


def prepare(work):
    sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
    import fixture
    import importlib.util
    spec = importlib.util.spec_from_file_location('stock_seed', Path(fixture.__file__).with_name('garm-identity.py'))
    seed = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(seed)
    seed.prepare(work/'stock')
    p = work/'stock'/'private'/'config.toml'
    private(p, p.read_text().replace('use_tls = true', 'use_tls = false'))
    fixture.prepare(work/'companion')
    pf = work/'companion'/'fixture-private'
    gw = work/'companion'/'gateway-private'
    reader = work/'reader'
    bridge = work/'bridge'
    probe = work/'probe'
    for p in (reader, bridge, probe):
        p.mkdir(mode=0o700)
    openssl('req','-x509','-newkey','ec','-pkeyopt','ec_paramgen_curve:P-256','-nodes',
            '-days','1','-subj','/CN='+URI,'-addext','subjectAltName=DNS:'+URI,
            '-addext','extendedKeyUsage=serverAuth','-keyout',bridge/'bridge.key','-out',bridge/'bridge.pem')
    def copy(a,b): private(b,a.read_text())
    for sn,dn in [('control.pem','reader.pem'),('ca.pem','reader-ca.pem')]:
        copy(pf/sn,bridge/dn)
    copy(Path(__file__).with_name('Caddyfile'),bridge/'Caddyfile')
    for sn,dn in [('control.pem','stock-client.pem'),('control.key','stock-client.key'),('api.pem','server.pem'),('api.key','server.key'),('ca.pem','gateway-ca.pem')]:
        copy(pf/sn,reader/dn)
    copy(bridge/'bridge.pem',reader/'stock-ca.pem')
    for sn,dn in [('control.pem','bridge-reader.pem'),('control.key','bridge-reader.key'),('wrong.pem','bridge-wrong.pem'),('wrong.key','bridge-wrong.key'),('ca.pem','ca.pem')]:
        copy(pf/sn,probe/dn)
    copy(bridge/'bridge.pem',probe/'bridge-ca.pem')
    copy(gw/'admin.token',probe/'admin.token')
    copy(gw/'client.pem',probe/'gateway-client.pem')
    copy(gw/'client.key',probe/'gateway-client.key')
    g = json.loads((gw/'gateway.local.json').read_text())
    g['garm'].update(endpoint=f'https://127.0.0.1:{READER}',managed_proxy=True,
                     proxy_server_key_pin=pin(reader/'server.pem'))
    del g['garm']['token_file']
    (gw/'inventory.token').unlink()
    private(gw/'gateway.local.json',json.dumps(g))
    cfg = dict(listen=f'127.0.0.1:{READER}',stock_endpoint=f'https://127.0.0.1:{BRIDGE}',
               stock_tls_name=URI,stock_server_key_pin=pin(bridge/'bridge.pem'),stock_ca_file=str(READ/'stock-ca.pem'),
               stock_client_certificate=str(READ/'stock-client.pem'),stock_client_key=str(READ/'stock-client.key'),
               stock_bearer_file=str(READ/'stock.token'),inventory_tls=dict(ca=str(READ/'gateway-ca.pem'),
               certificate=str(READ/'server.pem'),key=str(READ/'server.key')),gateway_leaf=leaf(gw/'client.pem'),
               providers=['capacity'],pools=[SCOPE],repositories=[SCOPE])
    private(reader/'staging.json',json.dumps(cfg))
    for p in bridge.iterdir(): p.chmod(0o600)


def call(port, path, method='GET', payload=None, token=None, tls=None, sni=None):
    c = http.client.HTTPConnection('127.0.0.1',port,timeout=2)
    try:
        body = None if payload is None else json.dumps(payload)
        h = {'Accept':'application/json'}
        if body is not None: h['Content-Type']='application/json'
        if token: h['Authorization']='Bearer '+token
        if tls is not None:
            c.sock=tls.wrap_socket(__import__('socket').create_connection(('127.0.0.1',port),timeout=2),server_hostname=sni or '127.0.0.1')
            h['Host']=sni or '127.0.0.1'
        c.request(method,path,body,headers=h)
        response=c.getresponse()
        value = response.read(1<<20)
        return response.status, value
    finally:
        c.close()


def wait(callable_, allowed, message):
    end=time.monotonic()+25
    while time.monotonic()<end:
        try:
            status,data=callable_()
            if status==allowed: return data
        except OSError: pass
        time.sleep(.2)
    raise RuntimeError(message)


def bootstrap():
    wait(lambda:call(STOCK,'/api/v1/controller'),409,'fresh stock unavailable')
    username='stockfixture'
    password=secrets.token_urlsafe(36)+'a9!'
    result,_=call(STOCK,'/api/v1/first-run','POST',{'username':username,'email':'stockfixture@example.invalid','password':password})
    assert result==200, ('stock first-run',result)
    result,body=call(STOCK,'/api/v1/auth/login','POST',{'username':username,'password':password})
    assert result==200, ('stock login',result)
    token=json.loads(body)['token']
    assert re.fullmatch(r'[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+',token)
    settings={k:f'https://127.0.0.1:{STOCK}' for k in ('metadata_url','callback_url','agent_url','webhook_url')}
    result,body=call(STOCK,'/api/v1/controller','PUT',settings,token)
    assert result==200, ('stock fixture settings',result)
    c=json.loads((READ/'staging.json').read_text())
    c['stock_controller']=json.loads(body)['controller_id']
    if RENEWABLE:
        del c['stock_bearer_file']
        c.update(stock_login_username=username, stock_login_password_file=str(READ/'stock.password'))
        private(READ/'stock.password', password)
    else:
        private(READ/'stock.token', token)
    private(READ/'inventory.json',json.dumps(c))
    print('PASS disposable stock initialization')


def context(ca, cert=None):
    t=ssl.create_default_context(cafile=str(ROOT/ca))
    t.minimum_version=ssl.TLSVersion.TLSv1_3
    if cert: t.load_cert_chain(ROOT/(cert+'.pem'),ROOT/(cert+'.key'))
    return t


def bridge_ready():
    bridge=context('bridge-ca.pem','bridge-reader')
    wait(lambda:call(BRIDGE,'/api/v1/instances',tls=bridge,sni=URI),401,'bridge did not reach stock')
    print('PASS private bridge ready')


def reader_ready():
    tls = context('ca.pem','gateway-client')
    for path in ('instances','pools','jobs'):
        raw = wait(lambda:call(READER,'/api/v1/'+path,tls=tls),200,
                   'reader could not fetch authorized '+path)
        assert json.loads(raw)==[], 'unexpected stock fixture inventory'
    print('PASS reader authenticated to stock and projected inventory')


def incus_ready():
    tls = context('ca.pem','gateway-client')
    for route in ('/1.0','/1.0/instances?recursion=2&project=runners','/1.0/operations?recursion=1&project=runners'):
        raw = wait(lambda:call(9553,route,tls=tls),200,'synthetic Incus not ready')
        assert json.loads(raw)['type']=='sync'
    print('PASS synthetic Incus ready')


def verify():
    bridge=context('bridge-ca.pem','bridge-reader')
    assert call(BRIDGE,'/api/v1/providers',tls=bridge,sni=URI)[0]==404
    invalid_login = {'username': 'missingfixture', 'password': 'intentionally-invalid'}
    status,_ = call(BRIDGE,'/api/v1/auth/login','POST',invalid_login,tls=bridge,sni=URI)
    assert status in ((401,403) if RENEWABLE else (404,)), ('unexpected login scope',status)
    assert call(BRIDGE,'/api/v1/auth/login?extra=1','POST',invalid_login,tls=bridge,sni=URI)[0]==404
    assert call(BRIDGE,'/api/v1/controller','POST',tls=bridge,sni=URI)[0]==404
    assert call(BRIDGE,'/api/v1/instances?extra=1',tls=bridge,sni=URI)[0]==404
    assert call(BRIDGE,'/api/v1/instances',tls=bridge,sni=URI)[0] in (401,403)
    for cert in (None,'bridge-wrong'):
        try: call(BRIDGE,'/api/v1/instances',tls=context('bridge-ca.pem',cert),sni=URI)
        except (ssl.SSLError, ConnectionResetError): pass
        else: raise RuntimeError('bridge did not enforce exact client identity')
    ui=context('ca.pem')
    token=(ROOT/'admin.token').read_text()
    endpoint='/admin/api/v1/overview'
    def reconciled():
        status, raw = call(9444,endpoint,token=token,tls=ui)
        if status==200 and json.loads(raw)['admission_ready'] is not True:
            return 503, b''
        return status, raw
    overview=json.loads(wait(reconciled,200,'gateway never completed authenticated stock/Incus reconciliation'))
    assert all(overview[p] is False for p in ('experimental_provisioning_enabled','management_enabled','garm_management_available'))
    assert call(9444,'/',tls=ui)[0]==200
    assert call(9444,endpoint,tls=ui)[0]==401
    assert call(9444,'/admin/api/v1/policy','POST',{},token,tls=ui)[0] in (401,403,404)
    print('PASS gateway UI and protected data; companion cannot write to stock')


def outer():
    import socket
    for port in (STOCK,BRIDGE,READER,9444):
        try:
            with socket.create_connection(('127.0.0.1',port),timeout=1): pass
        except ConnectionRefusedError: continue
        raise RuntimeError('private listener reachable off namespace')
    print('PASS off-namespace loopback refusal')


def stock_health():
    wait(lambda:call(STOCK,'/api/v1/instances'),401,'stock did not remain active without companion')
    print('PASS stock independently still serves')


if __name__=='__main__':
    mode=sys.argv[1]
    if mode=='prepare': prepare(Path(sys.argv[2]))
    else: {'bootstrap':bootstrap,'bridge-ready':bridge_ready,'reader-ready':reader_ready,'incus-ready':incus_ready,'verify':verify,'outer':outer,'stock-health':stock_health}[mode]()
