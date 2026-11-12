#!/usr/bin/env python3
"""Exact-public-image, empty-state lab only. Never read installed configuration."""
import http.client
import json
import os
from pathlib import Path
import secrets
import shutil
import ssl
import sys
import time


def private_file(path, value):
    path.write_text(value)
    path.chmod(0o600)


def prepare(root):
    import fixture  # Reuse the already-qualified strict ephemeral certificate generator.
    root.mkdir(mode=0o700, exist_ok=False)
    for name in ('private', 'state', 'gateway-canary'):
        (root / name).mkdir(mode=0o700)
    fixture.prepare(root / 'seed')
    source = root / 'seed' / 'gateway-private'
    for name in ('ca.pem', 'server.pem', 'server.key'):
        private_file(root / 'private' / name, (source / name).read_text())
    shutil.rmtree(root / 'seed')
    # Entirely new synthetic JWT/encryption material, kept out of ENV/argv/output.
    config = f'''[default]
callback_url = "https://127.0.0.1:9998"
metadata_url = "https://127.0.0.1:9998"
enable_webhook_management = false
debug_server = false
[logging]
enable_log_streamer = false
log_format = "json"
log_level = "error"
[metrics]
enable = false
disable_auth = false
[jwt_auth]
secret = "{secrets.token_urlsafe(48)}"
time_to_live = "24h"
[database]
backend = "sqlite3"
passphrase = "{secrets.token_hex(16)}"
[database.sqlite3]
db_file = "/var/lib/garm/controller.db"
busy_timeout_seconds = 5
[apiserver]
bind = "127.0.0.1"
port = 9998
use_tls = true
[apiserver.tls]
certificate = "/run/garm-private/server.pem"
key = "/run/garm-private/server.key"
[apiserver.webui]
enable = false
'''
    private_file(root / 'private' / 'config.toml', config)
    private_file(root / 'gateway-canary' / 'canary', secrets.token_hex(32))
    print('Prepared NEW synthetic Garm lab state/config; no credentials printed')


def probe():
    context = ssl.create_default_context(cafile='/lab-ca.pem')
    context.minimum_version = ssl.TLSVersion.TLSv1_3
    context.verify_flags |= ssl.VERIFY_X509_STRICT
    for attempt in range(90):
        connection = http.client.HTTPSConnection('127.0.0.1', 9998, context=context, timeout=2)
        try:
            connection.request('GET', '/api/v1/controller')
            response = connection.getresponse()
            data = response.read()
            if response.status == 409 and json.loads(data).get('error') == 'init_required':
                break
        except (OSError, ssl.SSLError, http.client.HTTPException):
            pass
        finally:
            connection.close()
        time.sleep(0.5)
    else:
        raise RuntimeError('synthetic Garm TLS/API startup unavailable')
    listeners = []
    for table in ('/proc/net/tcp', '/proc/net/tcp6'):
        for line in Path(table).read_text().splitlines()[1:]:
            fields = line.split()
            address, port = fields[1].split(':')
            if int(port, 16) == 9998 and fields[3] == '0A':
                assert address == '0100007F', 'Garm lab API escaped IPv4 loopback'
                listeners.append(port)
    assert len(listeners) == 1
    print('PASS exact upstream Garm serves strictly verified loopback TLS without root/capabilities; remains uninitialized')


if __name__ == '__main__':
    try:
        if sys.argv[1] == 'prepare':
            prepare(Path(sys.argv[2]))
        elif sys.argv[1] == 'probe':
            probe()
        else:
            raise ValueError('unknown lab action')
    except Exception as error:
        print('Garm identity lab failed:', type(error).__name__, file=sys.stderr)
        raise SystemExit(1)
