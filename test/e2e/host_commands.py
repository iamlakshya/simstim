"""End-to-end checks for the host commands of task C7 (CLI spec 11): `host
show`, `host upgrade`, `host set` and `host rm`, against the test host.

run.py calls run() with its helpers; `E2E_ONLY=host-commands just e2e` runs
only these. They change nothing on the host. `host show` and `host rm`
never install or prune a helper. `host upgrade` runs only when this build's
helper is on the host already, so it finds it and changes nothing: without
it, the check is skipped, and says so. `host set` changes only the idle
time of a container, which does not stop by itself. `host set` and `host
rm` run in a throwaway client config with a copy of hosts.toml, so the
user's own files are never changed. `host rm` never gets the typed name:
it keeps the machine.
"""
import json, os, re, shutil, subprocess, tempfile


class Ctx:
    """What the checks need from run.py."""
    def __init__(self, **kw):
        self.__dict__.update(kw)


def run(c):
    show(c)
    upgrade(c)
    tmp = tempfile.mkdtemp(prefix="e2e-host-")
    cfg = os.path.join(tmp, "config", c.NAME)
    os.makedirs(cfg)
    real = os.path.join(os.environ.get("XDG_CONFIG_HOME", os.path.expanduser("~/.config")), c.NAME, "hosts.toml")
    shutil.copy(real, cfg)
    envx = dict(os.environ, **c.env)
    envx.update(XDG_CONFIG_HOME=os.path.join(tmp, "config"), XDG_STATE_HOME=os.path.join(tmp, "state"))
    try:
        set_(c, envx)
        rm(c, envx, os.path.join(cfg, "hosts.toml"))
    finally:
        shutil.rmtree(tmp, ignore_errors=True)
    c.ok(re.search(r'name = "%s"' % re.escape(c.HOST), open(real).read()),
         "host commands: the user's own hosts.toml still has %s" % c.HOST)


def cli(c, *args, env=None):
    return subprocess.run([c.cli, *args], env=env, stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=120)


def show(c):
    r = cli(c, "host", "show", c.HOST)
    c.ok(r.returncode == 0 and r.stdout.startswith(c.HOST + "\n") and re.search(r"\n  Helper     %s-host " % re.escape(c.NAME), r.stdout)
         and "\n  Limits     " in r.stdout and "did not answer" not in r.stdout,
         "host show: the saved entry and what the host says (exit %d)" % r.returncode)
    r = cli(c, "host", "show", c.HOST, "--json")
    try:
        obj = json.loads(r.stdout)
    except ValueError:
        obj = {}
    c.ok(r.returncode == 0 and obj.get("v") == 1 and obj.get("name") == c.HOST and obj.get("reachable") is True
         and obj.get("info", {}).get("version") and "error" not in obj,
         "host show --json: one object, reachable, with the host's info")


def helpers_on_host(c):
    return c.host_ssh("ls -1 ~/.local/share/%s/bin" % c.NAME).stdout


def helper_there(c):
    """Whether this build's helper is on the host, as `version` names it."""
    r = cli(c, "version", "--json")
    try:
        helper = json.loads(r.stdout)["helper"]
    except (ValueError, KeyError):
        return False
    return c.host_rc("test -x ~/.local/share/%s/bin/%s/%s-host" % (c.NAME, helper, c.NAME)) == 0


def upgrade(c):
    if not helper_there(c):
        print("SKIP host upgrade: this build's helper is not on the host, and the check must change nothing")
        return
    before = helpers_on_host(c)
    r = cli(c, "host", "upgrade", c.HOST)
    c.ok(r.returncode == 0 and r.stdout == "" and re.search(r"^%s has helper \S+ already\.$" % re.escape(c.HOST), r.stderr, re.M),
         "host upgrade: the same helper is there already, so it changes nothing (exit %d)" % r.returncode)
    r = cli(c, "host", "upgrade", c.HOST, "--json")
    try:
        obj = json.loads(r.stdout)
    except ValueError:
        obj = {}
    c.ok(r.returncode == 0 and obj.get("outcome") == "unchanged" and obj.get("host") == c.HOST,
         "host upgrade --json: unchanged")
    c.ok(helpers_on_host(c) == before, "host upgrade: the helper folders on the host are as they were")


def set_(c, envx):
    r = cli(c, "host", "set", c.HOST, "idl=2h", env=envx)
    c.ok(r.returncode == 2 and 'Did you mean "idle"?' in r.stderr and "Try: %s host set %s idle=2h" % (c.NAME, c.HOST) in r.stderr,
         "host set: an unknown key fails with exit code 2, and suggests idle (exit %d)" % r.returncode)
    r = cli(c, "host", "set", c.HOST, env=envx)
    c.ok(r.returncode == 2 and "host set needs a setting" in r.stderr, "host set: no pair fails with exit code 2")
    machine = c.host_machine()
    if machine.get("provider") == "docker":
        r = cli(c, "host", "set", c.HOST, "idle=2h", env=envx)
        c.ok(r.returncode == 0 and "%s: idle " % c.HOST in r.stderr and "do not stop by themselves" in r.stderr,
             "host set: idle on a container is saved, and says that it does not stop by itself")
    elif not machine:
        r = cli(c, "host", "set", c.HOST, "idle=2h", env=envx)
        c.ok(r.returncode == 2 and "was not made by host create" in r.stderr, "host set: idle on a host you added fails with exit code 2")


def rm(c, envx, hosts):
    machine = c.host_machine()
    r = cli(c, "host", "rm", c.HOST, "--destroy", "--no-input", env=envx)
    if machine:
        c.ok(r.returncode == 4 and "Try: %s host rm %s --destroy --confirm %s" % (c.NAME, c.HOST, c.HOST) in r.stderr,
             "host rm --destroy with input off fails with exit code 4, and names --confirm (exit %d)" % r.returncode)
    else:
        c.ok(r.returncode == 2 and "no machine to delete" in r.stderr, "host rm --destroy on a host you added fails with exit code 2")
    c.ok(('name = "%s"' % c.HOST) in open(hosts).read(), "host rm: the refusal kept the host")
    # At a terminal, rm says what the machine holds, and asks for the typed
    # name. Enter keeps the machine, and forgets the host in the copy.
    t = c.Term([c.cli, "host", "rm", c.HOST], env=dict(envx))
    if machine:
        asked = t.wait_for(rb"Type \S+ to delete the machine too, or press Enter to keep it: ", 60)
        c.ok(asked and b"was made by `host create`" in t.buf and (b"It holds " in t.buf or b"It is " in t.buf),
             "host rm: at a terminal it says what the machine holds, and asks for the typed name")
        t.send(b"\r")
    code = t.exit_code(60)
    c.ok(code == 0 and b"Forgot " in t.buf and (not machine or b"stays, with its sessions" in t.buf),
         "host rm: Enter keeps the machine, and forgets the host (exit %s)" % code)
    c.ok(('name = "%s"' % c.HOST) not in open(hosts).read(), "host rm: the copy of hosts.toml no longer has the host")
