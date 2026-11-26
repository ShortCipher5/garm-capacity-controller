"""Observe one reviewed recovery case; this program cannot create capacity or jobs."""
import argparse
import hashlib
import json
import os
import re
import stat
import sys
from datetime import datetime, timedelta, timezone
from urllib.error import URLError
from urllib.request import HTTPRedirectHandler, ProxyHandler, Request, build_opener

API = "https://api.github.com"
BOUND = 2 * 1024 * 1024
MAX_ID = 2**63 - 1
POLICY_KEYS = {
    "schema", "reviewed_sha", "repo", "repo_id", "run_id", "attempt", "job_id",
    "run_head_sha", "scope_digest", "receipt_sha256", "declared_at", "window_seconds",
    "expected_runner_id",
}
RECEIPT_KEYS = {
    "schema", "repo_id", "run_id", "attempt", "job_id", "scope_digest",
    "queued_at", "released_at", "blocked_reservation_digest", "retired_reservation_digest",
    "expected_runner_id",
}


class Invalid(ValueError):
    """Stable diagnostics without printing inputs, credentials or remote content."""


def require(ok, message):
    if not ok:
        raise Invalid(message)


def digest(value, length):
    return isinstance(value, str) and re.fullmatch(rf"[0-9a-f]{{{length}}}", value) is not None


def positive_id(value):
    return type(value) is int and 0 < value <= MAX_ID


def parse_time(value):
    require(isinstance(value, str) and re.fullmatch(
        r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,6})?Z", value
    ), "invalid UTC timestamp")
    try:
        return datetime.fromisoformat(value[:-1] + "+00:00")
    except ValueError:
        raise Invalid("invalid UTC timestamp") from None


def decode(raw):
    require(len(raw) <= BOUND, "document too large")

    def unique(pairs):
        result = {}
        for key, value in pairs:
            require(key not in result, "duplicate JSON key")
            result[key] = value
        return result

    def invalid_constant(_):
        raise Invalid("invalid JSON constant")

    try:
        value = json.loads(raw, object_pairs_hook=unique, parse_constant=invalid_constant)
    except (ValueError, RecursionError):
        raise Invalid("invalid JSON document") from None
    require(isinstance(value, dict), "expected JSON object")
    return value


def load_file(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, "rb") as file:
        require(stat.S_ISREG(os.fstat(file.fileno()).st_mode), "expected regular evidence file")
        return file.read(BOUND + 1)


def validate_evidence(policy, receipt_raw, now):
    require(set(policy) == POLICY_KEYS and policy["schema"] == "recovery-policy-v1",
            "invalid reviewed policy")
    require(isinstance(policy["repo"], str) and re.fullmatch(
        r"[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_][A-Za-z0-9_.-]{0,99}", policy["repo"]
    ), "invalid repository allowlist entry")
    require(all(positive_id(policy[k]) for k in ("repo_id", "run_id", "attempt", "job_id")),
            "missing exact numeric job identity")
    require(all(digest(policy[k], 40) for k in ("reviewed_sha", "run_head_sha")) and
            all(digest(policy[k], 64) for k in ("scope_digest", "receipt_sha256")),
            "missing reviewed revision, scope or receipt digest")
    require(positive_id(policy["expected_runner_id"]), "missing expected runner registration ID")
    limit = policy["window_seconds"]
    require(type(limit) is int and 1 <= limit <= 300, "observation window must be 1–300 seconds")
    require(len(receipt_raw) <= BOUND and hashlib.sha256(receipt_raw).hexdigest() == policy["receipt_sha256"],
            "independently reviewed receipt digest mismatch")
    receipt = decode(receipt_raw)
    require(set(receipt) == RECEIPT_KEYS and receipt["schema"] == "capacity-retirement-observation-v1",
            "invalid retirement receipt")
    for key in ("repo_id", "run_id", "attempt", "job_id"):
        require(positive_id(receipt[key]) and receipt[key] == policy[key],
                "retirement receipt not scoped to exact waiting job")
    require(receipt["scope_digest"] == policy["scope_digest"],
            "retirement receipt not scoped to declared shared budget")
    require(positive_id(receipt["expected_runner_id"]) and
            receipt["expected_runner_id"] == policy["expected_runner_id"],
            "reviewed receipt not scoped to expected replacement registration")
    blocked, retired = receipt["blocked_reservation_digest"], receipt["retired_reservation_digest"]
    require(digest(blocked, 64) and digest(retired, 64) and blocked != retired,
            "waiting and retired reservations must be distinct and identified")
    declared, queued, released = map(parse_time, (
        policy["declared_at"], receipt["queued_at"], receipt["released_at"]
    ))
    try:
        deadline = released + timedelta(seconds=limit)
    except OverflowError:
        raise Invalid("invalid retirement deadline") from None
    require(declared < queued < released <= now, "invalid declaration, queue, or retirement ordering")
    return released, deadline


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def fetch(token, path):
    # Never forward credentials through redirects or accept caller-selected hosts.
    request = Request(API + path, method="GET", headers={
        "Authorization": "Bearer " + token,
        "Accept": "application/vnd.github+json",
        "User-Agent": "garm-capacity-recovery-observer",
        "X-GitHub-Api-Version": "2022-11-28",
    })
    try:
        with build_opener(ProxyHandler({}), NoRedirect()).open(request, timeout=10) as response:
            require(response.status == 200, "GitHub observation unavailable")
            require(response.headers.get("Link") is None, "unbounded GitHub observation")
            return decode(response.read(BOUND + 1))
    except (URLError, OSError):
        raise Invalid("GitHub observation unavailable") from None


def observe(policy, receipt_raw, now, get):
    released, deadline = validate_evidence(policy, receipt_raw, now)
    repo = policy["repo"]
    stem = f"/repos/{repo}/actions/runs/{policy['run_id']}/attempts/{policy['attempt']}"
    run = get(stem)
    for key in ("repository", "head_repository"):
        remote_repo = run.get(key)
        require(isinstance(remote_repo, dict) and remote_repo.get("full_name") == repo and
                positive_id(remote_repo.get("id")) and remote_repo["id"] == policy["repo_id"],
                "GitHub attempt or source belongs to another repository")
    for remote, local in (("id", "run_id"), ("run_attempt", "attempt")):
        require(positive_id(run.get(remote)) and run[remote] == policy[local],
                "GitHub attempt identity mismatch")
    require(run.get("head_sha") == policy["run_head_sha"], "fixture workflow revision mismatch")

    response = get(stem + "/jobs?per_page=100")
    jobs, count = response.get("jobs"), response.get("total_count")
    require(type(count) is int and isinstance(jobs, list) and 0 < count <= 100 and len(jobs) == count,
            "incomplete or unbounded attempt job list")
    seen = set()
    match = None
    for job in jobs:
        require(isinstance(job, dict) and positive_id(job.get("id")) and job["id"] not in seen,
                "ambiguous job list")
        seen.add(job["id"])
        if job["id"] == policy["job_id"]:
            match = job
    require(match is not None, "original waiting job absent from exact attempt")
    for remote, local in (("run_id", "run_id"), ("run_attempt", "attempt")):
        require(positive_id(match.get(remote)) and match[remote] == policy[local],
                "GitHub job ancestry mismatch")
    require(match.get("head_sha") == policy["run_head_sha"], "job revision mismatch")
    require(match.get("status") in ("in_progress", "completed"),
            "original waiting job has no observed execution")
    require(positive_id(match.get("runner_id")) and match["runner_id"] == policy["expected_runner_id"],
            "original waiting job was not assigned to the expected replacement registration")
    started = parse_time(match.get("started_at"))
    require(released < started <= min(deadline, now), "original job did not start inside recovery window")
    if match["status"] == "completed":
        require(match.get("conclusion") in ("success", "failure", "timed_out") and
                started <= parse_time(match.get("completed_at")) <= now,
                "completed job has no coherent execution outcome")
    else:
        require(match.get("conclusion") is None and match.get("completed_at") is None,
                "in-progress job carries contradictory completion")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--policy", required=True)
    parser.add_argument("--receipt", required=True)
    args = parser.parse_args()
    try:
        env = os.environ
        require(env.get("GITHUB_ACTIONS") == "true" and env.get("RUNNER_ENVIRONMENT") == "github-hosted" and
                env.get("GITHUB_EVENT_NAME") == "workflow_dispatch" and env.get("GITHUB_REF_PROTECTED") == "true",
                "requires trusted manual dispatch on a protected GitHub-hosted ref")
        policy = decode(load_file(args.policy))
        require(digest(policy.get("reviewed_sha"), 40) and env.get("GITHUB_SHA") == policy["reviewed_sha"],
                "reviewed controller revision mismatch")
        token = env.get("GARM_RECOVERY_ACTIONS_READ_TOKEN", "")
        require(re.fullmatch(r"[A-Za-z0-9_.-]{8,256}", token), "missing scoped Actions read token")
        observe(policy, load_file(args.receipt), datetime.now(timezone.utc), lambda path: fetch(token, path))
    except Invalid as exc:
        print(f"BLOCKED: {exc}", file=sys.stderr)
        return 1
    except (OSError, TimeoutError):
        print("BLOCKED: cannot read reviewed recovery inputs or observe GitHub", file=sys.stderr)
        return 1
    print("PASS: GitHub observed the exact waiting job start on the reviewed replacement registration inside the retirement window")
    return 0


if __name__ == "__main__":
    sys.exit(main())
