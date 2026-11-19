"""Bounded metadata diagnostics, never archive acceptance or extraction."""
import hashlib
import json
import re
import tarfile

MEDIA = {
    'application/vnd.oci.image.manifest.v1+json': 'oci_manifest',
    'application/vnd.docker.distribution.manifest.v2+json': 'docker_manifest',
    'application/vnd.oci.image.index.v1+json': 'oci_index',
    'application/vnd.docker.distribution.manifest.list.v2+json': 'docker_index',
    'application/vnd.oci.image.layer.v1.tar': 'tar',
    'application/vnd.oci.image.layer.v1.tar+gzip': 'gzip',
    'application/vnd.oci.image.layer.v1.tar+zstd': 'zstd',
    'application/vnd.docker.image.rootfs.diff.tar.gzip': 'docker_gzip',
}


def require(ok):
    if not ok:
        raise ValueError('structure unavailable')


def unique(pairs):
    result = {}
    for k,v in pairs:
        require(k not in result)
        result[k] = v
    return result


def describe(path, ids):
    with tarfile.open(path, 'r:') as archive:
        entries = {}
        for member in archive:
            require(len(entries) < 100000 and member.name not in entries)
            require(member.isfile() or member.isdir())
            require(not member.name.startswith('/') and '..' not in member.name.split('/'))
            entries[member.name] = member

        def read(name):
            m = entries.get(name)
            require(m is not None and m.isfile() and 0 < m.size <= 65536)
            raw = archive.extractfile(m).read(65537)
            require(len(raw) == m.size)
            return raw, json.loads(raw, object_pairs_hook=unique)

        _, docker = read('manifest.json')
        _, index = read('index.json')
        require(type(docker) is list and len(docker) <= 32)
        require(type(index) is dict and type(index.get('manifests')) is list)
        roots = index['manifests']
        require(len(roots) <= 32)
        out = {
            'docker_rows': len(docker), 'docker_rows_base_fields_only': True,
            'docker_layer_sources_present': False, 'docker_parent_present': False,
            'docker_unknown_fields_present': False, 'docker_tags_present': False,
            'index_roots': len(roots), 'index_standard_fields_only': set(index) <= {'schemaVersion','mediaType','manifests'},
            'root_descriptors_standard_fields_only': True,
            'root_annotations_present': False, 'root_platform_present': False,
            'root_unknown_media_types': 0, 'nested_index_roots': 0,
            'root_oci_manifests': 0, 'root_docker_manifests': 0,
            'manifest_schema_two': True, 'config_descriptor_standard_fields_only': True,
            'config_media_counts': {'oci':0,'docker':0,'unknown':0},
            'root_manifest_digest_size_matches': 0, 'root_missing_or_invalid_blobs': 0,
            'root_image_manifests': 0, 'manifest_extra_fields_present': False,
            'referenced_role_configs': 0, 'duplicate_role_configs': 0,
            'layer_descriptor_count': 0, 'layer_descriptor_extra_fields_present': False,
            'layer_media_counts': {k:0 for k in ('tar','gzip','zstd','docker_gzip','unknown')},
            'diagnostic_only': True,
        }
        for row in docker:
            require(type(row) is dict)
            keys = set(row)
            out['docker_rows_base_fields_only'] &= keys == {'Config','RepoTags','Layers'}
            out['docker_layer_sources_present'] |= 'LayerSources' in keys
            out['docker_parent_present'] |= 'Parent' in keys
            out['docker_unknown_fields_present'] |= bool(keys - {'Config','RepoTags','Layers','LayerSources','Parent'})
            out['docker_tags_present'] |= row.get('RepoTags') not in (None, [])
        configs = set()
        for desc in roots:
            require(type(desc) is dict)
            out['root_descriptors_standard_fields_only'] &= set(desc) <= {'mediaType','digest','size'}
            out['root_annotations_present'] |= 'annotations' in desc
            out['root_platform_present'] |= 'platform' in desc
            kind = MEDIA.get(desc.get('mediaType')) if type(desc.get('mediaType')) is str else None
            if kind in ('oci_index','docker_index'):
                out['nested_index_roots'] += 1
                continue
            if kind not in ('oci_manifest','docker_manifest'):
                out['root_unknown_media_types'] += 1
                continue
            out['root_oci_manifests' if kind == 'oci_manifest' else 'root_docker_manifests'] += 1
            digest,size = desc.get('digest'), desc.get('size')
            if not (type(digest) is str and re.fullmatch('sha256:[0-9a-f]{64}',digest)
                    and type(size) is int and 0 < size <= 65536):
                out['root_missing_or_invalid_blobs'] += 1
                continue
            try:
                raw, manifest = read('blobs/sha256/'+digest[7:])
                require(len(raw)==size and hashlib.sha256(raw).hexdigest()==digest[7:])
            except (ValueError, KeyError, TypeError):
                out['root_missing_or_invalid_blobs'] += 1
                continue
            out['root_manifest_digest_size_matches'] += 1
            require(type(manifest) is dict)
            out['manifest_extra_fields_present'] |= bool(set(manifest)-{'schemaVersion','mediaType','config','layers'})
            out['manifest_schema_two'] &= type(manifest.get('schemaVersion')) is int and manifest['schemaVersion'] == 2
            config = manifest.get('config')
            layers = manifest.get('layers')
            require(type(config) is dict and type(layers) is list and len(layers) <= 256)
            require(out['layer_descriptor_count'] + len(layers) <= 1024)
            out['root_image_manifests'] += 1
            out['config_descriptor_standard_fields_only'] &= set(config) <= {'mediaType','digest','size'}
            media = config.get('mediaType')
            kind = 'oci' if media == 'application/vnd.oci.image.config.v1+json' else 'docker' if media == 'application/vnd.docker.container.image.v1+json' else 'unknown'
            out['config_media_counts'][kind] += 1
            identity = config.get('digest')
            if type(identity) is str and identity in ids.values():
                if identity in configs:
                    out['duplicate_role_configs'] += 1
                configs.add(identity)
            for layer in layers:
                require(type(layer) is dict)
                out['layer_descriptor_count'] += 1
                out['layer_descriptor_extra_fields_present'] |= bool(set(layer)-{'mediaType','digest','size'})
                media = layer.get('mediaType')
                kind = MEDIA.get(media) if type(media) is str else None
                out['layer_media_counts'][kind if kind in out['layer_media_counts'] else 'unknown'] += 1
        out['referenced_role_configs'] = len(configs)
        return out
