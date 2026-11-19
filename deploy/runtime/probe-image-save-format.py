"""Explicit hosted-only archive format measurement. No archive delivery or import."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile

spec = importlib.util.spec_from_file_location('pack', Path(__file__).with_name('pack-pilot-images.py'))
pack = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pack)
structure_spec = importlib.util.spec_from_file_location('structure', Path(__file__).with_name('image-save-structure.py'))
structure = importlib.util.module_from_spec(structure_spec)
structure_spec.loader.exec_module(structure)
metadata_spec = importlib.util.spec_from_file_location('metadata', Path(__file__).with_name('docker-exporter-metadata.py'))
metadata = importlib.util.module_from_spec(metadata_spec)
metadata_spec.loader.exec_module(metadata)
ancillary_spec = importlib.util.spec_from_file_location('ancillary', Path(__file__).with_name('image-save-ancillary.py'))
ancillary = importlib.util.module_from_spec(ancillary_spec)
ancillary_spec.loader.exec_module(ancillary)


def layout(path):
    # Inspect headers only. Never extract or emit member names/content.
    with tarfile.open(path, 'r:') as archive:
        names = set()
        files = directories = 0
        for member in archive:
            pack.require(len(names) < 100000 and member.name not in names)
            pack.require(not member.name.startswith('/') and '..' not in member.name.split('/'))
            pack.require(member.isfile() or member.isdir())
            pack.require(0 <= member.size <= pack.MAX_ARCHIVE)
            names.add(member.name)
            files += int(member.isfile())
            directories += int(member.isdir())
        docker = 'manifest.json' in names
        oci = 'index.json' in names and 'oci-layout' in names
        kind = 'docker-and-oci' if docker and oci else 'docker' if docker else 'oci' if oci else 'unknown'
        return kind, files, directories


def probe(raw, parent, expected_commit, run_id, run_attempt, save=pack.save_images, sample_roles=False, role_save=pack.save_images):
    record, ids = pack.evidence(raw)
    pack.require((record['source_commit'], record['run_id'], record['run_attempt']) ==
                 (expected_commit, run_id, run_attempt))
    parent = Path(parent)
    pack.require(parent.is_absolute() and parent.resolve() == parent)
    st = parent.stat()
    pack.require(st.st_uid == os.geteuid() and st.st_mode & 0o022 == 0)
    # This directory is ours alone. No fixed output path or adoption of old files.
    with tempfile.TemporaryDirectory(prefix='garm-save-format-', dir=parent) as temporary:
        archive = Path(temporary) / 'images.tar'
        save(archive, ids)
        pack.require(archive.is_file() and not archive.is_symlink())
        size = archive.stat().st_size
        pack.require(0 < size <= pack.MAX_ARCHIVE)
        kind, files, directories = layout(archive)
        digest = hashlib.sha256()
        with archive.open('rb') as stream:
            for block in iter(lambda: stream.read(1024*1024), b''):
                digest.update(block)
        # No broadening for a new layout: unsupported modern metadata is measured,
        # never silently accepted as an identity-qualified archive.
        valid = False
        try:
            pack.inspect_archive(archive, ids)
            valid = True
        except (ValueError, KeyError, TypeError, OSError, tarfile.TarError):
            pass
        result = {'schema': 'image-save-format-probe/v2', 'layout': kind,
                  'files': files, 'directories': directories, 'archive_bytes': size,
                  'archive_sha256': digest.hexdigest(),
                  'validator_result': 'accepted' if valid else 'refused',
                  'archive_identity_verified': valid, 'deployment_authorized': False}
        if kind == 'docker-and-oci':
            result['structure'] = structure.describe(archive, ids)
    # Each optional archive is deleted before the next. The existing outer
    # six-minute command budget covers all samples, with one aggregate byte cap.
    if sample_roles and kind == 'docker-and-oci' and result['structure']['referenced_role_configs'] != len(ids):
        remaining = pack.MAX_ARCHIVE - size
        samples = {}
        for role, identity in ids.items():
            if remaining <= 0:
                samples[role] = {'status': 'budget-exhausted'}
                continue
            with tempfile.TemporaryDirectory(prefix='garm-save-format-', dir=parent) as temporary:
                archive = Path(temporary) / 'image.tar'
                role_save(archive, {role: identity}, max_bytes=remaining)
                pack.require(archive.is_file() and not archive.is_symlink())
                sample_size = archive.stat().st_size
                pack.require(0 < sample_size <= remaining)
                remaining -= sample_size
                sample_kind, sample_files, sample_dirs = layout(archive)
                summary = {'status': 'measured', 'layout': sample_kind,
                           'files': sample_files, 'directories': sample_dirs}
                if sample_kind == 'docker-and-oci':
                    summary['structure'] = structure.describe(archive, {role: identity})
                    summary['ancillary'] = ancillary.describe(archive, {role: identity})
            samples[role] = summary
        result['per_role_structure'] = samples
    # No result is returned/printed unless private temporary archive removal succeeds.
    return result


def main():
    pack.require(os.environ.get('GITHUB_ACTIONS') == 'true'
                 and os.environ.get('RUNNER_ENVIRONMENT') == 'github-hosted'
                 and os.environ.get('GITHUB_EVENT_NAME') == 'workflow_dispatch'
                 and os.environ.get('GARM_ARCHIVE_FORMAT_PROBE') == 'true')
    commit = subprocess.check_output(['git', 'rev-parse', 'HEAD'], text=True, timeout=5).strip()
    with Path('qualification-results/garm-three-service-preboot.json').open('rb') as stream:
        raw = stream.read(16385)
    result = probe(raw, os.environ['RUNNER_TEMP'], commit,
                   os.environ['GITHUB_RUN_ID'], os.environ['GITHUB_RUN_ATTEMPT'], sample_roles=True)
    result['exporter_metadata'] = metadata.collect()
    # Closed metadata only; never write into uploaded qualification-results.
    print(json.dumps(result, sort_keys=True, separators=(',', ':')))


if __name__ == '__main__':
    try:
        main()
    except Exception:
        raise SystemExit('Archive format probe unavailable; no archive qualification or delivery')
