#!/usr/bin/env python3
"""Read-only hosted metadata observations. No runtime/kernel test or authority."""
import json
import os
from pathlib import Path, PurePosixPath
import re
import selectors
import shutil
import stat
import subprocess
import sys
import time

BOUNDARY = "read-only hosted metadata only; M2 kernel tests NOT EXECUTED; no isolation or action authority"
MAX_READ = 4096
TOOL_NAMES = ("docker", "podman", "qemu-system-x86_64", "bwrap", "unshare", "go")
# --version is CLI metadata ONLY. No daemon connection, namespace or guest.
PROBES = {name: ("version",) if name == "go" else ("--version",) for name in TOOL_NAMES}
TRUSTED_PREFIXES = ("/usr/bin/", "/usr/sbin/", "/usr/local/bin/", "/usr/local/go/", "/bin/", "/sbin/", "/opt/hostedtoolcache/")

class Refusal(Exception):
    pass


def read_text(path):
    try:
        with open(path, "rb") as stream:
            data = stream.read(MAX_READ + 1)
        if len(data) > MAX_READ:
            return None
        return data.decode("ascii")
    except (OSError, UnicodeError):
        return None


def clean_env(home):
    return {"PATH": "/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
            "HOME": str(home), "XDG_CONFIG_HOME": str(home), "DOCKER_CONFIG": str(home),
            "GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "LANG": "C"}


def bounded_probe(argv, environment, timeout=3):
    """Only the owned short-lived metadata child is stopped on overrun."""
    child = None
    try:
        child = subprocess.Popen(argv, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                 stderr=subprocess.DEVNULL, env=environment, close_fds=True)
        data = bytearray()
        deadline = time.monotonic() + timeout
        with selectors.DefaultSelector() as selector:
            selector.register(child.stdout, selectors.EVENT_READ)
            while True:
                remaining = deadline - time.monotonic()
                if remaining <= 0 or not selector.select(remaining):
                    return "metadata_timeout", None
                part = os.read(child.stdout.fileno(), min(1024, MAX_READ + 1 - len(data)))
                if not part:
                    break
                data.extend(part)
                if len(data) > MAX_READ:
                    return "metadata_too_large", None
        try:
            result = child.wait(timeout=max(0.001, deadline - time.monotonic()))
        except subprocess.TimeoutExpired:
            return "metadata_timeout", None
        if result != 0:
            return "metadata_unavailable", None
        return "observed", bytes(data)
    except OSError:
        return "metadata_unavailable", None
    finally:
        if child is not None:
            if child.poll() is None:
                child.kill()
            child.wait()
            if child.stdout is not None:
                child.stdout.close()


def safe_label(value):
    return value if isinstance(value, str) and re.fullmatch(r"[A-Za-z0-9._+:-]{1,100}", value) else None


class Reader:
    def os_release(self):
        value = read_text("/etc/os-release")
        if value is None:
            return {}
        return {key: value.strip().strip('"') for line in value.splitlines()
                for key, separator, value in [line.partition("=")] if separator and key in ("ID", "VERSION_ID")}

    def tools(self, home):
        result = {}
        environment = clean_env(home)
        for name in TOOL_NAMES:
            executable = shutil.which(name, path=environment["PATH"])
            if executable is None:
                result[name] = {"status": "not_present", "version": None}
                continue
            resolved = str(Path(executable).resolve())
            if not any(resolved.startswith(prefix) for prefix in TRUSTED_PREFIXES):
                result[name] = {"status": "untrusted_path_refused", "version": None}
                continue
            status, output = bounded_probe([resolved, *PROBES[name]], environment)
            version = None
            if status == "observed":
                try:
                    first = output.decode("ascii").splitlines()[0]
                    if re.fullmatch(r"[A-Za-z0-9 ._+,:()/-]{1,200}", first):
                        version = first
                    else:
                        status = "metadata_unrecognized"
                except (UnicodeError, IndexError):
                    status = "metadata_unrecognized"
            result[name] = {"status": status, "version": version}
        return result

    def kernel(self):
        status = read_text("/proc/self/status") or ""
        values = dict(line.split(":", 1) for line in status.splitlines() if ":" in line)
        caps = values.get("CapEff", "").strip()
        nnp = values.get("NoNewPrivs", "").strip()
        return {"release": safe_label(os.uname().release),
                "effective_caps_nonzero": int(caps, 16) != 0 if re.fullmatch(r"[0-9a-fA-F]{1,32}", caps) else None,
                "no_new_privs": int(nnp) if nnp in ("0", "1") else None,
                "unprivileged_userns_clone_sysctl": self._policy_value("/proc/sys/kernel/unprivileged_userns_clone"),
                "apparmor_restrict_unprivileged_userns_sysctl": self._policy_value("/proc/sys/kernel/apparmor_restrict_unprivileged_userns")}

    @staticmethod
    def _policy_value(path):
        data = read_text(path)
        return int(data.strip()) if data is not None and data.strip() in ("0", "1") else None

    def cgroup(self):
        root = Path("/sys/fs/cgroup")
        controllers = read_text(root / "cgroup.controllers")
        result = {"v2_file_present": controllers is not None, "required_controllers_reported": [],
                  "self_directory_writable": None, "self_procs_file_writable": None,
                  "self_subtree_control_writable": None, "delegation_verified": False}
        if controllers is not None:
            result["required_controllers_reported"] = sorted(set(controllers.split()) & {"cpu", "memory", "pids"})
        for line in (read_text("/proc/self/cgroup") or "").splitlines():
            if not line.startswith("0::/"):
                continue
            relative = line[3:]
            parts = PurePosixPath(relative).parts
            if not relative.startswith("/") or any(part in ("..", ".") for part in parts):
                continue
            target = root.joinpath(*parts[1:])
            # Permission observations only; no mkdir/write/controller enablement.
            result["self_directory_writable"] = os.access(target, os.W_OK)
            result["self_procs_file_writable"] = os.access(target / "cgroup.procs", os.W_OK)
            result["self_subtree_control_writable"] = os.access(target / "cgroup.subtree_control", os.W_OK)
            break
        return result

    def kvm(self):
        try:
            device = os.lstat("/dev/kvm")
            present = stat.S_ISCHR(device.st_mode)
        except OSError:
            present = False
        return {"character_device_present": present,
                "read_write_permission_reported": present and os.access("/dev/kvm", os.R_OK | os.W_OK),
                "device_opened": False, "vm_boot_tested": False}


def require_host(environment, reader):
    expected = {"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted", "RUNNER_OS": "Linux", "RUNNER_ARCH": "X64"}
    if any(environment.get(key) != value for key, value in expected.items()):
        raise Refusal("dedicated standard hosted Linux workflow required")
    if reader.os_release() != {"ID": "ubuntu", "VERSION_ID": "24.04"}:
        raise Refusal("reviewed Ubuntu24 profile required")


def survey(output, environment, reader=None):
    reader = reader or Reader()
    path = Path(output)
    parent = Path(environment.get("RUNNER_TEMP", ""))
    if not path.is_absolute() or not parent.is_absolute() or path != parent / "garm-m2-capability-survey":
        raise Refusal("fixed fresh output location required")
    # Includes dangling symlinks. Check before probes/reservation/output acknowledgment.
    if os.path.lexists(path):
        raise Refusal("new output directory required")
    require_host(environment, reader)
    acknowledgment = environment.get("GITHUB_OUTPUT")
    acknowledged_file = None
    if acknowledgment:
        target = Path(acknowledgment)
        if not target.is_absolute() or not target.is_relative_to(parent):
            raise Refusal("workflow output location refused")
        try:
            acknowledged_file = os.lstat(target)
        except OSError:
            raise Refusal("workflow output unavailable")
        if not stat.S_ISREG(acknowledged_file.st_mode) or acknowledged_file.st_uid != os.geteuid():
            raise Refusal("workflow output is not an owned regular file")
    path.mkdir(mode=0o700)  # no parents/exist_ok: races refuse, never overwrite
    home = path / "private-home"
    home.mkdir(mode=0o700)
    observation = {
        "scope": "read_only_github_hosted_capability_observations",
        "boundary": BOUNDARY, "m2_kernel_tests_executed": False,
        "isolation_qualified": False, "execution_authority": "none",
        "image_version": safe_label(environment.get("ImageVersion")),
        "observations": {"tools": reader.tools(home), "kernel": reader.kernel(),
                         "cgroup": reader.cgroup(), "kvm": reader.kvm()},
        "not_probed": ["host_sockets_or_daemons", "container_or_vm_execution", "native_clone_pidfd_or_cgroup_fence",
                       "actual_uid_mount_ipc_isolation", "service_or_installed_runner_qualification"],
        "missing_prerequisites": ["separately_approved_disposable_runtime_execution_scope",
                                  "verified_native_creation_and_accepted_request_frontier",
                                  "independent_anchor_and_enrolled_privilege_boundary"],
    }
    encoded = json.dumps(observation, sort_keys=True, indent=2) + "\n"
    if len(encoded.encode()) > 16384:
        raise Refusal("bounded observation required")
    # The only shareable artifact is this whitelist, never home/tool configuration.
    with open(path / "survey.json", "x", encoding="utf-8") as stream:
        stream.write(encoded)
    if acknowledgment:
        descriptor = os.open(acknowledgment, os.O_WRONLY | os.O_APPEND | os.O_NOFOLLOW)
        with os.fdopen(descriptor, "a", encoding="utf-8") as stream:
            if not os.path.samestat(acknowledged_file, os.fstat(stream.fileno())):
                raise Refusal("workflow output identity changed")
            stream.write("results_created=true\n")
    return observation


def main():
    if len(sys.argv) != 2:
        print("fixed fresh output directory required", file=sys.stderr)
        return 2
    try:
        observation = survey(sys.argv[1], os.environ)
    except (Refusal, OSError):
        print("Read-only survey unavailable; no kernel test or qualification.", file=sys.stderr)
        return 2
    print("M2_READ_ONLY_OBSERVATIONS=" + json.dumps(observation, sort_keys=True))
    print("READ-ONLY SURVEY COMPLETE. M2 kernel tests NOT EXECUTED; no isolation or action authority.")
    return 0

if __name__ == "__main__":
    sys.exit(main())
