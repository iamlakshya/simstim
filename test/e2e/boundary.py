"""The one boundary between the e2e harness and the world. Every command on
the test host, every client process, every program on the laptop and
every terminal of the run goes through this module, and each has a
timeout. test_rules.py fails on a process that any other module starts.
On a host that the run did not make, each call is checked against
allowed.py before it runs, and a call that is not there is refused
(Refused): a client command and its flags, a command on the host with the
values that fill it, and each key sent to a terminal.

The run's client has a root of its own on the laptop (Root): a fresh HOME,
USERPROFILE and XDG folders, with the run's own hosts.toml (one host,
copied), no config.toml, and so the default detach key, its own control
sockets and its own debug folder. The user's own client config is never
read or changed. OpenSSH reads the user's keys, known_hosts and
~/.ssh/config through the password database, not HOME. The root's HOME
holds .ssh/config, one Include of the user's ~/.ssh/config, so that the
SSH config that the client writes, which includes $HOME/.ssh/config,
finds the user's host aliases too.
"""
import base64, json, os as _os, pwd, re, shutil, signal, subprocess as _subprocess, tempfile

import allowed, harness as _harness

TimeoutExpired = _subprocess.TimeoutExpired
CalledProcessError = _subprocess.CalledProcessError

# Each command ends within WAIT seconds, unless its call gives another time.
WAIT = 120

# run.py's context, set once, before the first call: cli, DISPOSABLE, NAME,
# HELPER and root. With none, every call is refused.
CTX = None

# Unreaped client terminals started through boundary.Term: drop_link only
# touches descendants of a client PID in LIVE.
LIVE = set()

# Session IDs owned by the current run: on non-disposable hosts, attach and
# pull accept only <HOST>/<id> for an ID registered here.
OWNED = set()


def own(sid):
    if isinstance(sid, str) and re.fullmatch(allowed.ID, sid):
        OWNED.add(sid)


def disown(sid):
    OWNED.discard(sid)

# The variables that a command on the host keeps (env -i): HOME, PATH and
# LANG, and the folders and the user bus that the helper and systemctl
# --user need. BASH_ENV, ENV and exported functions never reach it.
HOST_KEEP = ("XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS")
HOST_ENV = ('exec /usr/bin/env -i HOME="$HOME" PATH="$PATH" LANG=C.UTF-8'
            + "".join(' ${%s+"%s=$%s"}' % (v, v, v) for v in HOST_KEEP))

# The marker that CI writes in the container that it made for the run, with
# the value of E2E_DISPOSABLE: no other host has it.
MARKER = "cat /etc/e2e-disposable 2>/dev/null; true"


class Refused(Exception):
    """A call that the allow list does not hold, on a host that the run did
    not make. The check that made it fails, and the run goes on."""


def ctx():
    if CTX is None:
        raise Refused("the e2e run's context is not set, so no command runs")
    return CTX


def require_disposable(c):
    """Require run.py's marker-verified context, never a caller's flag."""
    if c is not ctx() or not ctx().DISPOSABLE:
        raise Refused("this check needs the canonical authenticated disposable host")


def inner(cmd):
    """The script that runs cmd after env -i (HOST_ENV), in /bin/sh. cmd goes
    in base64, so the script holds no quote or backslash of cmd's."""
    b64 = base64.b64encode(cmd.encode()).decode()
    return '%s /bin/sh -c "$(printf %%s %s | base64 -d)"' % (HOST_ENV, b64)


def on_host(cmd):
    """The command line that runs cmd on the host: /bin/sh, after env -i,
    whatever the user's login shell is, also fish: the line holds no
    backslash, and one pair of single quotes."""
    return "/bin/sh -c '%s'" % inner(cmd)


def check_env(c, env):
    if not isinstance(env, dict) or env.get("HOME") != c.root.home:
        raise Refused("a process of the run gets the run's own root, never the user's environment")
    for key in c.root.dirs:
        path = env.get(key)
        if not isinstance(path, str) or allowed.rooted_path(path, c, strict=True) is None:
            raise Refused("a process of the run needs physical XDG folders inside its own root")
    if not c.DISPOSABLE and env.get(c.NAME.upper() + "_NO_INSTALL") != "1":
        raise Refused("a client on an owner's host must refuse automatic helper installation")


def run(argv, *, timeout, env, cwd=None, data=None):
    """Runs argv, a client process or git on the laptop, with data or nothing
    as its input, and returns the CompletedProcess. timeout and env are
    required: no call waits forever, and each gets the run's own root."""
    c = ctx()
    check_env(c, env)
    argv = list(argv)
    if not c.DISPOSABLE:
        if argv[:1] == ["git"]:
            fixed = {"GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM"}
            if env.get("GIT_CONFIG_NOSYSTEM") != "1" or env.get("GIT_CONFIG_GLOBAL") != _os.devnull \
                    or any(k.startswith("GIT_") and k not in fixed for k in env):
                raise Refused("laptop git requires isolated configuration without environment redirects")
            if cwd is None:
                cwd = c.root.path
        if cwd is not None:
            cwd = allowed.rooted_path(cwd, c)
            if cwd is None or not _os.path.isdir(cwd):
                raise Refused("a laptop working directory must stay inside the run's root")
    if cwd is None:
        cwd = c.root.path
    cwd = allowed.rooted_path(cwd, c)
    if cwd is None or not _os.path.isdir(cwd):
        raise Refused("a laptop working directory must stay physically inside the run's root")
    why = allowed.laptop_refusal(argv, c, cwd=cwd)
    if why:
        raise Refused(why)
    return _subprocess.run(argv, input=data, stdin=None if data is not None else _subprocess.DEVNULL,
                           capture_output=True, timeout=timeout, env=env, cwd=cwd)


class Term:
    """A client at a terminal (harness.Term), whose keys are checked: on a
    host that the run did not make, an attach gets only the detach key, new
    only the y to its own question, and host doctor --fix only a no."""
    def __init__(self, argv, env, cols=120, rows=36):
        c = ctx()
        check_env(c, env)
        if not c.DISPOSABLE and list(argv)[:1] == ["git"]:
            raise Refused("laptop git uses the bounded process runner, not a terminal")
        why = allowed.laptop_refusal(list(argv), c)
        if why:
            raise Refused(why)
        t = _harness.Term(list(argv), cols=cols, rows=rows, env=env, inherit=False, cwd=c.root.path)
        LIVE.add(t)
        object.__setattr__(self, "_t", t)
        object.__setattr__(self, "_keys", allowed.keys_for(list(argv), c))
        object.__setattr__(self, "_config", _os.path.join(env["XDG_CONFIG_HOME"], c.NAME) if "XDG_CONFIG_HOME" in env else None)

    def signal(self, sig):
        """Sends sig to this client, a process that the run started."""
        if self._t.pid and getattr(self._t, "code", None) is None:
            _os.kill(self._t.pid, sig)

    def send(self, b):
        c = ctx()
        if not c.DISPOSABLE and (not c.root.detach_key_is_the_default() or
                                 (self._config and not c.root.detach_key_is_the_default(self._config))):
            raise Refused("the client's config does not have the default detach key")
        if self._keys is not None and b not in self._keys:
            raise Refused("the key %r is not one that this client may get on a host that the run did not make" % (b,))
        self._t.send(b)

    def close(self):
        try:
            self._t.close()
        finally:
            LIVE.discard(self._t)

    def __getattr__(self, name):
        if name == "fd":
            raise AttributeError("the terminal's descriptor stays inside the boundary")
        return getattr(self._t, name)

    def __setattr__(self, name, value):
        if name != "buf":
            raise AttributeError(name)
        self._t.buf = value


def term(argv, env=None, cols=120, rows=36):
    """A client at a terminal, with the run's own root and env's extra
    variables, such as TERM."""
    return Term(argv, ctx().root.env(env), cols=cols, rows=rows)


def real_home():
    """The user's home folder from the password database, as OpenSSH finds it."""
    return pwd.getpwuid(_os.getuid()).pw_dir


def toml_value(v):
    if isinstance(v, bool):
        return "true" if v else "false"
    if isinstance(v, int):
        return str(v)
    if isinstance(v, list):
        return "[" + ", ".join(toml_value(x) for x in v) + "]"
    return json.dumps(str(v))


def host_toml(h):
    """The TOML of one [[host]] table, h, with its sub-tables."""
    lines = ["[[host]]"] + ["%s = %s" % (k, toml_value(v)) for k, v in h.items() if not isinstance(v, dict)]
    for k, v in h.items():
        if isinstance(v, dict):
            lines += ["", "[host.%s]" % k] + ["%s = %s" % (json.dumps(kk), toml_value(vv)) for kk, vv in v.items()]
    return "\n".join(lines) + "\n"


class Root:
    """The run's own root on the laptop, in a new folder with a short path,
    so that the client's control sockets fit in it. product is the
    product's name, cli the client's path, hosts the user's hosts.toml and
    host the name of the test host: the run copies that one host's table,
    with an ssh_config that starts with ~/ made absolute and without its
    secrets, which come from the user's own config.toml."""
    KEEP = ("PATH", "USER", "LOGNAME", "LANG", "LC_ALL", "LC_CTYPE", "SSH_AUTH_SOCK", "TMPDIR", "DOCKER_HOST", "DOCKER_CONTEXT", "LIMA_HOME")

    def __init__(self, product, cli, hosts, host, base=None):
        import tomllib
        self.product, self.cli, self.host = product, cli, host
        for b in ([base] if base else [tempfile.gettempdir(), "/tmp"]):
            self.path = tempfile.mkdtemp(prefix="e2e-root-", dir=b)
            if base is not None or len(self.control_dir()) + 1 + 40 <= 103:  # OpenSSH's %C is 40 characters
                break
            _os.rmdir(self.path)
        else:
            raise SystemExit("the run's own root %s is too long for the client's control sockets.\nTry: TMPDIR=/tmp" % self.path)
        self.home = _os.path.join(self.path, "home")
        self.dirs = {k: _os.path.join(self.path, v) for k, v in (("XDG_CONFIG_HOME", "config"), ("XDG_STATE_HOME", "state"),
                                                                 ("XDG_DATA_HOME", "data"), ("XDG_CACHE_HOME", "cache"),
                                                                 ("XDG_RUNTIME_DIR", "runtime"))}
        self.config = _os.path.join(self.dirs["XDG_CONFIG_HOME"], product)
        for d in [self.home, _os.path.join(self.home, ".ssh"), self.config] + list(self.dirs.values()):
            _os.makedirs(d, mode=0o700, exist_ok=True)
        with open(hosts, "rb") as f:
            found = [h for h in tomllib.load(f).get("host", []) if h.get("name") == host]
        if not found:
            raise SystemExit("%s has no host %s.\nTry: %s host add %s" % (hosts, host, cli, host))
        h = {k: v for k, v in found[0].items() if k != "secrets"}
        if isinstance(h.get("ssh_config"), str) and h["ssh_config"].startswith("~/"):
            h["ssh_config"] = _os.path.join(real_home(), h["ssh_config"][2:])
        self.entry = h
        with open(_os.path.join(self.config, "hosts.toml"), "w") as f:
            f.write(host_toml(h))
        user_ssh = _os.path.join(real_home(), ".ssh", "config")
        if _os.path.exists(user_ssh):
            with open(_os.path.join(self.home, ".ssh", "config"), "w") as f:
                f.write("Include %s\n" % json.dumps(user_ssh))
        docker = _os.path.join(real_home(), ".docker")
        self.docker = docker if _os.path.isdir(docker) and "DOCKER_CONFIG" not in _os.environ else _os.environ.get("DOCKER_CONFIG")

    def env(self, extra=None):
        """The environment of every client process of the run: the root's
        folders, git without the user's or the system's config, and only the
        few variables of the user's that a client needs to reach a host."""
        e = {k: _os.environ[k] for k in self.KEEP if k in _os.environ}
        if self.docker:
            e["DOCKER_CONFIG"] = self.docker
        e.update(HOME=self.home, USERPROFILE=self.home, GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=_os.devnull, **self.dirs)
        for k, v in (extra or {}).items():
            # A folder of the client's may move, only to one in the root.
            if k in ("HOME", "USERPROFILE") or (k.startswith("XDG_") and not str(v).startswith(self.path + _os.sep)):
                continue
            e[k] = v
        # Apply after caller overrides. The process boundary checks it too.
        if CTX is None or not CTX.DISPOSABLE:
            e[self.product.upper() + "_NO_INSTALL"] = "1"
        return e

    def close_controls(self, env):
        """End only SSH masters whose physical sockets are in this root."""
        c = ctx()
        if c.root is not self:
            raise Refused("control sockets belong to the canonical run's root")
        check_env(c, env)
        d = allowed.rooted_path(_os.path.join(env["XDG_CACHE_HOME"], self.product, "cm"), c, strict=True)
        if d is None:
            raise Refused("control sockets must stay physically inside the run's root")
        for n in (_os.listdir(d) if _os.path.isdir(d) else []):
            sock = allowed.rooted_path(_os.path.join(d, n), c, strict=True)
            if sock is None:
                raise Refused("a control socket leaves the run's physical root")
            try:
                _subprocess.run(["ssh", "-o", "ControlPath=" + sock, "-O", "exit", "e2e"],
                                stdin=_subprocess.DEVNULL, capture_output=True, timeout=10, env=env, cwd=self.path)
            except (TimeoutExpired, OSError):
                pass

    def remove(self):
        """End the run's own shared connections and remove its laptop root."""
        try:
            self.close_controls(self.env())
        except (Refused, OSError):
            pass
        shutil.rmtree(self.path, ignore_errors=True)

    def detach_key_is_the_default(self, config=None):
        """Whether the client config in config (the root's, by default) keeps
        the default detach key, ctrl+\\."""
        import tomllib
        p = _os.path.join(config or self.config, "config.toml")
        if not _os.path.exists(p):
            return True
        try:
            with open(p, "rb") as f:
                cfg = tomllib.load(f)
        except (OSError, tomllib.TOMLDecodeError):
            return False
        attach = cfg.get("attach", {})
        return isinstance(attach, dict) and attach.get("detach_key", "ctrl+\\") == "ctrl+\\"

    def control_dir(self):
        """The folder of the run's own client's shared SSH connections, as
        internal/client works it out from the root's cache folder."""
        return _os.path.join(self.path, "cache", self.product, "cm")

    def client_digest(self):
        """Hash only this canonical root's actual selected local client."""
        import hashlib
        if ctx().root is not self:
            raise Refused("the client belongs to another isolated run")
        with open(self.cli, "rb") as stream:
            return hashlib.file_digest(stream, "sha256").hexdigest()


class Target:
    """The test host, as run.py reaches it beside the client: a command on
    the host, over SSH with an SSH config of the run's own and no shared
    connection, or `docker exec`; and the end of the run's own links to it,
    as a network drop would end them."""
    def __init__(self, root):
        self.root = root
        self.ssh_config = _os.path.join(root.path, "ssh_config")

    def machine(self):
        """The [host.machine] table of a host made by `host create`, or {}."""
        return self.root.entry.get("machine") or {}

    def argv(self, cmd):
        """The command that runs cmd on the host (on_host)."""
        h, machine = self.root.entry, self.machine()
        if machine.get("provider") == "docker":  # made by `host create docker`: no SSH
            return ["docker", "exec", "-i", "-u", machine.get("user", "root"), machine["id"], "/bin/sh", "-c", inner(cmd)]
        if not _os.path.exists(self.ssh_config):
            lines = ['Include "%s"' % h["ssh_config"]] if h.get("ssh_config") else []
            for p in (_os.path.join(real_home(), ".ssh", "config"), "/etc/ssh/ssh_config"):
                if _os.path.exists(p):
                    lines.append('Include "%s"' % p)
            with open(self.ssh_config, "w") as f:
                f.write("\n".join(lines + ["Host *", "    ConnectTimeout 15", ""]))
        return ["ssh", "-F", self.ssh_config, "-o", "BatchMode=yes", "-o", "ControlPath=none", "--", h["ssh"], on_host(cmd)]

    def _run(self, cmd, data, check=False):
        c = ctx()
        if not c.DISPOSABLE:
            why = allowed.host_refusal(cmd, c)
            if why:
                raise Refused(why)
        return _subprocess.run(self.argv(cmd), input=data, stdin=None if data is not None else _subprocess.DEVNULL,
                               capture_output=True, check=check, timeout=WAIT, env=self.root.env())

    def ssh(self, cmd, data=None):
        """Runs cmd on the host, and fails the check if it fails or does not end."""
        return self._run(cmd, data, check=True)

    def rc(self, cmd, data=None):
        """Runs cmd on the host and returns its exit code, 124 when it did
        not end; data goes to its stdin."""
        try:
            return self._run(cmd, data).returncode
        except TimeoutExpired:
            return 124

    def out(self, cmd):
        """Runs cmd on the host and returns its stdout as text, or "" when it
        failed or did not end."""
        try:
            r = self._run(cmd, None)
        except TimeoutExpired:
            return ""
        return r.stdout.decode("utf-8", errors="replace") if r.returncode == 0 else ""

    def marker(self):
        """The marker in /etc/e2e-disposable on a disposable host, or "": the
        one read that may run before CTX is set, when E2E_DISPOSABLE=1 and a
        32-hex E2E_DISPOSABLE_MARKER are provided."""
        try:
            r = _subprocess.run(self.argv(MARKER), stdin=_subprocess.DEVNULL, capture_output=True, timeout=WAIT, env=self.root.env())
        except TimeoutExpired:
            return ""
        return r.stdout.decode("utf-8", errors="replace").strip() if r.returncode == 0 else ""

    def drop_link(self, client, ps=None, kill=_os.kill, live=None):
        """Ends the run's own links to the host, as a network drop would: the
        bridges below client, a client process that the run started and has
        not reaped, and the shared SSH connection in the run's own control
        folder. Never a match by name or by host alone, so the user's own
        bridges and connections stay. ps, when given, answers as
        `ps -A -o pid=,ppid=,args=` does. Returns the bridges' PIDs."""
        active = live if live is not None else {t.pid for t in list(LIVE) if t.pid and getattr(t, "code", None) is None and t.exit_code(0) is None}
        if not client or client not in active:
            return []
        table = ps if ps is not None else _subprocess.run(["ps", "-A", "-ww", "-o", "pid=", "-o", "ppid=", "-o", "args="],
                                                          stdin=_subprocess.DEVNULL, capture_output=True, text=True, timeout=10).stdout
        rows = []
        for line in table.splitlines():
            f = line.split(None, 2)
            if len(f) == 3 and f[0].isdigit() and f[1].isdigit():
                rows.append((int(f[0]), int(f[1]), f[2]))
        mine, grew = {client}, True
        while grew:
            more = {pid for pid, ppid, _ in rows if ppid in mine} - mine
            mine |= more
            grew = bool(more)
        bridge = re.compile(re.escape(self.root.product) + r'-host"? bridge')  # the path may be quoted
        pids = [pid for pid, _, args in rows if pid in mine and pid != client and bridge.search(args)]
        for pid in pids:
            try:
                kill(pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
        if self.machine().get("provider") != "docker":
            wrapper = _os.path.join(self.root.config, "ssh", "config-" + self.root.host)
            if _os.path.exists(wrapper):
                try:
                    _subprocess.run(["ssh", "-F", wrapper, "-o", "ControlPath=" + _os.path.join(self.root.control_dir(), "%C"),
                                     "-O", "exit", "--", self.root.entry["ssh"]], stdin=_subprocess.DEVNULL,
                                    capture_output=True, timeout=10, env=self.root.env())
                except TimeoutExpired:
                    pass
        return pids
