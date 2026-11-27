"""Offline regressions for terminal diagnostics and the process boundary."""
import os, shutil, sys, tempfile, types, unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import allowed  # noqa: E402
import allowed_scan  # noqa: E402
import disposable as d  # noqa: E402
import disposable_checks as dc  # noqa: E402
import guided_setup as gs  # noqa: E402
import owned  # noqa: E402


HERE = os.path.dirname(os.path.abspath(__file__))
HOSTILE = "before\x1b]52;c;value\x07\x9b31m\u202ebidi\u200bhidden\U000e0041after"


class Context:
    NAME, HOST, cli, HELPER = "client", "box", "client", "~/helper"
    env, envx = {}, {}

    def __init__(self):
        self.messages = []

    def ok(self, value, message):
        self.messages.append(message)
        return bool(value)


class Diagnostics(unittest.TestCase):
    def assert_clean(self, value):
        self.assertFalse(any(owned.hidden(c) for c in str(value)), repr(value))

    def test_say_cleans_every_line_without_mutating_its_input(self):
        value = HOSTILE + "\n" + HOSTILE
        with mock.patch("builtins.print") as write:
            owned.say(value)
        self.assertEqual(write.call_args.args[0], owned.safe_text(HOSTILE) + "\n" + owned.safe_text(HOSTILE))
        self.assertEqual(value, HOSTILE + "\n" + HOSTILE)
        self.assertTrue(write.call_args.kwargs["flush"])

    def test_debug_cleanup_retains_raw_names_but_cleans_the_report(self):
        c = Context()
        commands = []
        c.host_out = lambda command: HOSTILE + "\ne2e-done"
        c.host_rc = lambda command: commands.append(command) or 0
        folder = "'/debug/" + HOSTILE + "'"
        state = {"debug": {"folder": folder, "before": set()}}
        problems = d.cleanup(c, state)
        self.assertEqual(commands, [], "an unsafe filename was used in a removal")
        self.assertEqual(d.remove_debug(c, state), [HOSTILE])
        self.assertEqual(state["debug"]["folder"], folder)
        self.assertEqual(len(problems), 1)
        self.assertIn(owned.safe_text(HOSTILE), problems[0])
        self.assertIn(owned.safe_text(folder), problems[0])
        self.assert_clean(problems[0])

    def test_debug_command_failures_clean_output_before_ok(self):
        c = Context()
        with tempfile.TemporaryDirectory(prefix="e2e-diagnostic-") as root:
            c.root = types.SimpleNamespace(path=root, dirs={"XDG_STATE_HOME": root})
            c.Term = lambda *args, **kwargs: types.SimpleNamespace(
                wait_for=lambda *args: True, buf=b" END", send=lambda *args: None,
                exit_code=lambda *args: 0, close=lambda: None)
            c.host_out = lambda command: ""
            with mock.patch.object(d, "debug_listing", return_value=set()), \
                    mock.patch.object(d, "remove_debug", return_value=[]), \
                    mock.patch.object(owned, "run_cmd", return_value=(1, HOSTILE, HOSTILE)):
                d.debug_check(c, {}, lambda *args: "mine1234", lambda *args: True, b"", "file", {})
        failures = [m for m in c.messages if "works on a running session" in m]
        self.assertEqual(len(failures), 4)
        for message in failures:
            self.assertIn(owned.safe_text(HOSTILE), message)
            self.assert_clean(message)

    def test_failed_path_removal_cleans_only_the_diagnostic(self):
        c, seen, printed = Context(), [], []
        path = "'/work/" + HOSTILE + "-" + owned.RUN + "'"
        made = [path]

        def fail(command):
            seen.append(command)
            raise RuntimeError(HOSTILE)

        c.host_rc = fail
        with mock.patch.object(owned, "say", side_effect=printed.append):
            left = d.remove_made(c, made)
        self.assertEqual(left, [path])
        self.assertEqual(made, [path])
        self.assertEqual(seen, ["rm -rf -- " + path])
        self.assertIn(owned.safe_text(path), printed[0])
        self.assert_clean(printed[0])

    def test_alert_cleanup_report_cleans_the_retained_host_path(self):
        c = Context()
        c.host_rc = lambda command: 1
        state = {"alerts_file": "/work/" + HOSTILE}
        with mock.patch.object(owned, "say"):
            problems = d.cleanup(c, state)
        self.assertEqual(state["alerts_file"], "/work/" + HOSTILE)
        self.assertEqual(len(problems), 1)
        self.assertIn(owned.safe_text(state["alerts_file"]), problems[0])
        self.assert_clean(problems[0])

    def test_guided_setup_failure_cleans_stdout_and_stderr(self):
        c, printed = Context(), []
        with mock.patch.object(gs, "names", return_value=set()), \
                mock.patch.object(owned, "run_cmd", return_value=(1, HOSTILE, HOSTILE)), \
                mock.patch.object(owned, "say", side_effect=printed.append):
            gs.with_flags(c, {})
        self.assertEqual(len(printed), 1)
        self.assertIn(owned.safe_text(HOSTILE), printed[0])
        self.assert_clean(printed[0])

    def test_guided_setup_new_names_are_compared_raw_and_reported_clean(self):
        c = Context()
        name = "login-" + HOSTILE
        out = ("Connecting to box\nAgents on box\n[ ] Claude Code\n"
               "Best effort, kept working by contributors:\nNothing to install.\n"
               "Done. box is ready.\n  ? Claude Code login unknown\n"
               "Start your first session:\n\n    client new box\n")
        with mock.patch.object(gs, "names", side_effect=[set(), {name}]), \
                mock.patch.object(owned, "run_cmd", return_value=(0, out, "")):
            gs.with_flags(c, {})
        self.assertIn(owned.safe_text(name), c.messages[-1])
        self.assert_clean(c.messages[-1])

    def test_memory_diagnostics_clean_host_fields_without_replacing_them(self):
        c, printed = Context(), []
        child = {"oom_killed": True, "state": HOSTILE, "reason": HOSTILE,
                 "memory_current": HOSTILE, "memory_max": HOSTILE}
        agent = {"oom_killed": True, "state": "errored", "reason": HOSTILE, "signal": HOSTILE}
        rows = {"other123": {"alive": True}, "mine0001": child, "mine0002": agent}
        starts = iter(("mine0001", "mine0002"))
        c.start = lambda *args, **kwargs: next(starts)
        c.session_rows = lambda: rows
        c.host_out = lambda command: HOSTILE
        with mock.patch.object(dc, "has_memory_caps", return_value=True), \
                mock.patch.object(dc.time, "sleep"), \
                mock.patch.object(owned, "say", side_effect=printed.append):
            dc.memory(c, "other123")
        self.assertEqual(child["reason"], HOSTILE)
        self.assertEqual(agent["reason"], HOSTILE)
        for message in c.messages + printed:
            self.assert_clean(message)
        self.assertTrue(any(owned.safe_text(HOSTILE) in m for m in c.messages))

    def test_resume_state_diagnostic_is_clean(self):
        c = Context()
        row = {"state": HOSTILE}
        c.start = lambda *args, **kwargs: "mine0001"
        c.session_rows = lambda: {"mine0001": row}
        c.exact = lambda *args: False
        with mock.patch.object(dc.time, "time", side_effect=[0, 0, 31]), \
                mock.patch.object(dc.time, "sleep"), \
                mock.patch.object(dc, "has_memory_caps", return_value=False), \
                mock.patch.object(owned, "say"):
            dc.resume(c)
        self.assertEqual(row["state"], HOSTILE)
        self.assertEqual(len(c.messages), 1)
        self.assertIn(owned.safe_text(HOSTILE), c.messages[0])
        self.assert_clean(c.messages[0])

    def test_resume_memory_and_unit_diagnostics_are_clean(self):
        c = Context()
        starts = iter(("mine0001", "mine0002"))
        c.start = lambda *args, **kwargs: next(starts)
        resumed = {"alive": True, "state": HOSTILE, "memory_max": HOSTILE, "unit": "unit"}
        rows = iter(({"mine0001": {"state": "exited"}},
                     {"mine0002": {"state": "exited", "oom_killed": True}},
                     {"mine0002": resumed}))
        c.session_rows = lambda: next(rows)
        c.exact = lambda sid, what: sid == "mine0002"
        statuses = iter((0, 1))
        c.host_rc = lambda command: next(statuses)
        c.host_out = lambda command: HOSTILE
        c.Term = lambda *args, **kwargs: types.SimpleNamespace(
            exit_code=lambda *args: 0, close=lambda: None, buf=b"Resumed")
        with mock.patch.object(dc.time, "sleep"), mock.patch.object(dc, "has_memory_caps", return_value=True):
            dc.resume(c)
        self.assertEqual(resumed["state"], HOSTILE)
        self.assertEqual(resumed["memory_max"], HOSTILE)
        self.assertTrue(any("MemoryMax=" in message for message in c.messages))
        for message in c.messages:
            self.assert_clean(message)

    def test_alert_mode_diagnostic_is_clean(self):
        c = Context()
        c.state, c.created, c.names = {}, [], {}
        c.host_rc = lambda command: 0
        c.host_out = lambda command: HOSTILE
        c.start = lambda *args, **kwargs: "mine0002"
        now = iter(range(0, 10000, 100))
        with mock.patch.object(d, "alerts_ready", return_value="/alerts.toml"), \
                mock.patch.object(d, "remove_alerts_file", return_value=True), \
                mock.patch.object(owned, "start_new", return_value=("mine0001", "", "")), \
                mock.patch.object(owned, "run_cmd", return_value=(0, "are on", "command are off")), \
                mock.patch.object(dc.time, "time", side_effect=lambda: next(now)):
            dc.alerts(c)
        modes = [message for message in c.messages if "mode 0600" in message]
        self.assertEqual(len(modes), 1)
        self.assertIn(owned.safe_text(HOSTILE), modes[0])
        self.assert_clean(modes[0])

    def test_shadow_terminal_cleans_host_output(self):
        c, printed = Context(), []
        c.start = lambda *args, **kwargs: "mine0001"
        c.host_out = lambda command: "killed " + HOSTILE
        c.attach = lambda sid: types.SimpleNamespace(
            read=lambda *args: None, buf=b"TICK-42\x1b[>1u", send=lambda *args: None,
            exit_code=lambda *args: 0, close=lambda: None)
        with mock.patch.object(dc.time, "sleep"), mock.patch.object(owned, "say", side_effect=printed.append):
            dc.shadow_terminal(c)
        self.assertEqual(len(printed), 1)
        self.assertIn(owned.safe_text(HOSTILE), printed[0])
        self.assert_clean(printed[0])


class ScannerPrimitives(unittest.TestCase):
    def copy_suite(self):
        folder = tempfile.mkdtemp(prefix="e2e-guard-")
        self.addCleanup(shutil.rmtree, folder, True)
        for name in os.listdir(HERE):
            if name.endswith(".py") and not name.startswith("test_"):
                shutil.copy(os.path.join(HERE, name), os.path.join(folder, name))
        return folder

    def problems(self, code, module="run.py"):
        folder = self.copy_suite()
        path = os.path.join(folder, module)
        with open(path, "a") as f:
            f.write("\n" + code + "\n")
        return allowed_scan.shape_problems(folder)

    def test_baseline_and_ordinary_getattr_stay_allowed(self):
        self.assertEqual(allowed_scan.shape_problems(self.copy_suite()), [])
        self.assertEqual(self.problems('value = getattr(c, "field", None)'), [])
        self.assertEqual(self.problems('import sys\nview = sys.modules["json"]'), [])

    def test_raw_process_modules_cannot_be_imported_under_aliases(self):
        for module in ("posix", "nt", "_posixsubprocess", "ctypes", "multiprocessing", "asyncio"):
            for code in ("import %s as escaped" % module, "from %s import start as escaped" % module):
                with self.subTest(code=code):
                    self.assertTrue(any("imports" in p and module in p for p in self.problems(code)))

    def test_boundary_keeps_its_process_imports(self):
        for module in ("posix", "nt", "_posixsubprocess", "ctypes", "multiprocessing", "asyncio"):
            with self.subTest(module=module):
                self.assertEqual(self.problems("import " + module, module="boundary.py"), [])

    def test_interpreter_calls_and_aliases_are_refused(self):
        for code in (
                'import builtins\nbuiltins.__import__("subprocess")',
                'import builtins as b\nb.__import__("subprocess")',
                'import builtins\nload = builtins.__import__',
                'from builtins import __import__ as load',
                'eval("process()")', 'exec("process()")',
                'import builtins\nbuiltins.eval("process()")',
                'import builtins\nbuiltins.exec("process()")',
                'evaluate = eval', 'from builtins import exec as execute',
        ):
            with self.subTest(code=code):
                problems = self.problems(code)
                self.assertTrue(any("interpreter" in p or "calls" in p for p in problems), problems)
                self.assertFalse(any("defines DISPOSABLE" in p for p in problems), problems)
                self.assertTrue(all(p.startswith("run.py:") for p in problems), problems)

    def test_module_registry_cannot_supply_banned_or_dynamic_modules(self):
        for code in (
                'import sys\nsys.modules["subprocess"].run([])',
                'import sys as s\ns.modules["os"].system("command")',
                'import sys\nsys.modules.get("multiprocessing").Process()',
                'import sys\nmodules = sys.modules',
                'import sys\nsys.modules[unknown].run([])',
                'from sys import modules as registry',
        ):
            with self.subTest(code=code):
                self.assertTrue(any("module" in p for p in self.problems(code)))

    def test_boundary_and_sys_getattr_cannot_hide_a_process_module(self):
        for code in (
                'getattr(boundary, "_subprocess")',
                'import boundary as edge\ngetattr(edge, "_subprocess")',
                'import builtins\nbuiltins.getattr(boundary, "_subprocess")',
                'import sys as s\ngetattr(s, "modules")',
                'from builtins import getattr as lookup', 'lookup = getattr',
        ):
            with self.subTest(code=code):
                self.assertTrue(self.problems(code))


if __name__ == "__main__":
    unittest.main()
