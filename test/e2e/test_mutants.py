"""Offline mutant tests for the e2e allow-list scanner and runtime boundary:
python3 test/e2e/test_mutants.py.

Copies the non-test e2e modules into a temporary folder, applies each mutant
from decision10.md (Part 1 item 3, Part 2 items R and U, Part 3 items V, Z
mutants M1 to M10, and AE), and checks that allowed.check(tmp) and/or the
runtime boundary refuse every single one.
"""
import os, shutil, sys, tempfile, types, unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fakes import HERE, read, write  # noqa: E402
import allowed  # noqa: E402
import boundary  # noqa: E402


class Mutants(unittest.TestCase):
    def copy_suite(self):
        d = tempfile.mkdtemp(prefix="e2e-mutant-")
        self.addCleanup(shutil.rmtree, d, True)
        for n in os.listdir(HERE):
            if n.endswith(".py") and not n.startswith("test_"):
                shutil.copy(os.path.join(HERE, n), os.path.join(d, n))
        return d

    def check_fails(self, mutate, needle=None):
        d = self.copy_suite()
        mutate(d)
        problems = allowed.check(d)
        self.assertTrue(problems, "mutant was NOT caught by allowed.check()")
        if needle:
            self.assertTrue(any(needle in p for p in problems), "%r not in %r" % (needle, problems))
        return problems

    def replace_in(self, d, filename, old, new):
        p = os.path.join(d, filename)
        src = read(p)
        self.assertIn(old, src, "%r not in %s" % (old, filename))
        write(p, src.replace(old, new, 1))

    def append_to(self, d, filename, code):
        p = os.path.join(d, filename)
        write(p, read(p) + "\n" + code + "\n")

    def test_baseline_copy_is_clean(self):
        self.assertEqual(allowed.check(self.copy_suite()), [])

    def test_part1_item3_mutants(self):
        # 1. Typing check outside `if DISPOSABLE`.
        self.check_fails(
            lambda d: self.replace_in(d, "run.py", 'run_parts.skip("typing")',
                                      'import disposable_checks as dc\n        dc.typing(C)'),
            "imports ['disposable_checks'] outside `if DISPOSABLE`")
        self.check_fails(
            lambda d: self.append_to(d, "run.py", 't.send(b"echo MARK-$((40+2))\\r")'),
            "runs on every host, and is not in allowed.py's owner table")
        # 2. A DISPOSABLE_ONLY command listed in the owner-host table (both tables / unused on owner).
        old = dict(allowed.CLIENT)
        try:
            allowed.CLIENT[("kill",)] = allowed.DELETE + ": mutant"
            problems = allowed.check(self.copy_suite())
            self.assertTrue(any("in both tables" in p or "runs on no host outside a disposable one" in p for p in problems), problems)
        finally:
            allowed.CLIENT.clear()
            allowed.CLIENT.update(old)

    def test_m1_owner_path_outside_run_dir(self):
        # M1: agent_setup.py sets exit_file = "$HOME/.profile".
        self.check_fails(
            lambda d: self.replace_in(d, "agent_setup.py",
                                      'exit_file = owned.run_dir("$HOME") + "/setup-exit"',
                                      'exit_file = "$HOME/.profile"'),
            "sets exit_file outside run_dir()")
        # Also caught at runtime by allowed.host_refusal / boundary.Target.rc.
        c = types.SimpleNamespace(NAME="client", HELPER='"$HOME/h"')
        self.assertIsNotNone(allowed.host_refusal('echo 7 > "$HOME/.profile"', c))

    def test_m2_built_client_list_with_plus(self):
        # M2: [cli, "host", "doctor", HOST] + ["--fix", "--yes"].
        self.check_fails(
            lambda d: self.replace_in(d, "run.py", '[cli, "host", "doctor", HOST]',
                                      '[cli, "host", "doctor", HOST] + ["--fix", "--yes"]'),
            "builds a client list with Add")

    def test_m3_aliased_client_and_direct_subprocess(self):
        # M3: exe = cli; subprocess.run([exe, "kill", ...], timeout=60).
        self.check_fails(
            lambda d: self.append_to(d, "run.py", 'exe = cli'),
            "uses the client other than as an argv's first word")
        self.check_fails(
            lambda d: self.append_to(d, "run_parts.py", 'fn = owned.host_rc'),
            "gives a host call, a terminal or the client another name")
        self.check_fails(
            lambda d: self.append_to(d, "run.py", 'import subprocess\nsubprocess.run(["id"], timeout=60)'),
            "only boundary.py starts a process")

    def test_m4_host_argv_bypass(self):
        # M4: building or running host argv outside boundary.Target._run.
        self.check_fails(
            lambda d: self.append_to(d, "run.py", 'T.argv("rm -f ~/.bash_history")'),
            "builds the host's command line itself")
        self.check_fails(
            lambda d: self.append_to(d, "boundary.py",
                                     'def bypass(self, c):\n    return subprocess.run(self.argv(c), stdin=subprocess.DEVNULL, timeout=10)\n'),
            "only _run and marker may build a host argv")

    def test_m5_starred_flag_in_start_or_client_list(self):
        # M5: wt = ["--worktree"]; start_new(..., *wt, *prog) or [cli, "new", *wt].
        self.check_fails(
            lambda d: self.replace_in(d, "agent_setup.py",
                                      'owned.start_new(c, envx, created, name, "--dir", repo, *prog)',
                                      'owned.start_new(c, envx, created, name, "--dir", repo, *["--worktree"], *prog)'),
            "gives a start *")
        self.check_fails(
            lambda d: self.append_to(d, "run.py", 'run_parts.run_or_timeout([cli, "ls", *["--json"]], envx, None)'),
            "builds a client list with *")

    def test_m6_import_from_disposable_module_outside_guard(self):
        # M6: `from guided_setup import run as guided` outside `if DISPOSABLE`.
        self.check_fails(
            lambda d: self.replace_in(d, "run.py", 'run_parts.skip("guided setup")',
                                      'from guided_setup import run as guided\n            guided(None)'),
            "imports ['guided_setup'] outside `if DISPOSABLE`")
        self.check_fails(
            lambda d: self.append_to(d, "run.py", 'import importlib\nimportlib.import_module("guided_setup")'),
            "imports ['importlib']")
        self.check_fails(
            lambda d: self.append_to(d, "run.py", '__import__("guided_setup")'),
            "calls __import__")

    def test_m7_second_disposable_assignment(self):
        # M7: `DISPOSABLE = True` after its definition, or setting c.DISPOSABLE / state["disposable"].
        self.check_fails(
            lambda d: self.append_to(d, "run.py", "DISPOSABLE = True"),
            "run.py defines DISPOSABLE 2 times")
        self.check_fails(
            lambda d: self.append_to(d, "agent_setup.py", "c.DISPOSABLE = True"),
            "sets DISPOSABLE to True")
        self.check_fails(
            lambda d: self.append_to(d, "run_parts.py", 'state["disposable"] = True'),
            "sets disposable to True")

    def test_m8_os_write_or_terminal_fd(self):
        # M8: os.write(t.fd, b"echo typed\r").
        self.check_fails(
            lambda d: self.append_to(d, "run.py", 'os.write(1, b"x")'),
            "calls os.write")
        self.check_fails(
            lambda d: self.append_to(d, "run.py", '_ = t.fd'),
            "reaches a terminal's descriptor")

    def test_m9_shell_true_or_os_system(self):
        # M9: subprocess.run(..., shell=True) or os.system(...).
        self.check_fails(
            lambda d: self.append_to(d, "run.py", 'run_parts.run_or_timeout([cli, "ls", "--json", "--host", HOST], envx, None, shell=True)'),
            "passes shell=")
        self.check_fails(
            lambda d: self.append_to(d, "run.py", 'os.system("id")'),
            "calls os.system")

    def test_m10_unlisted_extra_module(self):
        # M10: a new non-test module extra_checks.py in test/e2e.
        self.check_fails(
            lambda d: write(os.path.join(d, "extra_checks.py"), 'def bad(c):\n    pass\n'),
            "extra_checks.py is an e2e module in no list of allowed.py")

    def test_item_u_ae_missing_timeout_or_stdin_in_boundary(self):
        self.check_fails(
            lambda d: self.replace_in(d, "boundary.py", "timeout=10, env=self.env())", "env=self.env())"),
            "without timeout=")
        self.check_fails(
            lambda d: self.replace_in(d, "boundary.py", "stdin=_subprocess.DEVNULL, capture_output=True, text=True, timeout=10",
                                      "capture_output=True, text=True, timeout=10"),
            "without stdin=")

    def test_round11_alias_and_boundary_mutants(self):
        # Item 8: aliased imports, from-imports, and boundary.<mod> / boundary._* bypasses fail allowed.check().
        for code, needle in [
            ('import subprocess as sp\nsp.run(["id"], timeout=5)', "imports ['subprocess']"),
            ('from subprocess import run\nrun(["id"], timeout=5)', "imports ['run'] from subprocess"),
            ('import pty as p', "imports ['pty']"),
            ('import harness as h', "imports ['harness']"),
            ('import os as o\no.system("id")', "aliases os as o"),
            ('from os import system\nsystem("id")', "imports ['system'] from os"),
            ('from os import *', "imports ['*'] from os"),
            ('boundary.subprocess.run(["id"], timeout=5)', "reaches boundary.subprocess"),
            ('boundary._subprocess.run(["id"], timeout=5)', "reaches boundary._subprocess"),
            ('import boundary as b\nb._os.system("id")', "reaches boundary._os"),
            ('from boundary import _subprocess', "imports internal ['_subprocess'] from boundary"),
            ('getattr(os, "system")("id")', "calls getattr on os"),
            ('getattr(boundary, "_subprocess")', "calls getattr on boundary"),
        ]:
            with self.subTest(code=code):
                self.check_fails(lambda d, c=code: self.append_to(d, "run.py", c), needle)

    def test_item_v_default_alert_signal_or_memory_cap_on_owner_host(self):
        # Emitting OSC 777, OSC 99, hook notification or --memory-max outside `if DISPOSABLE` fails.
        self.check_fails(
            lambda d: self.append_to(d, "run.py", 'bad_seq = b"\\x1b]777;notify;Title;Body\\x1b\\\\"'),
            "has default-alert signal")
        self.check_fails(
            lambda d: self.append_to(d, "run.py", 'bad_hook = "helper hook notification"'),
            "has default-alert signal")
        self.check_fails(
            lambda d: self.append_to(d, "run.py", 'start("e2e-oom", "/bin/sh", "-c", "sleep 600", flags=("--memory-max", "256M"))'),
            "--memory-max")


if __name__ == "__main__":
    unittest.main()
