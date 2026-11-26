"""Regression tests for the Incus CLI wire and incarnation evidence."""
import importlib.util
import json
import unittest
from pathlib import Path
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location(
    "capture_job_counters", Path(__file__).with_name("capture-job-counters.py")
)
CAPTURE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CAPTURE)


class CaptureJobCountersTest(unittest.TestCase):
    def test_raw_query_scopes_project_in_uri(self):
        with patch.object(CAPTURE, "incus", return_value=b'{"status":"Running"}') as incus:
            self.assertEqual(
                CAPTURE.query("local:/1.0/instances/garm-ci-smoke/state", "garm-ci-project"),
                json.loads(b'{"status":"Running"}'),
            )
            incus.assert_called_once_with(
                "query", "local:/1.0/instances/garm-ci-smoke/state?project=garm-ci-project"
            )

    def test_replacement_between_metadata_reads_is_rejected(self):
        def metadata(generation):
            return {
                "name": "garm-ci-smoke",
                "type": "container",
                "status": "Running",
                "project": "garm-ci-project",
                "config": {
                    "volatile.uuid": "4f01c4b0-a743-4f31-affd-70e7d4452afe",
                    "volatile.uuid.generation": generation,
                },
            }

        state = {"pid": 42, "status": "Running", "status_code": 103}
        before = metadata("d9826efe-6580-4ac1-b3f9-f750a2c3524f")
        after = metadata("d9826efe-6580-4ac1-b3f9-f750a2c35250")
        with (
            patch.object(CAPTURE, "query", side_effect=[before, state, state, after]) as query,
            patch.object(CAPTURE, "process_ticks", return_value=11),
            self.assertRaisesRegex(ValueError, "incarnation changed"),
        ):
            CAPTURE.capture("garm-ci-smoke", "garm-ci-project")
        self.assertEqual(query.call_count, 4)
