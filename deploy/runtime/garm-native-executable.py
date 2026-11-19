"""Disposable image metadata validation only; no production consumer or execution."""
import json
import pathlib
import re
import sys


def discover(items, reference):
    if not isinstance(items, list) or len(items) != 1:
        raise ValueError('one inspected image required')
    image = items[0]
    image_id = image['Id']
    if not re.fullmatch(r'sha256:[0-9a-f]{64}', image_id):
        raise ValueError('platform image identity required')
    if image['Os'] != 'linux' or image['Architecture'] not in ('amd64', 'arm64'):
        raise ValueError('unsupported platform')
    repository, digest = reference.split('@')
    if not re.fullmatch(r'sha256:[0-9a-f]{64}', digest):
        raise ValueError('immutable reference required')
    repository = repository.rsplit(':', 1)[0]
    if repository + '@' + digest not in image['RepoDigests']:
        raise ValueError('pulled reference mismatch')
    config = image['Config']
    entry = config.get('Entrypoint')
    if not isinstance(entry, list) or len(entry) != 3 or entry[1] != '-config':
        raise ValueError('direct native entrypoint required')
    for path in (entry[0], entry[2]):
        if not isinstance(path, str) or not re.fullmatch(r'/[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)*', path):
            raise ValueError('canonical absolute path required')
        if any(part in ('.', '..') for part in path.split('/')):
            raise ValueError('path traversal refused')
    if pathlib.PurePosixPath(entry[0]).name != 'garm':
        raise ValueError('not a direct GARM executable')
    if config.get('Cmd') or config.get('OnBuild') or config.get('Volumes'):
        raise ValueError('implicit execution or mounts refused')
    return {'image_id': image_id, 'executable': entry[0], 'reference': reference,
            'os': image['Os'], 'architecture': image['Architecture'],
            'environment': config.get('Env'), 'working_directory': config.get('WorkingDir'),
            'production_binding': False, 'deployment_authorized': False}


if __name__ == '__main__':
    with pathlib.Path(sys.argv[1]).open('rb') as stream:
        raw = stream.read(1024 * 1024 + 1)
    if len(raw) > 1024 * 1024:
        raise ValueError('metadata too large')
    print(json.dumps(discover(json.loads(raw), sys.argv[2])))
