import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import survey

class FakeReader:
    calls = 0
    def os_release(self): return {"ID": "ubuntu", "VERSION_ID": "24.04"}
    def tools(self, home): self.calls += 1; return {"docker": {"status": "not_present", "version": None}}
    def kernel(self): return {"effective_caps_nonzero": False}
    def cgroup(self): return {"delegation_verified": False}
    def kvm(self): return {"device_opened": False, "vm_boot_tested": False}

def env(parent):
    return {"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted", "RUNNER_OS": "Linux",
            "RUNNER_ARCH": "X64", "RUNNER_TEMP": str(parent), "ImageVersion": "test-only"}

class SurveyTests(unittest.TestCase):
    def test_wrong_host_refuses_before_probe_or_creation(self):
        for key in ("GITHUB_ACTIONS", "RUNNER_ENVIRONMENT", "RUNNER_OS", "RUNNER_ARCH"):
            with self.subTest(key=key), tempfile.TemporaryDirectory() as directory:
                parent = Path(directory); output = parent / "garm-m2-capability-survey"
                context = env(parent); context[key] = "not-approved"
                reader = FakeReader()
                with self.assertRaises(survey.Refusal): survey.survey(output, context, reader)
                self.assertFalse(output.exists()); self.assertEqual(reader.calls, 0)

    def test_every_preexisting_output_kind_preserved(self):
        for kind in ("file", "directory", "symlink", "dangling-symlink"):
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as directory:
                parent = Path(directory); output = parent / "garm-m2-capability-survey"
                target = parent / "target"; target.write_text("untouched")
                if kind == "file": output.write_text("untouched")
                elif kind == "directory": output.mkdir(); (output / "sentinel").write_text("untouched")
                else: output.symlink_to(target if kind == "symlink" else parent / "missing")
                before = os.lstat(output); reader = FakeReader()
                with self.assertRaises(survey.Refusal): survey.survey(output, env(parent), reader)
                self.assertEqual(os.lstat(output).st_ino, before.st_ino)
                if kind == "file": self.assertEqual(output.read_text(), "untouched")
                if kind == "directory": self.assertEqual((output / "sentinel").read_text(), "untouched")
                self.assertEqual(target.read_text(), "untouched"); self.assertEqual(reader.calls, 0)

    def test_success_never_qualifies_or_runs_m2_and_artifact_is_whitelisted(self):
        with tempfile.TemporaryDirectory() as directory:
            parent = Path(directory); output = parent / "garm-m2-capability-survey"
            acknowledgment = parent / "workflow-output"; acknowledgment.write_text("before\n")
            context = env(parent); context["GITHUB_OUTPUT"] = str(acknowledgment)
            result = survey.survey(output, context, FakeReader())
            self.assertEqual(result["execution_authority"], "none")
            self.assertFalse(result["isolation_qualified"]); self.assertFalse(result["m2_kernel_tests_executed"])
            self.assertEqual(result["boundary"], survey.BOUNDARY)
            self.assertEqual(json.loads((output / "survey.json").read_text()), result)
            self.assertEqual(acknowledgment.read_text(), "before\nresults_created=true\n")
            self.assertEqual((output.stat().st_mode & 0o777), 0o700)
            self.assertEqual(set(p.name for p in output.iterdir()), {"private-home", "survey.json"})

    def test_unowned_or_indirect_workflow_output_is_not_opened(self):
        for kind in ("outside", "symlink", "dangling", "directory"):
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as directory:
                parent = Path(directory); output = parent / "garm-m2-capability-survey"
                context = env(parent); acknowledgment = parent / "workflow-output"
                sentinel = parent / "sentinel"; sentinel.write_text("untouched")
                if kind == "outside": acknowledgment = parent.parent / "outside-not-opened"
                elif kind == "directory": acknowledgment.mkdir()
                else: acknowledgment.symlink_to(sentinel if kind == "symlink" else parent / "missing")
                context["GITHUB_OUTPUT"] = str(acknowledgment)
                with self.assertRaises(survey.Refusal): survey.survey(output, context, FakeReader())
                self.assertFalse(output.exists()); self.assertEqual(sentinel.read_text(), "untouched")

    def test_failed_collection_never_acknowledges_an_artifact(self):
        with tempfile.TemporaryDirectory() as directory:
            parent = Path(directory); output = parent / "garm-m2-capability-survey"
            acknowledgment = parent / "workflow-output"; acknowledgment.write_text("before\n")
            context = env(parent); context["GITHUB_OUTPUT"] = str(acknowledgment)
            class Failure(FakeReader):
                def tools(self, home): raise survey.Refusal("test-only failure")
            with self.assertRaises(survey.Refusal): survey.survey(output, context, Failure())
            self.assertFalse((output / "survey.json").exists())
            self.assertEqual(acknowledgment.read_text(), "before\n")

    def test_probe_environment_never_inherits_tokens_or_runtime_endpoints(self):
        environment = survey.clean_env("/test-only-home")
        for key in ("GITHUB_TOKEN", "ACTIONS_RUNTIME_TOKEN", "DOCKER_HOST", "DOCKER_CONTEXT", "CONTAINER_HOST", "LD_PRELOAD", "PYTHONPATH"):
            self.assertNotIn(key, environment)
        self.assertEqual(environment["GOTOOLCHAIN"], "local")
        self.assertEqual(environment["GOPROXY"], "off")
        self.assertEqual(environment["GOSUMDB"], "off")
        self.assertEqual(survey.PROBES["docker"], ("--version",))
        self.assertEqual(survey.PROBES["podman"], ("--version",))

    def test_metadata_subprocess_output_is_bounded_and_timeout_is_not_runtime_cleanup(self):
        environment = survey.clean_env("/tmp")
        status, output = survey.bounded_probe([sys.executable, "-c", "import sys; sys.stdout.write('x'*1000000)"], environment)
        self.assertEqual(status, "metadata_too_large"); self.assertIsNone(output)
        status, output = survey.bounded_probe([sys.executable, "-c", "import time; time.sleep(3)"], environment, timeout=0.05)
        self.assertEqual(status, "metadata_timeout"); self.assertIsNone(output)
        status, output = survey.bounded_probe([sys.executable, "-c", "print('safe-metadata')"], environment)
        self.assertEqual(status, "observed"); self.assertEqual(output, b"safe-metadata\n")

    def test_no_runtime_operation_or_daemon_command_in_allowlist(self):
        self.assertEqual(set(survey.PROBES), set(survey.TOOL_NAMES))
        for name, arguments in survey.PROBES.items():
            self.assertEqual(arguments, ("version",) if name == "go" else ("--version",))
        source = Path(survey.__file__).read_text()
        for forbidden in ("sudo", "--privileged", "docker.sock", "podman.sock", "shell=True", "os.system(", "socket.socket(", "os.chmod(", "os.chown("):
            self.assertNotIn(forbidden, source)

    def test_wrong_output_path_and_os_profile_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            parent = Path(directory); reader = FakeReader()
            with self.assertRaises(survey.Refusal): survey.survey(parent / "other", env(parent), reader)
            class WrongOS(FakeReader):
                def os_release(self): return {"ID": "ubuntu", "VERSION_ID": "26.04"}
            with self.assertRaises(survey.Refusal): survey.survey(parent / "garm-m2-capability-survey", env(parent), WrongOS())
            self.assertEqual(list(parent.iterdir()), [])

if __name__ == "__main__": unittest.main()
