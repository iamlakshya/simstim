"""The fixtures and fakes of the offline tests of agent_login.py
(test_agent_login.py and test_agent_login_client.py): the answers of a fake
client, a fake host's rm, a fake terminal, and what `claude auth login`
prints.
"""
import json, os, re, subprocess, sys, types

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

# The helper's rm, as owned.clean_up runs it: an rm by ID, and a session
# that is already gone counts as deleted.
RM = re.compile(r'(~/helper rm \S+) \|\| \[ ! -e "\$\(d=\$\{XDG_STATE_HOME:-\}; .*\)/client/sessions/(\S+)" \]')


def rm_of(cmd):
    """cmd as "~/helper rm <id>" when it is clean_up's rm, else cmd."""
    m = RM.fullmatch(cmd)
    assert m is None or m.group(1).split()[2] == m.group(2), cmd
    return m.group(1) if m else cmd


READY = (0, json.dumps({"v": 1, "agent_checks": [{"kind": "claude", "installed": True, "on_path": True, "login": "no"}]}), "")
MANIFEST = json.dumps({"id": "mine1", "agent": {"kind": "", "argv": ["/bin/sh", "-c", "claude auth login"]}}).encode()

# Characters that a terminal acts on or hides, as the host may send them.
HOSTILE = "\x1b]52;c;aGk=\x07 \u009b2J \u202e \u200b \u2066 \U000E0041 \u061c \x7f \r"


def ls(*sessions, error=None):
    """The answer of `ls --json --host box`."""
    row = {"v": 1, "host": "box", "sessions": list(sessions)}
    if error:
        row["error"] = error
    return (0, json.dumps([row]), "")


def login(sid, state="idle"):
    return {"id": sid, "name": "login-claude", "state": state}


def started(sid="mine1", **kw):
    """The answer of `agents login claude box --json`."""
    obj = {"v": 1, "host": "box", "address": "box/" + sid, "id": sid, "name": "login-claude", "agent": "custom",
           "state": "starting", "alive": True, "started": True}
    obj.update(kw)
    return (0, json.dumps(obj, indent=2) + "\n", "")


class Run:
    """Answers each call of run_cmd by its command: the host doctor, each
    list in turn, and the login's start. A list after the start is after,
    or by default one that shows the session that the start named. Any
    other command fails the test."""
    def __init__(self, doctor=READY, lists=(), start=None, after=None):
        self.doctor, self.lists, self.start, self.calls = doctor, list(lists) or [ls()], start or started(), []
        self.after, self.started = after, False

    def __call__(self, argv, env=None, timeout=60, cwd=None):
        self.calls.append(argv)
        if argv[1:3] == ["host", "doctor"]:
            return self.doctor
        if argv[1] == "ls":
            if self.started:
                answer = self.after or ls(login(json.loads(self.start[1])["id"], "starting"))
            else:
                answer = self.lists.pop(0) if self.lists else ls()
            if answer == "timeout":
                raise subprocess.TimeoutExpired(argv, timeout)
            return answer
        if argv[1:3] == ["agents", "login"]:
            assert argv[3:] == ["claude", "box", "--json"], argv
            self.started = True
            if self.start == "timeout":
                raise subprocess.TimeoutExpired(argv, timeout)
            if self.start == "not utf-8":
                raise UnicodeDecodeError("utf-8", b"\xff", 0, 1, "invalid start byte")
            return self.start
        raise AssertionError("the check ran %s" % argv)


class Host:
    """A host's shell for the clean-up: its sessions, as {id: name}, and
    the helper's rm, which, as on a real host, finds a session by its ID
    alone (cmd/host, host.Delete)."""
    def __init__(self, sessions=None):
        self.sessions, self.ran = dict(sessions or {}), []

    def host_rc(self, cmd, data=None):
        cmd = rm_of(cmd)
        self.ran.append(cmd)
        words = cmd.split()
        assert words[:2] == ["~/helper", "rm"] and len(words) == 3, cmd
        self.sessions.pop(words[2], None)
        return 0  # deleted, or already gone


class Term:
    """A client in a terminal that prints chunks, one for each read, then
    stays quiet. Its exit code is code. fail names a call that raises."""
    made = []

    def __init__(self, argv, env=None, chunks=(), code=0, fail=None):
        self.argv, self.chunks, self.buf = argv, list(chunks), b""
        self.sent, self.closed, self.code, self.fail = [], False, code, fail
        Term.made.append(self)

    def read(self, timeout=0.5):
        if self.chunks:
            self.buf += self.chunks.pop(0)
        return self.buf

    def send(self, b):
        if self.fail == "send":
            raise OSError("the terminal closed")
        self.sent.append(b)

    def exit_code(self, timeout=10):
        if self.fail == "exit_code":
            raise OSError("waitpid failed")
        if b"\x1c" not in self.sent:
            return None
        while self.chunks:
            self.read()
        return self.code

    def close(self):
        self.closed = True


class Ctx:
    cli, HOST, NAME, HELPER = "client", "box", "client", "~/helper"

    def __init__(self, chunks=(), code=0, fail=None, manifest=MANIFEST, host=None):
        self.results, self.ssh = [], []
        self.Term = lambda argv, env=None: Term(argv, env, chunks, code, fail)
        self.manifest = manifest
        self.host = host or Host()
        self.host_rc = self.host.host_rc

    def ok(self, cond, msg):
        self.results.append((bool(cond), msg))
        return cond

    @property
    def failed(self):
        return sum(1 for ok, _ in self.results if not ok)

    def host_ssh(self, cmd):
        """As run.py's, which raises when the command fails (check=True)."""
        self.ssh.append(cmd)
        if isinstance(self.manifest, BaseException):
            raise self.manifest
        return types.SimpleNamespace(stdout=self.manifest, returncode=0)


# The first screen of `claude auth login`, as an attach draws it, in its two
# forms. When its local callback listener starts, it prints "Opening browser
# to sign in…", "If the browser didn't open, visit: <link>" and "Paste code
# here if prompted > ". Here the attach draws the blank cells as cursor
# moves. When the listener cannot start, as on a host with no loopback
# network, it prints a note, "Sign in at: <link>" and "Paste code here > ".
# The note's words here are not the login's own. The link is a terminal
# hyperlink (OSC 8).
LINK = "https://claude.ai/oauth/authorize?code=x"
PROMPT = ("\x1b[HOpening browser to sign in\u2026\r\nIf the browser didn't open, visit: %s\r\n" % LINK).encode() + \
    b"Paste\x1b[1Ccode\x1b[1Chere\x1b[1Cif\x1b[1Cprompted\x1b[1C>\x1b[1C"
PROMPT_NO_LISTENER = ("\x1b[HNote: the sign-in page cannot reach this terminal.\r\nSign in at: \x1b]8;;%s\x07%s\x1b]8;;\x07\r\n"
                      "Paste code here > " % (LINK, LINK)).encode()
# A login that failed, with a help link, and ended.
FAILED = b"\x1b[HAuthentication failed; see https://support.example.com/help\r\n"
DETACHED = b"\r\nDetached. login-claude keeps running on box.\r\n"

# The warnings of the client's debug mode, as it prints them: note() for
# debug_on, and stderrFor's writer for debug_incomplete (internal/cli).
WARNING_ON = '{"v":1,"warning":{"code":"debug_on","message":"debug on: ~/.local/state/client/debug/client-1.jsonl"}}'
WARNING_DONE = '{"v":1,"warning":{"code":"debug_incomplete","message":"the debug file is not complete"}}'
