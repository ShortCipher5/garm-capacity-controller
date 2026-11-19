"""Explicit local image-only export. Never uploads, builds, starts or commits containers."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile
import threading

MAX_ARCHIVE = 8 * 1024**3
STOCK_ID = 'sha256:cb7c076ddbf24a67a9aef4a4e61ace495e9ffc7bcb69df3bf5e89275a26b79f6'
ROLES = ('stock', 'gateway', 'ingress', 'ledger')
EXECUTABLE_HASHES = ('gateway_executable_sha256', 'ingress_executable_sha256')
TRUE = ('exact_four_ipv4_loopback_listeners_verified', 'actual_three_binaries_started', 'shared_isolated_network', 'stock_uninitialized')
FALSE = ('host_ports_published', 'supervisor_present', 'admission_ready', 'upstream_inventory_integrated', 'managed_topology_qualified', 'startup_authority', 'deployment_authorized')


def require(condition):
    if not condition:
        raise ValueError('pilot export refused')


def evidence(raw):
    require(len(raw) <= 16384)
    value = json.loads(raw)
    require(type(value) is dict)
    fields = {'schema', 'source_commit', 'run_id', 'run_attempt'} | {r+'_image' for r in ROLES} | set(TRUE+FALSE+EXECUTABLE_HASHES)
    require(set(value) == fields and value['schema'] == 'disposable-three-service-preboot/v2')
    require(type(value['source_commit']) is str and re.fullmatch('[0-9a-f]{40}', value['source_commit']))
    for key in ('run_id', 'run_attempt'):
        require(type(value[key]) is str and re.fullmatch('[1-9][0-9]{0,19}', value[key]))
    for key in EXECUTABLE_HASHES:
        require(type(value[key]) is str and re.fullmatch('[0-9a-f]{64}', value[key]))
    for key in TRUE:
        require(value[key] is True)
    for key in FALSE:
        require(value[key] is False)
    ids = {r: value[r+'_image'] for r in ROLES}
    require(all(type(v) is str and re.fullmatch('sha256:[0-9a-f]{64}', v) for v in ids.values()))
    require(len(set(ids.values())) == 4 and ids['stock'] == STOCK_ID)
    return value, ids


def inspect_archive(path, ids):
    # Read only: no tar extraction, layer execution or container import.
    with tarfile.open(path, 'r:') as archive:
        entries = {}
        for member in archive:
            require(len(entries) < 100000 and member.name not in entries)
            require(not member.name.startswith('/') and '..' not in member.name.split('/'))
            require(member.isfile() or member.isdir())
            entries[member.name] = member
        require('manifest.json' in entries and entries['manifest.json'].size <= 65536)
        manifest = json.load(archive.extractfile(entries['manifest.json']))
        require(type(manifest) is list and len(manifest) == len(ids))
        allowed = {'manifest.json'}
        observed = set()
        for row in manifest:
            require(type(row) is dict and set(row) == {'Config', 'RepoTags', 'Layers'})
            require(row['RepoTags'] in (None, []) and type(row['Layers']) is list and len(row['Layers']) <= 256)
            config = row['Config']
            require(type(config) is str and config in entries and entries[config].isfile() and entries[config].size <= 16*1024**2)
            config_raw = archive.extractfile(entries[config]).read()
            config_value = json.loads(config_raw)
            require(config_value.get('os') == 'linux' and config_value.get('architecture') == 'amd64')
            image_id = 'sha256:'+hashlib.sha256(config_raw).hexdigest()
            require(image_id in ids.values() and image_id not in observed)
            observed.add(image_id)
            allowed.add(config)
            rootfs = config_value.get('rootfs', {})
            require(rootfs.get('type') == 'layers' and type(rootfs.get('diff_ids')) is list and len(rootfs['diff_ids']) == len(row['Layers']))
            for layer, expected in zip(row['Layers'], rootfs['diff_ids']):
                require(type(layer) is str and layer in entries and entries[layer].isfile())
                digest = hashlib.sha256()
                with archive.extractfile(entries[layer]) as stream:
                    for block in iter(lambda: stream.read(1024*1024), b''):
                        digest.update(block)
                require(expected == 'sha256:'+digest.hexdigest())
                allowed.add(layer)
        # Refuse extra state/config files, tags, links and unsupported export formats.
        require(all(m.isdir() or name in allowed for name,m in entries.items()))
        require(observed == set(ids.values()))


def save_images(destination, ids, max_bytes=MAX_ARCHIVE):
    require(type(max_bytes) is int and 0 < max_bytes <= MAX_ARCHIVE)
    # Fixed local endpoint; never inherit a remote Docker context or credentials.
    with destination.open('xb') as output:
        os.chmod(destination, 0o600)
        proc = subprocess.Popen(['docker', '--host', 'unix:///var/run/docker.sock', 'image', 'save', *ids.values()], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
        deadline = threading.Timer(180, proc.kill)
        deadline.start()
        try:
            total = 0
            while True:
                block = proc.stdout.read(1024*1024)
                if not block:
                    break
                total += len(block)
                require(total <= max_bytes)
                output.write(block)
            require(proc.wait(timeout=30) == 0)
            output.flush()
            os.fsync(output.fileno())
        finally:
            deadline.cancel()
            if proc.poll() is None:
                proc.kill()
                proc.wait()
            proc.stdout.close()


def pack(raw, destination, expected_commit, save=save_images):
    record, ids = evidence(raw)
    require(record['source_commit'] == expected_commit)
    destination = Path(destination)
    require(destination.is_absolute() and destination.parent.resolve() == destination.parent)
    parent = destination.parent.stat()
    require(parent.st_uid == os.geteuid() and parent.st_mode & 0o022 == 0)
    destination.mkdir(mode=0o700, exist_ok=False)
    archive = destination / 'images.tar'
    # Failure leaves private partial files, without a success manifest; no destructive retry.
    save(archive, ids)
    require(archive.is_file() and not archive.is_symlink() and 0 < archive.stat().st_size <= MAX_ARCHIVE)
    inspect_archive(archive, ids)
    digest = hashlib.sha256()
    with archive.open('rb') as stream:
        for block in iter(lambda: stream.read(1024*1024), b''):
            digest.update(block)
    manifest = {'schema':'pilot-image-export/v1', 'source_commit':record['source_commit'], 'run_id':record['run_id'], 'run_attempt':record['run_attempt'], 'roles':ids, 'observed_executable_sha256':{k.removesuffix('_executable_sha256'):record[k] for k in EXECUTABLE_HASHES}, 'platform':'linux/amd64', 'archive':'images.tar', 'archive_bytes':archive.stat().st_size, 'archive_sha256':digest.hexdigest(), 'evidence_sha256':hashlib.sha256(raw).hexdigest(), 'deployment_authorized':False, 'uploaded':False}
    with (destination/'manifest.json').open('x') as output:
        os.chmod(destination/'manifest.json', 0o600)
        output.write(json.dumps(manifest, sort_keys=True, separators=(',', ':'))+'\n')
        output.flush()
        os.fsync(output.fileno())
    return manifest


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--evidence', required=True)
    parser.add_argument('--destination', required=True)
    parser.add_argument('--expected-commit', required=True)
    args = parser.parse_args()
    try:
        with open(args.evidence, 'rb') as stream:
            raw = stream.read(16385)
        pack(raw, args.destination, args.expected_commit)
    except Exception:
        raise SystemExit('Pilot image export refused; no success manifest guaranteed')
    print('Local image-only archive verified; not uploaded or authorized for deployment')
