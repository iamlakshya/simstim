"""One rule for every end-to-end check: how a check owns, attaches to and
deletes the sessions that it starts. A test host is someone else's machine,
and its owner's sessions run beside the checks' (docs/contributing/testing.md).
run.py and agent_setup.py follow it, through this module.

- A check owns a session only by a new ID: from the JSON of a start with
  --json, or, at a terminal where a start prints no JSON, from the client's
  own last "Started" line. The ID must be absent from the host's list just
  before the start. Never from the first match in text: the client prints a
  host's text, such as a repository's path, before its own lines.
- Each session that a check starts has a name unique to the run
  (e2e_name). A start that did not answer as expected is named in the
  output, and a session that it may have made is found in a list taken
  right after it, by that name and by newness, and deleted by ID.
- A check attaches only after exact(): a list shows the ID with the
  check's name. Each attach and pull uses --id --, so the command's own
  lookup remains exact if the session disappears after that guard.
- A check deletes only by ID, through the host helper's own `rm <id>`,
  which never takes a name (clean_up).
- One run at a time on a host: run.py holds a lock on it (take_lock).
- Each file that the run makes on the host is in its own folder,
  ~/.e2e-run-<token>, which mkdir makes or refuses, with the run's token
  and a random nonce inside (make_run_dir). The clean-up removes that
  folder only while it holds the nonce (remove_run_dir). A folder of that
  name that is already there is never used, overwritten or removed.
- Every program that a session of the run runs starts after env -i
  (program), and every git command on the host runs without the user's
  or the system's git config, hooks, templates, signing or fsmonitor (GIT).
"""
import json, re, secrets, shlex

import allowed, boundary

TimeoutExpired = boundary.TimeoutExpired
CalledProcessError = boundary.CalledProcessError

# This run's token: the end of each session name that the run gives, and
# what its lock on the host holds.
RUN = secrets.token_hex(3)

# This run's nonce, which proves that the run made its folder: it leaves the
# laptop only on the stdin of a host command, into the folder's .e2e-nonce
# file, and no output or command line ever holds it.
NONCE = secrets.token_hex(16)

# git on the host, without the user's or the system's config, so that no
# hook, template, signing or fsmonitor of the owner's runs.
GIT = "GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null git " + " ".join(allowed.GIT_FLAGS)


def say(text):
    """Prints text, and goes on when the terminal is gone (EIO after a
    hang-up): the clean-up must run to its end."""
    try:
        print("\n".join(safe_text(line) for line in str(text).split("\n")), flush=True)
    except (OSError, ValueError):
        pass


def shown(path):
    """path, the client's or another, as a word that a person can paste."""
    return shlex.quote(path)


def program(shell, script, wait=False, keep=()):
    """The argv of a session's program: script, run by shell (/bin/sh, or
    /bin/bash with no rc file), after env -i with only HOME, PATH, TERM,
    LANG and INPUTRC=/dev/null (plus holder vars in keep). So the host's
    BASH_ENV, ENV and exported functions never reach it (allowed.program)."""
    return allowed.program(shell, script, wait, keep)

# The states of a session that has ended.
ENDED = ("exited", "errored", "lost")

# An ID that a host makes: letters, digits, - and _ (cli-spec 12.3, rule 11).
ID = re.compile(r"[A-Za-z0-9_-]{1,64}")

# The lock that one run holds on the host, a folder that mkdir makes or
# refuses at once, with the run's token inside.
LOCK = "~/.e2e-lock"


def run_dir(home="~"):
    """The run's own folder on the host, under home: "~" for a client's
    argument or a shell word, "$HOME" inside double quotes."""
    return "%s/.e2e-run-%s" % (home, RUN)


def make_run_dir(c):
    """Makes the run's folder on the host: mkdir, which refuses a folder that
    is already there, then the nonce, from stdin, in .e2e-nonce, and the
    token in .e2e-run. Returns whether the folder is the run's own: the
    make answered 0, or, after an answer that failed or was lost, the
    folder holds the run's nonce. A folder that was already there, also
    one with the run's name and token, never holds it."""
    d = run_dir()
    try:
        if c.host_rc("mkdir %s && cat > %s/.e2e-nonce && printf %%s %s > %s/.e2e-run" % (d, d, RUN, d), NONCE.encode()) == 0:
            return True
    except Exception as e:  # noqa: BLE001 - the nonce in the folder tells
        say("RUN FOLDER: the mkdir of %s did not answer: %s" % (d, safe_text(e)))
    try:
        return c.host_rc('[ "$(cat %s/.e2e-nonce 2>/dev/null)" = "$(cat)" ]' % d, NONCE.encode()) == 0
    except Exception:  # noqa: BLE001 - no answer: the folder is not the run's
        return False


def remove_run_dir(c):
    """Removes the run's folder from the host, only while it holds the run's
    nonce, which the command reads from stdin. The nonce file goes last,
    so a removal that stops part way keeps it, and the next try finds the
    folder the run's own. Returns whether no folder of the run's is left:
    the folder is gone, or it has no nonce file, or one that the shell
    reads and that is not the run's."""
    d, f = run_dir(), run_dir() + "/.e2e-nonce"
    return c.host_rc('n=$(cat); [ -n "$n" ] || exit 1; if [ "$(cat %s 2>/dev/null)" = "$n" ]; then '
                     'for x in %s/* %s/.[!.]* %s/..?*; do [ "$x" = %s ] || { [ ! -e "$x" ] && [ ! -L "$x" ]; } || rm -rf -- "$x" || exit 1; done; '
                     'rm -f %s && rmdir %s || exit 1; fi; '
                     '[ ! -e %s ] || { [ ! -e %s ] && [ ! -L %s ]; } || { [ -r %s ] && [ "$(cat %s)" != "$n" ]; }'
                     % (f, d, d, d, f, f, d, d, f, f, f, f), NONCE.encode()) == 0


def e2e_name(base):
    """The name of a session that this run starts: base, an e2e-* word,
    with the run's token, so that no other run's session has it."""
    return "%s-%s" % (base, RUN)


def run_cmd(argv, env=None, timeout=60, cwd=None):
    """Runs argv with no input, and returns (exit code, stdout, stderr). It
    reads the client's bytes, and decodes them as UTF-8 with no newline
    translation, so that a carriage return or a form feed stays as the
    client wrote it, and the checks see it. Bytes that are not UTF-8 raise
    UnicodeDecodeError, a ValueError. It runs argv through the boundary,
    which checks it. The offline tests replace it."""
    r = boundary.run(argv, env=env, timeout=timeout, cwd=cwd)
    return r.returncode, r.stdout.decode("utf-8"), r.stderr.decode("utf-8")


def hidden(ch):
    """Whether a terminal acts on ch, or hides it: the characters that the
    client's own cleaner removes (api.CleanText). A newline stays, for the
    check's own diagnostic lines."""
    o = ord(ch)
    return (o < 0x20 and ch != "\n") or 0x7f <= o <= 0x9f or o in (0x061c, 0x200e, 0x200f, 0x2060, 0xfeff) \
        or 0x200b <= o <= 0x200d or 0x202a <= o <= 0x202e or 0x2066 <= o <= 0x2069 or 0xe0000 <= o <= 0xe007f


def safe_text(b):
    """b, the host's bytes or text, as the check prints it: decoded first,
    then each character that a terminal acts on or hides replaced by ?."""
    s = b.decode("utf-8", errors="replace") if isinstance(b, bytes) else str(b)
    return "".join("?" if hidden(ch) else ch for ch in s)


def json_of(argv, env):
    """The JSON that argv prints, or None when it failed or printed none."""
    try:
        code, out, _ = run_cmd(argv, env=env, timeout=120)
        return json.loads(out) if code == 0 else None
    except (TimeoutExpired, ValueError, boundary.Refused):
        return None


def own(sid, created):
    """Registers sid in boundary.OWNED and created for this run."""
    boundary.own(sid)
    if sid not in created:
        created.append(sid)


def version_1(v):
    """Whether v is the version 1: an integer, and not True, which Python
    counts as 1."""
    return type(v) is int and v == 1


def sessions_on(c, envx):
    """The sessions of the host, each a dict with a string id and name, or
    None when the list failed: an exit code, an answer that is not JSON or
    not a list of sessions, or an error for the host. A list that failed
    says nothing about what runs."""
    rows = json_of([c.cli, "ls", "--json", "--host", c.HOST], envx)
    if not isinstance(rows, list) or not rows:
        return None
    found = []
    for h in rows:
        if not isinstance(h, dict) or "error" in h or h.get("host") != c.HOST or "sessions" not in h:
            return None
        sessions = h["sessions"]
        if not isinstance(sessions, list):  # [] when the host has none; null, {}, "", false or 0 is no list
            return None
        for s in sessions:
            if not (isinstance(s, dict) and isinstance(s.get("id"), str) and ID.fullmatch(s["id"])
                    and isinstance(s.get("name"), str) and isinstance(s.get("state", ""), str)):
                return None
            found.append(s)
    return found


def new_id(sid, before):
    """Whether sid, the ID that a start gave, is new: no session of before,
    the host's list just before the start, had it. An ID that ran before is
    never the check's own, whatever the start said."""
    return sid not in {s["id"] for s in before}


def exact(c, envx, sid, name):
    """Whether, at the time of a list, <host>/<sid> means the session sid
    and nothing else: the list shows sid, with the name that the check gave
    it, and no session named sid, which the address would find on this
    base if sid went away. It is a guard: the host may change after the
    list. The rule of task C5 makes the address exact."""
    found = sessions_on(c, envx)
    return found is not None and any(s["id"] == sid and s["name"] == name for s in found) and not any(s["name"] == sid for s in found)


def clean_up(c, created):
    """Deletes each session of created by its ID alone, through the host
    helper's own rm, which never takes a name: a session that is gone
    deletes nothing, also when another session is named like its ID. It
    takes each ID that the helper deleted out of created, and returns the
    IDs that it did not delete, so that a later clean-up tries them again
    and the run can name them. A session that is already gone counts as
    deleted: after an rm that failed, its folder in the helper's state
    folder is not there, as when it ended and went, or when the answer of
    an rm that deleted it was lost. The helper's rm takes only an ID, so
    no other session matters. This is the harness's only delete of a
    session: no check runs the client's kill or rm on one."""
    failed = []
    for sid in list(created):
        if not (isinstance(sid, str) and ID.fullmatch(sid)):
            created.remove(sid)  # never an ID that a host makes: nothing to delete
            continue
        try:
            rc = c.host_rc('%s rm %s || [ ! -e "%s/%s/sessions/%s" ]' % (c.HELPER, shlex.quote(sid), xdg_sh("XDG_STATE_HOME", ".local/state"), c.NAME, sid))
        except Exception as e:  # noqa: BLE001 - the next ID must be tried
            say("CLEAN-UP: %s rm %s failed: %s" % (c.HELPER, sid, safe_text(e)))
            rc = None
        if rc == 0:
            created.remove(sid)
            boundary.disown(sid)
        else:
            failed.append(sid)
    return failed


def xdg_sh(var, fallback):
    """A shell expression for the folder that the host helper uses for var,
    as internal/paths does: $var when it is set and absolute, else
    $HOME/fallback. It goes inside double quotes. Each case pattern has its
    opening parenthesis: bash 3.2, which is sh on macOS, ends a $( ) at
    the first pattern's closing one."""
    return '$(d=${%s:-}; case $d in (/*) printf %%s "$d";; (*) printf %%s "$HOME/%s";; esac)' % (var, fallback)


def leftovers(c, envx, name, before, created, why):
    """After a start that did not answer as expected, says so, with why,
    and finds the sessions that it may have made: in a list taken now,
    those named name that no session of before had. A name unique to the
    run, an e2e-* one, makes them the run's own, so created gets them, to
    be deleted by ID. Any other name, such as login-claude, may be the
    owner's too: the check names those sessions, and deletes none."""
    say("FAILED START %s: %s" % (safe_text(name), safe_text(why)))
    found = sessions_on(c, envx)
    if found is None or before is None:
        say("  the host's list failed, so the check cannot tell what the start made")
        return []
    made = [s["id"] for s in found if s["name"] == name and new_id(s["id"], before) and ID.fullmatch(s["id"])]
    if not made:
        return []
    if name.startswith("e2e-") and name.endswith("-" + RUN):
        for sid in made:
            own(sid, created)
        say("  the start made %s on %s: the clean-up deletes it by ID" % (", ".join(made), c.HOST))
    else:
        say("  sessions named %s appeared on %s: %s. They may be the owner's, so the check leaves them" % (safe_text(name), c.HOST, ", ".join(made)))
    return made


def started_id(c, out, name, before):
    """The ID of the session in out, the JSON of new --detach --json, when
    it is one JSON object of version 1 for this host and the e2e-* name the
    check gave, with an ID that a host makes and that no session of before
    had. Else None."""
    try:
        obj = json.loads(out)
    except ValueError:
        return None
    if not isinstance(obj, dict) or "error" in obj or not version_1(obj.get("v")) \
            or obj.get("host") != c.HOST or obj.get("name") != name or not name.startswith("e2e-"):
        return None
    sid = obj.get("id")
    if not isinstance(sid, str) or not ID.fullmatch(sid) or not new_id(sid, before):
        return None
    return sid


def command(args):
    """args, the arguments of a start, when they end with an explicit
    command after --, which program() made: without one, the session would
    run the default agent, the host user's own shell, with the owner's rc
    files; and without env -i, the program would get the host's BASH_ENV,
    ENV and exported functions. The helper still runs the user's login
    shell once at each start, for the session's environment: testing.md's
    exception H. Else it raises ValueError."""
    args = list(args)
    if "--" not in args or not allowed.isolated(args[args.index("--") + 1:], allow_holder=True):
        raise ValueError("a start of the e2e run needs a command of owned.program after --: %r" % (args,))
    return args


def start_new(c, envx, created, name, *args):
    """Starts a session named name with new --detach --json, and returns
    its ID, or None, and the command's stdout and stderr. The ID comes from
    the JSON alone (started_id), and must be new. Only then does created
    get it. A start that fails, ends after its time or prints bytes that
    are not UTF-8 goes to leftovers."""
    args = command(args)
    before = sessions_on(c, envx)
    if before is None:
        return None, "", "the list of the host's sessions failed, so the check cannot tell a new ID"
    try:
        code, out, err = run_cmd([c.cli, "new", c.HOST, "--name", name, "--detach", "--json"] + args, env=envx, timeout=120)
    except TimeoutExpired:
        leftovers(c, envx, name, before, created, "new did not end")
        return None, "", "new did not end"
    except ValueError:
        leftovers(c, envx, name, before, created, "new printed bytes that are not UTF-8")
        return None, "", "new printed bytes that are not UTF-8"
    sid = started_id(c, out, name, before) if code == 0 else None
    if sid:
        own(sid, created)
    else:
        leftovers(c, envx, name, before, created, "new ended with %d, and printed no new session of this name" % code)
    return sid, out, err


def started_on_terminal(c, buf, name, before):
    """The ID of the session that new started at a terminal, where it
    cannot print JSON: the last line of buf that is exactly the client's
    own "Started <host>/<id> (<name>, <agent>).", with the e2e-* name the
    check gave, when that ID is new. Else None. A host's text, such as a
    repository's path in the setup question, comes before the client's own
    line, and names no new session. Bytes that are not UTF-8 raise
    UnicodeDecodeError, a ValueError: such a start is a failed start."""
    if not name.startswith("e2e-"):
        return None
    line_re = re.compile(r"Started %s/(%s) \(%s, [A-Za-z0-9_-]+\)\." % (re.escape(c.HOST), ID.pattern, re.escape(name)))
    text = re.sub(r"\x1b\[[0-9;?]*[A-Za-z]", "", buf.decode("utf-8"))
    last = None
    for line in text.split("\n"):
        m = line_re.fullmatch(line.strip("\r"))
        if m:
            last = m.group(1)
    return last if last and new_id(last, before) else None


def start_on_terminal(c, envx, created, name, args, wait=60):
    """Starts a session named name with new --detach at a terminal, as a
    person does, and returns its ID, or None. The ID comes from the
    client's own last line (started_on_terminal), and must be new. A start
    that does not end within wait seconds, or prints no such line, goes to
    leftovers. So does one that prints bytes that are not UTF-8. On every
    path the client ends."""
    args = command(args)
    before = sessions_on(c, envx)
    if before is None:
        say("FAILED START %s: the list of the host's sessions failed, so the check cannot tell a new ID" % safe_text(name))
        return None
    t = c.Term([c.cli, "new", c.HOST, "--name", name, "--detach"] + list(args), env=envx)
    try:
        code = t.exit_code(wait)
    finally:
        t.close()
    sid, why = terminal_id(c, t.buf, name, before, code, wait)
    if sid:
        own(sid, created)
        return sid
    say("  the terminal showed: %s" % safe_text(t.buf[-600:]))
    leftovers(c, envx, name, before, created, why)
    return None


def terminal_id(c, buf, name, before, code, wait):
    """The ID that a start at a terminal printed, from started_on_terminal,
    or None and why there is none. A client that did not end with 0
    started nothing of its own, whatever its text shows."""
    if code is None:
        return None, "new did not end within %d s" % wait
    if code != 0:
        return None, "new ended with %s" % code
    try:
        sid = started_on_terminal(c, buf, name, before)
    except ValueError:
        return None, "new printed bytes that are not UTF-8"
    return (sid, None) if sid else (None, "new printed no line of a new session of this name")


# A run's token, as secrets.token_hex(3) makes it.
TOKEN = re.compile(r"[0-9a-f]{6}")


def take_lock(c):
    """Takes the run's lock on the host: makes LOCK, a folder that mkdir
    makes at once or refuses, and writes the run's token in it. It returns
    True, or False and the token of the run that holds it. When the answer
    of the mkdir was lost, the token in the lock tells: the run's own
    token means that the run holds it."""
    try:
        if c.host_rc("mkdir %s && printf %%s %s > %s/run" % (LOCK, RUN, LOCK)) == 0:
            return True, RUN
    except Exception as e:  # noqa: BLE001 - the token in the lock tells
        say("LOCK: the mkdir of %s did not answer: %s" % (LOCK, safe_text(e)))
    try:
        holder = c.host_out("cat %s/run 2>/dev/null; true" % LOCK).strip()
    except Exception:  # noqa: BLE001 - no answer: the lock is not the run's
        holder = ""
    if holder == RUN:
        return True, RUN
    # A token is 6 hex characters. Any other text, from a hand edit or
    # another tool, is never repeated: the message would put it into a
    # command.
    return False, holder if TOKEN.fullmatch(holder) or holder == "" else "unknown"


# The safe command that removes the lock: its token file, then the empty
# folder. rmdir refuses a folder that holds anything else.
UNLOCK = "rm -f %s/run && rmdir %s" % (LOCK, LOCK)


def lock_state(c):
    """What the lock on the host is, by its token read back: "mine" when it
    holds this run's token, "other" when it holds another, "none" when
    there is no lock, and "unknown" when the host did not answer or the
    lock has no token yet. Only "mine" may be given up."""
    out = c.host_out("if [ -e %s ]; then printf 'lock:'; cat %s/run 2>/dev/null; else printf none; fi" % (LOCK, LOCK)).strip()
    if out == "none":
        return "none"
    if out == "lock:" + RUN:
        return "mine"
    if out.startswith("lock:") and out != "lock:":
        return "other"
    return "unknown"


def release_lock(c):
    """Removes the run's lock from the host, only when it holds this run's
    token: never another run's. Returns whether it removed it."""
    return c.host_rc('[ "$(cat %s/run 2>/dev/null)" = %s ] && %s' % (LOCK, RUN, UNLOCK)) == 0


def leftover_help(c, token):
    """What a run with token, that a kill ended, can leave on the host, and
    the safe command that removes each. Each holds token, which no other
    run or the owner has. A token that is not 6 hex characters goes into
    no command: the help then names the lock's file, and the command that
    removes the lock alone."""
    if token == "":
        return "\n".join([
            "The lock %s on %s holds no token yet: a run may be taking it at this moment, or one stopped while it took it." % (LOCK, c.HOST),
            "Look again in a minute. Only when no e2e run goes on, remove the lock on the host: %s" % UNLOCK])
    if not TOKEN.fullmatch(token):
        return "\n".join([
            "A run whose token cannot be read holds the lock. Look at %s/run on %s." % (LOCK, c.HOST),
            "Only when no e2e run goes on, remove the lock on the host: %s" % UNLOCK])
    folder = "~/.e2e-run-%s" % token
    helper = getattr(c, "HELPER", None) or ('"$HOME/.local/share/%s/bin/<version>/%s-host"' % (c.NAME, c.NAME))
    return "\n".join([
        "Only when no run with the token %s goes on (look for its sessions first), remove what a kill of it can leave on %s:" % (token, c.HOST),
        "  - its sessions, named e2e-*-%s: list them on the laptop: %s ls --host %s. Then delete each by its ID on the host: %s rm <id>"
        % (token, shown(c.cli), c.HOST, helper),
        "  - its folder %s: [ \"$(cat %s/.e2e-run)\" = %s ] && rm -rf %s" % (folder, folder, token, folder),
        "Then remove the lock on the host: %s" % UNLOCK])
