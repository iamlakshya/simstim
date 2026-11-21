"""Behavioural checks for source archives and checkout-only file selection."""
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest


SCRIPTS = Path(__file__).resolve().parent
CHECKS = ("check-name.sh", "check-private.sh", "check-size.sh", "check-scripts.sh", "docs-check.sh")
FIXTURE_NAME = "AsterUnit"
FAKE_TOOL = r'''#!/usr/bin/env python3
import json
import os
from pathlib import Path
import shutil
import sys

name = Path(sys.argv[0]).name
with open(os.environ["CHECK_CALLS"], "a") as log:
    log.write(json.dumps([name, *sys.argv[1:]]) + "\n")
if name == "go":
    args = sys.argv[1:]
    if args == ["version"]:
        pin = next(line.split()[1] for line in Path("go.mod").read_text().splitlines()
                   if line.startswith("toolchain "))
        print("go version " + pin + " fixture/fixture")
    elif args and args[0] in ("run", "build"):
        if "-tags=userns_cohort_v3,http2legacy" not in args:
            raise SystemExit("source checks lost the complete build tag list")
        if args[0] == "run":
            output = Path(args[args.index("-out") + 1])
            shutil.copyfile("docs/reference/agents.md", output / "agents.md")
        else:
            output = Path(args[args.index("-o") + 1])
            output.write_text("#!/bin/sh\nexit 0\n")
elif name == "typos":
    for arg in sys.argv[1:]:
        if not arg.startswith("-") and "bad-spelling-marker" in Path(arg).read_text():
            print("spelling fixture: " + arg)
            sys.exit(1)
'''


class SourceCheckTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.workspace = Path(self.temp.name)
        self.seed = self.workspace / "seed"
        self.seed.mkdir()
        self.calls = self.workspace / "calls.jsonl"
        self.bin = self.workspace / "bin"
        self.bin.mkdir()
        self.env = dict(os.environ, CHECK_CALLS=str(self.calls),
                        PATH=str(self.bin) + os.pathsep + os.environ["PATH"])
        for tool in ("typos", "vale", "lychee", "node", "pnpm", "npx", "go"):
            path = self.bin / tool
            path.write_text(FAKE_TOOL)
            path.chmod(0o755)
        for name in (*CHECKS, "source-files.sh", "source-files.py", "go-build-tags.sh"):
            target = self.seed / "scripts" / name
            target.parent.mkdir(exist_ok=True)
            shutil.copyfile(SCRIPTS / name, target)
        shutil.copyfile(SCRIPTS.parent / ".gitignore", self.seed / ".gitignore")
        toolchain = next(line for line in (SCRIPTS.parent / "go.mod").read_text().splitlines()
                         if line.startswith("toolchain "))
        self.write("go.mod", "module github.com/example/project\n\n" + toolchain + "\n")
        self.write("internal/brand/brand.go", 'package brand\nconst Name = "' + FIXTURE_NAME + '"\n')
        self.write("main.go", "package main\n")
        self.write("README.md", "# Public source\n")
        self.write("docs/reference/agents.md", "# Agents\n")
        for name in ("test-docs-commands.py", "check-site-links.py", "check-llms-txt.py"):
            self.write("scripts/" + name, "raise SystemExit(0)\n")
        self.write("scripts/docs-site.sh", "#!/bin/sh\nexit 0\n", executable=True)
        self.git(self.seed, "init", "--quiet", "--template=")
        self.git(self.seed, "add", ".")

    def write(self, name, text, root=None, executable=False):
        path = (root or self.seed) / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)
        if executable:
            path.chmod(0o755)
        return path

    def git(self, root, *args):
        result = subprocess.run(["git", "-C", str(root), "-c", "core.fsmonitor=false", *args],
                                env=self.env, capture_output=True, check=False, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr.decode())
        return result.stdout

    def archive(self, nested=False):
        # A tree is enough for git archive. No fixture commit or branch is made.
        tree = self.git(self.seed, "write-tree").decode().strip()
        data = self.git(self.seed, "archive", "--format=tar", tree)
        root = self.workspace / ("outer/snapshot" if nested else "snapshot")
        root.mkdir(parents=True)
        if nested:
            self.git(root.parent, "init", "--quiet", "--template=")
        with tarfile.open(fileobj=io.BytesIO(data)) as archive:
            archive.extractall(root, filter="data")
        self.assertFalse((root / ".git").exists())
        return root

    def run_check(self, root, script, *args):
        result = subprocess.run(["/bin/bash", str(root / "scripts" / script), *args],
                                cwd=root, env=self.env, capture_output=True, text=True,
                                check=False, timeout=60)
        self.assertNotIn("unbound variable", result.stdout + result.stderr)
        return result

    def assert_hinted_failure(self, result):
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("Try:", result.stdout + result.stderr)
        self.assertNotIn("Traceback", result.stdout + result.stderr)

    def test_clean_archive_checks_real_files(self):
        root = self.archive()
        for script in CHECKS:
            with self.subTest(script=script):
                result = self.run_check(root, script)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        calls = [json.loads(line) for line in self.calls.read_text().splitlines()]
        for tool in ("typos", "vale"):
            selected = next(call[1:] for call in calls if call[0] == tool)
            self.assertIn("README.md", selected)
            self.assertIn("docs/reference/agents.md", selected)
        self.assertFalse((root / ".git").exists())

    def test_archive_inside_another_checkout_finds_its_own_violation(self):
        root = self.archive(nested=True)
        self.write("internal/extra.go", "package main\n// " + FIXTURE_NAME + "\n", root)
        result = self.run_check(root, "check-name.sh")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("internal/extra.go:", result.stdout)

    def test_archive_private_detail_is_not_a_clean_empty_scan(self):
        root = self.archive()
        self.write("public.txt", "/home/" + "private-account" + "/work\n", root)
        result = self.run_check(root, "check-private.sh")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("public.txt:", result.stdout)

    def test_public_systemd_units_and_template_tokens_are_not_emails(self):
        root = self.archive()
        self.write("public.txt", "\n".join((
            "/etc/systemd/system/user@1001.service.d/50-@BRAND@-delegate.conf",
            "/etc/systemd/system/user@@UID@.service.d/50-@BRAND@-delegate.conf",
            "/etc/.@BRAND@-subid-lock.XXXXXXXX",
            "/etc/modprobe.d/50-@BRAND@-vsock.conf",
        )) + "\n", root)
        result = self.run_check(root, "check-private.sh")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_email_domains_still_fail_next_to_public_template_syntax(self):
        root = self.archive()
        for address in (
            "build.bot+tag" + "@" + "sub-zone.contact.test",
            "operator" + "@" + "x.contact.test",
            "a" + "@" + "contact.test",
        ):
            for suffix in ("", "/next", ".", ". See the contact.", ", next", "--next"):
                with self.subTest(address=address, suffix=suffix):
                    self.write("public.txt", "/etc/systemd/system/user@1001.service.d "
                               "@BRAND@ " + address + suffix + "\n", root)
                    result = self.run_check(root, "check-private.sh")
                    self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                    self.assertIn("public.txt:", result.stdout)

    def test_public_template_syntax_does_not_hide_other_private_details(self):
        root = self.archive()
        self.write("public.txt", "/etc/systemd/system/user@1001.service.d "
                   "/home/" + "private-account" + "/work\n", root)
        result = self.run_check(root, "check-private.sh")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("public.txt:", result.stdout)

    def test_archive_go_and_python_size_limits_still_apply(self):
        for name in ("internal/long.go", "test/e2e/long.py"):
            with self.subTest(name=name):
                root = self.archive()
                self.write(name, "# long\n" * 501, root)
                result = self.run_check(root, "check-size.sh")
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                self.assertIn("501 " + name, result.stdout)
                shutil.rmtree(root)

    def test_archive_markdown_violation_reaches_the_spelling_check(self):
        root = self.archive()
        self.write("docs/new page.md", "bad-spelling-marker\n", root)
        result = self.run_check(root, "docs-check.sh")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("spelling fixture: docs/new page.md", result.stdout)

    def test_archive_shell_variable_violation_is_checked(self):
        root = self.archive()
        self.write("scripts/extra.sh", 'printf "$' + 'value' + '\u2026"\n', root)
        result = self.run_check(root, "check-scripts.sh")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("scripts/extra.sh:", result.stdout)

    def test_archive_ignore_rules_and_negated_source_rule(self):
        root = self.archive()
        poison = FIXTURE_NAME + " /home/" + "private-account" + "/work\n"
        for path in ("private/notes.go", ".claude/worktrees/other/main.go",
                     "build/output.go", "bin/output.go", "docs/research/private/note.md",
                     "internal/client/helpers/host-linux-amd64"):
            self.write(path, poison * 501, root)
        self.write("generated/ignored.go", poison, root)
        with (root / ".gitignore").open("a") as output:
            output.write("\ngenerated/*\n!generated/keep.go\n")
        for script in CHECKS:
            result = self.run_check(root, script)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.write("generated/keep.go", "package main\n// " + FIXTURE_NAME + "\n", root)
        result = self.run_check(root, "check-name.sh")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("generated/keep.go:", result.stdout)

    def test_checkout_names_and_sizes_include_untracked_source(self):
        self.write("untracked.go", "// " + FIXTURE_NAME + "\n" * 501)
        for script in ("check-name.sh", "check-size.sh"):
            result = self.run_check(self.seed, script)
            self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
            self.assertIn("untracked.go", result.stdout)

    def test_checkout_private_scope_stays_tracked(self):
        self.write("untracked.txt", "/home/" + "private-account" + "/work\n")
        result = self.run_check(self.seed, "check-private.sh")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.git(self.seed, "add", "untracked.txt")
        result = self.run_check(self.seed, "check-private.sh")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("untracked.txt:", result.stdout)

    def test_checkout_script_scope_stays_tracked(self):
        self.write("untracked.sh", 'printf "$' + 'value' + '\u2026"\n')
        result = self.run_check(self.seed, "check-scripts.sh")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.git(self.seed, "add", "untracked.sh")
        result = self.run_check(self.seed, "check-scripts.sh")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("untracked.sh:", result.stdout)

    def test_source_names_beginning_with_a_dash_remain_filenames(self):
        root = self.archive()
        self.write("-e.go", "// " + FIXTURE_NAME + "\n", root)
        result = self.run_check(root, "check-name.sh")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("-e.go:", result.stdout)
        self.write("-E.txt", "/home/" + "private-account" + "/work\n", root)
        result = self.run_check(root, "check-private.sh")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("-E.txt:", result.stdout)

    def test_source_link_cannot_escape_the_tree(self):
        root = self.archive()
        outside = self.workspace / "outside.go"
        outside.write_text("must-not-inspect-this-token\n")
        (root / "escape.go").symlink_to(outside)
        result = self.run_check(root, "source-files.sh", "--", "*.go")
        self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
        self.assert_hinted_failure(result)
        self.assertEqual(result.stdout, "")
        self.assertNotIn("must-not-inspect-this-token", result.stderr)

    def test_broken_source_link_and_nonregular_source_fail_closed(self):
        for kind in ("link", "fifo"):
            with self.subTest(kind=kind):
                root = self.archive() if kind == "link" else self.seed
                path = root / ("unsafe.go" if kind == "link" else "main.go")
                if kind == "link":
                    path.symlink_to(root / "gone.go")
                else:
                    path.unlink()
                    os.mkfifo(path)
                result = self.run_check(root, "check-name.sh")
                self.assert_hinted_failure(result)
                shutil.rmtree(root)

    def test_empty_selection_and_history_in_an_archive_fail_with_hints(self):
        root = self.archive()
        result = self.run_check(root, "source-files.sh", "--", "not-present-*.go")
        self.assert_hinted_failure(result)
        result = self.run_check(root, "check-private.sh", "--history")
        self.assert_hinted_failure(result)
        self.assertIn("Git history", result.stderr)

    def test_no_markdown_fails_without_bash_array_crash(self):
        root = self.archive()
        for path in root.rglob("*.md"):
            path.unlink()
        self.assert_hinted_failure(self.run_check(root, "docs-check.sh"))

    def test_only_excluded_markdown_fails_without_bash_array_crash(self):
        for path in self.seed.rglob("*.md"):
            path.unlink()
        self.write("private/note.md", "# Private\n")
        self.git(self.seed, "add", "-u")
        self.git(self.seed, "add", "-f", "private/note.md")
        self.assert_hinted_failure(self.run_check(self.seed, "docs-check.sh"))

    def test_git_failure_never_becomes_a_successful_empty_scan(self):
        root = self.archive()
        failing_git = self.bin / "git"
        # A listing can fail after writing plausible paths. Partial output
        # is not proof that the selected tree was checked in full.
        failing_git.write_text("#!/bin/sh\nprintf 'main.go\\000'\nexit 42\n")
        failing_git.chmod(0o755)
        for script in CHECKS:
            result = self.run_check(root, script)
            self.assert_hinted_failure(result)
            self.assertNotIn(": ok", result.stdout)

    def test_malformed_metadata_fails_instead_of_using_an_archive_fallback(self):
        root = self.archive()
        (root / ".git").mkdir()
        self.assert_hinted_failure(self.run_check(root, "check-name.sh"))

    def test_git_environment_cannot_redirect_an_archive_scan(self):
        root = self.archive(nested=True)
        self.env.update(GIT_DIR=str(root.parent / ".git"), GIT_WORK_TREE=str(root.parent),
                        GIT_INDEX_FILE=str(self.workspace / "wrong-index"))
        self.write("internal/extra.go", "// " + FIXTURE_NAME + "\n", root)
        result = self.run_check(root, "check-name.sh")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("internal/extra.go:", result.stdout)

    def test_user_excludes_cannot_hide_archive_source(self):
        root = self.archive()
        ignores = self.workspace / "global-ignore"
        ignores.write_text("*.go\n*.md\n")
        config = self.workspace / "global-config"
        config.write_text("[core]\nexcludesFile = " + str(ignores) + "\n")
        self.env["GIT_CONFIG_GLOBAL"] = str(config)
        self.write("internal/extra.go", "// " + FIXTURE_NAME + "\n", root)
        result = self.run_check(root, "check-name.sh")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("internal/extra.go:", result.stdout)

    def test_checkout_listing_does_not_run_a_configured_monitor(self):
        marker = self.workspace / "monitor-called"
        monitor = self.workspace / "monitor.sh"
        monitor.write_text('#!/bin/sh\nprintf invoked > "$MONITOR_MARK"\n')
        monitor.chmod(0o755)
        self.git(self.seed, "config", "core.fsmonitor", str(monitor))
        self.env["MONITOR_MARK"] = str(marker)
        result = self.run_check(self.seed, "check-name.sh")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse(marker.exists(), "the file listing ran the checkout's monitor")


if __name__ == "__main__":
    unittest.main()
