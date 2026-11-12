"""Only NEW generated lab state; no installed source/input or archive support."""
import os,secrets,subprocess
from pathlib import Path

def need(ok):
 if not ok:raise RuntimeError('generated fixture rejected')
def write(path,data,mode=0o600):
 with path.open('xb') as f:f.write(data if isinstance(data,bytes) else data.encode())
 path.chmod(mode)
def openssl(args):subprocess.run(['openssl',*args],env={'PATH':os.environ.get('PATH','/usr/bin:/bin'),'HOME':'/nonexistent'},check=True,capture_output=True,timeout=30)
def prepare(root,public_ca,change_owner=False):
 need(not root.exists());root.mkdir(mode=0o700)
 for name in ('issuer','anchor','garm','state','bad-state','trust','garm-canary','tools-canary'):(root/name).mkdir(mode=0o700)
 issuer=root/'issuer'
 openssl(['req','-x509','-newkey','ec','-pkeyopt','ec_paramgen_curve:P-256','-nodes','-days','1','-subj','/CN=Generated image lab root','-addext','basicConstraints=critical,CA:TRUE','-addext','keyUsage=critical,keyCertSign,cRLSign','-keyout',str(issuer/'root.key'),'-out',str(issuer/'root.pem')])
 for role in ('anchor','garm'):
  private=root/role
  openssl(['req','-new','-newkey','ec','-pkeyopt','ec_paramgen_curve:P-256','-nodes','-subj','/CN=Generated '+role,'-keyout',str(private/'server.key'),'-out',str(issuer/(role+'.csr'))])
  ext=issuer/(role+'.ext');write(ext,'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=serverAuth\nsubjectAltName=IP:127.0.0.1\nsubjectKeyIdentifier=hash\nauthorityKeyIdentifier=keyid,issuer\n')
  openssl(['x509','-req','-days','1','-in',str(issuer/(role+'.csr')),'-CA',str(issuer/'root.pem'),'-CAkey',str(issuer/'root.key'),'-set_serial','2' if role=='anchor' else '3','-extfile',str(ext),'-out',str(private/'server.pem')])
 openssl(['req','-x509','-newkey','ec','-pkeyopt','ec_paramgen_curve:P-256','-nodes','-days','1','-subj','/CN=Unrelated generated root','-addext','basicConstraints=critical,CA:TRUE','-addext','keyUsage=critical,keyCertSign,cRLSign','-keyout',str(issuer/'unrelated.key'),'-out',str(issuer/'unrelated.pem')])
 nonce=secrets.token_hex(16);write(root/'anchor/witness',nonce)
 write(root/'trust/positive.pem',public_ca+(issuer/'root.pem').read_bytes(),0o444);write(root/'trust/unrelated.pem',(issuer/'unrelated.pem').read_bytes(),0o444);write(root/'trust/empty.pem',b'',0o444)
 for role in ('garm','tools'):write(root/(role+'-canary')/'value',nonce)
 config=f'''[default]
callback_url="https://127.0.0.1:9998"
metadata_url="https://127.0.0.1:9998"
enable_webhook_management=false
debug_server=false
[logging]
enable_log_streamer=false
log_format="json"
log_level="error"
[metrics]
enable=false
disable_auth=false
[jwt_auth]
secret="{secrets.token_urlsafe(48)}"
time_to_live="24h"
[database]
backend="sqlite3"
passphrase="{secrets.token_hex(16)}"
[database.sqlite3]
db_file="/var/lib/garm/controller.db"
busy_timeout_seconds=5
[apiserver]
bind="127.0.0.1"
port=9998
use_tls=true
[apiserver.tls]
certificate="/run/garm-private/server.pem"
key="/run/garm-private/server.key"
[apiserver.webui]
enable=true
'''
 write(root/'garm/config.toml',config)
 need((root/'garm/server.key').read_bytes()!=(root/'anchor/server.key').read_bytes())
 for p in root.rglob('*'):
  need(not p.is_symlink())
  if p.is_file() and p.parent.name!='trust':p.chmod(0o600)
 # Scope exception: ONLY called after build in the new hosted lab. No live source
 # or arbitrary destination is accepted; normal unit tests never change owner.
 if change_owner:
  for name,uid in [('anchor',65530),('garm',65531),('state',65531),('bad-state',65531),('garm-canary',65531),('tools-canary',65533)]:
   parent=root/name
   for p in [*parent.iterdir(),parent]:
    need(p.is_relative_to(root) and not p.is_symlink())
    subprocess.run(['sudo','-n','chown',str(uid)+':'+str(uid),str(p)],env={'PATH':os.environ.get('PATH','/usr/bin:/bin'),'HOME':'/nonexistent'},check=True,capture_output=True,timeout=10)
 # Parent transit is needed for Docker's bind setup only; private role leaves stay
 #0700/0600. This is a generated root, not an installed credential tree.
 return nonce
