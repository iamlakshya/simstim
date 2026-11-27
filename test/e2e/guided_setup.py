"""End-to-end checks for the guided setup (`setup`): a run with flags and no
terminal against the test host, the refusals that come before anything
changes, and Ctrl+C at a question.

run.py calls run() with its helpers; `E2E_ONLY=guided-setup just e2e` runs
only these. They use a throwaway client config with a copy of hosts.toml, so
the user's own files are never changed. They install no agent and start no
login: `--agents shell` installs nothing.
"""
import json, os, re, shutil, tempfile

import owned


class Ctx:
    """What the checks need from run.py."""
    def __init__(self, **kw):
        self.__dict__.update(kw)


def run(c):
    tmp = tempfile.mkdtemp(prefix="e2e-guided-", dir=c.root.path)
    cfg = os.path.join(tmp, "config", c.NAME)
    os.makedirs(cfg)
    shutil.copy(os.path.join(c.root.config, "hosts.toml"), cfg)
    envx = c.root.env(dict(c.env, XDG_CONFIG_HOME=os.path.join(tmp, "config"), XDG_STATE_HOME=os.path.join(tmp, "state")))
    try:
        with_flags(c, envx)
        refusals(c, envx)
        stop(c, envx)
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


def names(c, envx):
    _, out, _ = owned.run_cmd([c.cli, "ls", "--json", "--host", c.HOST], env=envx, timeout=60)
    return {s["name"] for h in json.loads(out or "[]") if isinstance(h.get("sessions"), list) for s in h["sessions"]}


def with_flags(c, envx):
    before = names(c, envx)
    code, stdout, stderr = owned.run_cmd([c.cli, "setup", "--host", c.HOST, "--agents", "shell", "--yes", "--no-login"], env=envx, timeout=300)
    out = stdout + stderr  # the setup talks on stderr
    if not c.ok(code == 0 and ("Done. %s is ready." % c.HOST) in out,
                "guided: setup --host --agents shell --yes --no-login finishes (exit %d)" % code):
        owned.say(owned.safe_text(out[-1500:] + stderr[-500:]))
        return
    c.ok("Connecting to %s" % c.HOST in out and "Agents on %s" % c.HOST in out,
         "guided: it connects the host, then shows the agent checklist")
    c.ok(re.search(r"\[[x ]\] Claude Code", out) and "Best effort, kept working by contributors:" in out,
         "guided: the checklist has the first-class agents, and the best-effort ones marked")
    c.ok("Nothing to install." in out and "Install" not in out.split("Nothing to install.")[1].split("Done.")[0],
         "guided: --agents shell installs nothing")
    c.ok("Log in now?" not in out and "Type the numbers" not in out and "[y/N]" not in out,
         "guided: with flags and no terminal it asks nothing")
    c.ok(re.search(r"Start your first session:\n\n    %s new %s" % (re.escape(c.NAME), re.escape(c.HOST)), out),
         "guided: it ends with the command for the first session")
    c.ok(re.search(r"  [✓!?✕] Claude Code ", out.split("Done.")[1]),
         "guided: the summary gives Claude Code's state on the host")
    new = [n for n in names(c, envx) - before if n.startswith(("install-", "login-"))]
    c.ok(not new, "guided: no installer or login session was started (%s)" % ", ".join(owned.safe_text(n) for n in sorted(new)))


def refusals(c, envx):
    code, out, err = owned.run_cmd([c.cli, "setup"], env=envx, timeout=60)
    c.ok(code == 4 and "Try:" in err and "--yes" in err,
         "guided: with no terminal and no flags, setup refuses with exit code 4 and says which flags to pass")
    code, out, err = owned.run_cmd([c.cli, "setup", "--host", c.HOST, "--agents", "no-such-agent", "--yes"], env=envx, timeout=60)
    c.ok(code == 3 and 'no agent called "no-such-agent"' in err and "Connecting" not in out + err,
         "guided: an unknown agent is refused before anything runs")


def stop(c, envx):
    t = c.Term([c.cli, "setup"], env=envx)
    try:
        asked = t.wait_for(rb"Pick one \[1-\d\]: ", 60)
        t.send(b"\x03")  # Ctrl+C, as the terminal sends it
        code = t.exit_code(30)
    finally:
        t.close()
    c.ok(asked and code == 130 and b"Cancelled. Nothing changed." in t.buf,
         "guided: Ctrl+C at the first question stops, and says nothing was changed (exit %s)" % code)
