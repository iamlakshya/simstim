"""The fakes of the e2e harness's offline tests: a fake client and a fake
host, so that the tests run no client and reach no host, and a local
folder that stands for the host's home, where the harness's own shell
commands run with HOME and the XDG folders in it. test_sessions.py,
test_cleanup.py and test_rules.py use them.
"""
import contextlib, io, json, os, subprocess, sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import owned as ow  # noqa: E402
import run_parts as rp  # noqa: E402

HERE = os.path.dirname(os.path.abspath(__file__))
rp.LIST_WAIT = 0

# The modules of the harness, which the structural tests read.
MODULES = ["run.py", "run_parts.py", "owned.py", "agent_setup.py", "harness.py"]


def source(name):
    with open(os.path.join(HERE, name)) as f:
        return f.read()


class Host:
    """A host's sessions, {id: name}, the client's answers about them, and
    the helper's rm, which deletes by ID alone. A start of "timeout" makes
    its session and ends after its time, as a slow start does. A start of
    "never" ends after its time and makes nothing. alerts is the answer of
    host alerts --json, when a test needs one."""
    def __init__(self, sessions=None, start="ok", made_id="mine1234", alerts=None):
        self.sessions, self.start, self.made_id, self.alerts = dict(sessions or {}), start, made_id, alerts
        self.ran, self.calls = [], []

    def rows(self):
        return [{"id": i, "name": n, "state": "idle"} for i, n in sorted(self.sessions.items())]

    def run_cmd(self, argv, env=None, timeout=60, cwd=None):
        self.calls.append(argv)
        if argv[1] == "ls":
            return 0, json.dumps([{"v": 1, "host": "box", "sessions": self.rows()}]), ""
        if argv[1:3] == ["host", "alerts"] and self.alerts is not None:
            return 0, json.dumps({"v": 1, "alerts": self.alerts(), "log": []}), ""
        if argv[1] == "new":
            assert argv[2] == "box" and "--detach" in argv and "--json" in argv, argv
            name = argv[argv.index("--name") + 1]
            if self.start in ("ok", "timeout", "not utf-8"):
                self.sessions[self.made_id] = name
            if self.start in ("timeout", "never"):
                raise subprocess.TimeoutExpired(argv, timeout)
            if self.start == "not utf-8":
                raise UnicodeDecodeError("utf-8", b"\xff", 0, 1, "invalid start byte")
            return 0, json.dumps({"v": 1, "host": "box", "id": self.made_id, "name": name, "address": "box/" + self.made_id}), ""
        if argv[0] == "git":
            return 0, "from the session" if "log" in argv else "", ""
        if argv[1] == "pull":
            return (1, "", "Error: the branch mine is checked out here") if "--into" in argv else (0, "", "")
        raise AssertionError("the check ran %s" % argv)

    def host_rc(self, cmd, data=None):
        self.ran.append(cmd)
        words = cmd.split()
        if words[:2] == ["~/helper", "rm"]:
            self.sessions.pop(words[2], None)
            return 0  # deleted, or already gone: clean_up's rm counts both
        return 0


class Ctx:
    """What the shared helpers need of run.py. The lock on the fake host is
    lock: "none", or the token that it holds."""
    cli, HOST, NAME, HELPER = "client", "box", "client", "~/helper"
    envx = {}

    def __init__(self, host, lock="none"):
        self.host, self.host_rc, self.results, self.lock = host, host.host_rc, [], lock

    def host_out(self, cmd):
        if "~/.e2e-lock" in cmd:
            return "none" if self.lock == "none" else "lock:" + self.lock
        return ""

    def ok(self, cond, msg):
        self.results.append((bool(cond), msg))
        return cond


class T:
    """A client at a terminal that printed buf, and ended with code."""
    def __init__(self, buf, code=0):
        self.buf, self.code, self.closed = buf, code, False

    def exit_code(self, timeout=10):
        return self.code

    def close(self):
        self.closed = True


@contextlib.contextmanager
def fake_client(host):
    old, ow.run_cmd = ow.run_cmd, host.run_cmd
    out = io.StringIO()
    try:
        with contextlib.redirect_stdout(out):
            yield out
    finally:
        ow.run_cmd = old


def deleted(host):
    return [cmd.split()[2] for cmd in host.ran if cmd.startswith("~/helper rm ")]


def local_shell(home, xdg=True, fail=None, path=None, extra_env=None):
    """host_rc and host_out that run the harness's shell commands on a local
    folder that stands for the host's home, with XDG_CONFIG_HOME and
    XDG_DATA_HOME set in it, as a host's SSH environment may have them.
    It does NOT set GIT_CONFIG_GLOBAL or GIT_CONFIG_NOSYSTEM itself, so the
    tests prove that the harness's own commands isolate git (item Y).
    fail(cmd[, data]) may answer a command in place of the shell, or raise."""
    env = {"HOME": home, "PATH": os.environ.get("PATH", "/usr/bin:/bin")}
    if path:
        env["PATH"] = path + os.pathsep + env["PATH"]
    if xdg:
        env.update(XDG_CONFIG_HOME=os.path.join(home, "xdg"), XDG_DATA_HOME=os.path.join(home, "data"))
    if extra_env:
        env.update(extra_env)

    def host_rc(cmd, data=None):
        if fail:
            r = fail(cmd, data) if fail.__code__.co_argcount >= 2 else fail(cmd)
            if r is not None:
                return r
        return subprocess.run(["sh", "-c", cmd], env=env, input=data,
                              stdin=None if data is not None else subprocess.DEVNULL,
                              capture_output=True, timeout=30).returncode

    def host_out(cmd):
        r = subprocess.run(["sh", "-c", cmd], env=env, stdin=subprocess.DEVNULL,
                           capture_output=True, timeout=30)
        return r.stdout.decode() if r.returncode == 0 else ""
    return host_rc, host_out


def write(path, text):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        f.write(text)


def read(path):
    try:
        with open(path) as f:
            return f.read()
    except OSError:
        return None


# What the owner has on the host: files at the old fixed paths, and an
# alerts file where the helper does not look (XDG_CONFIG_HOME is set).
OWNER = {"e2e-setup-repo/owner.txt": "mine\n", "e2e-pull/owner.txt": "mine\n", "e2e-seq.bin": "mine\n",
         "e2e-alerts.jsonl": "mine\n", ".e2e-setup-exit": "0\n", ".cache/e2e-resume-1": "",
         ".config/client/alerts.toml": "owner = true\n", "data/client/worktrees/e2e-setup-repo/owner.txt": "mine\n"}
