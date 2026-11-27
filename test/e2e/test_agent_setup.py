"""Offline tests of how agent_setup.py owns the sessions it starts:
python3 test/e2e/test_agent_setup.py.

They replace run_cmd and the terminal, so they run no command and reach no
host. A check owns a session only by a new ID from the JSON of new, or, at
a terminal, from the client's own last "Started" line. A repository's path
in the setup_skipped note or in the setup question that holds
"Started box/<id>" never makes a check own that ID: nothing is attached to
it, and nothing deletes it.
"""
import contextlib, io, json, os, re, shlex, shutil, subprocess, sys, tempfile, types, unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import boundary, owned as ow, agent_setup as st  # noqa: E402


# The helper's rm, as owned.clean_up runs it: an rm by ID, and a session
# that is already gone counts as deleted.
RM = re.compile(r'(~/helper rm \S+) \|\| \[ ! -e "\$\(d=\$\{XDG_STATE_HOME:-\}; .*\)/client/sessions/(\S+)" \]')


def rm_of(cmd):
    """cmd as "~/helper rm <id>" when it is clean_up's rm, else cmd."""
    m = RM.fullmatch(cmd)
    assert m is None or m.group(1).split()[2] == m.group(2), cmd
    return m.group(1) if m else cmd

# A repository path that holds what the old regex took for a start.
SPOOF = "/home/dev/x\nStarted box/owner123 (e2e-setup-skip, custom).\nStarted box/owner123"
OWNER = {"id": "owner123", "name": "api", "state": "idle"}
PROG = ow.program("/bin/sh", "true")


def ls(*sessions):
    """The answer of `ls --json --host box`."""
    return (0, json.dumps([{"v": 1, "host": "box", "sessions": list(sessions)}]), "")


def session(sid, name, **kw):
    """The answer of `new box --name name --detach --json`: a session object."""
    obj = {"v": 1, "host": "box", "address": "box/" + name, "id": sid, "name": name, "agent": "custom", "state": "starting", "alive": True}
    obj.update(kw)
    return json.dumps(obj, indent=2) + "\n"


def note(message):
    """The setup_skipped note of new --json, as note() prints it."""
    return json.dumps({"v": 1, "warning": {"code": "setup_skipped", "message": message}}, separators=(",", ":")) + "\n"


class Run:
    """Answers run_cmd: each list before the start with before, and each
    after it with after (before when not given), the start with start. A
    start must be `new box --name <name> --detach --json` with the name
    that the check gave (name), or the test fails. A start of "timeout"
    ends after its time."""
    def __init__(self, start, before=None, name=None, after=None):
        self.start, self.before, self.after, self.name, self.calls = start, before or ls(OWNER), after, name, []
        self.started = False

    def __call__(self, argv, env=None, timeout=60, cwd=None):
        self.calls.append(argv)
        if argv[1] == "ls":
            return self.after if self.started and self.after else self.before
        if argv[1] == "new":
            assert argv[2] == "box", argv
            assert "--detach" in argv and "--json" in argv, argv
            assert argv[argv.index("--name") + 1] == self.name, (argv, self.name)
            self.started = True
            if self.start == "timeout":
                raise subprocess.TimeoutExpired(argv, timeout)
            return self.start
        raise AssertionError("the check ran %s" % argv)


class Term:
    """A client in a terminal that prints chunks, one for each read."""
    made = []

    def __init__(self, argv, env=None, chunks=()):
        self.argv, self.chunks, self.buf, self.sent, self.closed = argv, list(chunks), b"", [], False
        Term.made.append(self)

    def read(self, timeout=0.5):
        if self.chunks:
            self.buf += self.chunks.pop(0)
        return self.buf

    def wait_for(self, pattern, timeout=20):
        while self.chunks and not re.search(pattern, self.buf):
            self.read()
        return re.search(pattern, self.buf) is not None

    def send(self, b):
        self.sent.append(b)
        if b == b"y\r":
            self.chunks.insert(0, b"y\r\n")  # the echo of the answer comes first

    def exit_code(self, timeout=10):
        while self.chunks:
            self.read()
        return 0

    def close(self):
        self.closed = True


class Ctx:
    cli, HOST, NAME, HELPER = "client", "box", "client", "~/helper"
    DISPOSABLE = False  # a host that the run did not make
    ok = staticmethod(lambda cond, msg: cond)

    def __init__(self, chunks=()):
        self.ran = []
        self.Term = lambda argv, env=None: Term(argv, env, chunks)

    def host_rc(self, cmd, data=None):
        self.ran.append(rm_of(cmd))
        return 0

    def host_ssh(self, cmd, data=None):
        code = self.host_rc(cmd, data)
        if code:
            raise boundary.CalledProcessError(code, cmd)
        return types.SimpleNamespace(stdout=b"", returncode=code)


class Ownership(unittest.TestCase):
    def setUp(self):
        Term.made = []

    def run_with(self, run, fn):
        old, ow.run_cmd = ow.run_cmd, run
        try:
            with contextlib.redirect_stdout(io.StringIO()):
                return fn()
        finally:
            ow.run_cmd = old

    def start(self, start, name="e2e-setup-skip", before=None):
        c, created = Ctx(), []
        sid, out, err = self.run_with(Run(start, before, name=name), lambda: ow.start_new(c, {}, created, name, "--dir", "~/e2e-setup-repo", "--", *PROG))
        return c, created, sid, err

    def test_a_start_of_its_own(self):
        c, created, sid, err = self.start((0, session("new12345", "e2e-setup-skip"), note("Skipping the setup script of ~/e2e-setup-repo: ...")))
        self.assertEqual((sid, created), ("new12345", ["new12345"]))

    def test_a_path_in_the_setup_skipped_note(self):
        # The note holds "Started box/owner123" from a crafted path. The ID
        # comes from the JSON on stdout alone: the new session, never the
        # owner's. When the start fails, the check owns nothing.
        c, created, sid, err = self.start((0, session("new12345", "e2e-setup-skip"), note("Skipping the setup script of %s: ..." % SPOOF)))
        self.assertEqual((sid, created), ("new12345", ["new12345"]))
        self.assertIn("owner123", err)
        for failed in [(1, "", note("Skipping the setup script of %s: ..." % SPOOF) + '{"v":1,"error":{"message":"x"}}\n'),
                       (0, "", note("Skipping the setup script of %s: ..." % SPOOF))]:
            with self.subTest(failed=failed):
                c, created, sid, err = self.start(failed)
                self.assertEqual((sid, created), (None, []))

    def test_an_answer_that_is_no_new_start_of_its_own(self):
        for name, start in {
            "the ID of a session that ran before": (0, session("owner123", "e2e-setup-skip"), ""),
            "another name": (0, session("new12345", "api"), ""),
            "another host": (0, session("new12345", "e2e-setup-skip", host="other"), ""),
            "a version that is true": (0, session("new12345", "e2e-setup-skip", v=True), ""),
            "an error object with an id": (0, json.dumps({"v": 1, "error": {"message": "x"}, "id": "new12345", "host": "box", "name": "e2e-setup-skip"}), ""),
            "an ID that a host does not make": (0, session("../x", "e2e-setup-skip"), ""),
            "text": (0, "Started box/owner123 (e2e-setup-skip, custom).\n", ""),
        }.items():
            with self.subTest(name):
                c, created, sid, err = self.start(start)
                self.assertEqual((sid, created), (None, []))
        # A name that is no e2e-* name is never the check's.
        c, created, sid, err = self.start((0, session("new12345", "api"), ""), name="api")
        self.assertEqual((sid, created), (None, []))

    def test_a_failed_list_owns_nothing(self):
        c, created, sid, err = self.start((0, session("new12345", "e2e-setup-skip"), ""), before=(1, "", "Error"))
        self.assertEqual((sid, created), (None, []))

    def test_a_path_in_the_setup_question(self):
        # At a terminal, new asks; the question shows the crafted path. The
        # check takes the client's own last "Started" line, after the yes.
        question = ("The repository %s on box has a setup script:\r\n\r\n    %s/.client/setup\r\n\r\nRun it? [y/N] " % (SPOOF.replace("e2e-setup-skip", "e2e-setup-ask"), "/x")).replace("\n", "\r\n")
        chunks = [question.encode()]
        for tail, want in [(b"\x1b[?25hStarted box/new12345 (e2e-setup-ask, custom).\r\n", "new12345"), (b"", None)]:
            with self.subTest(tail=tail):
                Term.made, c, created = [], Ctx(chunks + [tail]), []
                sid, asked = self.run_with(Run(None), lambda: st.start_asked(c, {}, created, "e2e-setup-ask", "--dir", "~/e2e-setup-repo", "--", *PROG))
                self.assertTrue(asked)
                self.assertEqual(sid, want)
                self.assertEqual(created, [want] if want else [])
                self.assertTrue(Term.made[0].closed)
                self.assertEqual(Term.made[0].sent, [b"y\r"])

    def test_started_on_terminal(self):
        c, before = Ctx(), [OWNER]
        line = b"Started box/new12345 (e2e-setup-ask, custom).\r\n"
        spoof = b"Started box/owner123 (e2e-setup-ask, custom).\r\n"
        for buf, want in [(spoof + line, "new12345"), (line + spoof, None), (spoof, None),
                          (b"x " + line, None), (line.replace(b"box/", b"other/"), None),
                          (line.replace(b"e2e-setup-ask", b"e2e-setup-skip"), None), (b"Started box/new12345\r\n", None)]:
            with self.subTest(buf=buf):
                self.assertEqual(ow.started_on_terminal(c, buf, "e2e-setup-ask", before), want)

    def test_screen_attaches_only_to_an_exact_address(self):
        # The session is gone, and another is named like its ID; or it is
        # there under another name: no attach.
        for after in [ls({"id": "other123", "name": "new12345", "state": "idle"}), ls({"id": "new12345", "name": "api", "state": "idle"}),
                      ls({"id": "new12345", "name": "e2e-setup-skip", "state": "idle"}, {"id": "other123", "name": "new12345", "state": "idle"})]:
            with self.subTest(after=after):
                Term.made = []
                out = self.run_with(Run(None, after), lambda: st.screen(Ctx([b"AGENT-RAN"]), {}, "new12345", "e2e-setup-skip", rb"AGENT-RAN"))
                self.assertEqual((out, Term.made), (b"", []))
        Term.made = []
        out = self.run_with(Run(None, ls({"id": "new12345", "name": "e2e-setup-skip", "state": "idle"})),
                            lambda: st.screen(Ctx([b"AGENT-RAN"]), {}, "new12345", "e2e-setup-skip", rb"AGENT-RAN"))
        self.assertIn(b"AGENT-RAN", out)
        self.assertEqual(Term.made[0].sent, [b"\x1c"])
        self.assertTrue(Term.made[0].closed)

    def test_nothing_of_the_owners_is_deleted(self):
        # What a spoofed start leaves in created is the clean-up's list: the
        # owner's ID is never on it.
        c, created, sid, err = self.start((0, session("new12345", "e2e-setup-skip"), note("Skipping the setup script of %s: ..." % SPOOF)))
        ow.clean_up(c, created)
        self.assertEqual(c.ran, ["~/helper rm new12345"])


class SetupHost:
    """A host for setup_checks: its sessions, {id: name}, and the client's
    answers. Each `new --json` must be for box, with --detach and --json and
    a name of setup_checks, unique to the run. It makes a session with a
    new ID. A start at a terminal makes its session too, as the host does,
    and its client prints tail(sid, name) after the question: by default
    no line of its own. No start makes a worktree."""
    def __init__(self, tail=None):
        self.sessions, self.n, self.ran = {"owner123": "api"}, 0, []
        self.names = {ow.e2e_name("e2e-setup-" + w) for w in ("skip", "ask", "changed", "reask", "fail")}
        self.tail = tail or (lambda sid, name: b"Started box/owner123 (api, custom). and more\r\n")

    def make(self, name):
        self.n += 1
        sid = "new%05d" % self.n
        self.sessions[sid] = name
        return sid

    def run_cmd(self, argv, env=None, timeout=60, cwd=None):
        if argv[1] == "ls":
            rows = [{"id": i, "name": n, "state": "idle"} for i, n in sorted(self.sessions.items())]
            return 0, json.dumps([{"v": 1, "host": "box", "sessions": rows}]), ""
        if argv[1] == "new":
            name = argv[argv.index("--name") + 1]
            assert argv[2] == "box" and "--detach" in argv and "--json" in argv and name in self.names, argv
            assert "--worktree" not in argv, argv
            sid = self.make(name)
            if name == ow.e2e_name("e2e-setup-changed"):
                return 0, session(sid, name), note("Skipping: it changed after you trusted it")
            return 0, session(sid, name), note("Skipping the setup script of ~/e2e-setup-repo: ...") if name == ow.e2e_name("e2e-setup-skip") else ""
        raise AssertionError("the check ran %s" % argv)

    def Term(self, argv, env=None):
        if argv[1] == "new":
            name = argv[argv.index("--name") + 1]
            assert argv[2] == "box" and "--detach" in argv and name in (ow.e2e_name("e2e-setup-ask"), ow.e2e_name("e2e-setup-reask")), argv
            sid = self.make(name)
            question = b"The repository ~/e2e-setup-repo on box has a setup script:\r\n\r\n    ~/e2e-setup-repo/.client/setup\r\n\r\nRun it? [y/N] "
            if name == ow.e2e_name("e2e-setup-reask"):
                question = b"The script changed after you trusted it. " + question
            return Term(argv, env, [question, self.tail(sid, name)])
        return Term(argv, env, [b"AGENT-RAN\r\n"])


class SetupChecks(unittest.TestCase):
    """setup_checks with a start at a terminal that the check cannot own,
    and no setup-trust.json: each check that needs them fails, and none
    goes missing; the start is named, and the session it made is found by
    its name unique to the run, and deleted by ID; nothing ends the suite."""
    def setup(self, host, host_rc=None):
        """Runs setup_checks with host, and returns the checks' results by
        their text, created, and what the checks printed."""
        results, created = [], []
        c = Ctx()
        c.Term, c.env = host.Term, {}
        if host_rc:
            c.host_rc = host_rc
        c.ok = lambda cond, msg: results.append((bool(cond), msg)) or cond
        old, ow.run_cmd = ow.run_cmd, host.run_cmd
        tmp = tempfile.mkdtemp(prefix="e2e-setup-")
        self.addCleanup(shutil.rmtree, tmp, True)
        try:
            with contextlib.redirect_stdout(io.StringIO()) as out:
                st.setup_checks(c, {}, tmp, created)
        finally:
            ow.run_cmd = old
        self.results = results
        return dict((msg, ok) for ok, msg in results), created, out.getvalue()

    def test_a_start_it_cannot_own_and_no_trust_file(self):
        host = SetupHost()
        got, created, said = self.setup(host)
        out = io.StringIO(said)
        self.assertIs(got["setup: new at a terminal starts a session of its own"], False)
        self.assertIs(got["setup: the trusted script ran in the session's terminal, before the program"], False)
        self.assertIs(got["setup: the answer is kept for the repository on the host (no setup-trust.json)"], False)
        self.assertIs(got["setup: new asks before it runs an untrusted repository's script"], True)
        self.assertIn("FAILED START " + ow.e2e_name("e2e-setup-ask"), out.getvalue())
        ask = [i for i, n in host.sessions.items() if n == ow.e2e_name("e2e-setup-ask")]
        self.assertEqual(len(ask), 1)
        self.assertIn(ask[0], created)
        self.assertNotIn("owner123", created)
        self.assertEqual(sorted(created), sorted(i for i in host.sessions if i != "owner123"))

    def test_bytes_that_are_not_utf8_at_the_terminal(self):
        # The client prints its own line of the new session, but a byte
        # before it is not UTF-8: a failed start, named, and the session that
        # it made is found by its unique name and newness. The same line
        # with no such byte gives the session.
        line = lambda sid, name: ("Started box/%s (%s, custom).\r\n" % (sid, name)).encode()
        for tail, owned in [(lambda sid, name: b"\xff" + line(sid, name), False), (line, True)]:
            with self.subTest(owned=owned):
                host = SetupHost(tail=tail)
                got, created, said = self.setup(host)
                self.assertIs(got["setup: new at a terminal starts a session of its own"], owned)
                ask = [i for i, n in host.sessions.items() if n == ow.e2e_name("e2e-setup-ask")]
                self.assertIn(ask[0], created)
                self.assertEqual("new printed bytes that are not UTF-8" in said, not owned)


class OneList(unittest.TestCase):
    """agent_setup.run puts what its checks start on run.py's own list, so
    that run.py's last step deletes it too, when run()'s own clean-up does
    not finish."""
    def test_run_uses_the_runs_lists(self):
        cfg = tempfile.mkdtemp(prefix="e2e-config-")
        self.addCleanup(shutil.rmtree, cfg, True)
        hosts = os.path.join(cfg, "hosts.toml")
        with open(hosts, "w") as f:
            f.write('[[host]]\nname = "box"\nssh = "box"\n')
        root = boundary.Root("client", cli="client", hosts=hosts, host="box", base=cfg)
        self.addCleanup(root.remove)
        saved = {k: getattr(st, k) for k in ("agent_checks", "secret_checks", "setup_checks", "fix_checks")}
        try:
            st.agent_checks, st.fix_checks = (lambda c, envx: None), (lambda c, made=None: None)
            st.secret_checks = lambda c, envx, token, created: created.append("sess1234")
            st.setup_checks = lambda c, envx, tmp, created, made=None: created.append("sess5678")
            c = Ctx()
            c.root, c.env, c.created = root, {}, []
            c.ok = lambda cond, msg: cond
            c.host_rc = lambda cmd, data=None: 1  # the host does not answer: nothing goes
            with contextlib.redirect_stdout(io.StringIO()):
                st.run(c)
        finally:
            for k, v in saved.items():
                setattr(st, k, v)
        self.assertEqual(c.created, ["sess1234", "sess5678"])


class SetupOnAHost(unittest.TestCase):
    """setup_checks with the harness's own shell commands on a local folder
    that stands for the host's home, with XDG_DATA_HOME set in it. The
    repository is in the run's own folder: the owner's ~/e2e-setup-repo,
    ~/.e2e-setup-exit and worktrees stay, after the checks and after their
    clean-up, and the checks make no worktree."""
    OWNER = {"e2e-setup-repo/owner.txt": "mine\n", ".e2e-setup-exit": "0\n", "data/client/worktrees/e2e-setup-repo/owner.txt": "mine\n"}

    def setUp(self):
        if shutil.which("git") is None:
            self.skipTest("no git")
        self.home = tempfile.mkdtemp(prefix="e2e-home-")
        self.addCleanup(shutil.rmtree, self.home, True)
        for rel, text in self.OWNER.items():
            os.makedirs(os.path.dirname(os.path.join(self.home, rel)), exist_ok=True)
            with open(os.path.join(self.home, rel), "w") as f:
                f.write(text)
        env = {"HOME": self.home, "XDG_DATA_HOME": os.path.join(self.home, "data"), "PATH": os.environ.get("PATH", "/usr/bin:/bin")}
        self.host_rc = lambda cmd, data=None: subprocess.run(
            ["sh", "-c", cmd], env=env, input=data, stdin=None if data is not None else subprocess.DEVNULL,
            capture_output=True, timeout=30).returncode

    def owner_files(self):
        out = {}
        for rel in self.OWNER:
            with open(os.path.join(self.home, rel)) as f:
                out[rel] = f.read()
        return out

    def run_checks(self, before=None):
        c = Ctx()
        self.assertTrue(ow.make_run_dir(types.SimpleNamespace(host_rc=self.host_rc, host_out=lambda cmd: "")))
        if before:
            before()
        host = SetupHost()
        results, created = [], []
        c.Term, c.env, c.host_rc = host.Term, {}, self.host_rc
        c.ok = lambda cond, msg: results.append((bool(cond), msg)) or cond
        old, ow.run_cmd = ow.run_cmd, host.run_cmd
        tmp = tempfile.mkdtemp(prefix="e2e-setup-")
        self.addCleanup(shutil.rmtree, tmp, True)
        try:
            with contextlib.redirect_stdout(io.StringIO()):
                st.setup_checks(c, {}, tmp, created)
        finally:
            ow.run_cmd = old
        return dict((m, ok) for ok, m in results)

    def test_the_owners_fixtures_stay(self):
        got = self.run_checks()
        self.assertIs(got["setup: the test repository %s/e2e-setup-repo-%s is made on the host, in the run's own folder (exit 0)" % (ow.run_dir(), ow.RUN)], True)
        self.assertTrue(os.path.isdir(os.path.join(self.home, ".e2e-run-" + ow.RUN, "e2e-setup-repo-" + ow.RUN, ".git")))
        self.assertFalse(os.path.exists(os.path.join(self.home, "data", "client", "worktrees", "e2e-setup-repo-" + ow.RUN)))
        # The clean-up: the run's folder.
        self.assertTrue(ow.remove_run_dir(types.SimpleNamespace(host_rc=self.host_rc)))
        self.assertFalse(os.path.exists(os.path.join(self.home, ".e2e-run-" + ow.RUN)))
        self.assertEqual(self.owner_files(), self.OWNER)

    def test_a_repository_folder_that_is_there(self):
        # A folder at the repository's path that the check did not make:
        # mkdir refuses it, the check fails, and nothing in it changes.
        repo = os.path.join(self.home, ".e2e-run-" + ow.RUN, "e2e-setup-repo-" + ow.RUN)

        def theirs():
            os.makedirs(repo)
            with open(os.path.join(repo, "theirs.txt"), "w") as f:
                f.write("x")
        got = self.run_checks(before=theirs)
        self.assertIs(got["setup: the test repository %s/e2e-setup-repo-%s is made on the host, in the run's own folder (exit 1)" % (ow.run_dir(), ow.RUN)], False)
        self.assertEqual(sorted(os.listdir(repo)), ["theirs.txt"])
        ow.remove_run_dir(types.SimpleNamespace(host_rc=self.host_rc))
        self.assertEqual(self.owner_files(), self.OWNER)


class Fix(unittest.TestCase):
    """fix_checks answers no to every fix of host doctor --fix, also to the
    owner's stale socket's, and makes and removes nothing on the host."""
    def run_fix(self, chunks, code=0):
        FixTerm.made, results = [], []
        c = Ctx()
        c.env = {}
        c.ok = lambda cond, msg: results.append((bool(cond), msg)) or cond
        c.host_rc = c.host_ssh = c.host_out = lambda *a, **k: self.fail("the fix check ran a command on the host: %r" % (a,))
        c.Term = lambda argv, env=None: FixTerm(chunks, code)
        with contextlib.redirect_stdout(io.StringIO()) as out:
            st.fix_checks(c)
        return results, out.getvalue()

    def test_a_no_to_every_fix(self):
        results, said = self.run_fix([b"checks\r\n" + SCROLLBACK, LINGER, SOCKETS, b"\r\nNothing was changed.\r\n"])
        self.assertEqual(FixTerm.made[0].sent, [b"n\r", b"n\r"])
        self.assertEqual([ok for ok, _ in results], [True])
        self.assertTrue(FixTerm.made[0].closed)
        self.assertIn("SKIP stale socket fix", said)

    def test_nothing_to_fix(self):
        results, said = self.run_fix([b"checks\r\n\r\nNothing to fix.\r\n"])
        self.assertEqual((FixTerm.made[0].sent, [ok for ok, _ in results]), ([], [True]))

    def test_a_doctor_that_fails(self):
        results, said = self.run_fix([LINGER, b"\r\nError: the host did not answer\r\n"], code=1)
        self.assertEqual((FixTerm.made[0].sent, [ok for ok, _ in results]), ([b"n\r"], [False]))


if __name__ == "__main__":
    unittest.main()
