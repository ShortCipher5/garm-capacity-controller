"""Check a real Incus state sample without publishing a PID or instance name."""
import json
import sys
from pathlib import Path


def sample(path):
    with Path(path).open(encoding="utf-8") as stream:
        value = json.load(stream)
    if not isinstance(value, dict):
        raise ValueError("expected an Incus state object")
    state = value.get("metadata", value)
    if state["status"] != "Running":
        raise ValueError("disposable Incus guest is not running")
    pid, cpu, memory = state["pid"], state["cpu"]["usage"], state["memory"]["usage"]
    if any(type(n) is not int or n <= 0 for n in (pid, cpu, memory)):
        raise ValueError("missing or nonpositive real guest CPU/memory counter")
    return pid, cpu


if __name__ == "__main__":
    if len(sys.argv) not in (2, 3):
        raise SystemExit("usage: check-state.py BEFORE [AFTER]")
    before = sample(sys.argv[1])
    if len(sys.argv) == 3:
        after = sample(sys.argv[2])
        if before[0] != after[0] or before[1] > after[1]:
            raise SystemExit("disposable guest restarted or CPU counter reset during smoke")
    print("PASS Incus guest CPU and memory counters observed")
