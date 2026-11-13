"""End-to-end checks for the session verbs (task C5 of the CLI plan): `show`,
`attach` with a session named without its host and on an agent that has
ended, and `rm` with its question.

run.py calls run() with its helpers. `E2E_ONLY=session-verbs just e2e` runs
only these. They use a throwaway client config that holds only the test
host, and a throwaway state folder, so the lookup of a session without its
host asks no other host, and the user's own session cache is never read or
changed. They start and delete only their own sessions, which run shells,
never an agent, and whose names hold this run's process ID. A session is
this run's only by a new ID: the ID that `new --detach --json` prints, which
the host's list did not have before the start. Every attach and every
delete names it by host/ID, and only after the host's list shows that ID
with the name that this run gave it. So an owner's session, of whatever
name, is never attached to or deleted. The clean-up deletes only what the
checks did not delete, through the host helper's own `rm <id>`, which takes
an ID alone and never a name: a session named like that ID is never taken
for it. The marker file of the restart check has this run's process ID.

`python3 test/e2e/session_verbs.py` runs the checks against a fake client
and host, with no host: an owner's session named like this run's is never
attached to or deleted, and the clean-up deletes by ID alone.
"""
import json, os, re, shlex, shutil, subprocess, tempfile, time


class Ctx:
    """What the checks need from run.py."""
    def __init__(self, **kw):
        self.__dict__.update(kw)


def only_host(text, host):
    """The [[host]] entry of host in hosts.toml's text, with its tables."""
    for block in re.split(r"(?m)^(?=\[\[host\]\])", text):
        if re.search(r'(?m)^name\s*=\s*"%s"\s*$' % re.escape(host), block):
            return block
    return ""


def run(c):
    tmp = tempfile.mkdtemp(prefix="e2e-verbs-")
    cfg = os.path.join(tmp, "config", c.NAME)
    os.makedirs(cfg)
    real = os.path.join(os.environ.get("XDG_CONFIG_HOME", os.path.expanduser("~/.config")), c.NAME, "hosts.toml")
    with open(os.path.join(cfg, "hosts.toml"), "w") as f:
        f.write(only_host(open(real).read(), c.HOST))
    envx = dict(os.environ, **c.env)
    envx.update(XDG_CONFIG_HOME=os.path.join(tmp, "config"), XDG_STATE_HOME=os.path.join(tmp, "state"))
    created, deleted = [], set()
    marker = "~/.e2e-verbs-ran-%d" % os.getpid()
    try:
        checks(c, envx, created, deleted, marker, "%d" % os.getpid())
    finally:
        clean_up(c, created, deleted)
        c.host_rc("rm -f " + marker)
        shutil.rmtree(tmp, ignore_errors=True)


def checks(c, envx, created, deleted, marker, tag):
    """The checks, on sessions whose names hold tag."""
    names = {"shown": "e2e-verbs-" + tag, "ended": "e2e-verbs-ended-" + tag}
    shown = show_checks(c, envx, created, names["shown"])
    ended = ended_checks(c, envx, created, marker, names["ended"])
    rm_checks(c, envx, shown, ended, deleted, names)


def to_clean(created, deleted):
    """The sessions that the clean-up deletes: those that this run started,
    and that the checks did not delete."""
    return [sid for sid in created if sid not in deleted]


def clean_up(c, created, deleted):
    """Deletes what the checks left, by ID alone, with the host helper's own
    rm. The client's rm would look the word up as an ID, then as a name, so
    an ID that went since could reach a session named like it."""
    for sid in to_clean(created, deleted):
        c.host_rc("%s rm %s" % (c.HELPER, shlex.quote(sid)))


def cli(c, envx, *args):
    if callable(c.cli):  # the self-test's fake client
        return c.cli(list(args))
    return subprocess.run([c.cli, *args], env=envx, stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=120)


def listed(c, envx):
    """The test host's sessions, as `ls --json` lists them: a map from each
    ID to its name, or None when the host did not answer."""
    r = cli(c, envx, "ls", c.HOST, "--json")
    try:
        hosts = json.loads(r.stdout)
    except ValueError:
        return None
    for h in hosts if r.returncode == 0 and isinstance(hosts, list) else []:
        if isinstance(h, dict) and h.get("host") == c.HOST and not h.get("error"):
            return {s.get("id"): s.get("name", "") for s in h.get("sessions") or []}
    return None


def start(c, envx, created, name, script):
    """Starts a session, and returns its ID, which created keeps for the
    clean-up. The ID is this run's only when the host's list did not have it
    before the start. A start that failed, or an ID that was there before,
    returns None, and adds nothing."""
    before = listed(c, envx)
    if before is None:
        return None
    r = cli(c, envx, "new", c.HOST, "--name", name, "--detach", "--json", "--", "/bin/sh", "-c", script)
    try:
        sid = json.loads(r.stdout)["id"]
    except (ValueError, KeyError, TypeError):
        print(r.stdout, r.stderr)
        return None
    if r.returncode != 0 or not sid or sid in before:
        return None
    created.append(sid)
    return sid


def address_of(c, envx, sid, name):
    """host/ID of the session sid, when the host lists it with the name that
    this run gave it, or None. Every attach and every delete of the checks
    takes its address from here, never a name."""
    if not sid:
        return None
    now = listed(c, envx)
    if now is None or now.get(sid) != name:
        return None
    return c.HOST + "/" + sid


def show_checks(c, envx, created, name):
    sid = start(c, envx, created, name, "echo READY-$((6*7)); exec sleep 600")
    addr = address_of(c, envx, sid, name)
    if not c.ok(addr, "verbs: a session to describe started, with a new ID"):
        return None
    # The ID alone, without the host: a word of an ID's form is an ID, never
    # a name, so it finds this session alone.
    r = cli(c, envx, "show", sid)
    c.ok(r.returncode == 0 and r.stdout.startswith("%s on %s\n" % (name, c.HOST)) and ("  ID              %s\n" % sid) in r.stdout
         and ("%s attach %s\n" % (c.NAME, addr)) in r.stdout,
         "verbs: show, with the ID without its host, describes the session and ends with the command that attaches by host/ID (exit %d)" % r.returncode)
    r = cli(c, envx, "show", addr, "--json")
    try:
        js = json.loads(r.stdout)
    except ValueError:
        js = {}
    c.ok(r.returncode == 0 and js.get("v") == 1 and js.get("id") == sid and js.get("address") == addr and r.stderr == "",
         "verbs: show --json prints the session object, and nothing on stderr")
    t = c.Term([c.cli, "attach", addr], env=envx)
    c.ok(t.wait_for(rb"READY-42", 30), "verbs: attach by host/ID attaches")
    t.send(b"\x1c")
    c.ok(t.exit_code(10) == 0, "verbs: and detaches")
    t = c.Term([c.cli, "attach", "-"], env=envx)
    c.ok(t.wait_for(rb"READY-42", 30), "verbs: attach - attaches to it again")
    t.send(b"\x1c")
    t.exit_code(10)
    r = cli(c, envx, "attach", addr)
    c.ok(r.returncode == 2 and "attach needs a terminal" in r.stderr and ("send %s 'text'" % addr) in r.stderr,
         "verbs: attach in a script fails with exit code 2, and its Try: line names host/ID (exit %d)" % r.returncode)
    r = cli(c, envx, "rename", addr, "abcd2345")
    c.ok(r.returncode == 2 and "looks like a session ID" in r.stderr, "verbs: rename refuses a name of the form of an ID (exit %d)" % r.returncode)
    return sid


def ended_state(c, envx, sid):
    r = cli(c, envx, "show", c.HOST + "/" + sid, "--json")
    try:
        return json.loads(r.stdout).get("state")
    except ValueError:
        return None


def ended_checks(c, envx, created, marker, name):
    c.host_rc("rm -f " + marker)
    script = "if [ -e %s ]; then echo SECOND-RUN; exec sleep 600; fi; touch %s; exit 3" % (marker, marker)
    sid = start(c, envx, created, name, script)
    if not c.ok(sid, "verbs: a session whose command ends started, with a new ID"):
        return None
    deadline = time.time() + 30
    while ended_state(c, envx, sid) not in ("exited", "errored") and time.time() < deadline:
        time.sleep(0.5)
    addr = address_of(c, envx, sid, name)
    if not c.ok(addr, "verbs: the host lists the ended session with its name"):
        return sid
    t = c.Term([c.cli, "attach", addr, "--no-input"], env=envx)
    code = t.exit_code(60)
    c.ok(code == 4 and ("attach %s --no-input --restart" % addr).encode() in t.buf,
         "verbs: attach on an ended agent at a terminal with --no-input fails with exit code 4, and its Try: line names --restart and host/ID (exit %s)" % code)
    t = c.Term([c.cli, "attach", addr], env=envx)
    asked = t.wait_for(rb"Start shell again in " + re.escape(("%s on %s" % (name, c.HOST)).encode()) + rb"\? \[Y/n\] ", 60)
    t.send(b"n\r")
    code = t.exit_code(30)
    c.ok(asked and code == 130 and b"ended (exit code 3)" in t.buf, "verbs: no at the question says why the agent ended, and exits with 130 (exit %s)" % code)
    t = c.Term([c.cli, "attach", addr], env=envx)
    asked = t.wait_for(rb"\[Y/n\] ", 60)
    t.send(b"\r")
    c.ok(asked and t.wait_for(rb"SECOND-RUN", 60), "verbs: Enter at the question starts the agent again, and attaches")
    t.send(b"\x1c")
    c.ok(t.exit_code(10) == 0, "verbs: and detaches")
    return sid


def rm_checks(c, envx, shown, ended, deleted, names):
    """Deletes the two sessions that this run started, by host/ID, after the
    host's list shows each with its name, and adds each one deleted to
    deleted."""
    addr = address_of(c, envx, shown, names["shown"])
    if addr:
        t = c.Term([c.cli, "rm", addr], env=envx)
        asked = t.wait_for(rb"Delete it\? \[y/N\] ", 60)
        t.send(b"\r")
        code = t.exit_code(30)
        c.ok(asked and code == 130 and b"This stops shell on " in t.buf, "verbs: rm asks, says what stops, and Enter keeps the session (exit %s)" % code)
        c.ok(ended_state(c, envx, shown) is not None, "verbs: the session is still there")
        r = cli(c, envx, "rm", addr, "-y")
        if r.returncode == 0:
            deleted.add(shown)
        c.ok(r.returncode == 0 and r.stdout == "" and ("Deleted %s (" % names["shown"]) in r.stderr and "Warning" not in r.stderr,
             "verbs: rm -y deletes without asking, and says so on stderr")
    addr = address_of(c, envx, ended, names["ended"])
    if addr:
        r = cli(c, envx, "kill", addr)
        if r.returncode == 0:
            deleted.add(ended)
        c.ok(r.returncode == 0 and r.stderr.count("Warning: ") == 1 and '"kill" is now "rm"' in r.stderr and ("Deleted %s (" % names["ended"]) in r.stderr,
             "verbs: kill still deletes, and warns once that it is now rm")


class _FakeHost:
    """A host for the self-test of the clean-up: its sessions by ID, and the
    helper's rm, which, as on a real host, finds a session by its ID alone
    (TestTheHelpersDeleteTakesAnIDAlone)."""
    def __init__(self, sessions):
        self.sessions = dict(sessions)
        self.ran = []

    def host_rc(self, cmd):
        self.ran.append(cmd)
        words = shlex.split(cmd)
        if words[:2] == ["rm", "-f"]:
            return 0
        assert words[1] == "rm" and len(words) == 3, cmd
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
            return _Done(0, json.dumps({"v": 1, "id": sid, "name": name, "address": self.host + "/" + sid}))
        sid = self.find(rest[0])
        if verb == "attach":
            return _Done(2, "", "Error: attach needs a terminal.\n\nTry: x send %s 'text'\n" % rest[0])
        if sid is None:
            return _Done(3, "", "Error: no session.")
        if verb == "show" and "--json" in rest:
            state = "exited" if sid in self.ended else "working"
            return _Done(0, json.dumps({"v": 1, "id": sid, "address": self.host + "/" + sid, "state": state}))
        if verb == "show":
            return _Done(0, "%s on %s\n  ID              %s\n  Attach          x attach %s/%s\n" % (self.sessions[sid], self.host, sid, self.host, sid))
        if verb == "rename":
            return _Done(2, "", "Error: a name of 8 letters a to z and digits 2 to 7, with a digit, looks like a session ID.")
        if verb in ("rm", "kill"):
            name = self.sessions.pop(sid)
            warn = 'Warning: "kill" is now "rm".\n' if verb == "kill" else ""
            return _Done(0, "", "%sDeleted %s (%s) on %s.\n" % (warn, name, sid, self.host))
        raise AssertionError(args)

    def term(self, argv, env=None):
        return _FakeTerm(self, argv[1:])


class _FakeTerm:
    """A terminal for the self-test, on _FakeClient."""
    def __init__(self, fake, args):
        self.fake, self.args, self.buf, self.code = fake, args, b"", None
        sid = fake.find(args[1])
        self.sid = sid
        if sid is None:
            self.buf, self.code = b"Error: no session.", 3
        elif args[0] == "rm":
            self.buf = ("Delete %s on %s?\nThis stops shell on %s and removes the session.\nDelete it? [y/N] " % (fake.sessions[sid], fake.host, fake.host)).encode()
        elif sid in fake.ended and "--no-input" in args:
            self.buf, self.code = ("Try: x attach %s --no-input --restart\n" % args[1]).encode(), 4
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


def _run_fake(sessions, new_ids, renamed=None):
    """Runs the checks and the clean-up against a fake client and host, and
    returns the fake client, the results and the helper's host."""
    fake = _FakeClient("box", sessions, new_ids, renamed)
    helper = _FakeHost({})
    results = []
    c = Ctx(cli=fake, HOST="box", NAME="x", HELPER="~/helper", Term=fake.term, host_rc=helper.host_rc,
            ok=lambda cond, what: results.append((bool(cond), what)) or bool(cond))
    created, deleted = [], set()
    try:
        checks(c, {}, created, deleted, "~/marker", "42")
    finally:
        clean_up(c, created, deleted)
    return fake, results, helper


def _self_test():
    # gone1234 was started by this run, and another client deleted it after
    # the run chose it. other123 is named like it, and must stay.
    fake = _FakeHost({"keepme11": "e2e-verbs", "other123": "gone1234"})
    c = Ctx(HELPER="~/helper", host_rc=fake.host_rc, cli=None)
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


if __name__ == "__main__":
    _self_test()
