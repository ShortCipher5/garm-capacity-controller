"""Fixed Debian 13 Incus onboarding recipe. This module is never imported from the host."""
import fcntl
import http.client
import json
import os
import re
import socket
import subprocess
import sys
import tempfile

SOCKET = "/var/lib/incus/unix.socket"
BASE = "/var/lib/garm-capacity-incus-onboarding"
ENV = {"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "LANG": "C", "LC_ALL": "C",
       "DEBIAN_FRONTEND": "noninteractive", "HOME": "/root"}
LIMIT = 1 << 20
STATES = {"missing", "pristine", "configured", "manual_review", "unsupported"}


class Unknown(Exception):
    pass


class UnixHTTP(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(5)
        self.sock.connect(SOCKET)


def api(path):
    conn = UnixHTTP("localhost")
    try:
        conn.request("GET", "/1.0" + path)
        resp = conn.getresponse()
        raw = resp.read(LIMIT + 1)
        if resp.status != 200 or len(raw) > LIMIT:
            raise Unknown()
        doc = json.loads(raw)
        if doc.get("type") != "sync" or doc.get("status_code") != 200:
            raise Unknown()
        return doc["metadata"]
    finally:
        conn.close()


def classify(server, pools, profile, fetch):
    # Existing non-empty installations are never reinitialized. Only classify as
    # locally configured when the default profile resolves a usable root pool.
    if server.get("auth") != "trusted" or server.get("environment", {}).get("server") != "incus":
        raise Unknown()
    version = server["environment"]["server_version"]
    if not re.fullmatch(r"[0-9]+\.[0-9]+(?:\.[0-9]+)?(?:[-+][A-Za-z0-9.+~-]+)?", version):
        raise Unknown()
    if version.split(".")[0] not in ("6", "7"):
        return "unsupported", version
    if not isinstance(pools, list) or not isinstance(profile.get("devices"), dict):
        raise Unknown()
    roots = [v.get("pool") for v in profile["devices"].values()
             if v.get("type") == "disk" and v.get("path") == "/"]
    if len(roots) == 1 and any(isinstance(p, dict) and p.get("name") == roots[0]
         and p.get("status") == "Created" and p.get("driver") for p in pools):
        return "configured", version
    if (pools or profile["devices"] or profile.get("config") != {} or
            profile.get("name") != "default" or server.get("config") != {} or
            server["environment"].get("server_clustered") is not False):
        return "manual_review", version
    if (fetch("/projects") != ["/1.0/projects/default"] or
            fetch("/profiles") != ["/1.0/profiles/default"] or
            fetch("/instances?all-projects=true") != [] or fetch("/images") != [] or
            fetch("/certificates") != []):
        return "manual_review", version
    networks = fetch("/networks?recursion=1")
    if not isinstance(networks, list) or any(n.get("managed") is not False for n in networks):
        return "manual_review", version
    return "pristine", version


def os_supported():
    import platform
    rel = platform.freedesktop_os_release()
    return rel.get("ID") == "debian" and rel.get("VERSION_ID") == "13" and os.path.isdir("/run/systemd/system")


def distro_origin(origin):
    stable = origin.origin == "Debian" and origin.codename in (
        "trixie", "trixie-updates", "trixie-security")
    backports = origin.origin == "Debian Backports" and origin.codename == "trixie-backports"
    return origin.trusted and (stable or backports)


def candidate():
    # python3-apt uses the existing, signed distro cache. Never add keys, edit
    # apt sources, refresh a suite, or upgrade a working daemon here.
    try:
        import apt
        cache = apt.Cache()
        pkg = cache["incus"]
        c = pkg.candidate
        if not c or not re.fullmatch(r"[0-9][0-9a-zA-Z.+:~-]{0,79}", c.version):
            return ""
        if c.version.split(":")[-1].split(".")[0] not in ("6", "7"):
            return ""
        if not c.origins or not all(distro_origin(o) for o in c.origins):
            return ""
        pkg.mark_install(auto_fix=False, auto_inst=True)
        if cache.broken_count or any(p.is_installed for p in cache.get_changes()):
            return ""
        return c.version
    except (ImportError, KeyError, SystemError):
        return ""


def inspect():
    if not os_supported():
        return "unsupported", ""
    if os.path.exists(SOCKET):
        return classify(api(""), api("/storage-pools?recursion=1"), api("/profiles/default"), api)
    # An unavailable daemon, live LXD or retained database is not "not installed".
    paths = ("/var/lib/incus", "/var/lib/lxd", "/var/snap/lxd", "/usr/bin/incus",
             "/usr/local/bin/incus", "/opt/incus", "/usr/libexec/incus")
    if any(os.path.lexists(p) for p in paths):
        return "manual_review", ""
    for pkg in ("incus", "incus-base", "incus-client", "lxd"):
        p = subprocess.run(["/usr/bin/dpkg-query", "-W", "-f=${db:Status-Abbrev}", pkg],
                           env=ENV, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                           stderr=subprocess.DEVNULL, timeout=10, check=False)
        if p.returncode == 0:
            return "manual_review", ""
        if p.returncode != 1:
            raise Unknown()
    version = candidate()
    return ("missing", version) if version else ("manual_review", "")


def run(argv, env=None):
    subprocess.run(argv, env=env or ENV, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                   stderr=subprocess.DEVNULL, timeout=240, check=True)


def install(version):
    # No added repos/keys, unattended system upgrade, forced removal, or downloaded script.
    run(["/usr/bin/apt-get", "-y", "--no-remove", "--no-install-recommends",
         "-o", "APT::Get::AllowUnauthenticated=false", "-o", "Acquire::AllowInsecureRepositories=false",
         "--no-upgrade", "install", "incus="+version])


def initialize():
    # Do not inherit a user's default remote or writable CLI configuration.
    with tempfile.TemporaryDirectory(prefix="incus-onboarding-") as d:
        run(["/usr/bin/incus", "admin", "init", "--minimal"], dict(ENV, HOME=d, INCUS_CONF=d))


def transition(expected, observe, install_pkg, init, before_write):
    state, version = observe()
    if (state, version) != expected:
        return "changed", state, version
    if state == "configured":
        return "preserved", state, version
    if state not in ("missing", "pristine"):
        return "refused", state, version
    before_write()
    if state == "missing":
        install_pkg(version)
        state, version = observe()
    if state == "pristine":
        init()
        state, version = observe()
    if state != "configured":
        raise Unknown()
    return "verified", state, version


def apply(expected, digest):
    # A fixed, root-only file records unknown BEFORE touching APT/Incus. A lost
    # response is not permission to repeat an install or delete partial state.
    try:
        os.mkdir(BASE, 0o700)
    except FileExistsError:
        pass
    d = os.open(BASE, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        st = os.fstat(d)
        if st.st_uid or st.st_mode & 0o077:
            raise Unknown()
        f = os.open("operation", os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600, dir_fd=d)
        try:
            import stat
            s = os.fstat(f)
            if not stat.S_ISREG(s.st_mode) or s.st_uid or s.st_mode & 0o077 or s.st_nlink != 1:
                raise Unknown()
            fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
            history = os.read(f, 65537)
            if len(history) > 64000:
                raise Unknown()
            # Unknown/partial intent requires operator examination. Inspection
            # remains available even if this file is damaged.
            if history and (not history.endswith(b"\n") or not history.splitlines()[-1].startswith(b"verified ")):
                raise Unknown()
            def record(tag):
                raw = (tag + " " + digest + "\n").encode("ascii")
                if os.write(f, raw) != len(raw):
                    raise Unknown()
                os.fsync(f)
                os.fsync(d)
                parent = os.open("/var/lib", os.O_RDONLY | os.O_DIRECTORY)
                try:
                    os.fsync(parent)
                finally:
                    os.close(parent)
            result = transition(expected, inspect, install, initialize, lambda: record("unknown"))
            if result[0] == "verified":
                record("verified")
            return result
        finally:
            os.close(f)
    finally:
        os.close(d)


def main(args):
    if os.geteuid() != 0 or len(args) != 4:
        raise Unknown()
    mode, expected, expected_version, nonce = args
    if mode not in ("inspect", "apply") or not re.fullmatch("[a-f0-9]{64}", nonce):
        raise Unknown()
    if mode == "inspect":
        result = ("observed", *inspect())
    else:
        if expected not in ("missing", "pristine", "configured") or not re.fullmatch(r"[0-9a-zA-Z.+:~-]{0,80}", expected_version):
            raise Unknown()
        result = apply((expected, expected_version), nonce)
    reply = dict(zip(("outcome", "state", "server_version"), result))
    if result[1] == "configured":
        env = api("")["environment"]
        fingerprint = env.get("certificate_fingerprint", "")
        if not re.fullmatch(r"[0-9a-f]{64}", fingerprint) or env.get("server_version") != result[2]:
            raise Unknown()
        reply["server_certificate_sha256"] = fingerprint
    print(json.dumps(reply | {"nonce": nonce, "placement_eligible": False}))


if __name__ == "__main__":
    try:
        main(json.loads(sys.stdin.buffer.read(1024)))
    except Exception:
        sys.exit("Onboarding outcome unknown; inspect the target before acting.")
