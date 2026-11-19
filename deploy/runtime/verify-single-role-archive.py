"""Offline strict OCI/Docker closure building block. Not wired to export/import."""
import base64
import importlib.util
from pathlib import Path
import hashlib
import json
import os
import re
import stat
import tarfile

MAX_ARCHIVE = 8 * 1024**3
MAX_METADATA = 256 * 1024
MAX_CONFIG = 16 * 1024**2
INDEX = 'application/vnd.oci.image.index.v1+json'
MANIFEST = 'application/vnd.oci.image.manifest.v1+json'
CONFIG = 'application/vnd.oci.image.config.v1+json'
LAYER = 'application/vnd.oci.image.layer.v1.tar'


def require(ok):
    if not ok:
        raise ValueError('single-role archive refused')


def pairs(items):
    result = {}
    for key, value in items:
        require(key not in result)
        result[key] = value
    return result


def decode(raw):
    def invalid(_):
        raise ValueError('single-role archive refused')
    return json.loads(raw, object_pairs_hook=pairs, parse_constant=invalid)


def is_digest(value):
    return type(value) is str and re.fullmatch('sha256:[0-9a-f]{64}', value) is not None


def physical_records(stream, archive_size):
    """Inspect physical USTAR headers before tarfile can consume extensions.

    Reads only bounded headers/padding, seeking past payloads whose hashes are
    checked downstream. No extraction and no second open of the archive path.
    """
    require(archive_size % 512 == 0)
    records = {}
    offset = 0

    def octal(field):
        value = field.strip(b'\0 ')
        require(not value or all(c in b'01234567' for c in value))
        return int(value, 8) if value else 0

    def string(field):
        value, separator, tail = field.partition(b'\0')
        require(not separator or not any(tail))
        try:
            return value.decode('ascii')
        except UnicodeDecodeError:
            raise ValueError('single-role archive refused') from None

    while offset < archive_size:
        stream.seek(offset)
        header = stream.read(512)
        require(len(header) == 512)
        if not any(header):
            # Two end markers plus bounded all-zero record padding only.
            remaining = archive_size - offset
            require(1024 <= remaining <= 10240)
            tail = stream.read(remaining - 512)
            require(len(tail) == remaining - 512 and not any(tail))
            stream.seek(0)
            return records
        require(len(records) < 1024)
        require(header[156:157] in (b'0', b'\0', b'5'))
        require(header[257:265] == b'ustar\x00' + b'00')
        require(not any(header[345:512]))  # no prefix or hidden header tail
        require(not any(header[157:257]))  # no link target on ordinary files
        checksum = octal(header[148:156])
        require(checksum == sum(header[:148]) + 8 * 32 + sum(header[156:]))
        for begin, end in ((100,108),(108,116),(116,124),(136,148),(329,337),(337,345)):
            octal(header[begin:end])
        size = octal(header[124:136])
        name = string(header[:100])
        directory = header[156:157] == b'5'
        if directory:
            name = name.removesuffix('/')
            require(name in ('blobs', 'blobs/sha256') and size == 0)
        else:
            require(name in ('manifest.json', 'index.json', 'oci-layout')
                    or re.fullmatch('blobs/sha256/[0-9a-f]{64}', name))
            require(0 < size <= MAX_ARCHIVE)
        require(name not in records)
        data_offset = offset + 512
        end = data_offset + size
        padded_end = (end + 511) // 512 * 512
        require(padded_end <= archive_size - 1024)
        stream.seek(end)
        padding = stream.read(padded_end - end)
        require(len(padding) == padded_end - end and not any(padding))
        records[name] = (data_offset, size, directory)
        offset = padded_end
    raise ValueError('single-role archive refused')


def verify_single_role_archive(path, expected_image_id):
    """Strict closure-only mode still refuses all ancillary blobs."""
    return _verify(path, expected_image_id, None)


def verify_modern_single_role_archive(path, expected_image_id, exporter, proof_binary):
    """Validate one measured modern archive, not authorize import or delivery.

    proof_binary is the trusted reviewed helper built by the opt-in harness, not
    an archive-supplied program or a caller-supplied proof boolean.
    """
    return _verify(path, expected_image_id, (exporter, proof_binary))


def _verify(path, expected_image_id, proof):
    require(is_digest(expected_image_id))
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
    with os.fdopen(fd, 'rb') as stream:
        info = os.fstat(stream.fileno())
        require(stat.S_ISREG(info.st_mode) and 0 < info.st_size <= MAX_ARCHIVE)
        physical = physical_records(stream, info.st_size)
        with tarfile.open(fileobj=stream, mode='r:') as archive:
            entries = {}
            for member in archive:
                require(len(entries) < 1024 and member.name not in entries)
                require(not member.pax_headers and not member.issparse())
                require(member.isfile() or member.isdir())
                if member.isdir():
                    require(member.name in ('blobs', 'blobs/sha256') and member.size == 0)
                else:
                    require(member.name in ('manifest.json', 'index.json', 'oci-layout')
                            or re.fullmatch('blobs/sha256/[0-9a-f]{64}', member.name))
                    require(0 < member.size <= MAX_ARCHIVE)
                require(physical.get(member.name) == (member.offset_data, member.size, member.isdir()))
                entries[member.name] = member
            require(set(entries) == set(physical))
            # Reject concatenated archives or hidden trailing data, not zero tar padding.
            stream.seek(archive.offset)
            tail = stream.read(10241)
            require(len(tail) <= 10240 and not any(tail))
            allowed = {'manifest.json', 'index.json', 'oci-layout'}
            verified = {}

            def read_json(name, limit):
                m = entries.get(name)
                require(m is not None and m.isfile() and m.size <= limit)
                raw = archive.extractfile(m).read(limit + 1)
                require(len(raw) == m.size)
                return decode(raw)

            def descriptor(desc, media, limit):
                require(type(desc) is dict and set(desc) == {'mediaType', 'digest', 'size'})
                require(desc['mediaType'] == media and is_digest(desc['digest']))
                size = desc['size']
                require(type(size) is int and 0 < size <= limit)
                name = 'blobs/sha256/' + desc['digest'][7:]
                m = entries.get(name)
                require(m is not None and m.isfile() and m.size == size)
                if name not in verified:
                    digest = hashlib.sha256()
                    length = 0
                    with archive.extractfile(m) as blob:
                        for block in iter(lambda: blob.read(1024*1024), b''):
                            length += len(block)
                            require(length <= size)
                            digest.update(block)
                    require(length == size and digest.hexdigest() == desc['digest'][7:])
                    verified[name] = (media, size)
                require(verified[name] == (media, size))
                allowed.add(name)
                return name

            require(read_json('oci-layout', MAX_METADATA) == {'imageLayoutVersion': '1.0.0'})
            index = read_json('index.json', MAX_METADATA)
            require(type(index) is dict and set(index) == {'schemaVersion', 'mediaType', 'manifests'})
            require(type(index['schemaVersion']) is int and index['schemaVersion'] == 2 and index['mediaType'] == INDEX)
            require(type(index['manifests']) is list and len(index['manifests']) == 1)
            manifest_path = descriptor(index['manifests'][0], MANIFEST, MAX_METADATA)
            manifest = read_json(manifest_path, MAX_METADATA)
            require(type(manifest) is dict and set(manifest) == {'schemaVersion', 'mediaType', 'config', 'layers'})
            require(type(manifest['schemaVersion']) is int and manifest['schemaVersion'] == 2 and manifest['mediaType'] == MANIFEST)
            config_path = descriptor(manifest['config'], CONFIG, MAX_CONFIG)
            require(manifest['config']['digest'] == expected_image_id)
            config = read_json(config_path, MAX_CONFIG)
            require(type(config) is dict and config.get('os') == 'linux' and config.get('architecture') == 'amd64'
                    and config.get('variant', '') == '')
            root = config.get('rootfs')
            require(type(root) is dict and set(root) == {'type', 'diff_ids'} and root['type'] == 'layers')
            layers = manifest['layers']
            diffs = root['diff_ids']
            require(type(layers) is list and 1 <= len(layers) <= 256
                    and type(diffs) is list and len(diffs) == len(layers))
            require(all(is_digest(v) for v in diffs) and len(set(diffs)) == len(diffs))
            paths = []
            for layer, diff in zip(layers, diffs):
                paths.append(descriptor(layer, LAYER, MAX_ARCHIVE))
                # Only measured uncompressed tar media: content digest equals DiffID.
                require(layer['digest'] == diff)
            docker = read_json('manifest.json', MAX_METADATA)
            require(type(docker) is list and len(docker) == 1)
            row = docker[0]
            require(type(row) is dict and set(row) == {'Config', 'RepoTags', 'Layers', 'LayerSources'})
            require(row['Config'] == config_path and row['RepoTags'] in (None, []) and row['Layers'] == paths)
            sources = row['LayerSources']
            require(type(sources) is dict and set(sources) == set(diffs))
            for diff, layer in zip(diffs, layers):
                require(sources[diff] == layer)
                # Equality alone must not allow bool/int aliases or unknown descriptor keys.
                descriptor(sources[diff], LAYER, MAX_ARCHIVE)
            files = {n for n,m in entries.items() if m.isfile()}
            ancillary_count = 0
            if proof is not None:
                extras = files - allowed
                require(len(extras) == len(layers))
                def private_raw(name, limit):
                    member = entries[name]
                    require(member.isfile() and 0 < member.size <= limit)
                    raw = archive.extractfile(member).read(limit + 1)
                    require(len(raw) == member.size)
                    require(hashlib.sha256(raw).hexdigest() == name.rsplit('/', 1)[1])
                    return raw
                nodes = {}
                for name in sorted(extras):
                    require(re.fullmatch('blobs/sha256/[0-9a-f]{64}', name))
                    nodes['sha256:' + name.rsplit('/', 1)[1]] = base64.b64encode(private_raw(name, MAX_METADATA)).decode('ascii')
                payload = {'image_id': expected_image_id,
                           'config': base64.b64encode(private_raw(config_path, MAX_CONFIG)).decode('ascii'),
                           'nodes': nodes, 'exporter': proof[0]}
                spec = importlib.util.spec_from_file_location('ancillary_proof', Path(__file__).with_name('ancillary-proof.py'))
                helper = importlib.util.module_from_spec(spec)
                spec.loader.exec_module(helper)
                checked = helper.prove_payload(payload, proof[1])
                require(checked['matched_nodes'] == len(extras))
                ancillary_count = len(extras)
                allowed.update(extras)
            require(files == allowed)
            # A path is opened only once. Also refuse observable in-place mutation
            # during verification instead of mixing descriptor/layer/private bytes.
            after = os.fstat(stream.fileno())
            require((info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns, info.st_ctime_ns)
                    == (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns, after.st_ctime_ns))
            return {'schema': 'modern-single-role-archive-closure/v1' if proof is not None else 'single-role-archive-closure/v1',
                    'image_id': expected_image_id, 'layers': len(layers),
                    'platform': 'linux/amd64', 'closure_verified': True,
                    'ancillary_nodes': ancillary_count,
                    'ancillary_derivation_verified': proof is not None,
                    'archive_accepted': False, 'deployment_authorized': False}
