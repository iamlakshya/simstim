"""The legacy session-verb scenarios on C8's existing offline process seam."""
import contextlib, io, json, os, re, shlex, sys, tempfile, types, unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import boundary, owned, session_verbs as sv  # noqa: E402

checks, clean_up = sv.checks, sv.clean_up
Ctx = types.SimpleNamespace

class _FakeHost:
    """A host for the self-test of the clean-up: its sessions by ID, and the
    helper's rm, which, as on a real host, finds a session by its ID alone
    (TestTheHelpersDeleteTakesAnIDAlone)."""
    def __init__(self, sessions):
        self.sessions = dict(sessions)
        self.ran = []

    def host_rc(self, cmd):
        words = shlex.split(cmd)
        self.ran.append(" ".join(words[:3]) if words[0] != "rm" else cmd)
        if words[:2] == ["rm", "-f"]:
            return 0
        assert words[1] == "rm" and len(words) >= 3, cmd
        return 0 if self.sessions.pop(words[2], None) is not None else 1


class _Done:
    def __init__(self, rc, out="", err=""):
        self.returncode, self.stdout, self.stderr = rc, out, err


class _FakeClient:
    """A client and a host for the self-test of the checks. The host's
    sessions are a map from ID to name, and an ended session is one whose
    name holds "ended". The client finds a session by its ID alone, as it
    finds a word of an ID's form. Every word that it got as an address is in
    addrs."""
    def __init__(self, host, sessions, new_ids, renamed=None):
        self.host, self.sessions, self.new_ids = host, dict(sessions), list(new_ids)
        self.ended, self.addrs, self.last = set(), [], None
        # renamed maps a new ID to the name that another client gives it
        # right after the start.
        self.renamed = renamed or {}

    def find(self, word):
        self.addrs.append(word)
        if word == "-":
            word = self.last or ""
        sid = word.split("/", 1)[1] if word.startswith(self.host + "/") else word
        return sid if sid in self.sessions else None

    def __call__(self, args):
        verb, rest = args[0], args[1:]
        if verb == "ls":
            rows = [{"id": i, "name": n} for i, n in self.sessions.items()]
            return _Done(0, json.dumps([{"v": 1, "host": self.host, "sessions": rows}]))
        if verb == "new":
            if not self.new_ids:
                return _Done(1, "", "Error: no room.")
            sid, name = self.new_ids.pop(0), rest[rest.index("--name") + 1]
            self.sessions[sid] = self.renamed.get(sid, name)
            if "ended" in name:
                self.ended.add(sid)
            return _Done(0, json.dumps({"v": 1, "host": self.host, "id": sid, "name": name, "address": self.host + "/" + sid}))
        target = rest[rest.index("--") + 1] if "--" in rest else rest[0]
        sid = self.find(target)
        if verb == "attach":
            return _Done(2, "", "Error: attach needs a terminal.\n\nTry: x send %s 'text'\n" % target)
        if sid is None:
            return _Done(3, "", "Error: no session.")
        if verb == "show" and "--json" in rest:
            state = "exited" if sid in self.ended else "working"
            return _Done(0, json.dumps({"v": 1, "id": sid, "address": self.host + "/" + sid, "state": state}))
        if verb == "show":
            return _Done(0, "%s on %s\n  ID              %s\n  Attach          x attach --id -- %s/%s\n" % (self.sessions[sid], self.host, sid, self.host, sid))
        if verb == "rename":
            return _Done(2, "", "Error: a name of 8 letters a to z and digits 2 to 7, with a digit, looks like a session ID.")
        if verb in ("rm", "kill"):
            name = self.sessions.pop(sid)
            warn = 'Warning: "kill" is now "rm".\n' if verb == "kill" else ""
            return _Done(0, "", "%sDeleted %s (%s) on %s.\n" % (warn, name, sid, self.host))
        raise AssertionError(args)

    def term(self, argv, env=None):
        t = _FakeTerm(self, argv[1:])
        if self.last and env and "XDG_STATE_HOME" in env:
            folder = os.path.join(env["XDG_STATE_HOME"], "x")
            os.makedirs(folder, exist_ok=True)
            with open(os.path.join(folder, "last.json"), "w") as f:
                json.dump({"host": self.host, "id": self.last}, f)
        return t


class _FakeTerm:
    """A terminal for the self-test, on _FakeClient."""
    def __init__(self, fake, args):
        self.fake, self.args, self.buf, self.code = fake, args, b"", None
        target = args[args.index("--") + 1] if "--" in args else args[1]
        sid = fake.find(target)
        self.sid = sid
        if sid is None:
            self.buf, self.code = b"Error: no session.", 3
        elif args[0] == "rm":
            self.buf = ("Delete %s on %s?\nThis stops shell on %s and removes the session.\nDelete it? [y/N] " % (fake.sessions[sid], fake.host, fake.host)).encode()
        elif sid in fake.ended and "--no-input" in args:
            self.buf, self.code = ("Try: x attach --no-input --restart --id -- %s\n" % target).encode(), 4
        elif sid in fake.ended:
            self.buf = ("Start shell again in %s on %s? [Y/n] " % (fake.sessions[sid], fake.host)).encode()
        else:
            fake.last, self.buf = sid, b"READY-42"

    def wait_for(self, pattern, timeout=20):
        return re.search(pattern, self.buf) is not None

    def send(self, b):
        if b == b"\x1c":
            self.code = 0
        elif b == b"n\r":
            self.buf, self.code = self.buf + b"ended (exit code 3)", 130
        elif self.args[0] == "rm":
            self.code = 130
        else:
            self.fake.ended.discard(self.sid)
            self.fake.last, self.buf = self.sid, self.buf + b"SECOND-RUN"

    def exit_code(self, timeout=10):
        return self.code

    def close(self):
        pass


def _run_fake(sessions, new_ids, renamed=None):
    """Runs the checks and the clean-up against a fake client and host, and
    returns the fake client, the results and the helper's host."""
    fake = _FakeClient("box", sessions, new_ids, renamed)
    helper = _FakeHost({})
    helper.sessions = fake.sessions
    results = []
    c = Ctx(cli="client", HOST="box", NAME="x", HELPER="~/helper", Term=fake.term, host_rc=helper.host_rc, created=[],
            ok=lambda cond, what: results.append((bool(cond), what)) or bool(cond))
    created, deleted = [], set()
    def run_cmd(argv, env=None, timeout=60, cwd=None):
        result = fake(argv[1:])
        return result.returncode, result.stdout, result.stderr
    with tempfile.TemporaryDirectory(prefix="e2e-verbs-test-") as root, \
            mock.patch.object(owned, "run_cmd", run_cmd), mock.patch.object(boundary, "OWNED", set()):
        try:
            checks(c, {"XDG_STATE_HOME": root}, created, deleted, "~/marker", "42")
        finally:
            clean_up(c, created, deleted)
    return fake, results, helper


def _self_test():
    # gone1234 was started by this run, and another client deleted it after
    # the run chose it. other123 is named like it, and must stay.
    fake = _FakeHost({"keepme11": "e2e-verbs", "other123": "gone1234"})
    c = Ctx(HELPER="~/helper", NAME="x", host_rc=fake.host_rc, cli="client", created=["gone1234", "keepme11"], ok=lambda good, text: good)
    clean_up(c, ["gone1234", "keepme11", "deleted1"], {"deleted1"})
    assert fake.sessions == {"other123": "gone1234"}, fake.sessions
    assert fake.ran == ["~/helper rm gone1234", "~/helper rm keepme11"], fake.ran
    print("session_verbs: the clean-up deletes by ID alone, and never twice")

    # An owner has sessions named like this run's. The checks attach to and
    # delete only the new IDs, by host/ID, and pass.
    owner = {"own23456": "e2e-verbs-42", "own34567": "e2e-verbs", "own45678": "e2e-verbs-ended-42"}
    client, results, _ = _run_fake(owner, ["new23456", "new34567"])
    failed = [w for good, w in results if not good]
    assert not failed, failed
    for i, n in owner.items():
        assert client.sessions.get(i) == n, (i, client.sessions)
    for word in client.addrs:
        assert word == "-" or word.split("/")[-1] in ("new23456", "new34567"), word
    assert "new23456" not in client.sessions and "new34567" not in client.sessions, client.sessions
    # new prints an ID that the host had before: the run owns nothing, and
    # attaches to and deletes nothing.
    client, results, helper = _run_fake(owner, ["own23456", "own45678"])
    assert client.sessions == owner and client.addrs == [] and helper.ran == ["rm -f ~/marker"], (client.sessions, client.addrs, helper.ran)
    assert any(not good for good, _ in results)
    # Another client renames the run's first session right after its start:
    # the checks no longer take it for theirs, and the client never names
    # it. The clean-up still deletes it by its ID, through the helper.
    client, results, helper = _run_fake(owner, ["new23456", "new34567"], {"new23456": "e2e-verbs"})
    assert all(w.split("/")[-1] != "new23456" for w in client.addrs), client.addrs
    assert helper.ran.count("~/helper rm new23456") == 1, helper.ran
    print("session_verbs: an owner's session named like this run's is never attached to or deleted")


class SessionVerbs(unittest.TestCase):
    def test_all_legacy_scenarios_and_ownership_controls(self):
        with mock.patch.object(boundary, "OWNED", set()), contextlib.redirect_stdout(io.StringIO()):
            _self_test()

    def test_legacy_or_unowned_ids_never_reach_client_deletion(self):
        for sid in ("gone", "abcd2345", "abcd2345/owner", "\x1bowner"):
            c = Ctx(created=[], HOST="box")
            self.assertIsNone(sv.address_of(c, {}, sid, "name"))


if __name__ == "__main__":
    unittest.main()
