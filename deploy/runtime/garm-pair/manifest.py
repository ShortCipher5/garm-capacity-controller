#!/usr/bin/env python3
"""Public build evidence only. Unsigned, not a deployment or SLSA attestation."""
import hashlib
import json
import re
from pathlib import Path
import subprocess
import sys

GARM = '154638445c3949c1958b01812f69d9a1e4d82684'
PATCH = '87e0d61751d4b9ffa15322cff09557f0a1094fdd68464fe840a12ffe01bb0660'
FRONTEND = 'docker/dockerfile:1'
BASES = {'node:22.23.4-bookworm-slim', 'golang:1.26.10-bookworm', 'debian:bookworm-slim'}


def sha(path): return hashlib.sha256(Path(path).read_bytes()).hexdigest()
def canonical(value): return json.dumps(value, sort_keys=True, separators=(',', ':')).encode()
def version(inputs, images): return 'experimental-pair-' + hashlib.sha256(canonical({'inputs': inputs, 'images': images})).hexdigest()[:24]


def lock(inputs, outputs, record):
    sources = [Path(p).read_text() for p in inputs]
    external = set()
    for text in sources:
        assert text.splitlines()[0] == '# syntax=' + FRONTEND, 'unreviewed Dockerfile frontend'
        assert sum(bool(re.match(r'(?i)^\s*#\s*syntax=', line)) for line in text.splitlines()) == 1
        aliases = set()
        for line in text.splitlines():
            if not re.match(r'(?i)^\s*from(?:\s|$)', line): continue
            match = re.fullmatch(r'FROM (\S+) AS ([a-z0-9-]+)', line)
            assert match, 'unsupported FROM grammar'
            image, alias = match.groups()
            assert alias not in aliases
            if image not in aliases and image != 'scratch':
                assert image in BASES, 'unreviewed base'
                external.add(image)
            aliases.add(alias)
    assert external == BASES
    locks = {}
    for name in sorted(external | {FRONTEND}):
        subprocess.run(['docker', 'pull', name], check=True)
        refs = json.loads(subprocess.check_output(['docker', 'image', 'inspect', '--format', '{{json .RepoDigests}}', name]))
        assert refs and re.fullmatch(r'[^\s@]+@sha256:[0-9a-f]{64}', refs[0])
        assert refs[0].split('@')[0].removeprefix('docker.io/').removeprefix('library/') == name.split(':')[0]
        locks[name] = refs[0]
    for text, output in zip(sources, outputs):
        for name, pinned in locks.items(): text = text.replace('FROM ' + name + ' AS ', 'FROM ' + pinned + ' AS ')
        text = text.replace('# syntax=' + FRONTEND, '# syntax=' + locks[FRONTEND])
        assert text.splitlines()[0] == '# syntax=' + locks[FRONTEND]
        # Every external FROM must now contain a digest; no silent unpinned stage.
        for line in text.splitlines():
            if line.startswith('FROM '): assert not any('FROM ' + name + ' AS ' in line for name in BASES)
        Path(output).write_text(text)
    Path(record).write_text(json.dumps({'bases': {k: locks[k] for k in BASES}, 'frontend': locks[FRONTEND]}, sort_keys=True))


def validate(record):
    assert set(record) == {'schema', 'bundle_version', 'inputs', 'images', 'qualification'}
    assert record['schema'] == 'experimental-garm-pair/v1'
    values = record['inputs']
    assert set(values) == {'controller_commit', 'controller_tree', 'garm_commit', 'garm_patch_sha256', 'bases', 'build_frontend', 'dependency_files', 'dockerfiles', 'binaries', 'platform', 'toolchain', 'runtime_libc'}
    assert values['garm_commit'] == GARM and values['garm_patch_sha256'] == PATCH
    for name in ('controller_commit', 'controller_tree'): assert re.fullmatch('[0-9a-f]{40}', values[name])
    assert set(values['bases']) == BASES
    for base, digest in values['bases'].items():
        assert re.fullmatch(r'[^\s@]+@sha256:[0-9a-f]{64}', digest)
        assert digest.split('@')[0].removeprefix('docker.io/library/') == base.split(':')[0]
    assert re.fullmatch(r'(?:docker.io/)?docker/dockerfile@sha256:[0-9a-f]{64}', values['build_frontend'])
    for hashes in ('dependency_files', 'dockerfiles', 'binaries'):
        assert values[hashes] and all(re.fullmatch('[0-9a-f]{64}', v) for v in values[hashes].values())
    assert set(values['dependency_files']) == {'controller_go_mod', 'controller_go_sum', 'web_lock', 'garm_go_mod', 'garm_go_sum', 'garm_vendor_modules'}
    assert set(values['dockerfiles']) == {'controller', 'garm'}
    assert set(values['binaries']) == {'garm', 'capacity-provider', 'capacity-gateway', 'capacity-ledger', 'pair-probe'}
    assert values['platform'] == 'linux/amd64' and values['toolchain'] == 'go1.26.10'
    assert values['runtime_libc'] == 'glibc 2.36'
    expected = version(values, record['images'])
    assert record['bundle_version'] == expected
    assert set(record['images']) == {'paired_garm', 'gateway', 'ledger_tool'}
    assert all(re.fullmatch('sha256:[0-9a-f]{64}', v) for v in record['images'].values())
    assert record['qualification'] == {'public_build_evidence': True, 'signed_release': False, 'installed_stack_qualified': False, 'deployment_authorized': False}


def assemble(work, images, output):
    work = Path(work)
    read = lambda *args: subprocess.check_output(args, text=True).strip()
    inspect = lambda image, field: read('docker', 'image', 'inspect', '--format', field, image)
    paired, gateway, ledger = images
    args = ['docker', 'run', '--rm', '--network', 'none', '--read-only', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges', '--pids-limit', '64', '--memory', '128m', '--cpus', '1', '--entrypoint', '/bin/sh']
    def binary_hash(image, path):
        value = read(*args, image, '-ec', 'ldd '+path+' >/dev/null; sha256sum '+path).split()[0]
        assert re.fullmatch('[0-9a-f]{64}', value)
        return value
    binaries = {'garm': binary_hash(paired, '/usr/local/bin/garm'), 'capacity-provider': binary_hash(paired, '/usr/local/bin/capacity-provider'), 'capacity-gateway': binary_hash(gateway, '/usr/local/bin/capacity-gateway'), 'capacity-ledger': binary_hash(ledger, '/usr/local/bin/capacity-ledger'), 'pair-probe': sha(work / 'probe' / 'pair-probe')}
    assert binaries['capacity-provider'] == sha(work / 'provider' / 'capacity-provider')
    values = {'controller_commit': read('git', 'rev-parse', 'HEAD'), 'controller_tree': read('git', 'rev-parse', 'HEAD^{tree}'), 'garm_commit': GARM, 'garm_patch_sha256': PATCH, 'bases': json.loads((work / 'bases.json').read_text())['bases'], 'build_frontend': json.loads((work / 'bases.json').read_text())['frontend'], 'dependency_files': {name: sha(path) for name, path in {'controller_go_mod': 'go.mod', 'controller_go_sum': 'go.sum', 'web_lock': 'web/package-lock.json', 'garm_go_mod': work / 'build/source/go.mod', 'garm_go_sum': work / 'build/source/go.sum', 'garm_vendor_modules': work / 'build/source/vendor/modules.txt'}.items()}, 'dockerfiles': {'controller': sha(work / 'controller.Dockerfile'), 'garm': sha(work / 'build/Dockerfile')}, 'binaries': binaries, 'platform': inspect(paired, '{{.Os}}/{{.Architecture}}'), 'toolchain': read('docker', 'run', '--rm', '--network', 'none', '--read-only', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges', '--pids-limit', '64', '--memory', '128m', '--cpus', '1', '--mount', f'type=bind,src={work}/probe/pair-probe,dst=/pair-probe,readonly', '--entrypoint', '/pair-probe', paired, 'version'), 'runtime_libc': read(*args, paired, '-ec', 'getconf GNU_LIBC_VERSION')}
    assert all(inspect(i, '{{.Os}}/{{.Architecture}}') == values['platform'] for i in images)
    record = {'schema': 'experimental-garm-pair/v1', 'bundle_version': '', 'inputs': values, 'images': {k: inspect(v, '{{.Id}}') for k, v in zip(('paired_garm', 'gateway', 'ledger_tool'), images)}, 'qualification': {'public_build_evidence': True, 'signed_release': False, 'installed_stack_qualified': False, 'deployment_authorized': False}}
    record['bundle_version'] = version(values, record['images'])
    validate(record)
    Path(output).write_text(json.dumps(record, indent=2))


if __name__ == '__main__':
    try:
        if sys.argv[1] == 'lock': lock(['Dockerfile', 'deploy/runtime/garm-pair/Dockerfile'], sys.argv[2:4], sys.argv[4])
        elif sys.argv[1] == 'assemble': assemble(sys.argv[2], sys.argv[3:6], sys.argv[6])
        elif sys.argv[1] == 'validate': validate(json.loads(Path(sys.argv[2]).read_text()))
        else: raise ValueError('unknown public artifact operation')
    except Exception as error:
        print('Public artifact operation failed:', type(error).__name__, file=sys.stderr)
        raise SystemExit(1)
