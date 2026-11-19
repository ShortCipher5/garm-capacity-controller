"""Closed nullable hosted daemon metadata. Never dumps docker info or errors."""
import json
import re
import subprocess
import threading

VERSION_FORMAT = '{"version":{{json .Server.Version}},"api":{{json .Server.APIVersion}},"git":{{json .Server.GitCommit}},"os":{{json .Server.Os}},"arch":{{json .Server.Arch}}}'
INFO_FORMAT = '{"driver":{{json .Driver}},"driver_types":[""{{range .DriverStatus}}{{if eq (index . 0) "driver-type"}},{{json (index . 1)}}{{end}}{{end}}]}'


def query(kind, template):
    proc = subprocess.Popen(['docker', '--host', 'unix:///var/run/docker.sock',
                             kind, '--format', template],
                            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    def stop():
        if proc.poll() is None:
            try:
                proc.kill()
            except ProcessLookupError:
                pass
    timer = threading.Timer(5, stop)
    timer.start()
    try:
        raw = proc.stdout.read(4097)
        if len(raw) > 4096:
            return None
        if proc.wait(timeout=1) != 0:
            return None
        return json.loads(raw)
    finally:
        timer.cancel()
        stop()
        proc.wait()
        proc.stdout.close()


def collect(reader=query):
    value = {k:None for k in ('server_version','api_version','engine_git_commit',
                             'platform','storage_driver','image_store')}
    try:
        v = reader('version', VERSION_FORMAT)
        if type(v) is dict and set(v) == {'version','api','git','os','arch'}:
            for source, dest, pattern in (
                ('version','server_version',r'[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}[A-Za-z0-9.+-]{0,32}'),
                ('api','api_version',r'[0-9]{1,3}\.[0-9]{1,3}'),
                ('git','engine_git_commit',r'[0-9a-f]{7,40}')):
                if type(v[source]) is str and re.fullmatch(pattern,v[source]):
                    value[dest] = v[source]
            if (v['os'], v['arch']) == ('linux','amd64'):
                value['platform'] = 'linux/amd64'
    except (OSError, ValueError, subprocess.SubprocessError):
        pass
    try:
        v = reader('info', INFO_FORMAT)
        if type(v) is dict and set(v) == {'driver','driver_types'}:
            if type(v['driver']) is str and v['driver'] in ('overlay2','overlayfs','btrfs','zfs','vfs','devicemapper','fuse-overlayfs'):
                value['storage_driver'] = v['driver']
            if v['driver_types'] == ['', 'io.containerd.snapshotter.v1']:
                value['image_store'] = 'containerd-snapshotter'
            # Absence does not prove classic exporter implementation.
    except (OSError, ValueError, subprocess.SubprocessError):
        pass
    return value
