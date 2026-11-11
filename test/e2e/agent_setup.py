"""End-to-end checks for setting up hosts and sessions: agent checks, per-session
secrets, repository setup scripts and their trust, and `host doctor --fix`.

run.py calls run() with its helpers; `E2E_ONLY=agent-setup just e2e` runs only
these. They use a throwaway client config and state, so the user's own
config.toml, answers and secrets are never read or changed.
"""
import json, os, re, secrets, shutil, subprocess, tempfile, time


class Ctx:
    """What the checks need from run.py."""
    def __init__(self, **kw):
        self.__dict__.update(kw)


def run(c):
    tmp = tempfile.mkdtemp(prefix="e2e-setup-")
    cfg = os.path.join(tmp, "config", c.NAME)
    os.makedirs(cfg)
    real = os.path.join(os.environ.get("XDG_CONFIG_HOME", os.path.expanduser("~/.config")), c.NAME, "hosts.toml")
    shutil.copy(real, cfg)
    token = "e2e-" + secrets.token_hex(16)
    with open(os.path.join(cfg, "config.toml"), "w") as f:
        f.write('[secrets]\nE2E_TOKEN = "printf \'%%s\\\\n\' %s"\n' % token)
    envx = dict(os.environ, **c.env)
    envx.update(XDG_CONFIG_HOME=os.path.join(tmp, "config"), XDG_STATE_HOME=os.path.join(tmp, "state"))
    created = []
    try:
        agent_checks(c)
        secret_checks(c, envx, token, created)
        setup_checks(c, envx, tmp, created)
        fix_checks(c)
    finally:
        for s in created:
            subprocess.run([c.cli, "kill", c.HOST + "/" + s], capture_output=True)
        c.host_rc("rm -rf ~/e2e-setup-repo ~/.e2e-setup-exit ~/.local/share/%s/worktrees/e2e-setup-repo" % c.NAME)
        shutil.rmtree(tmp, ignore_errors=True)


def new(c, envx, *args, stdin=subprocess.DEVNULL):
    r = subprocess.run([c.cli, "new", c.HOST] + list(args), env=envx, stdin=stdin, capture_output=True, text=True, timeout=120)
    m = re.search(r"Started [^/]+/(\w+)", r.stdout)
    return (m.group(1) if m else None), r


def screen(c, envx, sid, want, timeout=30):
    """Attach to a session until want shows, then detach; returns what showed."""
    t = c.Term([c.cli, "attach", c.HOST + "/" + sid], env=envx)
    t.wait_for(want, timeout)
    t.send(b"\x1c")
    t.exit_code(10)
    # A new client gets a snapshot of the screen, which moves the cursor over
    # blank cells instead of printing spaces.
    return re.sub(rb"\x1b\[(\d*)C", lambda m: b" " * int(m.group(1) or 1), t.buf)


def agent_checks(c):
    r = subprocess.run([c.cli, "host", "doctor", c.HOST, "--json"], capture_output=True, text=True, timeout=120)
    checks = {a["kind"]: a for a in json.loads(r.stdout).get("agent_checks") or []}
    claude = checks.get("claude", {})
    c.ok("claude" in checks and claude.get("login") in ("yes", "no", "unknown"),
         "agents: host doctor checks Claude Code (installed=%s, on PATH=%s, logged in=%s)" % (claude.get("installed"), claude.get("on_path"), claude.get("login")))
    if claude.get("installed"):
        # Harmless: an installed agent is reported, and no installer runs.
        r = subprocess.run([c.cli, "agents", "install", "claude", "--host", c.HOST], stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=120)
        c.ok(r.returncode == 0 and "is already installed on" in r.stdout and "install-claude" not in r.stdout,
             "agents: install of an installed agent says so and runs nothing")


def secret_checks(c, envx, token, created):
    sid, r = new(c, envx, "--name", "e2e-secret", "--secret", "E2E_TOKEN", "--detach", "--", "/bin/sh", "-c", "sleep 600")
    if not c.ok(sid, "secrets: new --secret starts a session (%s)" % (r.stdout + r.stderr).strip()):
        return
    created.append(sid)
    c.ok(token not in r.stdout + r.stderr, "secrets: new prints no value")
    m = json.loads(c.host_ssh("cat ~/.local/state/%s/sessions/%s/manifest.json" % (c.NAME, sid)).stdout)
    pid = m["runtime"]["agent_pid"]
    c.ok(m.get("secrets") == ["E2E_TOKEN"], "secrets: the manifest names the secret (%s)" % m.get("secrets"))
    # The pattern goes on stdin, as fd 3, so no command line on the host
    # holds the value.
    pat = (token + "\n").encode()
    rc = c.host_rc("exec 3<&0; tr '\\0' '\\n' < /proc/%d/environ | grep -qxF -f /dev/fd/3" % pid, ("E2E_TOKEN=" + token + "\n").encode())
    c.ok(rc == 0, "secrets: the value is in the agent's environment")
    named = c.host_rc("grep -rqF -f - ~/.local/state/%s/" % c.NAME, b"E2E_TOKEN\n")
    c.ok(named == 0, "secrets: the search of the host's state folder finds the secret's name (grep %d)" % named)
    rc = c.host_rc("grep -rqF -f - ~/.local/state/%s/" % c.NAME, pat)
    c.ok(rc == 1, "secrets: no file in the host's state folder holds the value (manifests, holder logs) (grep %d)" % rc)
    # Command lines come from /proc, which every Linux host has; ps may be missing.
    cmdlines = "for f in /proc/[0-9]*/cmdline; do tr '\\0' ' ' < $f; echo; done 2>/dev/null"
    seen = c.host_rc("exec 3<&0; " + cmdlines + " | grep -qF -f /dev/fd/3", b"sleep 600\n")
    c.ok(seen == 0, "secrets: the search of command lines finds the session's own (grep %d)" % seen)
    rc = c.host_rc("exec 3<&0; " + cmdlines + " | grep -qF -f /dev/fd/3", pat)
    c.ok(rc == 1, "secrets: no command line on the host holds the value (grep %d)" % rc)
    unit = m["runtime"].get("unit") or ""
    if unit:
        shown = c.host_rc("exec 3<&0; systemctl --user show %s | grep -qF -f /dev/fd/3" % unit, ("Id=" + unit + "\n").encode())
        c.ok(shown == 0, "secrets: the search of the session's systemd unit finds its name (grep %d)" % shown)
        rc = c.host_rc("exec 3<&0; systemctl --user show %s | grep -qF -f /dev/fd/3" % unit, pat)
        c.ok(rc == 1, "secrets: the session's systemd unit does not hold the value (grep %d)" % rc)
    ls = subprocess.run([c.cli, "ls", "--json"], env=envx, capture_output=True, text=True, timeout=60).stdout
    c.ok(sid in ls and token not in ls, "secrets: ls --json does not show the value")
    local = subprocess.run(["grep", "-rqF", token, envx["XDG_STATE_HOME"]], capture_output=True).returncode
    c.ok(local != 0, "secrets: nothing in the client's state holds the value")


def setup_checks(c, envx, tmp, created):
    if c.host_rc("command -v git >/dev/null") != 0:
        print("SKIP setup scripts: the host has no git")
        return
    env_name = c.NAME.upper() + "_"
    script = ('#!/bin/sh\\necho "SETUP-RAN name=$%sSESSION_NAME new=$%sWORKTREE_NEW"\\n'
              'exit $(cat "$HOME/.e2e-setup-exit" 2>/dev/null || echo 0)\\n') % (env_name, env_name)
    c.host_ssh("set -e; rm -rf ~/e2e-setup-repo ~/.e2e-setup-exit; mkdir -p ~/e2e-setup-repo/.%s; cd ~/e2e-setup-repo; "
               "git init -q -b main; printf '%s' > .%s/setup; chmod +x .%s/setup; "
               "git add -A; git -c user.name=e2e -c user.email=e2e@example.com commit -qm init" % (c.NAME, script, c.NAME, c.NAME))
    prog = ["--", "/bin/sh", "-c", "echo AGENT-RAN; sleep 600"]

    # Without a terminal and without an answer, the script is skipped, and new says so.
    sid, r = new(c, envx, "--dir", "~/e2e-setup-repo", "--name", "e2e-setup-skip", "--detach", *prog)
    created += [sid] if sid else []
    c.ok(sid and "Skipping the setup script" in r.stderr, "setup: with no answer and no terminal, new skips the script and says so")
    if sid:
        out = screen(c, envx, sid, rb"AGENT-RAN")
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
    sid, r = new(c, envx, "--dir", "~/e2e-setup-repo", "--name", "e2e-setup-fail", "--setup", "--detach", *prog)
    created += [sid] if sid else []
    if c.ok(sid, "setup: --setup starts a session (%s)" % r.stderr.strip()):
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
    answered, seen = [], 0
    end = time.time() + 120
    while time.time() < end:
        t.read(0.3)
        prompts = [m.start() for m in re.finditer(rb"Fix it\? \[y/N\] ", t.buf)]
        if len(prompts) > seen:
            before = t.buf[(prompts[seen - 1] if seen else 0):prompts[seen]]
            yes = b"stale session socket" in before
            answered.append(yes)
            t.send(b"y\r" if yes else b"n\r")  # only the socket fix: the host is shared
            seen += 1
        pid, st = os.waitpid(t.pid, os.WNOHANG)
        if pid:
            break
    out = t.buf.decode(errors="replace")
    c.ok(True in answered and re.search(r"✓ fixed: \d+ stale session sockets? with no session", out),
         "fix: host doctor --fix showed the socket fix, asked, and fixed it")
    c.ok(c.host_rc("test -e " + sock) == 1, "fix: the stale socket is gone")
    ls = subprocess.run([c.cli, "ls", "--json", "--host", c.HOST], capture_output=True, text=True, timeout=60).stdout
    rows = [s for h in json.loads(ls) for s in (h["sessions"] or []) if s["name"].startswith("e2e-")]
    c.ok(rows and all(s["alive"] for s in rows), "fix: every running session kept its socket (%d sessions)" % len(rows))
