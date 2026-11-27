"""Offline tests of the checks that run only on a disposable host
(disposable_checks.py, disposable_setup.py): python3 test/e2e/test_disposable.py.
The typing shell reads none of the host user's files, the client's kill
and the agent install run only on a session or agent that the check
proved its own, and the socket fix gets a yes only for the check's own
socket.
"""
import contextlib, io, json, os, shlex, shutil, subprocess, sys, tempfile, time, types, unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fakes import Host, fake_client, read, write  # noqa: E402
from harness import Term  # noqa: E402
import boundary  # noqa: E402
import disposable_checks as dc  # noqa: E402
import disposable_setup as ds  # noqa: E402
import owned as ow  # noqa: E402


def fake_client_script(folder, stdout="", stderr="", code=0):
    """A client that prints stdout and stderr, ends with code, and writes
    its argv to folder/argv, one word a line."""
    path = os.path.join(folder, "client")
    write(path, "#!%s\nimport sys\nwith open(%r, 'a') as f:\n    f.write('\\n'.join(sys.argv[1:]) + '\\n--\\n')\n"
                "sys.stdout.write(%r)\nsys.stderr.write(%r)\nsys.exit(%d)\n" % (sys.executable, os.path.join(folder, "argv"), stdout, stderr, code))
    os.chmod(path, 0o755)
    return path


def bind_disposable(test, c):
    """Gives c a laptop root of its own and binds boundary.CTX for the test."""
    hosts = os.path.join(tempfile.mkdtemp(prefix="e2e-hosts-"), "hosts.toml")
    test.addCleanup(shutil.rmtree, os.path.dirname(hosts), True)
    write(hosts, '[[host]]\nname = "box"\nssh = "box"\n')
    root = boundary.Root("client", c.cli, hosts, "box")
    test.addCleanup(root.remove)
    c.root, c.envx, c.DISPOSABLE = root, root.env(), True
    old = boundary.CTX
    boundary.CTX = c
    test.addCleanup(setattr, boundary, "CTX", old)


class Ctx:
    cli, HOST, NAME, HELPER = "client", "box", "client", "~/helper"
    DISPOSABLE = True
    envx, env = {}, {}

    def __init__(self, host=None):
        self.host = host or Host()
        self.host_rc, self.results, self.names, self.created = self.host.host_rc, [], {}, []

    def ok(self, cond, msg):
        self.results.append((bool(cond), msg))
        return cond


class TypingShell(unittest.TestCase):
    """The shell that the typing checks type into reads no rc file, no
    ~/.inputrc, no PROMPT_COMMAND and no exported function of the user's."""
    def test_none_of_the_users_files(self):
        home = tempfile.mkdtemp(prefix="e2e-home-")
        self.addCleanup(shutil.rmtree, home, True)
        for name in [".bashrc", ".bash_profile", ".profile", ".bash_login", "env.sh"]:
            write(os.path.join(home, name), "touch %s/READ-%s\n" % (home, name))
        write(os.path.join(home, ".inputrc"), '"\\C-xq": "echo MACRO-RAN\\n"\n')
        env = {"HOME": home, "PATH": os.environ.get("PATH", "/usr/bin:/bin"), "TERM": "xterm", "PROMPT_COMMAND": "echo PC-RAN",
               "BASH_FUNC_greet%%": "() {  echo GREET;\n}", "ENV": os.path.join(home, "env.sh"), "BASH_ENV": os.path.join(home, "env.sh")}
        old = dict(os.environ)
        os.environ.clear()  # the terminal's child gets only env
        try:
            t = Term(list(dc.TYPING_SHELL), env=env)
        finally:
            os.environ.update(old)
        try:
            self.assertTrue(t.wait_for(rb"\$ ", 10), t.buf)
            t.send(b"\x18q")
            time.sleep(0.3)
            t.send(b"type greet >/dev/null 2>&1 || echo NO-GREET; echo IN=$INPUTRC; exit\r")
            t.exit_code(10)
        finally:
            t.close()
        out = t.buf
        self.assertIn(b"NO-GREET", out)
        self.assertIn(b"IN=/dev/null", out)
        self.assertNotIn(b"MACRO-RAN", out)
        self.assertNotIn(b"PC-RAN", out)
        self.assertEqual([f for f in os.listdir(home) if f.startswith("READ-")], [])


class Kill(unittest.TestCase):
    """The client's kill runs only on the check's own session, after exact()."""
    def run_kill(self, exact):
        folder = tempfile.mkdtemp(prefix="e2e-client-")
        self.addCleanup(shutil.rmtree, folder, True)
        host = Host({"owner123": "api"})
        R = Ctx(host)
        R.cli = fake_client_script(folder, stderr="Deleted %s (mine1234) on box.\n" % ow.e2e_name("e2e-kill"))
        bind_disposable(self, R)
        R.exact = lambda sid, what: exact
        base_run = host.run_cmd

        def run_cmd(argv, env=None, timeout=60, cwd=None):
            if argv[1] == "kill":
                r = boundary.run(argv, env=env, timeout=timeout, cwd=cwd)
                return r.returncode, r.stdout.decode(), r.stderr.decode()
            return base_run(argv, env=env, timeout=timeout, cwd=cwd)

        host.run_cmd = run_cmd
        with fake_client(host):
            dc.kill(R)
        argv = read(os.path.join(folder, "argv")) or ""
        return R, argv

    def test_a_kill_of_its_own(self):
        R, argv = self.run_kill(True)
        self.assertEqual(argv, "kill\nbox/mine1234\n--\n")
        self.assertTrue(all(ok for ok, _ in R.results), R.results)
        self.assertEqual(R.created, [])

    def test_no_kill_unless_the_address_is_exact(self):
        R, argv = self.run_kill(False)
        self.assertEqual(argv, "")
        self.assertEqual(R.created, ["mine1234"])  # the clean-up deletes it by ID


class Install(unittest.TestCase):
    def test_only_an_agent_that_is_there(self):
        for installed, want in [(True, "agents\ninstall\nclaude\n--host\nbox\n--\n"), (False, "")]:
            with self.subTest(installed=installed):
                folder = tempfile.mkdtemp(prefix="e2e-client-")
                self.addCleanup(shutil.rmtree, folder, True)
                c = Ctx()
                c.cli = fake_client_script(folder, stderr="Claude Code is already installed on box.\n")
                bind_disposable(self, c)
                with contextlib.redirect_stdout(io.StringIO()):
                    ds.install_check(c, {"installed": installed})
                argv = read(os.path.join(folder, "argv")) or ""
                self.assertEqual(argv, want)
                self.assertTrue(all(ok for ok, _ in c.results), c.results)


# The text of `host doctor --fix`, as the client prints it (internal/cli,
# offerFixes): the scrollback with the names of full-screen sessions, then
# each fix in its own block, and its question.
SOCK = "/run/user/1000/client/s/e2estale-%s.sock" % ow.RUN
SCROLLBACK = b"Full-screen sessions:\r\n  why a stale session socket stays\r\n"
LINGER = (b"\r\nFound 2 to fix:\r\n\r\n1. Linger is off, so sessions stop when your last login on the host ends.\r\n"
          b"   Fix: turn linger on.\r\n   Runs: loginctl enable-linger dev\r\n   Fix it? [y/N] ")
FIXED = b"\r\n\xe2\x9c\x93 fixed: 1 stale session socket with no session behind them, in /run/user/1000/client/s\r\n"


def socket_fix(n, paths):
    return (("\r\n%d. %d stale session socket%s with no session behind them, in /run/user/1000/client/s.\r\n"
             "   Fix: remove them; no running session uses them.\r\n   Runs: rm %s\r\n   Fix it? [y/N] ")
            % (n, len(paths), "" if len(paths) == 1 else "s", " ".join(paths))).encode()


class FixTerm:
    """host doctor --fix at a terminal: a chunk for each read, and the end
    once every question has its answer and no chunk is left."""
    made = []

    def __init__(self, chunks):
        self.chunks, self.buf, self.sent, self.closed = list(chunks), b"", [], False
        FixTerm.made.append(self)

    def read(self, timeout=0.5):
        if self.chunks:
            self.buf += self.chunks.pop(0)
        return self.buf

    def send(self, b):
        self.sent.append(b)
        self.buf += b[:-1] + b"\r\n"

    def exit_code(self, timeout=10):
        return 0 if not self.chunks and len(self.sent) == self.buf.count(b"Fix it? [y/N] ") else None

    def close(self):
        self.closed = True


class SocketFix(unittest.TestCase):
    """On a disposable host, host doctor --fix applies the socket fix to the
    check's own stale socket: a yes only to that fix's own block."""
    def test_fix_answer(self):
        own = socket_fix(2, [SOCK])
        for name, text, want in [
            ("the fix of the check's own socket", own, True),
            ("another socket beside the check's own", socket_fix(2, [SOCK, "/run/user/1000/client/s/old.sock"]), False),
            ("another socket alone", socket_fix(2, ["/run/user/1000/client/s/old.sock"]), False),
            ("one socket whose path holds the check's own", socket_fix(2, ["/x" + SOCK]), False),
            ("linger, after a session named like the fix", SCROLLBACK + LINGER, False),
            ("another fix whose text holds the socket's path", LINGER.replace(b"dev", SOCK.encode()), False),
            ("a block with a line more", own.replace(b"   Fix it?", b"   rm %s\r\n   Fix it?" % SOCK.encode()), False),
        ]:
            with self.subTest(name):
                self.assertIs(ds.fix_answer(text, SOCK), want)

    def run_fix(self, chunks, stale="", there=False, lost=False):
        FixTerm.made = []
        c, ran, made = Ctx(), [], []

        def host_rc(cmd, data=None):
            ran.append(cmd)
            if cmd.startswith("[ ! -e "):
                return 1 if there else 0
            return 1 if cmd.startswith("test -e ") else 0
        c.host_rc = host_rc
        c.host_out = lambda cmd: SOCK.rsplit("/", 1)[0] if "printf" in cmd else ""

        def host_ssh(cmd, data=None):
            ran.append(cmd)
            if data == ds.STALE:
                return types.SimpleNamespace(stdout=stale.encode(), returncode=0)
            if lost:
                raise subprocess.CalledProcessError(255, cmd)
            return types.SimpleNamespace(stdout=b"", returncode=0)
        c.host_ssh = host_ssh
        c.Term = lambda argv, env=None: FixTerm(chunks)
        old, ow.run_cmd = ow.run_cmd, lambda *a, **k: (0, json.dumps([{"v": 1, "host": "box", "sessions": [{"id": "new12345", "name": "e2e-x", "alive": True}]}]), "")
        try:
            with contextlib.redirect_stdout(io.StringIO()) as out:
                ds.socket_fix_check(c, made)
        finally:
            ow.run_cmd = old
        return c.results, ran, made, out.getvalue()

    def test_the_fix_of_its_own_socket(self):
        results, ran, made, said = self.run_fix([b"checks\r\n" + SCROLLBACK, LINGER, socket_fix(2, [SOCK]), FIXED])
        self.assertEqual(FixTerm.made[0].sent, [b"n\r", b"y\r"])
        self.assertTrue(all(ok for ok, _ in results), results)
        self.assertEqual(made, [shlex.quote(SOCK)])
        self.assertTrue(FixTerm.made[0].closed)

    def test_another_stale_socket(self):
        results, ran, made, said = self.run_fix([], stale="/run/user/1000/client/s/old.sock\n")
        self.assertIn("SKIP fix: the host has stale sockets of its own", said)
        self.assertEqual((FixTerm.made, made, results), ([], [], []))

    def test_another_socket_that_comes_beside_the_check_own(self):
        results, ran, made, said = self.run_fix([LINGER, socket_fix(2, [SOCK, "/run/user/1000/client/s/old.sock"]), b"\r\nNothing was changed.\r\n"])
        self.assertEqual(FixTerm.made[0].sent, [b"n\r", b"n\r"])
        self.assertIn((False, "fix: host doctor --fix showed the fix of the check's own socket, asked, and fixed it"), results)

    def test_a_path_that_is_there(self):
        # A file at the socket's path that the check did not make: refused,
        # and never recorded, so the clean-up never removes it.
        results, ran, made, said = self.run_fix([], there=True)
        self.assertEqual((made, FixTerm.made), ([], []))
        self.assertFalse(results[-1][0])

    def test_a_lost_answer_of_the_make(self):
        results, ran, made, said = self.run_fix([], lost=True)
        self.assertEqual(made, [shlex.quote(SOCK)])
        self.assertEqual(FixTerm.made, [])

    def test_stale_socket_finder_script(self):
        import socket
        d = tempfile.mkdtemp(prefix="e2e-s-", dir="/tmp")
        self.addCleanup(shutil.rmtree, d, True)
        dead = os.path.join(d, "dead.sock")
        s_dead = socket.socket(socket.AF_UNIX)
        s_dead.bind(dead)
        s_dead.close()
        live = os.path.join(d, "live.sock")
        s_live = socket.socket(socket.AF_UNIX)
        self.addCleanup(s_live.close)
        s_live.bind(live)
        s_live.listen(1)
        write(os.path.join(d, "plain.sock"), "not a socket")
        r = subprocess.run([sys.executable, "-", d], input=ds.STALE, capture_output=True, timeout=10)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.decode().splitlines(), [dead])


class DesktopNotification(unittest.TestCase):
    def test_desktop_notification(self):
        c = Ctx()
        ssh = []
        c.host_ssh = lambda cmd, data=None: ssh.append((cmd, data))
        c.start = lambda base, *argv: "not12345"
        closed, sent = [], []

        class NotifyTerm:
            buf = b"\x1b]777;notify;Title;Body\x1b\\ END"
            def wait_for(self, pat, timeout=30): return True
            def send(self, b): sent.append(b)
            def exit_code(self, timeout=10): return 0
            def close(self): closed.append(True)

        c.attach = lambda sid: NotifyTerm()
        dc.desktop_notification(c)
        self.assertEqual(ssh, [("cat > %s/notify.bin" % ow.run_dir(), b"\x1b]777;notify;Title;Body\x1b\\ END")])
        self.assertTrue(all(ok for ok, _ in c.results), c.results)
        self.assertEqual(sent, [b"\x1c"])
        self.assertEqual(closed, [True])


class MemoryAndResumeCapabilities(unittest.TestCase):
    """Item 4: memory and resume gate on host doctor's systemd + memory caps
    checks, while non-systemd disposable hosts still run exited-session resume."""
    def make_runner(self, provider, systemd_ok, mem_ok):
        c = Ctx()
        c.host_machine = lambda: ({"provider": provider} if provider != "ssh" else {})
        started, terms = [], []
        rows = {"keep1234": {"id": "keep1234", "alive": True, "state": "idle"}}

        def start(base, *argv, flags=()):
            sid = {"e2e-oom": "oomchild", "e2e-oom-agent": "oomagent",
                   "e2e-resume-exit": "resexit1", "e2e-resume": "resoom12"}[base]
            started.append((base, flags))
            c.names[sid] = ow.e2e_name(base)
            rows[sid] = {
                "e2e-oom": {"id": sid, "oom_killed": True, "memory_max": 256 << 20, "state": "idle", "reason": "oom"},
                "e2e-oom-agent": {"id": sid, "oom_killed": True, "state": "errored", "reason": "oom"},
                "e2e-resume-exit": {"id": sid, "alive": False, "state": "exited"},
                "e2e-resume": {"id": sid, "alive": False, "state": "errored", "oom_killed": True, "unit": "u1.service"},
            }[base]
            return sid

        class ResumeTerm:
            def __init__(self, argv, env=None):
                terms.append(argv)
                target = argv[2].split("/", 1)[1]
                if rows[target].get("alive"):
                    self.buf, self._rc = b"Error: session still runs\r\n", 1
                else:
                    self.buf, self._rc = b"Resumed\r\n", 0
                    rows[target] = {"id": target, "alive": True, "state": "working", "oom_killed": False,
                                    "memory_max": 300 << 20, "unit": "u2.service"}
            def exit_code(self, timeout=60): return self._rc
            def close(self): pass

        pgrep_calls = [0]

        def host_rc(cmd, data=None):
            if "pgrep" in cmd:
                pgrep_calls[0] += 1
                return 0 if pgrep_calls[0] == 1 else 1
            return 0

        c.start, c.session_rows, c.Term = start, lambda: rows, ResumeTerm
        c.exact = lambda sid, what: True
        c.host_rc = host_rc
        c.host_out = lambda cmd: str(300 << 20) if "u2.service" in cmd else str(1024 << 20)
        doctor = {"v": 1, "checks": [{"name": "systemd user manager", "ok": systemd_ok},
                                     {"name": "memory caps", "ok": mem_ok}]}
        return c, doctor, started, terms

    def test_docker_without_systemd_skips_memory_and_runs_non_systemd_resume(self):
        c, doctor, started, terms = self.make_runner("docker", False, False)
        old, ow.run_cmd = ow.run_cmd, lambda argv, **kw: (0, json.dumps(doctor), "")
        try:
            with contextlib.redirect_stdout(io.StringIO()) as out:
                dc.memory(c, "keep1234")
                dc.resume(c)
        finally:
            ow.run_cmd = old
        self.assertEqual(started, [("e2e-resume-exit", ())])
        self.assertEqual([t[1:] for t in terms],
                         [["resume", "box/resexit1", "--detach"], ["resume", "box/resexit1", "--detach"]])
        self.assertTrue(c.results and all(ok for ok, _ in c.results), c.results)
        self.assertIn("SKIP memory cap", out.getvalue())
        self.assertIn("SKIP resume memory cap", out.getvalue())

    def test_disposable_host_with_systemd_runs_full_memory_and_resume_across_providers(self):
        for provider in ("ssh", "lima", "docker"):
            with self.subTest(provider=provider):
                c, doctor, started, terms = self.make_runner(provider, True, True)
                old, ow.run_cmd = ow.run_cmd, lambda argv, **kw: (0, json.dumps(doctor), "")
                try:
                    with contextlib.redirect_stdout(io.StringIO()):
                        dc.memory(c, "keep1234")
                        dc.resume(c)
                finally:
                    ow.run_cmd = old
                self.assertEqual([b for b, _ in started],
                                 ["e2e-oom", "e2e-oom-agent", "e2e-resume-exit", "e2e-resume"])
                self.assertIn(["client", "resume", "box/resoom12", "--memory-max", "300M", "--detach"], terms)
                self.assertTrue(len(c.results) >= 12 and all(ok for ok, _ in c.results), c.results)


class AlertsHookSocketPassthrough(unittest.TestCase):
    """Item 7: dc.alerts preserves <NAME>_SOCK and <NAME>_SESSION across env -i
    so `hook notification` delivers needs_input without loading user rc files."""
    def test_alerts_passes_holder_socket_to_hook_notification_in_isolated_env(self):
        import socket, threading
        d = tempfile.mkdtemp(prefix="e2e-alert-", dir="/tmp")
        self.addCleanup(shutil.rmtree, d, True)
        home = os.path.join(d, "home")
        os.makedirs(os.path.join(home, ".e2e-run-" + ow.RUN))
        pwned = os.path.join(d, "rc-ran")
        for rc in (".bashrc", ".profile", "evil.sh"):
            write(os.path.join(home, rc), "touch %s\n" % pwned)
        write(os.path.join(home, ".inputrc"), "set bell-style visible\n")
        alerts_toml = os.path.join(d, "alerts.toml")
        out_file = os.path.join(home, ".e2e-run-" + ow.RUN, "alerts.jsonl")
        sock_path = os.path.join(d, "h.sock")
        seen_env = {}

        srv = socket.socket(socket.AF_UNIX)
        self.addCleanup(srv.close)
        srv.settimeout(5)
        srv.bind(sock_path)
        srv.listen(2)

        def accept_once():
            try:
                conn, _ = srv.accept()
            except OSError:
                return
            with conn:
                payload = json.loads(conn.recv(4096).decode())
            with open(out_file, "a") as f:
                f.write(json.dumps({"v": 1, "event": "needs_input",
                                    "session": {"id": "ask12345", "name": ow.e2e_name("e2e-alert-ask")},
                                    "has_msg": bool(payload.get("message"))}) + "\n")

        t_srv = threading.Thread(target=accept_once, daemon=True)
        t_srv.start()

        helper, env_dump = os.path.join(d, "helper"), os.path.join(d, "hook-env.json")
        write(helper,
              "#!%s\nimport json, os, socket, sys\n"
              "assert sys.argv[1:] == ['hook', 'notification'], sys.argv\n"
              "sock = os.environ.get('CLIENT_SOCK', '')\n"
              "with open(%r, 'w') as f:\n    f.write(json.dumps({'SOCK': sock, 'SESSION': os.environ.get('CLIENT_SESSION', ''),"
              " 'BASH_ENV': os.environ.get('BASH_ENV', ''), 'INPUTRC': os.environ.get('INPUTRC', '')}))\n"
              "if sock:\n    s = socket.socket(socket.AF_UNIX); s.connect(sock); s.sendall(sys.stdin.buffer.read()); s.close()\n"
              % (sys.executable, env_dump))
        os.chmod(helper, 0o755)
        R, procs = Ctx(), []
        R.HELPER, R.state, R.made = shlex.quote(helper), {"made": []}, []
        self.addCleanup(lambda: [((os.killpg(p.pid, 9) if p.poll() is None else None), p.wait()) for p in procs])
        R.start = lambda base, *a: (write(out_file, (read(out_file) or "") + json.dumps(
            {"v": 1, "event": "failed", "exit_code": 3, "summary": "session failed (exit 3)", "session": {"id": "fail1234"}}) + "\n"), "fail1234")[1]
        R.host_rc = lambda cmd, data=None: 0 if ("! -e" in cmd and not os.path.exists(alerts_toml)) or "grep" in cmd or "rm" in cmd else 1
        R.host_out = lambda cmd: "600\n" if cmd.startswith("stat ") else (read(out_file) or "")

        def fake_run_cmd(argv, env=None, timeout=60, cwd=None):
            w = argv[1:]
            if w == ["host", "alerts", "box", "--json"]:
                return 0, json.dumps({"v": 1, "alerts": {"path": alerts_toml, "exists": os.path.exists(alerts_toml)}, "log": []}), ""
            if w[:3] == ["host", "alerts", "set"]:
                write(alerts_toml, "run = %r\n" % w[w.index("--command") + 1])
                return 0, "Alerts are on.\n", ""
            if w[:3] == ["host", "alerts", "test"]:
                write(out_file, (read(out_file) or "") + json.dumps({"v": 1, "event": "test"}) + "\n")
                return 0, "", "✓ command\n"
            if w[:3] == ["host", "alerts", "log"]:
                return 0, "needs_input\nfailed\n", ""
            if w[:3] == ["host", "alerts", "off"]:
                if os.path.exists(alerts_toml):
                    os.remove(alerts_toml)
                return 0, "", "Alerts are off.\n"
            if w == ["host", "alerts", "box"]:
                return 0, "Alerts are off.\n", ""
            if w[:1] == ["ls"]:
                return 0, json.dumps([{"v": 1, "host": "box", "sessions": []}]), ""
            if w[:1] == ["new"]:
                prog_argv = w[w.index("--") + 1:]
                seen_env["prog_argv"] = prog_argv
                holder_env = {"HOME": home, "PATH": os.environ.get("PATH", "/usr/bin:/bin"), "TERM": "xterm",
                              "CLIENT_SOCK": sock_path, "CLIENT_SESSION": "ask12345",
                              "BASH_ENV": os.path.join(home, "evil.sh"), "ENV": os.path.join(home, "evil.sh")}
                procs.append(subprocess.Popen(prog_argv, env=holder_env, stdin=subprocess.DEVNULL,
                                              stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True))
                return 0, json.dumps({"v": 1, "host": "box", "id": "ask12345", "name": w[w.index("--name") + 1], "address": "box/ask12345"}), ""
            raise AssertionError(argv)

        old, ow.run_cmd = ow.run_cmd, fake_run_cmd
        try:
            dc.alerts(R)
        finally:
            ow.run_cmd = old
        t_srv.join(timeout=5)
        self.assertTrue(all(ok for ok, _ in R.results), R.results)
        hook_env = json.loads(read(env_dump))
        self.assertEqual(hook_env, {"SOCK": sock_path, "SESSION": "ask12345", "BASH_ENV": "", "INPUTRC": "/dev/null"})
        self.assertFalse(os.path.exists(pwned))
        import allowed
        self.assertFalse(allowed.isolated(seen_env["prog_argv"], allow_holder=False))
        self.assertTrue(allowed.isolated(seen_env["prog_argv"], allow_holder=True))


if __name__ == "__main__":
    unittest.main()
