"""Offline tests of what the e2e run checks on the host before it starts,
at its end, and in the debug check of a disposable host:
python3 test/e2e/test_host_checks.py.
The helper of this build must be on the host before any client call, every
delete uses that version's own path, the run says which of the host's own
hooks run in its sessions, start_gate checks alerts and the slice limit,
end_check catches helper-version or slice-limit changes, and an interrupted
debug_check is already registered in state and cleaned up by finish.
"""
import json, os, shutil, sys, tempfile, types, unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fakes import Ctx, Host, fake_client, local_shell, write  # noqa: E402
import boundary  # noqa: E402
import disposable  # noqa: E402
import owned as ow  # noqa: E402
import run_parts as rp  # noqa: E402

VERSION = (0, json.dumps({"v": 1, "version": "0.3.0", "protocol": "7", "helper": "0.3.0-abcdef12"}), "")


class Helper(unittest.TestCase):
    """The run never installs or prunes the helper on a host that it did
    not make: it stops when this build's helper is not there, and deletes
    through that version's path, never bin/current."""
    def ready(self, present, disposable_host=False, version=VERSION, cli="client"):
        host = Host()
        calls = []

        def run(argv, env=None, timeout=60, cwd=None):
            calls.append(argv)
            if argv[1:] == ["version", "--json"]:
                return version
            raise AssertionError("a client call before the helper check: %r" % argv)
        host.run_cmd = run
        c = Ctx(host)
        c.cli, c.HELPER = cli, None
        ran = []

        def host_rc(cmd, data=None):
            ran.append(cmd)
            return 0 if present else 1
        c.host_rc = host_rc
        with fake_client(host) as out:
            got = rp.helper_ready(c, {}, disposable_host)
        return got, c, ran, out.getvalue()

    def test_a_helper_that_is_there(self):
        (ok, why), c, ran, _ = self.ready(True)
        self.assertEqual((ok, why), (True, None))
        self.assertEqual(ran, ['test -x "$HOME/.local/share/client/bin/0.3.0-abcdef12/client-host"'])
        self.assertEqual(c.HELPER, '"$HOME/.local/share/client/bin/0.3.0-abcdef12/client-host"')
        self.assertNotIn("current", c.HELPER)

    def test_no_helper_on_someone_elses_host(self):
        (ok, why), c, ran, _ = self.ready(False)
        self.assertFalse(ok)
        self.assertIsNone(c.HELPER)
        self.assertIn("never installs a helper on someone else's machine", why)
        self.assertIn("Try: ask the host's owner to run client host test box. It installs the helper 0.3.0-abcdef12, "
                      "points the current link at it, and removes the helper versions that nothing uses.", why)
        (ok2, why2), _, _, _ = self.ready(False, cli="/opt/my bin/my-cli")
        self.assertFalse(ok2)
        self.assertIn("'/opt/my bin/my-cli' host test box", why2)

    def test_no_helper_on_a_disposable_host(self):
        (ok, why), c, ran, said = self.ready(False, disposable_host=True)
        self.assertEqual((ok, why), (True, None))
        self.assertEqual(c.HELPER, '"$HOME/.local/share/client/bin/0.3.0-abcdef12/client-host"')
        self.assertIn("installs it on this disposable host", said)

    def test_a_version_that_names_no_helper(self):
        for version in [(1, "", "Error"), (0, json.dumps({"v": 1}), ""), (0, json.dumps({"v": 1, "helper": "x; rm -rf ~"}), "")]:
            with self.subTest(version=version):
                (ok, why), _, ran, _ = self.ready(True, version=version)
                self.assertFalse(ok)
                self.assertEqual(ran, [])

    def test_no_client_call_in_the_clean_up_before_the_check(self):
        host = Host()
        host.run_cmd = lambda *a, **k: self.fail("the clean-up ran the client: %r" % (a,))
        c = Ctx(host, lock=ow.RUN)
        with fake_client(host):
            self.assertTrue(rp.finish(c, [], {"client_ok": False}))
        self.assertTrue(any(".e2e-lock" in cmd and "rmdir" in cmd for cmd in host.ran))

    def test_delete_uses_versioned_helper_not_current(self):
        home = tempfile.mkdtemp(prefix="e2e-helper-")
        self.addCleanup(shutil.rmtree, home, True)
        ver_bin = os.path.join(home, ".local", "share", "client", "bin", "0.3.0-abcdef12", "client-host")
        cur_bin = os.path.join(home, ".local", "share", "client", "bin", "current")
        sess = os.path.join(home, ".local", "state", "client", "sessions", "mine1234")
        os.makedirs(sess)
        write(ver_bin, '#!/bin/sh\n[ "$1" = rm ] && rm -rf "$HOME/.local/state/client/sessions/$2"\n')
        os.chmod(ver_bin, 0o755)
        # bin/current points to a broken binary that always fails: clean_up must use bin/<ver>.
        broken = os.path.join(home, ".local", "share", "client", "bin", "broken-host")
        write(broken, "#!/bin/sh\nexit 1\n")
        os.chmod(broken, 0o755)
        os.symlink(broken, cur_bin)
        host = Host()
        host.run_cmd = lambda argv, **k: VERSION if argv[1:] == ["version", "--json"] else self.fail(argv)
        c = Ctx(host)
        c.host_rc, c.host_out = local_shell(home, xdg=False)
        with fake_client(host):
            ok, why = rp.helper_ready(c, {}, False)
            self.assertEqual((ok, why), (True, None))
            _, hpath = rp.helper_path(c, {})
            self.assertIn("0.3.0-abcdef12/client-host", ow.leftover_help(c, "bbbbbb"))
            self.assertEqual(c.HELPER, hpath)
        created = ["mine1234"]
        self.assertEqual(ow.clean_up(c, created), [])
        self.assertEqual(created, [])
        self.assertFalse(os.path.exists(sess))


class GatesAndEndCheck(unittest.TestCase):
    """start_gate checks alerts.toml and the slice limit before the first
    start; end_check detects helper-version or slice-limit changes at the end."""
    def gate_ctx(self, helpers=("0.3.0-abcdef12", "current->0.3.0-abcdef12/client-host"),
                 slice_val="3000000000", alerts_exists=False, doctor_mem=3000000000):
        host = Host()
        calls = []

        def run_cmd(argv, env=None, timeout=60, cwd=None):
            calls.append(argv)
            if argv[1:3] == ["host", "alerts"]:
                return 0, json.dumps({"v": 1, "alerts": {"path": "~/.config/client/alerts.toml", "exists": alerts_exists}, "log": []}), ""
            if argv[1:3] == ["host", "doctor"]:
                return 0, json.dumps({"v": 1, "memory_max": doctor_mem}), ""
            raise AssertionError("unexpected call: %r" % (argv,))
        host.run_cmd = run_cmd
        c = Ctx(host)
        c.state_helpers, c.state_slice = list(helpers) if helpers is not None else None, slice_val

        def host_out(cmd):
            if "readlink" in cmd:
                return "" if c.state_helpers is None else "\n".join(c.state_helpers + ["e2e-done", ""])
            if "cgroup.controllers" in cmd:
                return "" if c.state_slice is None else c.state_slice + "\n"
            return ""
        c.host_out = host_out
        return c, host, calls

    def test_start_gate_stops_when_alerts_exist(self):
        c, host, calls = self.gate_ctx(alerts_exists=True)
        state = {}
        with fake_client(host):
            why = rp.start_gate(c, {}, state)
        self.assertIsNotNone(why)
        self.assertIn("has an alerts file, ~/.config/client/alerts.toml", why)
        self.assertIn("\nTry: ", why)
        self.assertFalse(any(a[1:3] == ["host", "doctor"] for a in calls))

    def test_start_gate_checks_slice_limit(self):
        # slice == "none": no systemd memory controller, so no start changes the slice.
        c, host, calls = self.gate_ctx(slice_val="none")
        state = {}
        with fake_client(host):
            self.assertIsNone(rp.start_gate(c, {}, state))
        self.assertFalse(any(a[1:3] == ["host", "doctor"] for a in calls))

        # slice == "infinity" or mismatched number: start would change it -> stops with Try.
        for val in ("infinity", "2000000000"):
            with self.subTest(slice=val):
                c, host, _ = self.gate_ctx(slice_val=val, doctor_mem=3000000000)
                with fake_client(host):
                    why = rp.start_gate(c, {}, {})
                self.assertIsNotNone(why)
                self.assertIn("from %s to 3000000000" % val, why)
                self.assertIn("\nTry: ", why)

        # slice matches doctor's memory_max -> passes and records state.
        c, host, _ = self.gate_ctx(slice_val="3000000000", doctor_mem=3000000000)
        state = {}
        with fake_client(host):
            self.assertIsNone(rp.start_gate(c, {}, state))
        self.assertEqual(state["slice"], "3000000000")
        self.assertEqual(rp.end_check(c, state), [])

        # Slice changes during the run -> end_check fails and prints the restore command.
        c.state_slice = "2500000000"
        problems = rp.end_check(c, state)
        self.assertEqual(len(problems), 1)
        self.assertIn("systemctl --user set-property client.slice MemoryMax=3000000000", problems[0])

    def test_helper_versions_and_current_link_checked_at_end(self):
        home = tempfile.mkdtemp(prefix="e2e-vers-")
        self.addCleanup(shutil.rmtree, home, True)
        bin_dir = os.path.join(home, ".local", "share", "client", "bin")
        os.makedirs(os.path.join(bin_dir, "0.2.0"))
        os.makedirs(os.path.join(bin_dir, "0.3.0"))
        os.symlink("0.3.0/client-host", os.path.join(bin_dir, "current"))
        c = Ctx(Host())
        c.host_rc, c.host_out = local_shell(home, xdg=False)
        before = rp.helper_versions(c)
        self.assertIn("0.2.0", before)
        self.assertIn("0.3.0", before)
        self.assertIn("current->0.3.0/client-host", before)
        state = {"helpers": before, "slice": "none"}
        self.assertEqual(rp.end_check(c, state), [])

        # Repointing bin/current without adding/removing folders is caught too.
        os.unlink(os.path.join(bin_dir, "current"))
        os.symlink("0.2.0/client-host", os.path.join(bin_dir, "current"))
        problems = rp.end_check(c, state)
        self.assertEqual(len(problems), 1)
        self.assertIn("changed during the run", problems[0])
        self.assertIn("current->0.2.0/client-host", problems[0])

        # Adding a version folder is caught as well.
        os.makedirs(os.path.join(bin_dir, "0.4.0"))
        self.assertIn("0.4.0", rp.end_check(c, state)[0])

    def test_hostile_strings_cleaned_in_start_gate_and_end_check(self):
        evil = "v1\x1b]52;c;aGk=\x07\x1b[2J\x9b31m\u202e\u2066\x07"
        # 1. Hostile doctor memory_max and host name in start_gate.
        c, host, _ = self.gate_ctx(slice_val="3000000000", doctor_mem=evil)
        c.HOST = "box" + evil
        with fake_client(host):
            why = rp.start_gate(c, {}, {})
        self.assertIsNotNone(why)
        self.assertFalse(any(ow.hidden(ch) for ch in why), repr(why))
        for bad in ("\x1b", "\x07", "\x9b", "\u202e", "\u2066"):
            self.assertNotIn(bad, why)

        # 2. Hostile alerts path is rejected by alerts_report, and cleaned even if alerts_report is bypassed.
        c2, host2, _ = self.gate_ctx(alerts_exists=True)
        host2.run_cmd = lambda argv, **k: (0, json.dumps({"v": 1, "alerts": {"path": "~/.config/" + evil + "/alerts.toml", "exists": True}, "log": []}), "")
        with fake_client(host2):
            why_bad = rp.start_gate(c2, {}, {})
        self.assertIn("gave no alerts report", why_bad)
        old_rep = rp.alerts_report
        rp.alerts_report = lambda info: True
        try:
            with fake_client(host2):
                why_bypass = rp.start_gate(c2, {}, {})
        finally:
            rp.alerts_report = old_rep
        self.assertFalse(any(ow.hidden(ch) for ch in why_bypass), repr(why_bypass))

        # 3. Raw state["helpers"] and state["slice"] are kept for equality, while end_check output is sanitized.
        home = tempfile.mkdtemp(prefix="e2e-evil-")
        self.addCleanup(shutil.rmtree, home, True)
        bin_dir = os.path.join(home, ".local", "share", "client", "bin")
        os.makedirs(os.path.join(bin_dir, "0.3.0-\u202d"))
        os.symlink("target-" + evil, os.path.join(bin_dir, "current"))
        ch = Ctx(Host())
        ch.HOST, ch.NAME = "box" + evil, "client"
        ch.host_rc, ch.host_out = local_shell(home, xdg=False)
        before = rp.helper_versions(ch)
        self.assertTrue(any("\x1b" in x for x in before), before)
        state = {"helpers": before, "slice": "3000" + evil}
        # Replace \u202d folder with \u202e (both sanitize to ?, yet raw lists differ!).
        os.rmdir(os.path.join(bin_dir, "0.3.0-\u202d"))
        os.makedirs(os.path.join(bin_dir, "0.3.0-\u202e"))
        orig_out = ch.host_out
        ch.host_out = lambda cmd: ("2500\n" if "cgroup.controllers" in cmd else orig_out(cmd))
        old_sl = rp.slice_limit
        rp.slice_limit = lambda _c: "2500" + evil
        try:
            problems = rp.end_check(ch, state)
        finally:
            rp.slice_limit = old_sl
        self.assertEqual(len(problems), 2, problems)
        for p in problems:
            self.assertFalse(any(ow.hidden(ch) for ch in p), repr(p))
            self.assertNotIn("\n", p)


class OnTheHost(unittest.TestCase):
    """The hooks notice, exact session guard, lock with no token, and the
    debug check's registration and clean-up, on a local folder."""
    def setUp(self):
        self.home = tempfile.mkdtemp(prefix="e2e-home-")
        self.addCleanup(shutil.rmtree, self.home, True)
        self.addCleanup(setattr, ow, "RUN", ow.RUN)
        ow.RUN = "aaaaaa"
        self.c = Ctx(Host(), lock="aaaaaa")
        self.c.host_rc, self.c.host_out = local_shell(self.home)

    def test_the_hosts_own_hooks_and_notice(self):
        self.assertEqual(rp.host_hooks(self.c), [])
        self.assertTrue(rp.hook_notice(self.c).endswith(": none"))
        write(os.path.join(self.home, "xdg", "client", "hooks", "setup"), "#!/bin/sh\n")
        self.assertEqual(rp.host_hooks(self.c), ["setup"])
        self.assertTrue(rp.hook_notice(self.c).endswith(": setup"))
        write(os.path.join(self.home, "xdg", "client", "hooks", "resume"), "#!/bin/sh\n")
        self.assertEqual(rp.host_hooks(self.c), ["setup", "resume"])
        self.assertTrue(rp.hook_notice(self.c).endswith(": setup, resume"))
        self.c.host_out = lambda cmd: ""
        self.assertIsNone(rp.host_hooks(self.c))
        self.assertEqual(rp.hook_notice(self.c), "NOTE: the host did not say which of its own hooks are there")

    def test_on_session_skips_when_gone_or_shadowed_by_name(self):
        name = ow.e2e_name("e2e-attach")
        for sessions, want in [
            ({"mine1234": name}, True),
            ({"other999": "mine1234"}, False),
            ({"mine1234": name, "other999": "mine1234"}, False),
            ({"mine1234": "api"}, False),
            ({}, False),
        ]:
            with self.subTest(sessions=sessions):
                host = Host(sessions)
                c = Ctx(host)
                with fake_client(host):
                    self.assertIs(rp.on_session(c, {}, "mine1234", name, "attach"), want)
                if not want:
                    self.assertEqual(c.results, [(False, "attach: box/mine1234 is the run's own session, and its address is exact")])

    def test_a_lock_with_no_token(self):
        write(os.path.join(self.home, ".e2e-lock", "run"), "")
        ow.RUN = "bbbbbb"
        locked, holder = ow.take_lock(self.c)
        self.assertEqual((locked, holder), (False, ""))
        text = ow.leftover_help(self.c, holder)
        self.assertIn("holds no token yet: a run may be taking it at this moment", text)
        self.assertIn(ow.UNLOCK, text)

    def folder(self):
        return '"%s/client/debug"' % ow.xdg_sh("XDG_STATE_HOME", ".local/state")

    def test_the_debug_listing(self):
        path = os.path.join(self.home, ".local", "state", "client", "debug")
        self.assertEqual(disposable.debug_listing(self.c, self.folder()), set())
        write(os.path.join(path, "   "), "a name of spaces")
        self.assertEqual(disposable.debug_listing(self.c, self.folder()), {"   "})
        self.c.host_out = lambda cmd: ""
        self.assertIsNone(disposable.debug_listing(self.c, self.folder()))

    def test_the_debug_files_go_at_the_end(self):
        path = os.path.join(self.home, ".local", "state", "client", "debug")
        state = {"disposable": True, "debug": {"folder": self.folder(), "before": set()}}
        write(os.path.join(path, "bridge-20261112-101500-77.jsonl"), "x")
        write(os.path.join(path, "bridge-20261112-101500-77-cpu.pprof"), "x")
        with fake_client(Host()):
            self.assertTrue(rp.finish(self.c, [], state))
        self.assertEqual(os.listdir(path), [])
        self.assertFalse(os.path.exists(os.path.join(self.home, ".e2e-lock")))

    def test_debug_check_interrupted_after_attach_is_cleaned_up(self):
        # Interrupt debug_check() right after the attach starts and writes a
        # bridge debug file: state["debug"] was already recorded before the
        # attach, the terminal closes, and finish() deletes the new file.
        base = tempfile.mkdtemp(prefix="e2e-dbg-root-")
        self.addCleanup(shutil.rmtree, base, True)
        hosts = os.path.join(base, "hosts.toml")
        write(hosts, '[[host]]\nname = "box"\nssh = "box"\n')
        root = boundary.Root("client", cli="client", hosts=hosts, host="box", base=base)
        self.addCleanup(root.remove)
        self.c.root, self.c.env = root, {}
        path = os.path.join(self.home, ".local", "state", "client", "debug")
        closed = []

        class InterruptedTerm:
            buf = b""
            def wait_for(self, pat, timeout=30):
                write(os.path.join(path, "bridge-20261112-101500-99.jsonl"), '{"ev":"start"}\n')
                raise KeyboardInterrupt
            def send(self, b): pass
            def exit_code(self, timeout=10): return 0
            def close(self): closed.append(True)

        self.c.Term = lambda argv, env=None: InterruptedTerm()
        state = {"disposable": True}
        ow.take_lock(self.c)
        ow.make_run_dir(self.c)
        with self.assertRaises(KeyboardInterrupt):
            disposable.debug_check(self.c, root.env(), lambda *a: "dbg12345", lambda sid, w: True, b"END", "~/seq", state)
        self.assertEqual(closed, [True])
        self.assertIn("debug", state)
        self.assertTrue(os.path.exists(os.path.join(path, "bridge-20261112-101500-99.jsonl")))
        with fake_client(Host()):
            self.assertTrue(rp.finish(self.c, [], state))
        self.assertEqual(os.listdir(path), [])
        self.assertFalse(os.path.exists(os.path.join(self.home, ".e2e-lock")))

    def test_a_debug_file_that_stays(self):
        ow.take_lock(self.c)
        path = os.path.join(self.home, ".local", "state", "client", "debug")
        state = {"disposable": True, "debug": {"folder": self.folder(), "before": set()}}
        write(os.path.join(path, "a b"), "x")
        with fake_client(Host()) as out:
            self.assertFalse(rp.finish(self.c, [], state))
        self.assertIn("the debug check's files a b are still in the host's debug folder", out.getvalue())
        self.assertTrue(os.path.isdir(os.path.join(self.home, ".e2e-lock")))


class Environ(unittest.TestCase):
    def test_the_debug_variable_goes(self):
        environ = {"CLIENT_DEBUG_FILE": "/tmp/x.jsonl", "PATH": "/bin"}
        self.assertEqual(rp.clean_environ("client", environ), "/tmp/x.jsonl")
        self.assertEqual(environ, {"PATH": "/bin"})
        self.assertIsNone(rp.clean_environ("client", environ))


class Starts(unittest.TestCase):
    def test_every_start_has_its_own_command(self):
        for args in [(), ("--dir", "~/x"), ("--dir", "~/x", "--"), ("--", "true"), ("--", "/bin/sh", "-c", "true"),
                     ("--", "/bin/sh", "-c", "env -i /bin/sh -c \"$1\"", "sh", "true")]:
            with self.subTest(args=args):
                with self.assertRaises(ValueError):
                    ow.command(args)
        prog = ow.program("/bin/sh", "true")
        self.assertEqual(ow.command(["--dir", "~/x", "--"] + prog), ["--dir", "~/x", "--"] + prog)


if __name__ == "__main__":
    unittest.main()
