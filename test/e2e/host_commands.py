"""End-to-end checks for the host commands of task C7 (CLI spec 11): `host
show`, `host upgrade`, `host set` and `host rm`, against the test host.

run.py calls run() with its authenticated disposable context; `E2E_ONLY=host-commands just e2e` runs
only these. They run only on an authenticated disposable host. `host show` and `host rm`
never install or prune a helper. The harness verifies this build's helper before these checks.
`host upgrade` still runs only on a disposable host: a helper can disappear
after a preflight, and this explicit command may install it again. `host set` changes only the idle
time of a container, which does not stop by itself. `host set` and `host
rm` run in a throwaway client config with a copy of hosts.toml, so the
user's own files are never changed. `host rm` never gets the typed name:
it keeps the machine.
"""
import json, os, re, shutil, tempfile

import boundary, owned


class Ctx:
    """What the checks need from run.py."""
    def __init__(self, **kw):
        self.__dict__.update(kw)


def read_text(path):
    """Read a config copy without keeping its file open."""
    with open(path) as f:
        return f.read()


def run(c):
    boundary.require_disposable(c)
    show(c)
    upgrade(c)
    tmp = tempfile.mkdtemp(prefix="e2e-host-", dir=c.root.path)
    cfg = os.path.join(tmp, "config", c.NAME)
    os.makedirs(cfg)
    real = os.path.join(c.root.config, "hosts.toml")
    shutil.copy(real, cfg)
    envx = c.root.env(dict(c.env, XDG_CONFIG_HOME=os.path.join(tmp, "config"), XDG_STATE_HOME=os.path.join(tmp, "state")))
    try:
        set_(c, envx)
        rm(c, envx, os.path.join(cfg, "hosts.toml"))
    finally:
        shutil.rmtree(tmp, ignore_errors=True)
    c.ok(re.search(r'name = "%s"' % re.escape(c.HOST), read_text(real)),
         "host commands: the run's original hosts.toml still has %s" % owned.safe_text(c.HOST))


def show(c):
    code, out, err = owned.run_cmd([c.cli, "host", "show", c.HOST], env=c.envx, timeout=120)
    c.ok(code == 0 and out.startswith(c.HOST + "\n") and re.search(r"\n  Helper     %s-host " % re.escape(c.NAME), out)
         and "\n  Limits     " in out and "did not answer" not in out,
         "host show: the saved entry and what the host says (exit %d)" % code)
    code, out, err = owned.run_cmd([c.cli, "host", "show", c.HOST, "--json"], env=c.envx, timeout=120)
    try:
        obj = json.loads(out)
    except ValueError:
        obj = {}
    c.ok(code == 0 and obj.get("v") == 1 and obj.get("name") == c.HOST and obj.get("reachable") is True
         and obj.get("info", {}).get("version") and "error" not in obj,
         "host show --json: one object, reachable, with the host's info")


def helpers_on_host(c):
    return c.host_ssh('ls -1A "$HOME/.local/share/%s/bin" 2>/dev/null; printf \'current->\'; readlink "$HOME/.local/share/%s/bin/current" 2>/dev/null; echo; echo e2e-done' % (c.NAME, c.NAME)).stdout


def upgrade(c):
    before = helpers_on_host(c)
    code, out, err = owned.run_cmd([c.cli, "host", "upgrade", c.HOST], env=c.envx, timeout=120)
    c.ok(code == 0 and out == "" and re.search(r"^%s has helper \S+ already\.$" % re.escape(c.HOST), err, re.M),
         "host upgrade: the same helper is there already, so it changes nothing (exit %d)" % code)
    code, out, err = owned.run_cmd([c.cli, "host", "upgrade", c.HOST, "--json"], env=c.envx, timeout=120)
    try:
        obj = json.loads(out)
    except ValueError:
        obj = {}
    c.ok(code == 0 and obj.get("outcome") == "unchanged" and obj.get("host") == c.HOST,
         "host upgrade --json: unchanged")
    c.ok(helpers_on_host(c) == before, "host upgrade: the helper folders on the host are as they were")


def set_(c, envx):
    code, out, err = owned.run_cmd([c.cli, "host", "set", c.HOST, "idl=2h"], env=envx, timeout=120)
    c.ok(code == 2 and 'Did you mean "idle"?' in err and "Try: %s host set %s idle=2h" % (c.NAME, c.HOST) in err,
         "host set: an unknown key fails with exit code 2, and suggests idle (exit %d)" % code)
    code, out, err = owned.run_cmd([c.cli, "host", "set", c.HOST], env=envx, timeout=120)
    c.ok(code == 2 and "host set needs a setting" in err, "host set: no pair fails with exit code 2")
    machine = c.host_machine()
    if machine.get("provider") == "docker":
        code, out, err = owned.run_cmd([c.cli, "host", "set", c.HOST, "idle=2h"], env=envx, timeout=120)
        c.ok(code == 0 and "%s: idle " % c.HOST in err and "do not stop by themselves" in err,
             "host set: idle on a container is saved, and says that it does not stop by itself")
    elif not machine:
        code, out, err = owned.run_cmd([c.cli, "host", "set", c.HOST, "idle=2h"], env=envx, timeout=120)
        c.ok(code == 2 and "was not made by host create" in err, "host set: idle on a host you added fails with exit code 2")


def rm(c, envx, hosts):
    machine = c.host_machine()
    code, out, err = owned.run_cmd([c.cli, "host", "rm", c.HOST, "--destroy", "--no-input"], env=envx, timeout=120)
    if machine:
        c.ok(code == 4 and "Try: %s host rm %s --destroy --confirm %s" % (c.NAME, c.HOST, c.HOST) in err,
             "host rm --destroy with input off fails with exit code 4, and names --confirm (exit %d)" % code)
    else:
        c.ok(code == 2 and "no machine to delete" in err, "host rm --destroy on a host you added fails with exit code 2")
    c.ok(('name = "%s"' % c.HOST) in read_text(hosts), "host rm: the refusal kept the host")
    # At a terminal, rm says what the machine holds, and asks for the typed
    # name. Enter keeps the machine, and forgets the host in the copy.
    t = c.Term([c.cli, "host", "rm", c.HOST], env=dict(envx))
    if machine:
        asked = t.wait_for(rb"Type \S+ to delete the machine too, or press Enter to keep it: ", 60)
        c.ok(asked and b"was made by `host create`" in t.buf and (b"It holds " in t.buf or b"It is " in t.buf),
             "host rm: at a terminal it says what the machine holds, and asks for the typed name")
        t.send(b"\r")
    try:
        code = t.exit_code(60)
    finally:
        t.close()
    c.ok(code == 0 and b"Forgot " in t.buf and (not machine or b"stays, with its sessions" in t.buf),
         "host rm: Enter keeps the machine, and forgets the host (exit %s)" % code)
    c.ok(('name = "%s"' % c.HOST) not in read_text(hosts), "host rm: the copy of hosts.toml no longer has the host")
