import hashlib
import json
import os
import unittest
from copy import deepcopy
from datetime import datetime, timezone
from unittest.mock import MagicMock, patch

import verify


NOW = datetime(2026, 11, 25, 12, tzinfo=timezone.utc)
RUN = 9007199254740993
JOB = 9007199254740995
REPO = 9007199254740997


def fixtures():
    policy = dict(schema="recovery-policy-v1", reviewed_sha="a" * 40,
                  repo="fixture-org/recovery-fixture", repo_id=REPO, run_id=RUN, attempt=3,
                  job_id=JOB, run_head_sha="b" * 40, scope_digest="c" * 64,
                  declared_at="2026-11-25T11:40:00Z", window_seconds=30)
    receipt = dict(schema="capacity-retirement-observation-v1", repo_id=REPO, run_id=RUN,
                   attempt=3, job_id=JOB, scope_digest="c" * 64,
                   queued_at="2026-11-25T11:45:00Z", released_at="2026-11-25T11:51:00Z",
                   blocked_reservation_digest="d" * 64, retired_reservation_digest="e" * 64)
    run = dict(repository=dict(id=REPO, full_name=policy["repo"]), id=RUN, run_attempt=3,
               head_sha="b" * 40, head_repository=dict(id=REPO, full_name=policy["repo"]))
    job = dict(id=JOB, run_id=RUN, run_attempt=3, head_sha="b" * 40, status="completed",
               conclusion="success", runner_id=17, started_at="2026-11-25T11:51:23Z",
               completed_at="2026-11-25T11:51:58Z")
    jobs = dict(jobs=[dict(job, id=JOB-1), job], total_count=2)
    return policy, receipt, run, jobs


def bind(policy, receipt):
    raw = json.dumps(receipt, sort_keys=True).encode()
    policy["receipt_sha256"] = hashlib.sha256(raw).hexdigest()
    return raw


class RecoveryEvidenceTest(unittest.TestCase):
    def test_exact_job_started(self):
        policy, receipt, run, jobs = fixtures()
        raw = bind(policy, receipt)
        calls = []

        def get(path):
            calls.append(path)
            return jobs if path.endswith("/jobs?per_page=100") else run

        verify.observe(policy, raw, NOW, get)
        stem = f"/repos/fixture-org/recovery-fixture/actions/runs/{RUN}/attempts/3"
        self.assertEqual(calls, [stem, stem + "/jobs?per_page=100"])

    def test_negative_identity_and_lifecycle_cases(self):
        changes = [
            ("wrong repository", "run", "repository", {"id": REPO+1, "full_name": "fixture-org/recovery-fixture"}),
            ("fork source", "run", "head_repository", {"id": REPO+1, "full_name": "other/recovery-fixture"}),
            ("wrong run", "run", "id", RUN+1), ("wrong attempt", "run", "run_attempt", 2),
            ("wrong revision", "run", "head_sha", "a"*40),
            ("other job started", "job", "id", JOB+1),
            ("wrong job ancestry", "job", "run_attempt", 2),
            ("no runner", "job", "runner_id", None),
            ("queued", "job", "status", "queued"),
            ("skipped", "job", "conclusion", "skipped"),
            ("unknown completion", "job", "conclusion", "unknown"),
            ("completed before start", "job", "completed_at", "2026-11-25T11:51:22Z"),
            ("before release", "job", "started_at", "2026-11-25T11:50:59Z"),
            ("same second as release", "job", "started_at", "2026-11-25T11:51:00Z"),
            ("after window", "job", "started_at", "2026-11-25T11:51:31Z"),
            ("ambiguous time zone", "job", "started_at", "2026-11-25T11:51:15"),
            ("pagination incomplete", "jobs", "total_count", 3),
        ]
        for label, where, key, value in changes:
            with self.subTest(label):
                policy, receipt, run, jobs = fixtures()
                targets = dict(run=run, jobs=jobs, job=jobs["jobs"][1])
                targets[where][key] = value
                with self.assertRaises(verify.Invalid):
                    verify.observe(policy, bind(policy, receipt), NOW,
                                   lambda path: jobs if "/jobs?" in path else run)

    def test_unreviewed_missing_or_unrelated_release_fails_before_network(self):
        policy, receipt, _, _ = fixtures()
        raw = bind(policy, receipt)
        with self.assertRaises(verify.Invalid):
            verify.validate_evidence(policy, raw + b" ", NOW)
        cases = [
            ("receipt", "job_id", JOB-1), ("receipt", "scope_digest", "f"*64),
            ("receipt", "retired_reservation_digest", receipt["blocked_reservation_digest"]),
            ("receipt", "queued_at", receipt["released_at"]),
            ("receipt", "released_at", "2026-11-26T11:51:00Z"),
            ("policy", "declared_at", receipt["released_at"]),
            ("policy", "window_seconds", True),
            ("policy", "window_seconds", 301),
            ("policy", "repo", "fixture-org/recovery-fixture?token=foo"),
        ]
        for description, key, value in cases:
            with self.subTest(description, key=key):
                p, r = deepcopy(policy), deepcopy(receipt)
                dict(policy=p, receipt=r)[description][key] = value
                with self.assertRaises(verify.Invalid):
                    verify.observe(p, bind(p, r), NOW, lambda _: self.fail("unexpected request"))

    def test_ref_protection_before_loading_token(self):
        bad_env = dict(GITHUB_ACTIONS="true", RUNNER_ENVIRONMENT="github-hosted",
                       GITHUB_EVENT_NAME="pull_request", GITHUB_REF_PROTECTED="true")
        with patch.dict(os.environ, bad_env, clear=True), patch(
                "sys.argv", ["verify.py", "--policy", "policy", "--receipt", "receipt"]
        ), patch("verify.load_file") as load, patch("sys.stderr"):
            self.assertEqual(verify.main(), 1)
            load.assert_not_called()

    def test_fixed_https_get_no_redirect_no_proxy(self):
        response = MagicMock()
        response.__enter__.return_value = response
        response.status, response.headers = 200, {}
        response.read.return_value = b'{"repository": {}}'
        with patch("verify.build_opener") as factory:
            opened = factory.return_value.open
            opened.return_value = response
            self.assertEqual(verify.fetch("redacted-fixture", "/repos/o/r"), {"repository": {}})
            request = opened.call_args.args[0]
            self.assertEqual(request.get_method(), "GET")
            self.assertEqual(request.full_url, "https://api.github.com/repos/o/r")
            self.assertIsInstance(factory.call_args.args[1], verify.NoRedirect)
            self.assertEqual(factory.call_args.args[0].proxies, {})
            response.headers = {"Link": '<https://example.invalid>; rel="next"'}
            with self.assertRaises(verify.Invalid):
                verify.fetch("redacted-fixture", "/repos/o/r")

    def test_duplicate_json_fails_closed(self):
        with self.assertRaises(verify.Invalid):
            verify.decode(b'{"x":1,"x":2}')


if __name__ == "__main__":
    unittest.main()
