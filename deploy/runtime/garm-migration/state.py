#!/usr/bin/env python3
"""Generated-fixture-only stopped-writer transfer. NOT a live backup/migration tool."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import secrets
import tomllib
import sqlite3
import subprocess
import sys
import tempfile

HERE = Path(__file__).resolve().parent
RUNTIME = '/lab/garm-synthetic-storage-runtime'
DBS = ('controller.db', 'blob-controller.db')


def module(name, path):
    sys.path.insert(0, str(HERE.parent))
    spec = importlib.util.spec_from_file_location(name, path)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


def generated(root):
    assert root.name.startswith('garm-synthetic-storage-') and root.is_dir() and not root.is_symlink()
    assert (root / 'private' / '.synthetic-only').read_text() == 'NEW GENERATED FIXTURE ONLY\n'
    for part in ('private', 'state'):
        assert not (root / part).is_symlink()
        for child in (root / part).rglob('*'):
            assert child.is_file() and not child.is_symlink() and child.stat().st_nlink == 1


def hashes(root):
    generated(root)
    return {str(p.relative_to(root)): hashlib.sha256(p.read_bytes()).hexdigest()
            for part in ('private', 'state') for p in sorted((root / part).iterdir())}


def prepare(root, runtime=RUNTIME):
    identity = module('garm_identity', HERE.parent / 'garm-identity.py')
    identity.prepare(root)
    private = root / 'private'
    config = private / 'config.toml'
    config.write_text(config.read_text().replace('/var/lib/garm', runtime + '/state').replace('/run/garm-private', runtime + '/private'))
    marker = private / '.synthetic-only'
    marker.write_text('NEW GENERATED FIXTURE ONLY\n'); marker.chmod(0o600)
    # Distinct one-run provider/source leaves belong to Garm; gateway keys stay
    # outside both runtime binds. No provider is configured or called in this lab.
    fixture = module('fixture', HERE.parent / 'fixture.py')
    fixture.prepare(root / 'roles')
    for role, leaf in (('provider', 'provider'), ('source', 'wrong')):
        for extension in ('pem', 'key'):
            dest = private / f'{role}.{extension}'
            shutil.copyfile(root / 'roles' / 'fixture-private' / f'{leaf}.{extension}', dest)
            dest.chmod(0o600)
    garm_keys = {p.read_bytes() for p in private.glob('*.key')}
    gateway_keys = {p.read_bytes() for p in (root / 'roles' / 'gateway-private').glob('*.key')}
    assert garm_keys.isdisjoint(gateway_keys) and len(garm_keys) == 3
    generated(root)


def transfer(source, target, uid):
    generated(source)
    assert not target.exists() and target.name.startswith('garm-synthetic-storage-')
    before = hashes(source)
    target.mkdir(mode=0o700)
    # The shell must prove every old daemon/helper stopped BEFORE this action.
    # Preserve full original directory, including WAL/SHM, without opening it.
    with tempfile.TemporaryDirectory(prefix='garm-stopped-staging-', dir=target.parent) as temp:
        staging = Path(temp) / 'state'
        shutil.copytree(source / 'state', staging)
        shutil.copytree(source / 'private', target / 'private')
        (target / 'state').mkdir(mode=0o700)
        for name in DBS:
            src = staging / name
            assert src.is_file()
            dest = target / 'state' / name
            dest.touch(mode=0o600, exist_ok=False)
            with sqlite3.connect(f'file:{src}?mode=ro', uri=True) as reader:
                with sqlite3.connect(dest) as writer:
                    reader.backup(writer)
                    assert writer.execute('PRAGMA integrity_check').fetchone()[0] == 'ok'
    generated(target)
    assert hashes(source) == before
    assert {p.name: p.read_bytes() for p in (source / 'private').iterdir()} == {p.name: p.read_bytes() for p in (target / 'private').iterdir()}
    # Only NEW generated destination, never the preserved original/live mount.
    for path in [target, target / 'private', target / 'state', *(target / 'private').iterdir(), *(target / 'state').iterdir()]:
        os.chown(path, uid, uid)
    print('PASS stopped generated pair transferred via staging; original bytes and private material unchanged')



def private_mismatch(target, kind):
    private = target / 'private'
    config = private / 'config.toml'
    if kind in ('wrong-passphrase', 'wrong-jwt'):
        data = tomllib.loads(config.read_text())
        old = data['database']['passphrase'] if kind == 'wrong-passphrase' else data['jwt_auth']['secret']
        new = secrets.token_hex(16) if kind == 'wrong-passphrase' else secrets.token_urlsafe(48)
        assert new != old and config.read_text().count('"' + old + '"') == 1
        config.write_text(config.read_text().replace('"' + old + '"', '"' + new + '"'))
    elif kind == 'wrong-ca':
        with tempfile.TemporaryDirectory(prefix='garm-synthetic-wrong-ca-', dir=target.parent) as temp:
            subprocess.run(['openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256', '-nodes', '-days', '1', '-subj', '/CN=unrelated-synthetic-ca', '-addext', 'basicConstraints=critical,CA:TRUE', '-addext', 'keyUsage=critical,keyCertSign,cRLSign', '-keyout', str(Path(temp) / 'key.pem'), '-out', str(Path(temp) / 'ca.pem')], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            (private / 'ca.pem').write_bytes((Path(temp) / 'ca.pem').read_bytes())
    elif kind == 'wrong-tls-key':
        assert (private / 'server.key').read_bytes() != (private / 'provider.key').read_bytes()
        (private / 'server.key').write_bytes((private / 'provider.key').read_bytes())
    else: raise ValueError('unknown private mismatch')


def negative(source, target, kind):
    generated(source)
    assert not target.exists() and target.name.startswith('garm-synthetic-storage-')
    target.mkdir(mode=0o700)
    for part in ('private', 'state'):
        shutil.copytree(source / part, target / part)
    for path in [target, target / 'private', target / 'state', *(target / 'private').iterdir(), *(target / 'state').iterdir()]:
        os.chown(path, 65531, 65531)
    db = target / 'state' / 'controller.db'
    if kind == 'missing-main': db.unlink()
    elif kind == 'missing-blob': (target / 'state' / 'blob-controller.db').unlink()
    elif kind == 'mode': db.chmod(0o644)
    elif kind == 'owner': os.chown(db, 65532, 65532)
    elif kind == 'symlink':
        db.rename(target / 'state' / 'saved.db'); db.symlink_to('saved.db')
    elif kind == 'hardlink': os.link(db, target / 'state' / 'linked.db')
    elif kind == 'wrong-controller':
        with sqlite3.connect(db) as c: c.execute("UPDATE controller_infos SET controller_id='00000000-0000-0000-0000-000000000000'")
    elif kind == 'changed-config':
        config = target / 'private' / 'config.toml'; config.write_text(config.read_text() + '\n# substituted private configuration\n')
    elif kind == 'interrupted-copy': db.write_bytes(db.read_bytes()[:100])
    elif kind in ('wrong-passphrase', 'wrong-jwt', 'wrong-ca', 'wrong-tls-key'): private_mismatch(target, kind)
    else: raise ValueError('unknown negative')


def main():
    action = sys.argv[1]
    if action == 'prepare': prepare(Path(sys.argv[2]))
    elif action == 'transfer': transfer(Path(sys.argv[2]), Path(sys.argv[3]), 65531)
    elif action == 'negative': negative(Path(sys.argv[2]), Path(sys.argv[3]), sys.argv[4])
    elif action == 'freeze': Path(sys.argv[3]).write_text(json.dumps(hashes(Path(sys.argv[2]))))
    elif action == 'unchanged': assert hashes(Path(sys.argv[2])) == json.loads(Path(sys.argv[3]).read_text())
    elif action == 'selftest':
        # Native supplemental storage/guard test, NOT a container/UID/patch proof.
        binary = str(Path(sys.argv[2]).resolve())
        with tempfile.TemporaryDirectory(prefix='garm-transfer-native-') as temp:
            source = Path(temp) / 'garm-synthetic-storage-source'
            target = Path(temp) / 'garm-synthetic-storage-target'
            # Same fixed runtime path in source config for the real container lab;
            # native uses source location, then exact bytes preserved on copy.
            prepare(source, str(source))
            subprocess.run([binary, 'seed', str(source)], check=True)
            subprocess.run([binary, 'guard', str(source)], check=True)
            transfer(source, target, os.getuid())
            # Config path changes are native-only validation setup, not migration.
            config = target / 'private' / 'config.toml'
            config.write_text(config.read_text().replace(str(source), str(target)))
            expected = target / 'private' / 'expected.json'
            x = json.loads(expected.read_text()); x['Private']['config.toml'] = hashlib.sha256(config.read_bytes()).hexdigest(); expected.write_text(json.dumps(x))
            subprocess.run([binary, 'guard', str(target)], check=True)
            baseline = Path(temp) / 'native-baseline'
            shutil.copytree(target, baseline)
            for kind in ('missing-main', 'missing-blob', 'mode', 'directory-mode', 'symlink', 'hardlink', 'wrong-controller', 'changed-config', 'interrupted-copy', 'wrong-passphrase', 'wrong-jwt', 'wrong-ca', 'wrong-tls-key'):
                shutil.rmtree(target); shutil.copytree(baseline, target)
                subprocess.run([binary, 'guard', str(target)], check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
                db = target / 'state' / 'controller.db'
                if kind == 'missing-main': db.unlink()
                elif kind == 'missing-blob': (target / 'state' / 'blob-controller.db').unlink()
                elif kind == 'mode': db.chmod(0o644)
                elif kind == 'directory-mode': (target / 'state').chmod(0o755)
                elif kind == 'symlink':
                    db.rename(target / 'saved.db'); db.symlink_to('../saved.db')
                elif kind == 'hardlink': os.link(db, target / 'linked.db')
                elif kind == 'wrong-controller':
                    with sqlite3.connect(db) as c: c.execute("UPDATE controller_infos SET controller_id='00000000-0000-0000-0000-000000000000'")
                elif kind == 'changed-config':
                    config = target / 'private' / 'config.toml'; config.write_text(config.read_text() + '\n# substituted private config\n')
                elif kind == 'interrupted-copy': db.write_bytes(db.read_bytes()[:100])
                elif kind in ('wrong-passphrase', 'wrong-jwt', 'wrong-ca', 'wrong-tls-key'): private_mismatch(target, kind)
                result = subprocess.run([binary, 'guard', str(target)], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
                assert result.returncode == 1
                if kind == 'missing-main': assert not db.exists()
                if kind == 'missing-blob': assert not (target / 'state' / 'blob-controller.db').exists()
            print('PASS thirteen native guard negatives with positive controls; missing databases stay absent')
        print('PASS native storage helper only; container/UID/availability not tested')
    else: raise ValueError('unknown synthetic action')


if __name__ == '__main__':
    try: main()
    except Exception as error:
        print('Synthetic migration operation failed:', type(error).__name__, file=sys.stderr)
        raise SystemExit(1)
