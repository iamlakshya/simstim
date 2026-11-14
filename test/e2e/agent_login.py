"""The end-to-end check of `agents login claude` (cli-spec 12.4), which
agent_setup.py runs only on a disposable host (run.py's DISPOSABLE): the
login session has no name of the run's. It starts Claude Code's own login
in a session on the test host, stops at the login's first prompt, and
never types into it.

It follows the harness's one rule for the sessions it starts (owned.py):

- It never takes ownership from text. It starts the login with
  `agents login --json`, and owns the session only when that command ends
  with 0, prints one JSON object with "started": true and an ID that a host
  makes, prints on stderr nothing but the warnings it may print, and the ID
  is new.
- It attaches only after owned.exact(). The rule of task C5, under which an
  8-character word of the ID alphabet is always an ID, makes the attach
  exact. The list before it is a guard: the host can change after it.
- It deletes only by ID, through the helper's own `rm` (owned.clean_up).

login-claude is the name that cli-spec 12.3 gives the session, so it is no
name unique to the run: after a start that did not answer as expected, the
check names the new login-claude sessions, and deletes none.

It skips when Claude Code is not ready or is logged in, when the list before
the start fails, and when an external login-claude session already runs (or
starts ahead of the check). Any failed or malformed login answer, or an owned
session that vanishes or becomes ambiguous before attach, fails the check
without attaching to or deleting unowned sessions. On every attach path it
detaches, waits, and closes its client. test_agent_login.py tests it offline.
"""
import json, re, time

import owned  # run.py puts this folder on the path

# How long the check waits for the start, for the first prompt, and for the
# client to end after the detach key.
LOGIN_WAIT, PROMPT_WAIT, EXIT_WAIT = 90, 60, 30

# The warnings that `agents login --json` may print on stderr, by code, all
# from the client's debug mode, which a debug file in the environment turns
# on: debug_on, with the file's path (internal/cli/debug.go, startDebug,
# through note), and debug_incomplete, when the file did not finish
# (internal/cli/main.go, through stderrFor). Each is one line of exactly
# {"v": 1, "warning": {"code": <code>, "message": <string>}}.
WARNINGS = ("debug_on", "debug_incomplete")


def plain_text(s):
    """Whether s, a string that the client wrote in a warning, holds no
    control character, a line break included, and nothing that a terminal
    hides. The client's warning writers put every message on one line."""
    return not any(ord(ch) < 0x20 or owned.hidden(ch) for ch in s)


def warning_line(line):
    """Whether line, from stderr, is a warning that agents login --json may
    print, in its exact shape, with no control or hidden character around
    it, and none in its decoded strings either: a JSON escape such as
    \\u001b or \\n in the message is one too."""
    if line != line.strip() or any(owned.hidden(ch) for ch in line):
        return False
    try:
        w = json.loads(line)
    except ValueError:
        return False
    if not (isinstance(w, dict) and set(w) == {"v", "warning"} and owned.version_1(w["v"]) and isinstance(w["warning"], dict)
            and set(w["warning"]) == {"code", "message"} and w["warning"]["code"] in WARNINGS and isinstance(w["warning"]["message"], str)):
        return False
    return plain_text(w["warning"]["message"])


def warnings_only(err):
    """Whether err, the command's stderr, holds only warnings of WARNINGS:
    lines split at a newline alone, each a warning, and none blank but the
    one after the last newline. A form feed, a carriage return or spaces
    on a line of their own are no warning."""
    lines = err.split("\n")
    if lines[-1] == "":
        lines.pop()
    return all(warning_line(line) for line in lines)


def concurrent_login(c, envx, sid, before):
    """Whether sid is a new running login-claude session started externally."""
    if not owned.new_id(sid, before):
        return False
    found = owned.sessions_on(c, envx)
    return found is not None and any(s["id"] == sid and s["name"] == "login-claude" and s.get("state") not in owned.ENDED for s in found)


def start_login(c, envx, before, created):
    """Starts the login with agents login --json. Returns (sid, why, failed).
    The only answer that gives an ID: exit code 0, stdout one JSON object with
    "v": 1, this host, the name login-claude, "started": true, a new valid ID
    and its address, and on stderr only warnings of WARNINGS. Only the race
    where "started" is false for a new running login-claude session skips;
    every other non-start fails, while leaving unowned sessions untouched."""
    try:
        code, out, err = owned.run_cmd([c.cli, "agents", "login", "claude", c.HOST, "--json"], env=envx, timeout=LOGIN_WAIT)
    except owned.TimeoutExpired:
        owned.leftovers(c, envx, "login-claude", before, created, "agents login --json did not end")
        return None, "agents login --json did not end", True
    except ValueError:
        owned.leftovers(c, envx, "login-claude", before, created, "agents login --json printed bytes that are not UTF-8")
        return None, "agents login --json printed bytes that are not UTF-8", True
    if code != 0:
        return None, "agents login --json failed with %d: %s" % (code, owned.safe_text(err[-400:])), True
    try:
        obj = json.loads(out)  # one value, and nothing after it
    except ValueError:
        return None, "agents login --json printed more or less than one JSON value: %s" % owned.safe_text(out[-400:]), True
    if not warnings_only(err):
        return None, "agents login --json printed more than its warnings on stderr: %s" % owned.safe_text(err[-400:]), True
    if not isinstance(obj, dict) or "error" in obj or not owned.version_1(obj.get("v")) or obj.get("host") != c.HOST or obj.get("name") != "login-claude":
        return None, "agents login --json printed no login session: %s" % owned.safe_text(out[-400:]), True
    sid = obj.get("id")
    if not isinstance(sid, str) or not owned.ID.fullmatch(sid) or obj.get("address") != c.HOST + "/" + sid:
        return None, "agents login --json printed a session ID that a host does not make: %s" % owned.safe_text(out[-400:]), True
    if obj.get("started") is False:
        if concurrent_login(c, envx, sid, before):
            return None, "a login-claude session started on the host before the check's own, and the check left it alone", False
        return None, "agents login --json said started=false without a new running login-claude session: %s" % owned.safe_text(out[-400:]), True
    if obj.get("started") is not True:
        return None, "agents login --json did not say that it started the login: %s" % owned.safe_text(out[-400:]), True
    if not owned.new_id(sid, before):
        return None, "the start named %s/%s, a session that ran before it, and the check left it alone" % (c.HOST, sid), True
    return sid, None, False


def manifest_argv(c, sid):
    """The command of the session sid, from its manifest on the host, as
    one string, or None when the manifest did not read: the session went
    away, or the host did not answer. A failed read never ends the suite.
    The path holds sid, which start_login checked."""
    try:
        m = c.host_ssh("cat ~/.local/state/%s/sessions/%s/manifest.json" % (c.NAME, sid))
        return " ".join(json.loads(m.stdout)["agent"]["argv"])
    except (owned.CalledProcessError, owned.TimeoutExpired, OSError, ValueError, KeyError, TypeError):
        return None


def login_checks(c, envx, created):
    """Runs the check. It adds to created only the ID of the session that
    its own start printed in its JSON, when that ID is new, so that the
    caller's clean_up deletes nothing else."""
    doctor = owned.json_of([c.cli, "host", "doctor", c.HOST, "--json"], envx)
    checks = doctor.get("agent_checks") if isinstance(doctor, dict) else None
    claude = next((a for a in checks or [] if isinstance(a, dict) and a.get("kind") == "claude"), {})
    if not (claude.get("installed") and claude.get("on_path")):
        owned.say("SKIP login: Claude Code is not ready on the host, or host doctor failed")
        return
    if claude.get("login") == "yes":
        owned.say("SKIP login: Claude Code is logged in on the host, and a new login could change that")
        return
    before = owned.sessions_on(c, envx)  # every session, to tell a new ID from one that ran
    if before is None:
        owned.say("SKIP login: the list of the host's sessions failed, so the check cannot tell what runs")
        return
    if any(s["name"] == "login-claude" and s.get("state") not in owned.ENDED for s in before):
        owned.say("SKIP login: a login-claude session already runs on the host")
        return
    own, why, failed = start_login(c, envx, before, created)
    if failed:
        c.ok(False, "login: agents login claude --json starts a login session of its own (%s)" % why)
        return
    if not own:
        owned.say("SKIP login: " + why)
        return
    owned.own(own, created)  # the check's own: its start said so, in JSON, and its ID is new
    c.ok(True, "login: agents login claude --json starts a login session of its own (%s)" % own)
    argv = manifest_argv(c, own)
    if argv is None:
        found = owned.sessions_on(c, envx)
        if found is not None and not any(s["id"] == own for s in found):
            c.ok(False, "login: %s/%s went away before the check could read it" % (c.HOST, own))
            return
        argv = ""  # its manifest did not read: the check below fails
    c.ok("claude auth login" in argv, "login: the session runs Claude Code's documented login (%s)" % owned.safe_text(argv))
    if not owned.exact(c, envx, own, "login-claude"):
        c.ok(False, "login: %s/%s is gone, or a session is named like it, so an attach could reach another session" % (c.HOST, own))
        return
    t = c.Term([c.cli, "attach", "--id", "--", c.HOST + "/" + own], env=envx)
    prompt, code = False, None
    try:
        prompt = wait_for_prompt(t)
    finally:
        try:
            t.send(b"\x1c")  # the detach key of the throwaway config
            code = t.exit_code(EXIT_WAIT)
        finally:
            t.close()
    if not c.ok(prompt, "login: Claude Code's login shows its first prompt, %r" % LOGIN_PROMPT):
        owned.say(owned.safe_text(t.buf[-800:]))
    # An attach ends with 0 also when the agent ends, as a failed login
    # does. Only the session's state after the detach says that it runs.
    runs = still_runs(c, envx, own)
    c.ok(code == 0 and runs, "login: after the detach, the attach ends with 0, and the login still runs (exit %s, runs %s)" % (code, runs))


# The first prompt of `claude auth login`, which waits for the code that the
# sign-in page shows. It has two forms: "Paste code here if prompted > "
# when the login's local callback listener started, and "Paste code here > "
# when it could not. Both start with these words. A link alone is no
# prompt: a login that failed may print a help link and end.
LOGIN_PROMPT = "Paste code here"


def screen_text(buf):
    """buf, what an attach drew, as text: a cursor move to the right is the
    blank cells it skips, and the other escape sequences go."""
    s = re.sub(rb"\x1b\[(\d*)C", lambda m: b" " * int(m.group(1) or 1), buf)
    return re.sub(rb"\x1b\[[0-9;?]*[A-Za-z]", b"", s).decode("utf-8", errors="replace")


def wait_for_prompt(t):
    """Waits for the login's first prompt, LOGIN_PROMPT, on the screen."""
    end = time.time() + PROMPT_WAIT
    while time.time() < end:
        if LOGIN_PROMPT in screen_text(t.buf):
            return True
        t.read(0.3)
    return LOGIN_PROMPT in screen_text(t.buf)


def still_runs(c, envx, sid):
    """Whether a list of the host shows the session sid, and that it has
    not ended."""
    found = owned.sessions_on(c, envx)
    return found is not None and any(s["id"] == sid and s.get("state") not in owned.ENDED for s in found)
