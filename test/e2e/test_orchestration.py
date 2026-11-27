"""Offline orchestration and signal-boundary tests for run.py and run_parts.py
(decision11.md items 6 and 9.4): python3 test/e2e/test_orchestration.py.

Executes the real run.py module and run_parts.finish() against fast in-memory
host/client fakes so mutations to run.py's disposable calls, hook_notice,
attach's exact() guard, initial-ls abort, end_check(), pthread_sigmask, or the
finally retry loop each fail a targeted test.
"""
import ast, contextlib, io, json, os, re, shutil, signal, sys, tempfile, time, types, unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fakes import HERE, Ctx, Host, deleted, fake_client, source, write  # noqa: E402
import agent_setup  # noqa: E402
import boundary  # noqa: E402
import disposable  # noqa: E402
import disposable_checks as dc  # noqa: E402
import guided_setup  # noqa: E402
import host_commands, host_git, sandbox_hosts, session_sockets, session_verbs  # noqa: E402
import owned as ow  # noqa: E402
import run_parts as rp  # noqa: E402

ROWS, COLS, ABC = 30, 100, b"ABCDEFGHIJKLMNOPQRSTUVWXYZ"
FILL_WANT = b"".join(b"\x1b[%d;1H" % r + bytes([ABC[(r - 1) % 26]]) * COLS for r in range(1, ROWS + 1)) + b"\x1b[1;1H"
SEQ_BYTES = (b"\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\ "
             b"\x1b[38;2;255;100;0mtruecolor\x1b[0m \x1b[4:3mcurly\x1b[0m "
             b"\x1b]52;c;aGVsbG8=\x1b\\\x1b[?2026h\x1b[?2026l END")


class OrchestrationHarness:
    """In-memory host and terminal fakes for executing source('run.py')."""
    def __init__(self, *, is_disposable=False, shadow_attach=False, fail_initial_ls=False):
        self.is_disposable = is_disposable
        self.shadow_attach = shadow_attach
        self.fail_initial_ls = fail_initial_ls
        self.marker = "ab" * 16
        self.sessions = {}
        self.seq = 0
        self.lock = None
        self.nonce = None
        self.term_calls = []
        self.dc_calls = []
        self.live_terms = {}
        self.ls_count = 0

    def next_sid(self):
        self.seq += 1
        return "sid%05d" % self.seq

    def target_cls(self):
        h = self

        class FakeTarget:
            def __init__(self, root):
                self.root = root

            def machine(self):
                return {"provider": "docker", "id": "c1"} if h.is_disposable else {}

            def marker(self):
                return h.marker if h.is_disposable else ""

            def ssh(self, cmd, data=None):
                return types.SimpleNamespace(stdout=b"", returncode=0)

            def rc(self, cmd, data=None):
                if cmd.startswith("mkdir ~/.e2e-lock"):
                    h.lock = ow.RUN
                    return 0
                if "rmdir ~/.e2e-lock" in cmd:
                    h.lock = None
                    return 0
                if cmd.startswith("mkdir ~/.e2e-run-"):
                    h.nonce = data
                    return 0
                if "rmdir ~/.e2e-run-" in cmd:
                    h.nonce = None
                    return 0
                if " rm " in cmd:
                    for tok in cmd.split():
                        h.sessions.pop(tok, None)
                    return 0
                if cmd == "command -v git >/dev/null":
                    return 1
                return 0

            def out(self, cmd):
                if "printf 'lock:'" in cmd:
                    return ("lock:" + h.lock) if h.lock else "none"
                if "current->" in cmd:
                    return "0.3.0\ncurrent->0.3.0\ne2e-done\n"
                if "MemoryMax" in cmd:
                    return "none\n"
                if "for h in setup resume" in cmd:
                    return "setup\ne2e-done\n"
                return ""

            def drop_link(self, pid):
                t = h.live_terms.get(pid)
                if t and t.kind == "e2e-counter":
                    t.buf += b"".join(b"N%d\r\n" % i for i in range(25, 401))
                return [9999] if pid else []

        return FakeTarget

    def make_term(self, argv, env=None, cols=120, rows=36):
        self.term_calls.append(list(argv))
        h = self
        if argv[1] == "new":
            name = argv[argv.index("--name") + 1]
            sid = self.next_sid()
            self.sessions[sid] = {"id": sid, "name": name, "state": "working", "alive": True,
                                  "full_screen": "e2e-fullscreen" in name}
            if self.shadow_attach and name.startswith("e2e-attach-"):
                self.sessions["shad1234"] = {"id": "shad1234", "name": sid, "state": "idle", "alive": True}
            buf = ("Started box/%s (%s, shell).\r\n" % (sid, name)).encode()
            return FakeTerm("new", 0, buf, h)
        sid = argv[argv.index("--") + 1].split("/", 1)[1]
        name = self.sessions.get(sid, {}).get("name", "")
        kind = next((k for k in ("e2e-attach", "e2e-counter", "e2e-bytes", "e2e-wake",
                                 "e2e-snapshot", "e2e-size", "e2e-fill") if name.startswith(k + "-")), "attach")
        bufs = {
            "e2e-attach": b"MARK-42\r\n",
            "e2e-counter": b"".join(b"N%d\r\n" % i for i in range(1, 25)),
            "e2e-bytes": SEQ_BYTES,
            "e2e-wake": b"T100.0\r\n",
            "e2e-snapshot": b"\x1b[>1u\x1b[?2004hFINAL-SCREEN-MARKER\r\n",
            "e2e-size": b"%d %d\r\n" % (rows, cols),
            "e2e-fill": FILL_WANT,
        }
        return FakeTerm(kind, 0, bufs.get(kind, b""), h)

    def run_cmd(self, argv, env=None, timeout=60, cwd=None):
        w = argv[1:] if argv else []
        if argv and argv[0] == "git":
            return 0, "", ""
        if w == ["version", "--json"]:
            return 0, json.dumps({"v": 1, "helper": "0.3.0"}), ""
        if w == ["host", "alerts", "box", "--json"]:
            return 0, json.dumps({"v": 1, "alerts": {"path": "~/.config/x/alerts.toml", "exists": False}, "log": []}), ""
        if w[:2] == ["host", "doctor"]:
            names = " ".join("%s (full screen)" % s["name"] for s in self.sessions.values() if s.get("full_screen"))
            return 0, names + "\n", ""
        if w[:2] == ["ls", "--json"]:
            self.ls_count += 1
            if self.fail_initial_ls and self.ls_count == 1:
                return 1, "", "ls failed"
            return 0, json.dumps([{"v": 1, "host": "box", "sessions": list(self.sessions.values())}]), ""
        if w[:1] == ["new"] and "--detach" not in w:
            return 2, "", json.dumps({"v": 1, "error": {"kind": "usage", "message": "need --detach"}})
        if w[:1] == ["new"]:
            name = w[w.index("--name") + 1]
            sid = self.next_sid()
            self.sessions[sid] = {"id": sid, "name": name, "state": "idle", "alive": True}
            return 0, json.dumps({"v": 1, "host": "box", "id": sid, "name": name, "address": "box/" + sid}), ""
        if w[:1] == ["pull"]:
            return 1, "", "Error: pull needs SSH"
        raise AssertionError("unexpected run_cmd: %r" % (argv,))


class FakeTerm:
    _next_pid = 40000

    def __init__(self, kind, code, buf, harness):
        FakeTerm._next_pid += 1
        self.pid = FakeTerm._next_pid
        self.kind, self._code, self.buf, self._h = kind, code, buf, harness
        self.woke = False
        harness.live_terms[self.pid] = self

    def read(self, timeout=0.1):
        if self.kind == "e2e-wake" and self.woke:
            self.buf += ("T%.3f\r\n" % time.time()).encode()
        return self.buf

    def wait_for(self, pattern, timeout=20):
        return re.search(pattern, self.buf) is not None

    def send(self, b):
        if b == b"\x1c":
            self.buf += b"\r\nDetached.\r\n"

    def resize(self, cols, rows):
        self.buf += b"%d %d\r\n" % (rows, cols)

    def signal(self, sig):
        if sig == signal.SIGCONT:
            self.woke = True

    def exit_code(self, timeout=10):
        return self._code

    def close(self):
        self._h.live_terms.pop(self.pid, None)


class OrchestrationTests(unittest.TestCase):
    """Items 6 and 9.4: run.py and finish() orchestration mutants."""
    def exec_run_py(self, h, only=None):
        d = tempfile.mkdtemp(prefix="e2e-orch-", dir="/tmp")
        self.addCleanup(shutil.rmtree, d, True)
        with open(os.path.join(os.path.dirname(os.path.dirname(HERE)), "internal/brand/brand.go")) as f:
            brand_name = re.search(r'^const Name = "(.*)"', f.read(), re.M).group(1)
        write(os.path.join(d, "xdg", brand_name, "hosts.toml"), '[[host]]\nname = "box"\nssh = "box"\n')

        saved_env = dict(os.environ)
        saved_argv = list(sys.argv)
        saved_sigs = {s: signal.getsignal(s) for s in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP)}
        patches = [
            (boundary, "Target", h.target_cls()),
            (boundary, "term", h.make_term),
            (ow, "run_cmd", h.run_cmd),
            (time, "sleep", lambda _s: None),
            (agent_setup, "run", lambda c: h.dc_calls.append("agent_setup")),
            (guided_setup, "run", lambda c: h.dc_calls.append("guided_setup")),
            (host_commands, "run", lambda c: h.dc_calls.append("host_commands")),
            (host_git, "run", lambda c: h.dc_calls.append("host_git")),
            (sandbox_hosts, "run", lambda c: h.dc_calls.append("sandbox_hosts")),
            (session_sockets, "run", lambda c: h.dc_calls.append("session_sockets")),
            (session_verbs, "run", lambda c: h.dc_calls.append("session_verbs")),
            (disposable, "debug_check", lambda *a: h.dc_calls.append("debug_check")),
            (disposable, "cleanup", lambda c, state: []),
        ]
        for fn_name in ("kill", "double_detach", "typing", "keys", "paste",
                        "desktop_notification", "memory", "resume", "shadow_terminal", "alerts"):
            patches.append((dc, fn_name, (lambda n: lambda *a, **k: h.dc_calls.append(n))(fn_name)))

        olds = [(obj, attr, getattr(obj, attr)) for obj, attr, _ in patches]
        for obj, attr, val in patches:
            setattr(obj, attr, val)
        os.environ.update(E2E_HOST="box", XDG_CONFIG_HOME=os.path.join(d, "xdg"),
                          E2E_DISPOSABLE="1" if h.is_disposable else "",
                          E2E_DISPOSABLE_MARKER=h.marker if h.is_disposable else "")
        os.environ.pop("E2E_ONLY", None)
        if only:
            os.environ["E2E_ONLY"] = only
        sys.argv = ["run.py", os.path.join(d, "cli")]
        out = io.StringIO()
        code = None
        try:
            with contextlib.redirect_stdout(out):
                try:
                    ns = {"__name__": "__main__", "__file__": os.path.join(HERE, "run.py")}
                    exec(compile(source("run.py"), os.path.join(HERE, "run.py"), "exec"), ns)
                except SystemExit as e:
                    code = e.code
        finally:
            for obj, attr, old_val in reversed(olds):
                setattr(obj, attr, old_val)
            os.environ.clear()
            os.environ.update(saved_env)
            sys.argv[:] = saved_argv
            for s, handler in saved_sigs.items():
                signal.signal(s, handler)
            boundary.CTX = None
            boundary.OWNED.clear()
        return code, out.getvalue()

    def test_run_py_owner_orchestration_prints_hook_notice_and_runs_cleanly(self):
        h = OrchestrationHarness(is_disposable=False)
        code, said = self.exec_run_py(h)
        self.assertEqual(code, 0, said)
        self.assertIn("NOTE: the host's own hooks run in each session that the run starts, as in every session there: setup", said)
        self.assertNotIn("typing", h.dc_calls)
        self.assertIsNone(h.lock)
        self.assertIsNone(h.nonce)

    def test_run_py_disposable_orchestration_invokes_typing_and_all_disposable_checks(self):
        h = OrchestrationHarness(is_disposable=True)
        code, said = self.exec_run_py(h)
        self.assertEqual(code, 0, said)
        for expected in ("kill", "double_detach", "typing", "keys", "paste", "desktop_notification",
                         "debug_check", "memory", "resume", "shadow_terminal", "alerts", "guided_setup",
                         "host_commands", "host_git", "sandbox_hosts", "session_sockets", "session_verbs"):
            self.assertIn(expected, h.dc_calls, said)

    def test_explicit_mutating_suite_on_owner_fails_before_its_entry_point(self):
        for suite in ("host-commands", "host-git", "sandbox-hosts", "session-sockets", "session-verbs"):
            with self.subTest(suite=suite):
                h = OrchestrationHarness(is_disposable=False)
                code, said = self.exec_run_py(h, only=suite)
                self.assertEqual(code, 1, said)
                self.assertIn(suite + " needs an authenticated disposable host; no check ran", said)
                self.assertEqual(h.dc_calls, [])
                self.assertIsNone(h.lock)
                self.assertIsNone(h.nonce)

    def test_each_selected_disposable_suite_uses_the_canonical_context(self):
        for suite, module in (("host-commands", "host_commands"), ("host-git", "host_git"),
                              ("sandbox-hosts", "sandbox_hosts"), ("session-sockets", "session_sockets"),
                              ("session-verbs", "session_verbs")):
            with self.subTest(suite=suite):
                h = OrchestrationHarness(is_disposable=True)
                code, said = self.exec_run_py(h, only=suite)
                self.assertEqual(code, 0, said)
                self.assertEqual(h.dc_calls, [module])
                self.assertIsNone(h.lock)
                self.assertIsNone(h.nonce)

    def test_run_py_attach_refuses_to_open_terminal_when_exact_fails(self):
        h = OrchestrationHarness(is_disposable=False, shadow_attach=True)
        code, said = self.exec_run_py(h)
        self.assertEqual(code, 1, said)
        attach_targets = [a[a.index("--") + 1] for a in h.term_calls if a[1] == "attach"]
        self.assertNotIn("box/sid00002", attach_targets)
        self.assertIn("FAIL attach: box/sid00002", said)

    def test_run_py_aborts_before_starting_sessions_when_initial_ls_fails(self):
        h = OrchestrationHarness(is_disposable=False, fail_initial_ls=True)
        code, said = self.exec_run_py(h)
        self.assertEqual(code, 1, said)
        self.assertEqual(h.term_calls, [])
        self.assertIn("FAIL the list of the host's sessions before the run succeeded", said)

    def test_finish_invokes_end_check_and_fails_on_helper_or_slice_drift(self):
        for state, helpers_now, slice_now, want in [
            ({"before": set(), "helpers": ["0.3.0", "current->0.3.0"], "slice": "none"},
             "0.3.0\n0.4.0\ncurrent->0.4.0\ne2e-done\n", "none\n", "helper versions on box changed"),
            ({"before": set(), "helpers": ["0.3.0", "current->0.3.0"], "slice": "1000"},
             "0.3.0\ncurrent->0.3.0\ne2e-done\n", "2000\n", "slice limit on box changed"),
        ]:
            with self.subTest(want=want):
                host = Host()
                c = Ctx(host, lock=ow.RUN)
                c.host_out = lambda cmd, h=helpers_now, s=slice_now: (
                    "lock:" + ow.RUN if "printf 'lock:'" in cmd else h if "current->" in cmd else s)
                with fake_client(host) as out:
                    clean = rp.finish(c, [], dict(state))
                self.assertFalse(clean)
                self.assertIn(want, out.getvalue())

    def test_finish_pthread_sigmask_blocks_signal_during_handler_install(self):
        # Injects the remaining un-ignored stop signal inside the first
        # signal.signal(s, SIG_IGN) call of rp.finish(): without pthread_sigmask
        # blocking stop first, the signal fires before SIG_IGN is installed on it.
        host = Host({"mine1234": ow.e2e_name("e2e-x")})
        c = Ctx(host, lock=ow.RUN)
        real_sig = signal.signal
        stop_sigs = [signal.SIGINT, signal.SIGTERM, signal.SIGHUP]
        saved = {s: signal.getsignal(s) for s in stop_sigs}

        def raise_stop(signum, _frame):
            raise SystemExit(128 + signum)

        fired = [False]

        def hooked_signal(sig, handler):
            prev = real_sig(sig, handler)
            if handler == signal.SIG_IGN and not fired[0]:
                fired[0] = True
                for other in stop_sigs:
                    if other != sig:
                        os.kill(os.getpid(), other)
            return prev

        try:
            for s in stop_sigs:
                real_sig(s, raise_stop)
            signal.signal = hooked_signal
            with fake_client(host):
                clean = rp.finish(c, ["mine1234"], {"before": set()})
        finally:
            signal.signal = real_sig
            for s, hnd in saved.items():
                real_sig(s, hnd)
        self.assertTrue(fired[0])
        self.assertTrue(clean)
        self.assertEqual(deleted(host), ["mine1234"])

    def test_run_py_finally_retries_when_signal_interrupts_before_ignore_signals(self):
        # Executes the real AST finalbody of run.py while injecting a signal
        # interruption on the first entry to ignore_signals(): removing the
        # `for _ in (1, 2, 3, 4)` retry loop leaves cleanup unexecuted.
        tree = ast.parse(source("run.py"))
        try_node = next(n for n in tree.body if isinstance(n, ast.Try))
        final_mod = ast.Module(body=try_node.finalbody, type_ignores=[])
        ast.fix_missing_locations(final_mod)

        host = Host({"mine1234": ow.e2e_name("e2e-x")})
        c = Ctx(host, lock=ow.RUN)
        removed = []
        calls = [0]
        real_ignore = rp.ignore_signals
        saved = {s: signal.getsignal(s) for s in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP)}

        def interrupt_once():
            calls[0] += 1
            if calls[0] == 1:
                raise KeyboardInterrupt
            real_ignore()

        state = {"before": set()}
        created = ["mine1234"]
        ns = {"run_parts": rp, "owned": ow, "C": c, "created": created, "state": state,
              "R": types.SimpleNamespace(remove=lambda: removed.append(True)),
              "HOST": "box", "sys": sys, "traceback": __import__("traceback")}
        rp.ignore_signals = interrupt_once
        try:
            with fake_client(host):
                exec(compile(final_mod, "run.py", "exec"), ns)
        finally:
            rp.ignore_signals = real_ignore
            for s, hnd in saved.items():
                signal.signal(s, hnd)
        self.assertGreaterEqual(calls[0], 2)
        self.assertTrue(state.get("finished"))
        self.assertEqual(deleted(host), ["mine1234"])
        self.assertEqual(removed, [True])


if __name__ == "__main__":
    unittest.main()
