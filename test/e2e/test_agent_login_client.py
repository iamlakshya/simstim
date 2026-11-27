"""Offline tests of agent_login.py's prompt check and of start_login with
a real process, a script that prints what a client could print:
python3 test/e2e/test_agent_login_client.py.
"""

import contextlib, io, json, os, re, shutil, subprocess, sys, tempfile, types, unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import agent_login as al  # noqa: E402
import boundary  # noqa: E402
import owned as ow  # noqa: E402

from login_fakes import Ctx, DETACHED, FAILED, HOSTILE, Host, LINK, MANIFEST, PROMPT, PROMPT_NO_LISTENER, READY, RM, Run, Term, WARNING_DONE, WARNING_ON, login, ls, rm_of, started  # noqa: E402


class Prompt(unittest.TestCase):
    """The check passes only on the login's own prompt, and only when the
    login still runs after the detach."""
    def setUp(self):
        Term.made = []
        al.LOGIN_WAIT, al.PROMPT_WAIT, al.EXIT_WAIT = 0.2, 0.2, 0.1

    def results(self, chunks, state):
        c, created = Ctx(chunks), []
        old, ow.run_cmd = ow.run_cmd, Run(after=ls(login("mine1", state)))
        try:
            with contextlib.redirect_stdout(io.StringIO()):
                al.login_checks(c, {}, created)
        finally:
            ow.run_cmd = old
        return [ok for ok, _ in c.results]

    def test_the_login_prompt_and_a_login_that_runs(self):
        for prompt in [PROMPT, PROMPT_NO_LISTENER]:
            with self.subTest(prompt=prompt):
                self.assertEqual(self.results([prompt, DETACHED], "starting"), [True, True, True, True])

    def test_a_failed_login_with_a_help_link(self):
        # The transcript holds a link, but no prompt, and the session ended:
        # the check passes neither line, though the attach ended with 0.
        self.assertEqual(self.results([FAILED, b"\r\nThe session login-claude on box ended (exit code 1).\r\n"], "errored"),
                         [True, True, False, False])

    def test_a_prompt_and_then_an_early_end(self):
        # The prompt showed, but the login ended before the detach.
        self.assertEqual(self.results([PROMPT, DETACHED], "exited"), [True, True, True, False])

    def test_a_link_alone_is_no_prompt(self):
        for link in [LINK.encode() + b"\r\n", PROMPT_NO_LISTENER[:PROMPT_NO_LISTENER.index(b"Paste")]]:
            with self.subTest(link=link):
                self.assertEqual(self.results([link, DETACHED], "starting"), [True, True, False, True])


class RealClient(unittest.TestCase):
    """start_login with the real run_cmd and a real process, a script that
    prints the bytes a client could print: what the check reads is what the
    client wrote, a carriage return included, and a warning's decoded
    strings hold no control or hidden character either."""
    def setUp(self):
        al.LOGIN_WAIT = 60  # a real process: time to start Python
        base = tempfile.mkdtemp(prefix="e2e-login-root-")
        self.addCleanup(shutil.rmtree, base, True)
        hosts = os.path.join(base, "hosts.toml")
        with open(hosts, "w") as f:
            f.write('[[host]]\nname = "box"\nssh = "box"\n')
        self.root = boundary.Root("client", cli="client", hosts=hosts, host="box", base=base)
        self.addCleanup(self.root.remove)
        self.addCleanup(setattr, boundary, "CTX", boundary.CTX)

    def client(self, stdout, stderr):
        d = tempfile.mkdtemp(prefix="fake-client-")
        self.addCleanup(shutil.rmtree, d, True)
        path = os.path.join(d, "client")
        with open(path, "w") as f:
            f.write("#!%s\nimport sys\nsys.stdout.buffer.write(%r)\nsys.stderr.buffer.write(%r)\n" % (sys.executable, stdout, stderr))
        os.chmod(path, 0o700)
        return path

    def test_the_bytes_that_the_client_wrote(self):
        start = started("mine1")[1].encode()
        on = WARNING_ON.encode()
        for name, out, err, want in [
            ("a start", start, b"", "mine1"),
            ("a start with a warning", start, on + b"\n", "mine1"),
            ("a warning that ends in CR LF", start, on + b"\r\n", None),
            ("a form feed", start, b"\x0c", None),
            ("an escaped ESC in a warning", start, on.replace(b"debug on", b"debug\\u001b on") + b"\n", None),
            ("an escaped LF in a warning", start, on.replace(b"debug on", b"debug\\n on") + b"\n", None),
            ("an escaped LF as \\u000a in a warning", start, on.replace(b"debug on", b"debug\\u000a on") + b"\n", None),
            ("an escaped CR in a warning", start, on.replace(b"debug on", b"debug\\r on") + b"\n", None),
            ("an escaped tab in a warning", start, on.replace(b"debug on", b"debug\\t on") + b"\n", None),
            ("an escaped C1 control in a warning", start, on.replace(b"debug on", b"debug\\u009b on") + b"\n", None),
            ("an escaped bidirectional control in a warning", start, on.replace(b"debug on", b"debug\\u202e on") + b"\n", None),
            ("a raw C1 control in a warning", start, on.replace(b"debug on", "debug\u009b on".encode()) + b"\n", None),
            ("stderr that is not UTF-8", start, b"\xff\n", None),
            ("a warning with a byte that is not UTF-8 in its message", start, on.replace(b"debug on", b"debug\xff on") + b"\n", None),
            ("stdout that is not UTF-8", start + b"\xff", b"", None),
            ("a start that ends in CR LF", start.replace(b"\n", b"\r\n"), b"", "mine1"),
        ]:
            with self.subTest(name):
                c = Ctx()
                c.cli = self.client(out, err)
                c.root, c.DISPOSABLE = self.root, True
                boundary.CTX = c
                with contextlib.redirect_stdout(io.StringIO()):
                    sid, why, failed = al.start_login(c, self.root.env(), [], [])
                self.assertEqual(sid, want, why)
                # Every malformed or non-UTF-8 answer fails; only valid starts succeed.
                self.assertEqual(failed, want is None)


if __name__ == "__main__":
    unittest.main()
