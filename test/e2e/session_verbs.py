"""End-to-end checks for the session verbs (task C5 of the CLI plan): `show`,
`attach` with a session named without its host and on an agent that has
ended, and `rm` with its question.

run.py calls run() with the canonical authenticated disposable context. `E2E_ONLY=session-verbs just e2e` runs
only these. They use a throwaway client config that holds only the test
host, and a throwaway state folder, so the lookup of a session without its
host asks no other host, and the user's own session cache is never read or
changed. They start and delete only their own sessions, which run shells,
never an agent, and whose names hold this run's token. A session is
this run's only by a new ID: the ID that `new --detach --json` prints, which
the host's list did not have before the start. Every attach and every
delete names it by host/ID, and only after the host's list shows that ID
with the name that this run gave it. So an owner's session, of whatever
name, is never attached to or deleted. The clean-up deletes only what the
checks did not delete, through the host helper's own `rm <id>`, which takes
an ID alone and never a name: a session named like that ID is never taken
for it. The restart marker is in the run's nonce-owned folder.

`test_session_verbs.py` runs the checks against a fake client
and host, with no host: an owner's session named like this run's is never
attached to or deleted, and the clean-up deletes by ID alone.
"""
import json, os, re, shutil, tempfile, time

import boundary, owned


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
    boundary.require_disposable(c)
    tmp = tempfile.mkdtemp(prefix="e2e-verbs-", dir=c.root.path)
    cfg = os.path.join(tmp, "config", c.NAME)
    os.makedirs(cfg)
    real = os.path.join(c.root.config, "hosts.toml")
    shutil.copy(real, cfg)
    envx = c.root.env(dict(c.env, XDG_CONFIG_HOME=os.path.join(tmp, "config"), XDG_STATE_HOME=os.path.join(tmp, "state")))
    created, deleted = [], set()
    marker = owned.run_dir() + "/verbs-ran"
    try:
        checks(c, envx, created, deleted, marker, owned.RUN)
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
    remaining = to_clean(created, deleted)
    failed = owned.clean_up(c, remaining)
    c.ok(not failed, "verbs: the remaining owned sessions are deleted by ID")
    for sid in created:
        if sid not in remaining and sid in c.created:
            c.created.remove(sid)
            boundary.disown(sid)
        elif sid in remaining and sid not in c.created:
            owned.own(sid, c.created)


def listed(c, envx):
    """The test host's sessions, as `ls --json` lists them: a map from each
    ID to its name, or None when the host did not answer."""
    rows = owned.sessions_on(c, envx)
    return {s["id"]: s["name"] for s in rows} if rows is not None else None


def start(c, envx, created, name, script):
    """Starts a session, and returns its ID, which created keeps for the
    clean-up. The ID is this run's only when the host's list did not have it
    before the start. A start that failed, or an ID that was there before,
    returns None, and adds nothing."""
    sid, _, _ = owned.start_new(c, envx, created, name, "--", *owned.program("/bin/sh", script))
    for made in created:
        owned.own(made, c.created)
    return sid


def address_of(c, envx, sid, name):
    """host/ID of the session sid, when the host lists it with the name that
    this run gave it, or None. Every attach and every delete of the checks
    takes its address from here, never a name."""
    if not isinstance(sid, str) or not re.fullmatch(r"(?=[a-z2-7]*[2-7])[a-z2-7]{8}", sid):
        return None
    if sid not in c.created or sid not in boundary.OWNED or not owned.exact(c, envx, sid, name):
        return None
    return c.HOST + "/" + sid


def show_checks(c, envx, created, name):
    sid = start(c, envx, created, name, "echo READY-$((6*7)); exec sleep 600")
    addr = address_of(c, envx, sid, name)
    if not c.ok(addr, "verbs: a session to describe started, with a new ID"):
        return None
    # The ID alone, without the host: a word of an ID's form is an ID, never
    # a name, so it finds this session alone.
    code, out, err = owned.run_cmd([c.cli, "show", sid], env=envx, timeout=120)
    c.ok(code == 0 and out.startswith("%s on %s\n" % (name, c.HOST)) and ("  ID              %s\n" % sid) in out
         and ("%s attach --id -- %s\n" % (c.NAME, addr)) in out,
         "verbs: show, with the ID without its host, describes the session and ends with the command that attaches by host/ID (exit %d)" % code)
    code, out, err = owned.run_cmd([c.cli, "show", addr, "--json"], env=envx, timeout=120)
    try:
        js = json.loads(out)
    except ValueError:
        js = {}
    c.ok(code == 0 and js.get("v") == 1 and js.get("id") == sid and js.get("address") == addr and err == "",
         "verbs: show --json prints the session object, and nothing on stderr")
    t = c.Term([c.cli, "attach", "--id", "--", addr], env=envx)
    try:
        c.ok(t.wait_for(rb"READY-42", 30), "verbs: attach by host/ID attaches")
        t.send(b"\x1c")
        c.ok(t.exit_code(10) == 0, "verbs: and detaches")
    finally:
        t.close()
    # The sentinel is safe only after the root's own last.json records this
    # exact owned ID. No other client reads this isolated state folder.
    last = os.path.join(envx["XDG_STATE_HOME"], c.NAME, "last.json")
    try:
        with open(last) as f:
            cached = json.load(f)
    except (OSError, ValueError):
        cached = {}
    if not c.ok(cached.get("host") == c.HOST and cached.get("id") == sid and address_of(c, envx, sid, name),
                "verbs: the isolated last-session cache names this owned ID"):
        return sid
    t = c.Term([c.cli, "attach", "-"], env=envx)
    try:
        c.ok(t.wait_for(rb"READY-42", 30), "verbs: attach - attaches to it again")
        t.send(b"\x1c")
        t.exit_code(10)
    finally:
        t.close()
    code, out, err = owned.run_cmd([c.cli, "attach", "--id", "--", addr], env=envx, timeout=120)
    c.ok(code == 2 and "attach needs a terminal" in err and ("send %s 'text'" % addr) in err,
         "verbs: attach in a script fails with exit code 2, and its Try: line names host/ID (exit %d)" % code)
    code, out, err = owned.run_cmd([c.cli, "rename", addr, "abcd2345"], env=envx, timeout=120)
    c.ok(code == 2 and "looks like a session ID" in err, "verbs: rename refuses a name of the form of an ID (exit %d)" % code)
    return sid


def ended_state(c, envx, sid):
    code, out, err = owned.run_cmd([c.cli, "show", c.HOST + "/" + sid, "--json"], env=envx, timeout=120)
    try:
        return json.loads(out).get("state")
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
    t = c.Term([c.cli, "attach", "--no-input", "--id", "--", addr], env=envx)
    try:
        code = t.exit_code(60)
        c.ok(code == 4 and ("attach --no-input --restart --id -- %s" % addr).encode() in t.buf,
             "verbs: attach on an ended agent at a terminal with --no-input fails with exit code 4, and its Try: line names --restart and host/ID (exit %s)" % code)
    finally:
        t.close()
    t = c.Term([c.cli, "attach", "--id", "--", addr], env=envx)
    try:
        asked = t.wait_for(rb"Start shell again in " + re.escape(("%s on %s" % (name, c.HOST)).encode()) + rb"\? \[Y/n\] ", 60)
        t.send(b"n\r")
        code = t.exit_code(30)
        c.ok(asked and code == 130 and b"ended (exit code 3)" in t.buf, "verbs: no at the question says why the agent ended, and exits with 130 (exit %s)" % code)
    finally:
        t.close()
    t = c.Term([c.cli, "attach", "--id", "--", addr], env=envx)
    try:
        asked = t.wait_for(rb"\[Y/n\] ", 60)
        t.send(b"\r")
        c.ok(asked and t.wait_for(rb"SECOND-RUN", 60), "verbs: Enter at the question starts the agent again, and attaches")
        t.send(b"\x1c")
        c.ok(t.exit_code(10) == 0, "verbs: and detaches")
    finally:
        t.close()
    return sid


def rm_checks(c, envx, shown, ended, deleted, names):
    """Deletes the two sessions that this run started, by host/ID, after the
    host's list shows each with its name, and adds each one deleted to
    deleted."""
    addr = address_of(c, envx, shown, names["shown"])
    if addr:
        t = c.Term([c.cli, "rm", addr], env=envx)
        try:
            asked = t.wait_for(rb"Delete it\? \[y/N\] ", 60)
            t.send(b"\r")
            code = t.exit_code(30)
        finally:
            t.close()
        c.ok(asked and code == 130 and b"This stops shell on " in t.buf, "verbs: rm asks, says what stops, and Enter keeps the session (exit %s)" % code)
        c.ok(ended_state(c, envx, shown) is not None, "verbs: the session is still there")
        code, out, err = owned.run_cmd([c.cli, "rm", addr, "-y"], env=envx, timeout=120)
        if code == 0:
            deleted.add(shown)
        c.ok(code == 0 and out == "" and ("Deleted %s (" % names["shown"]) in err and "Warning" not in err,
             "verbs: rm -y deletes without asking, and says so on stderr")
    addr = address_of(c, envx, ended, names["ended"])
    if addr:
        code, out, err = owned.run_cmd([c.cli, "kill", addr], env=envx, timeout=120)
        if code == 0:
            deleted.add(ended)
        c.ok(code == 0 and err.count("Warning: ") == 1 and '"kill" is now "rm"' in err and ("Deleted %s (" % names["ended"]) in err,
             "verbs: kill still deletes, and warns once that it is now rm")

