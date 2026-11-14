"""End-to-end checks for setting up hosts and sessions: the agent checks of
host doctor, per-session secrets, repository setup scripts and their trust,
and `host doctor --fix` with a no to every fix.

run.py calls run() with its helpers; `E2E_ONLY=agent-setup just e2e` runs only
these. They use a throwaway client config and state in the run's own root
(boundary.Root), so the user's own config.toml, answers and secrets are
never read or changed. On the host
they do only what run.py allows: they start sessions of their own, read,
attach and send only the detach key, delete their sessions by ID, and make
files only in the run's folder. An agent's install and login, a worktree
and the stale-socket fix run only on a disposable host (c.DISPOSABLE;
agent_login.py and disposable_setup.py), and are skipped elsewhere
(run_parts.SKIPPED).
"""
import json, os, re, secrets, shlex, shutil, tempfile, time

import owned, run_parts  # run.py puts this folder on the path


class Ctx:
    """What the checks need from run.py."""
    def __init__(self, **kw):
        self.__dict__.update(kw)


# The command lines of the host's processes, from /proc, which every Linux
# host has; ps may be missing.
CMDLINES = "for f in /proc/[0-9]*/cmdline; do tr '\\0' ' ' < $f; echo; done 2>/dev/null"


def run(c):
    tmp = tempfile.mkdtemp(prefix="e2e-setup-", dir=c.root.path)
    cfg = os.path.join(tmp, "config", c.NAME)
    os.makedirs(cfg)
    try:
        shutil.copy(os.path.join(c.root.config, "hosts.toml"), cfg)
    except OSError as e:
        c.ok(False, "setup: the client's hosts.toml is copied to the throwaway config (%s)" % owned.safe_text(e))
        shutil.rmtree(tmp, ignore_errors=True)
        return
    token = "e2e-" + secrets.token_hex(16)
    with open(os.path.join(cfg, "config.toml"), "w") as f:
        f.write('[secrets]\nE2E_TOKEN = "printf \'%%s\\\\n\' %s"\n' % token)
    envx = c.root.env(dict(c.env, XDG_CONFIG_HOME=os.path.join(tmp, "config"), XDG_STATE_HOME=os.path.join(tmp, "state")))
    if not c.ok(c.root.detach_key_is_the_default(cfg), "setup: the throwaway config has the default detach key"):
        shutil.rmtree(tmp, ignore_errors=True)
        return
    # run.py's list, when it gives it: then its last step also deletes what
    # these checks started, when this finally does not finish.
    created = c.created if isinstance(getattr(c, "created", None), list) else []
    made = c.made if isinstance(getattr(c, "made", None), list) else []
    try:
        agent_checks(c, envx)
        if c.DISPOSABLE:
            import agent_login
            agent_login.login_checks(c, envx, created)
        else:
            run_parts.skip("agents login")
        secret_checks(c, envx, token, created)
        setup_checks(c, envx, tmp, created, made)
        fix_checks(c, made)
    finally:
        for s in created:
            subprocess.run([c.cli, "rm", c.HOST + "/" + s, "-y"], capture_output=True)
        c.host_rc("rm -rf ~/e2e-setup-repo ~/.e2e-setup-exit ~/.local/share/%s/worktrees/e2e-setup-repo" % c.NAME)
        shutil.rmtree(tmp, ignore_errors=True)


# Every check here follows the harness's one rule for the sessions it
# starts (owned.py): it owns a session only by a new ID, from the JSON of
# its start or from the client's own last "Started" line, never from text;
# it attaches only after owned.exact(); and it deletes only by ID, through
# the helper's own rm. Each session has a name unique to the run
# (owned.e2e_name).


def start_asked(c, envx, created, name, *args):
    """Starts a session named name with new --detach at a terminal, where
    new asks whether the repository's setup script may run, and answers
    yes. It returns the session's ID, or None, and whether new asked. The
    ID comes from the client's own last line (started_on_terminal), and
    must be new. Without such a line, the start goes to owned.leftovers,
    which names it and deletes what it made by ID. On every path the
    client ends."""
    args = owned.command(args)
    before = owned.sessions_on(c, envx)
    if before is None:
        owned.say("FAILED START %s: the list of the host's sessions failed, so the check cannot tell a new ID" % name)
        return None, False
    t = c.Term([c.cli, "new", c.HOST, "--name", name, "--detach"] + list(args), env=envx)
    asked, code = False, None
    try:
        asked = bool(t.wait_for(rb"Run it\? \[y/N\]", 60)) and b"has a setup script" in t.buf
        if asked:
            t.send(b"y\r")
        code = t.exit_code(60)
    finally:
        t.close()
    sid, why = owned.terminal_id(c, t.buf, name, before, code, 60)
    if sid:
        owned.own(sid, created)
    else:
        owned.say("  the terminal showed: %s" % owned.safe_text(t.buf[-600:]))
        owned.leftovers(c, envx, name, before, created, why)
    return sid, asked


def screen(c, envx, sid, name, want, timeout=30):
    """Attach to the session sid, named name, until want shows, then detach;
    returns what showed. It attaches only when <host>/<sid> is exact
    (owned.exact), and otherwise returns b"". On every path it
    detaches, and ends its client."""
    if not owned.exact(c, envx, sid, name):
        c.ok(False, "attach: %s/%s is the run's own session, by ID and name, and no session is named like it" % (c.HOST, sid))
        return b""
    t = c.Term([c.cli, "attach", "--id", "--", c.HOST + "/" + sid], env=envx)
    try:
        t.wait_for(want, timeout)
    finally:
        try:
            t.send(b"\x1c")
            t.exit_code(10)
        finally:
            t.close()
    # A new client gets a snapshot of the screen, which moves the cursor over
    # blank cells instead of printing spaces.
    return re.sub(rb"\x1b\[(\d*)C", lambda m: b" " * int(m.group(1) or 1), t.buf)


def read_text(path):
    """The text of the file at path, or None when it does not read: a file
    that a check expects, and that is not there, is a failed check, never
    an end of the suite."""
    try:
        with open(path) as f:
            return f.read()
    except OSError:
        return None


def agent_checks(c, envx):
    doctor = owned.json_of([c.cli, "host", "doctor", c.HOST, "--json"], envx)
    checks = doctor.get("agent_checks") if isinstance(doctor, dict) else None
    claude = next((a for a in checks or [] if isinstance(a, dict) and a.get("kind") == "claude"), {})
    c.ok(claude and claude.get("login") in ("yes", "no", "unknown"),
         "agents: host doctor checks Claude Code (installed=%s, on PATH=%s, logged in=%s)" % tuple(
             owned.safe_text(claude.get(k)) for k in ("installed", "on_path", "login")))
    if c.DISPOSABLE:
        import disposable_setup
        disposable_setup.install_check(c, claude)
    else:
        run_parts.skip("agents install")


def manifest(c, sid):
    """The manifest of the session sid on the host, or None when it did not
    read. The path holds sid, an ID that owned checked."""
    try:
        return json.loads(c.host_ssh("cat ~/.local/state/%s/sessions/%s/manifest.json" % (c.NAME, sid)).stdout)
    except (owned.CalledProcessError, owned.TimeoutExpired, OSError, ValueError):
        return None


def secret_checks(c, envx, token, created):
    name = owned.e2e_name("e2e-secret")
    # The session's first process keeps its environment, with the secret,
    # and the program runs after env -i (owned.program with wait).
    sid, out, err = owned.start_new(c, envx, created, name, "--secret", "E2E_TOKEN", "--", *owned.program("/bin/sh", "sleep 600", wait=True))
    if not c.ok(sid, "secrets: new --secret starts a session (%s)" % owned.safe_text((out + err).strip()[-400:])):
        return
    c.ok(token not in out + err, "secrets: new prints no value")
    m = manifest(c, sid)
    if not c.ok(isinstance(m, dict) and isinstance(m.get("runtime"), dict) and isinstance(m["runtime"].get("agent_pid"), int),
                "secrets: the session's manifest reads"):
        return
    pid = m["runtime"]["agent_pid"]
    c.ok(m.get("secrets") == ["E2E_TOKEN"], "secrets: the manifest names the secret (%s)" % owned.safe_text(m.get("secrets")))
    # The pattern goes on stdin, as fd 3, so no command line on the host
    # holds the value.
    pat = (token + "\n").encode()
    rc = c.host_rc("exec 3<&0; tr '\\0' '\\n' < /proc/%d/environ | grep -qxF -f /dev/fd/3" % pid, ("E2E_TOKEN=" + token + "\n").encode())
    c.ok(rc == 0, "secrets: the value is in the agent's environment")
    named = c.host_rc("grep -rqF -f - ~/.local/state/%s/" % c.NAME, b"E2E_TOKEN\n")
    c.ok(named == 0, "secrets: the search of the host's state folder finds the secret's name (grep %d)" % named)
    rc = c.host_rc("grep -rqF -f - ~/.local/state/%s/" % c.NAME, pat)
    c.ok(rc == 1, "secrets: no file in the host's state folder holds the value (manifests, holder logs) (grep %d)" % rc)
    seen = c.host_rc("exec 3<&0; " + CMDLINES + " | grep -qF -f /dev/fd/3", b"sleep 600\n")
    c.ok(seen == 0, "secrets: the search of command lines finds the session's own (grep %d)" % seen)
    rc = c.host_rc("exec 3<&0; " + CMDLINES + " | grep -qF -f /dev/fd/3", pat)
    c.ok(rc == 1, "secrets: no command line on the host holds the value (grep %d)" % rc)
    unit = m["runtime"].get("unit") or ""
    if unit:
        shown = c.host_rc("exec 3<&0; systemctl --user show %s | grep -qF -f /dev/fd/3" % shlex.quote(unit), ("Id=" + unit + "\n").encode())
        c.ok(shown == 0, "secrets: the search of the session's systemd unit finds its name (grep %d)" % shown)
        rc = c.host_rc("exec 3<&0; systemctl --user show %s | grep -qF -f /dev/fd/3" % shlex.quote(unit), pat)
        c.ok(rc == 1, "secrets: the session's systemd unit does not hold the value (grep %d)" % rc)
    _, ls, _ = run_parts.run_or_timeout([c.cli, "ls", "--json", "--host", c.HOST], envx, None)
    c.ok(sid in ls and token not in ls, "secrets: ls --json does not show the value")
    c.ok(not holds(envx["XDG_STATE_HOME"], token.encode()), "secrets: nothing in the client's state holds the value")


def holds(folder, value):
    """Whether a file under folder, on the laptop, holds value."""
    for d, _, files in os.walk(folder):
        for n in files:
            try:
                with open(os.path.join(d, n), "rb") as f:
                    if value in f.read():
                        return True
            except OSError:
                pass
    return False


def setup_checks(c, envx, tmp, created, made=None):
    """The setup-script checks. The repository is in the run's own folder,
    with the run's token in its name. A folder of that name that is already
    there is never used, overwritten or removed: its check fails instead.
    The only key that a check sends is the y to the client's own question,
    at the local terminal: no key reaches a session."""
    if c.host_rc("command -v git >/dev/null") != 0:
        owned.say("SKIP setup scripts: the host has no git")
        return
    base = "e2e-setup-repo-" + owned.RUN
    repo = owned.run_dir() + "/" + base
    exit_file = owned.run_dir("$HOME") + "/setup-exit"
    env_name = c.NAME.upper() + "_"
    script = ('#!/bin/sh\\necho "SETUP-RAN name=$%sSESSION_NAME new=$%sWORKTREE_NEW"\\n'
              'exit $(cat "%s" 2>/dev/null || echo 0)\\n') % (env_name, env_name, exit_file)
    # mkdir without -p refuses a folder that is already there.
    rc = c.host_rc("set -e; mkdir %s; mkdir %s/.%s; cd %s; %s init --template= -q -b main; printf '%s' > .%s/setup; chmod +x .%s/setup; "
                   "%s add -A; %s commit -qm init" % (repo, repo, c.NAME, repo, owned.GIT, script, c.NAME, c.NAME, owned.GIT, owned.GIT))
    if not c.ok(rc == 0, "setup: the test repository %s is made on the host, in the run's own folder (exit %d)" % (repo, rc)):
        return
    prog = ["--"] + owned.program("/bin/sh", "echo AGENT-RAN; sleep 600")

    # Without a terminal and without an answer, the script is skipped, and new says so.
    name = owned.e2e_name("e2e-setup-skip")
    sid, out, err = owned.start_new(c, envx, created, name, "--dir", repo, *prog)
    c.ok(sid and "Skipping the setup script" in err, "setup: with no answer and no terminal, new skips the script and says so")
    out = screen(c, envx, sid, name, rb"AGENT-RAN") if sid else b""
    c.ok(b"AGENT-RAN" in out and b"SETUP-RAN" not in out, "setup: the skipped script did not run")

    # On a terminal, new asks; yes runs the script before the program, and is kept.
    t = c.Term([c.cli, "new", c.HOST, "--dir", "~/e2e-setup-repo", "--name", "e2e-setup-ask", "--detach"] + prog, env=envx)
    asked = t.wait_for(rb"Run it\? \[y/N\]", 60)
    c.ok(asked and b"has a setup script" in t.buf, "setup: new asks before it runs an untrusted repository's script")
    t.send(b"y\r")
    t.exit_code(60)
    m = re.search(rb"Started [^/]+/(\w+)", t.buf)
    if m:
        sid = m.group(1).decode()
        created.append(sid)
        out = screen(c, envx, sid, rb"AGENT-RAN")
        i, j = out.find(b"SETUP-RAN name=e2e-setup-ask new=0"), out.find(b"AGENT-RAN")
        if not c.ok(0 <= i < j and b": running " in out, "setup: the trusted script ran in the session's terminal, before the program"):
            print(out.decode(errors="replace")[-600:])
    with open(os.path.join(tmp, "state", c.NAME, "setup-trust.json")) as f:
        trust = json.load(f)
    kept = [e for r, e in trust.get("trusted", {}).get(c.HOST, {}).items() if r.endswith("/e2e-setup-repo")]
    c.ok(trust.get("version") == 2 and len(kept) == 1 and re.fullmatch(r"[0-9a-f]{64}", kept[0].get("sha256", "")),
         "setup: the answer is kept for the repository on the host, with the hash of its scripts")

    # The answer goes along: a new worktree runs the script with no question.
    sid, r = new(c, envx, "--dir", "~/e2e-setup-repo", "--name", "e2e-setup-wt", "--worktree", "--detach", *prog)
    created += [sid] if sid else []
    c.ok(sid and "Skipping" not in r.stderr, "setup: a kept answer asks nothing")
    if sid:
        out = screen(c, envx, sid, rb"AGENT-RAN")
        c.ok(b"SETUP-RAN name=e2e-setup-wt new=1" in out, "setup: in a new worktree the script runs, and knows the worktree is new")

    # A commit that changes the trusted script asks again (spec 19.2): without
    # a terminal, new skips it with a note; at one, it asks and says why.
    changed = script.replace("SETUP-RAN", "SETUP-CHANGED")
    c.host_ssh("set -e; cd ~/e2e-setup-repo; printf '%s' > .%s/setup; chmod +x .%s/setup; "
               "git add -A; git -c user.name=e2e -c user.email=e2e@example.com commit -qm change" % (changed, c.NAME, c.NAME))
    sid, r = new(c, envx, "--dir", "~/e2e-setup-repo", "--name", "e2e-setup-changed", "--detach", *prog)
    created += [sid] if sid else []
    c.ok(sid and "it changed after you trusted it" in r.stderr, "setup: without a terminal, a changed script is skipped with a note")
    if sid:
        out = screen(c, envx, sid, rb"AGENT-RAN")
        c.ok(b"AGENT-RAN" in out and b"SETUP-CHANGED" not in out, "setup: the changed script did not run")
    t = c.Term([c.cli, "new", c.HOST, "--dir", "~/e2e-setup-repo", "--name", "e2e-setup-reask", "--detach"] + prog, env=envx)
    asked = t.wait_for(rb"Run it\? \[y/N\]", 60)
    c.ok(asked and b"changed after you trusted it" in t.buf, "setup: at a terminal, new asks again and says that the script changed")
    t.send(b"y\r")
    t.exit_code(60)
    m = re.search(rb"Started [^/]+/(\w+)", t.buf)
    if c.ok(m, "setup: the new yes starts the session"):
        sid = m.group(1).decode()
        created.append(sid)
        out = screen(c, envx, sid, rb"AGENT-RAN")
        c.ok(b"SETUP-CHANGED name=e2e-setup-reask" in out, "setup: after the new yes, the changed script runs")

    # A failing script shows its exit code, and the program still starts.
    c.host_ssh("echo 7 > ~/.e2e-setup-exit")
    sid, r = new(c, envx, "--dir", "~/e2e-setup-repo", "--name", "e2e-setup-fail", "--setup-script", "--detach", *prog)
    created += [sid] if sid else []
    if c.ok(sid, "setup: --setup-script starts a session (%s)" % r.stderr.strip()):
        out = screen(c, envx, sid, rb"AGENT-RAN")
        i, j = out.find(b"exited with 7; the agent starts anyway"), out.find(b"AGENT-RAN")
        c.ok(0 <= i < j, "setup: a failing script shows its exit code, then the program starts")
    c.host_rc("rm -f ~/.e2e-setup-exit")


def fix_checks(c):
    if c.host_rc("command -v python3 >/dev/null") != 0:
        print("SKIP fix: the host has no python3 to make a stale socket with")
        return
    # The helper's runtime folder: under XDG_RUNTIME_DIR, or in /tmp without one.
    sock = c.host_ssh("if [ -d \"${XDG_RUNTIME_DIR:-}\" ]; then d=$XDG_RUNTIME_DIR/%s; else d=/tmp/%s-$(id -u); fi; "
                      "d=$d/s; mkdir -p $d; p=$d/e2estale.sock; "
                      "python3 -c 'import socket,sys; socket.socket(socket.AF_UNIX).bind(sys.argv[1])' $p; echo $p" % (c.NAME, c.NAME)).stdout.decode().strip()
    c.ok(c.host_rc("test -S " + sock) == 0, "fix: made a stale socket at " + sock)
    t = c.Term([c.cli, "host", "doctor", c.HOST, "--fix"], env=c.env)
    seen = 0
    try:
        end = time.time() + 120
        while time.time() < end:
            t.read(0.3)
            prompts = len(re.findall(rb"Fix it\? \[y/N\] ", t.buf))
            while seen < prompts:
                t.send(b"n\r")  # never a yes: the e2e run applies no fix on a test host
                seen += 1
            if t.exit_code(0) is not None:
                break
    finally:
        t.close()
    out = t.buf.decode(errors="replace")
    c.ok(t.exit_code(0) == 0 and ("Nothing was changed." in out or "Nothing to fix." in out),
         "fix: host doctor --fix with a no to each of its %d fixes changes nothing, and says so" % seen)
    if c.DISPOSABLE:
        import disposable_setup
        disposable_setup.socket_fix_check(c, made if made is not None else [])
    else:
        run_parts.skip("stale socket fix")
