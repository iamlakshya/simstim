"""The checks of agent_setup.py that run only on a disposable host (run.py's
DISPOSABLE): the install of an installed agent, a worktree outside the
run's folder, and host doctor --fix applying its socket fix to a stale
socket that the check makes in the helper's runtime folder. On a host that
the run did not make, agent_setup.py prints their SKIP lines instead, with
the Go tests that cover them (run_parts.SKIPPED).
"""
import os, re, shlex, time

import owned


def install_check(c, claude):
    """agents install of an agent that the host has says so, and runs
    nothing. claude is the doctor's check of Claude Code."""
    if claude.get("installed") is not True:
        owned.say("SKIP agents install: Claude Code is not installed on the host, and the check installs nothing")
        return
    code, out, err = owned.run_cmd([c.cli, "agents", "install", "claude", "--host", c.HOST], env=c.envx, timeout=120)
    c.ok(code == 0 and "is already installed on" in err and "install-claude" not in out + err,
         "agents: install of an installed agent says so and runs nothing")


def worktree_check(c, envx, created, made, repo, base, prog, screen):
    """The kept answer goes along: a new worktree runs the setup script with
    no question. The client makes the worktree in a folder named like the
    repository, with the run's token, in the helper's data folder
    (XDG_DATA_HOME, as internal/paths has it). One that is already there is
    not the run's: refused. The folder is recorded in made before the start."""
    name = owned.e2e_name("e2e-setup-wt")
    wt = '"%s/%s/worktrees/%s"' % (owned.xdg_sh("XDG_DATA_HOME", ".local/share"), c.NAME, base)
    if c.ok(c.host_rc("[ ! -e %s ]" % wt) == 0, "setup: the worktrees folder %s, where the client makes the worktree, is not there yet" % base):
        made.append(wt)
        sid, out, err = owned.start_new(c, envx, created, name, "--dir", repo, "--worktree", *prog)
    else:
        sid, out, err = None, "", ""
    c.ok(sid and "Skipping" not in err, "setup: a kept answer asks nothing")
    out = screen(c, envx, sid, name, rb"AGENT-RAN") if sid else b""
    c.ok(b"SETUP-RAN name=" + name.encode() + b" new=1" in out, "setup: in a new worktree the script runs, and knows the worktree is new")


# The helper's runtime folder for sockets, as internal/paths has it: under
# XDG_RUNTIME_DIR when that is a folder, or in /tmp without one.
SOCKETS = 'if [ -d "${XDG_RUNTIME_DIR:-}" ]; then d=$XDG_RUNTIME_DIR/%s; else d=/tmp/%s-$(id -u); fi; d=$d/s'

# Lists the stale sockets in a folder, as the helper's socket fix finds
# them: each *.sock socket that no process answers. One path a line.
STALE = b"""import os, socket, stat, sys
d = sys.argv[1]
try:
    names = sorted(os.listdir(d))
except FileNotFoundError:
    names = []
for n in names:
    p = os.path.join(d, n)
    try:
        if not n.endswith(".sock") or not stat.S_ISSOCK(os.lstat(p).st_mode):
            continue
    except OSError:
        continue
    s = socket.socket(socket.AF_UNIX)
    s.settimeout(0.3)
    try:
        s.connect(p)
    except OSError:
        print(p)
    finally:
        s.close()
"""


def fix_answer(text, sock):
    """Whether the check answers yes to the fix that text shows: the text
    from the end of the question before it to its own question. Only the
    fix's own block counts: its numbered line, the last one in text, and
    the lines after it. The answer is yes only to the socket fix whose
    Runs line is exactly rm <sock>, the check's own socket: no other fix,
    and no socket of the owner's, gets a yes."""
    text = re.sub(rb"\x1b\[[0-9;?]*[A-Za-z]", b"", text).replace(b"\r", b"")
    heads = list(re.finditer(rb"(?:^|\n)\d+\. ", text))
    if not heads:
        return False
    lines = [line for line in text[heads[-1].start():].split(b"\n") if line.strip()]
    return (len(lines) == 4
            and re.fullmatch(rb"\d+\. 1 stale session socket with no session behind them, in [^\n]*\.", lines[0]) is not None
            and lines[1].startswith(b"   Fix: ")
            and lines[2] == b"   Runs: rm " + sock.encode()
            and lines[3].startswith(b"   Fix it?"))


def socket_fix_check(c, made):
    """host doctor --fix on a stale socket that the check makes. The check
    runs only on a host with no stale socket of its own, so the socket fix
    can remove no socket of the owner's. It answers yes only to the fix
    whose own block runs rm of the check's own socket (fix_answer), and no
    to every other question."""
    if c.host_rc("command -v python3 >/dev/null") != 0:
        owned.say("SKIP fix: the host has no python3 to make a stale socket with")
        return
    try:
        stale = c.host_ssh((SOCKETS + '; python3 - "$d"') % (c.NAME, c.NAME), STALE).stdout.decode().split()
    except (owned.CalledProcessError, owned.TimeoutExpired, OSError, ValueError) as e:
        c.ok(False, "fix: the host's stale sockets are listed (%s)" % owned.safe_text(e))
        return
    if stale:
        owned.say("SKIP fix: the host has stale sockets of its own (%s), and the socket fix would remove them too. "
              "The fix check needs a host without them." % owned.safe_text(" ".join(stale)))
        return
    folder = c.host_out((SOCKETS + '; mkdir -p "$d" && printf %%s "$d"') % (c.NAME, c.NAME)).strip()
    if not c.ok(re.fullmatch(r"/[A-Za-z0-9._/-]+", folder), "fix: the helper's socket folder is a plain path (%s)" % owned.safe_text(folder)):
        return
    sock = "%s/e2estale-%s.sock" % (folder, owned.RUN)
    q = shlex.quote(sock)
    # A path that is already there is not the run's: refused, never recorded.
    if not c.ok(c.host_rc("[ ! -e %s ]" % q) == 0, "fix: no file is at %s yet" % owned.safe_text(sock)):
        return
    # Recorded before it is made: the clean-up removes it by its path, which
    # holds the run's token, also when the answer of the make is lost.
    made.append(q)
    try:
        c.host_ssh("python3 -c 'import socket,sys; socket.socket(socket.AF_UNIX).bind(sys.argv[1])' %s" % q)
    except (owned.CalledProcessError, owned.TimeoutExpired, OSError, ValueError) as e:
        c.ok(False, "fix: made a stale socket of the check's own (%s)" % owned.safe_text(e))
        return
    if not c.ok(c.host_rc("test -S " + q) == 0, "fix: made a stale socket at " + owned.safe_text(sock)):
        return
    t = c.Term([c.cli, "host", "doctor", c.HOST, "--fix"], env=c.env)
    answered, seen = [], 0
    try:
        end = time.time() + 120
        while time.time() < end:
            t.read(0.3)
            prompts = [m.end() for m in re.finditer(rb"Fix it\? \[y/N\] ", t.buf)]
            if len(prompts) > seen:
                yes = fix_answer(t.buf[(prompts[seen - 1] if seen else 0):prompts[seen]], sock)
                answered.append(yes)
                t.send(b"y\r" if yes else b"n\r")
                seen += 1
            if t.exit_code(0) is not None:
                break
    finally:
        t.close()
    out = t.buf.decode(errors="replace")
    c.ok(answered.count(True) == 1 and re.search(r"✓ fixed: 1 stale session socket with no session", out),
         "fix: host doctor --fix showed the fix of the check's own socket, asked, and fixed it")
    c.ok(c.host_rc("test -e " + shlex.quote(sock)) == 1, "fix: the stale socket is gone")
    found = owned.sessions_on(c, c.envx)
    rows = [s for s in (found or []) if s["name"].startswith("e2e-")]
    c.ok(rows and all(s.get("alive") for s in rows), "fix: every running session kept its socket (%d sessions)" % len(rows))
