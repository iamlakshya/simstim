"""Exercise setup with isolated tools; no compiler or download runs."""
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parent.parent
ASSERTION = "fixture_test.go:17: the file holds 99 writes, want 100"
TOOLS = ("go", "git", "ssh", "just", "golangci-lint", "goreleaser", "gitleaks",
         "zig", "node", "pnpm", "typos", "vale", "lychee")
STUB = r'''#!/bin/sh
set -eu
name=${0##*/}
if [ "$name" = go ]; then
    printf '%s %s\n' "${GOTOOLCHAIN:-}" "$*" >>"$SETUP_CALLS"
    case "$1" in
        version) printf 'go version fixture fixture\n' ;;
        test)
            if [ "$SETUP_FAIL" = 1 ]; then
                printf '%s\n' '--- FAIL: TestFixture' "$SETUP_ASSERTION" 'FAIL' >&2
                exit 1
            fi
            ;;
        *) exit 2 ;;
    esac
else
    printf '%s fixture\n' "$name"
fi
'''


class SetupTests(unittest.TestCase):
    def run_setup(self, fail=False):
        with tempfile.TemporaryDirectory(prefix="setup-test-") as temporary:
            root = Path(temporary)
            (root / "scripts").mkdir()
            shutil.copyfile(ROOT / "scripts/setup.sh", root / "scripts/setup.sh")
            shutil.copyfile(ROOT / "go.mod", root / "go.mod")
            (root / "scripts/tools.lock").write_text("")
            tools = root / ".tools/bin"
            tools.mkdir(parents=True)
            for name in TOOLS:
                tool = tools / name
                tool.write_text(STUB)
                tool.chmod(0o700)
            env = {"PATH": str(tools) + ":/usr/bin:/bin", "GOTOOLCHAIN": "local",
                   "SETUP_CALLS": str(root / "calls"), "SETUP_FAIL": "1" if fail else "0",
                   "SETUP_ASSERTION": ASSERTION}
            for key in ("HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_STATE_HOME",
                        "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR",
                        "XDG_CONFIG_DIRS", "XDG_DATA_DIRS", "TMPDIR"):
                folder = root / key.lower()
                folder.mkdir(mode=0o700)
                env[key] = str(folder)
            result = subprocess.run(["bash", "scripts/setup.sh"], cwd=root, env=env,
                                    capture_output=True, text=True, timeout=20)
            calls = (root / "calls").read_text().splitlines()
            self.assertEqual(list((root / "tmpdir").iterdir()), [])
            return result, calls

    def test_uses_the_repository_toolchain_over_the_image_setting(self):
        result, calls = self.run_setup()
        self.assertEqual(result.returncode, 0, result.stderr)
        pin = next(line.split()[1] for line in (ROOT / "go.mod").read_text().splitlines()
                   if line.startswith("toolchain "))
        self.assertEqual(calls, [pin + " version", pin + " test -count=1 ./..."])
        self.assertIn("Ready.", result.stderr)

    def test_prints_the_complete_test_failure_before_cleanup(self):
        result, _ = self.run_setup(fail=True)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn(ASSERTION, result.stderr)
        self.assertIn("--- FAIL: TestFixture", result.stderr)
        self.assertIn("Error: a test failed", result.stderr)
        self.assertNotIn("Ready.", result.stderr)


if __name__ == "__main__":
    unittest.main()
