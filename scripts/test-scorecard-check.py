"""Offline behavioural tests: python3 scripts/test-scorecard-check.py.

Run the public readiness script in a throwaway checkout with local tool fixtures.
Assertions cover exit status and diagnostics, not tool invocation details.
"""
import copy
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parent
NAMES = ("Binary-Artifacts", "Dangerous-Workflow", "Dependency-Update-Tool",
         "Fuzzing", "License", "Pinned-Dependencies", "SAST", "Security-Policy",
         "Token-Permissions", "Vulnerabilities")
PASS = {"score": 8.1, "checks": [
    {"name": name, "score": 9 if name == "Pinned-Dependencies" else 10, "reason": "fixture"}
    for name in NAMES]}


class ReadinessTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / "scripts").mkdir()
        (self.root / "bin").mkdir()
        for name in ("public-check.sh", "check-scorecard.py"):
            shutil.copyfile(ROOT / name, self.root / "scripts" / name)
        (self.root / "go.mod").write_text("module github.com/example/project\n")
        for name in ("check-name.sh", "check-private.sh", "docs-check.sh"):
            self.executable("scripts/" + name, "exit ${LOCAL_EXIT:-0}\n")
        for name in ("gitleaks", "uvx"):
            self.executable("bin/" + name, "exit 0\n")
        self.executable("bin/scorecard", 'cat "$REPORT"\n'
                        'echo "fixture Scorecard diagnostic" >&2\n'
                        'exit "${SCORECARD_EXIT:-0}"\n')

    def executable(self, name, body):
        path = self.root / name
        path.write_text("#!/usr/bin/env bash\n" + body)
        path.chmod(0o755)

    def run_report(self, report, expected, diagnostic="", **extra):
        path = self.root / "report.json"
        path.write_text(report if isinstance(report, str) else json.dumps(report))
        env = dict(os.environ, PATH=str(self.root / "bin") + os.pathsep + os.environ["PATH"],
                   GITHUB_AUTH_TOKEN="offline-fixture", REPORT=str(path),
                   LOCAL_EXIT="0", SCORECARD_EXIT="0")
        env.update(extra)
        result = subprocess.run(["bash", str(self.root / "scripts/public-check.sh")],
                                env=env, capture_output=True, text=True, timeout=10)
        output = result.stdout + result.stderr
        self.assertEqual(result.returncode, expected, output)
        self.assertIn(diagnostic, output)
        self.assertNotIn("Traceback", output)
        if expected and extra.get("LOCAL_EXIT") != "1":
            self.assertIn("Try:", output)
        return output

    def test_complete_report(self):
        output = self.run_report(PASS, 0, "public-check: ok")
        self.assertIn("Local checkout: ok", output)
        self.assertIn("GitHub state, not the local checkout", output)

    def test_missing_checks(self):
        for report in ({"checks": [], "score": 0},
                       dict(PASS, checks=PASS["checks"][:-1])):
            with self.subTest(report=report):
                self.run_report(report, 1, "missing expected checks:")

    def test_duplicate(self):
        self.run_report(dict(PASS, checks=PASS["checks"] + [PASS["checks"][0]]),
                        1, "duplicate expected check: Binary-Artifacts")

    def test_each_threshold(self):
        for index, name in enumerate(NAMES):
            with self.subTest(name=name):
                report = copy.deepcopy(PASS)
                report["checks"][index]["score"] -= 1
                self.run_report(report, 1, "below their thresholds")

    def test_malformed_payloads(self):
        for report in ("", "{", "null", "[]", {}, {"checks": None, "score": 0},
                       dict(PASS, checks=[None]), dict(PASS, checks=[{}])):
            with self.subTest(report=report):
                self.run_report(report, 1, "Hosted Scorecard:")

    def test_malformed_scores(self):
        for score in (None, True, "10", -2, 11, float("nan"), float("inf"), [], {}):
            with self.subTest(score=score):
                self.run_report(dict(PASS, score=score), 1, "overall score")
                report = copy.deepcopy(PASS)
                report["checks"][0]["score"] = score
                self.run_report(report, 1, "Binary-Artifacts: score")
        report = copy.deepcopy(PASS)
        report["checks"][0]["score"] = 9.5
        self.run_report(report, 1, "score must be an integer")

    def test_unscored_expected_check(self):
        report = copy.deepcopy(PASS)
        report["checks"][0]["score"] = -1
        self.run_report(report, 1, "below their thresholds")

    def test_extra_check_does_not_need_to_pass(self):
        report = dict(PASS, checks=PASS["checks"] + [
            {"name": "Other-Check", "score": -1, "reason": "not applicable"}])
        self.run_report(report, 0, "Other-Check")

    def test_failed_command_with_valid_report(self):
        output = self.run_report(PASS, 1, "command failed (exit 7)", SCORECARD_EXIT="7")
        self.assertIn("fixture Scorecard diagnostic", output)

    def test_local_failure_not_hidden_by_hosted_pass(self):
        self.run_report(PASS, 1, "Local checkout: FAILED", LOCAL_EXIT="1")


if __name__ == "__main__":
    unittest.main()
