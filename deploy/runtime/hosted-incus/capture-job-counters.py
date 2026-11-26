#!/usr/bin/env python3
"""Read real guest counters for a CI-only synthetic-job test."""
import datetime
import json
import os
import re
import subprocess
import sys
import time
import uuid
from pathlib import Path

NAME = re.compile(r"[a-z][a-z0-9-]{0,62}\Z")


def positive(value, field):
    if type(value) is not int or not 0 < value < 1 << 63:
        raise ValueError(f"invalid positive {field}")
    return value


def identity_uuid(value):
    parsed = uuid.UUID(value)
    if not parsed.int or str(parsed) != value:
        raise ValueError("invalid Incus incarnation UUID")
    return value


def process_ticks(pid):
    # Linux comm may contain spaces and right parentheses; field 22 is
    # the twentieth field following the final right parenthesis.
    stat = Path(f"/proc/{pid}/stat").read_text(encoding="utf-8")
    return positive(int(stat.rsplit(")", 1)[1].split()[19]), "process start ticks")


def incus(*arguments):
    return subprocess.run(
        ["sudo", "-n", "env", "-u", "INCUS_DIR", "-u", "INCUS_REMOTE",
         "incus", *arguments],
        check=True, capture_output=True, timeout=12,
    ).stdout


def query(path, project):
    # Incus 6 rejects the global --project flag for raw query. Keep the
    # project in the API URI and local socket selection explicit.
    return json.loads(incus("query", f"{path}?project={project}"))


def guest_identity(metadata, guest, project):
    if metadata["name"] != guest or metadata["type"] != "container" or metadata["status"] != "Running":
        raise ValueError("incorrect guest metadata")
    if metadata.get("project", project) != project:
        raise ValueError("incorrect guest project")
    config = metadata["config"]
    return identity_uuid(config["volatile.uuid"]), identity_uuid(config["volatile.uuid.generation"])


def capture(guest, project):
    instance = f"local:/1.0/instances/{guest}"
    identity = guest_identity(query(instance, project), guest, project)
    observed = datetime.datetime.now(datetime.timezone.utc)
    state = query(f"{instance}/state", project)
    if state["status"] != "Running" or state["status_code"] != 103:
        raise ValueError("guest not running")
    pid = positive(state["pid"], "guest PID")
    if not 1 < pid <= 2**31 - 1:
        raise ValueError("invalid guest PID")
    ticks = process_ticks(pid)
    confirmed = query(f"{instance}/state", project)
    if confirmed["pid"] != pid or confirmed["status"] != "Running" or process_ticks(pid) != ticks:
        raise ValueError("guest process changed during sample")
    if guest_identity(query(instance, project), guest, project) != identity:
        raise ValueError("guest incarnation changed during sample")
    btime = next(line.split()[1] for line in Path("/proc/stat").read_text().splitlines()
                 if line.startswith("btime "))
    tick_hz = positive(os.sysconf("SC_CLK_TCK"), "kernel tick rate")
    return {
        "project": project,
        "name": guest,
        "uuid": identity[0],
        "generation": identity[1],
        "boot_id": identity_uuid(Path("/proc/sys/kernel/random/boot_id").read_text().strip()),
        "pid": pid,
        "start_ticks": ticks,
        "started_at": datetime.datetime.fromtimestamp(
            int(btime) + ticks // tick_hz, datetime.timezone.utc).isoformat(),
        "observed_at": observed.isoformat(timespec="microseconds"),
        "cpu_ns": positive(state["cpu"]["usage"], "CPU counter"),
        "ram_bytes": positive(state["memory"]["usage"], "RAM gauge"),
    }


def main():
    if os.environ.get("GITHUB_ACTIONS") != "true" or os.environ.get("RUNNER_ENVIRONMENT") != "github-hosted":
        raise SystemExit("refusing a non-hosted runner")
    if len(sys.argv) != 4:
        raise SystemExit("usage: capture-job-counters.py GUEST PROJECT OUTPUT")
    guest, project, destination = sys.argv[1:]
    if not NAME.fullmatch(guest) or not guest.startswith("garm-ci-") or not NAME.fullmatch(project):
        raise SystemExit("invalid disposable guest/project name")
    work = Path(os.environ["RUNNER_TEMP"]).resolve(strict=True)
    output = Path(destination)
    if work not in output.resolve().parents:
        raise SystemExit("output must stay under runner temp")
    stage = "before-sample"
    try:
        before = capture(guest, project)
        # Run only this finite, known workload on the disposable guest.
        stage = "workload"
        incus("--project", project, "exec", f"local:{guest}", "--", "/bin/sh", "-ec",
              'i=0; while [ "$i" -lt 20000 ]; do i=$((i+1)); done')
        time.sleep(0.25)
        stage = "after-sample"
        after = capture(guest, project)
        stage = "private-output"
        fd = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as stream:
            json.dump([before, after], stream, separators=(",", ":"))
    except (IndexError, KeyError, TypeError, ValueError, OSError, StopIteration, subprocess.SubprocessError):
        # Report only the fixed stage, not stderr, metadata, paths, or argv.
        raise SystemExit(f"could not capture disposable guest counter evidence: {stage}") from None
    print("Captured private real Incus counters for synthetic job qualification")


if __name__ == "__main__":
    try:
        main()
    except (KeyError, ValueError, OSError, StopIteration, subprocess.SubprocessError):
        # Do not print guest metadata, private paths, or command output.
        raise SystemExit("could not capture complete disposable guest counter evidence") from None
