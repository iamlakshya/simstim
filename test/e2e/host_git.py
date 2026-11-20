"""End-to-end checks for git on the host (spec 19.3, P0.7): the helper runs
git with a repository's hooks, its config's hooks and its fsmonitor off, and
on a host with a systemd user manager, each git step runs in a scope under
the sessions' slice, with whatever git starts. On git 2.54, the helper
refuses to make a worktree.

run.py calls run() with its helpers. `E2E_ONLY=host-git just e2e` runs only
these, and test_host_git.py checks the cleanup offline. Each run makes a
folder of its own with `mktemp -d`, a repository in it and one session in a
worktree of that repository, with a name that no other run uses.

The cleanup removes one path only: the folder that its own mktemp returned.
The session's worktree goes with the product's own `kill`. The cleanup
removes that folder only when `ls --json` shows the host, without an error,
and none of the run's sessions. When `new` gave no clear answer, it first
waits up to 90 seconds for the session to appear, and deletes it. When it
cannot show that the run's sessions are gone, it keeps everything, and
says where.
"""
import json, os, re, subprocess, time

SAFE_PATH = re.compile(r"^/[A-Za-z0-9._/-]+$")
HOOKS = ("post-checkout", "post-index-change", "reference-transaction")
ATTEMPTS = 45  # of 2 seconds each: how long the cleanup waits, 90 seconds


def at_least(version, major, minor):
    """Whether `git --version` says major.minor or later."""
    m = re.search(r"(\d+)\.(\d+)", version)
    return bool(m) and (int(m.group(1)), int(m.group(2))) >= (major, minor)


class Ctx:
    """What the checks need from run.py."""
    def __init__(self, **kw):
        self.__dict__.update(kw)


def q(s):
    """Quotes s for the host's shell."""
    return "'" + s.replace("'", "'\\''") + "'"


def own_base(base):
    """Whether base is what this run's mktemp returns: a full path of plain
    characters, without a . or .. part, named e2e-hostgit.<suffix>."""
    return (bool(base) and bool(SAFE_PATH.match(base)) and not ({".", ".."} & set(base.split("/")[1:]))
            and re.search(r"/e2e-hostgit\.[A-Za-z0-9]+$", base) is not None)


def run(c):
    if c.host_rc("command -v git >/dev/null") != 0:
        print("SKIP host git: the host has no git")
        return
    state = subprocess.run(c.host_argv("systemctl --user is-system-running"), capture_output=True, text=True).stdout.strip()
    systemd = state in ("running", "degraded", "starting")
    version = c.host_ssh("git --version").stdout.decode().strip()
    envx = dict(os.environ, **c.env)
    base = c.host_ssh('mktemp -d "${TMPDIR:-/tmp}/e2e-hostgit.XXXXXX"').stdout.decode().strip()
    if not c.ok(own_base(base), "host git: a folder of this run's own on the host (%s)" % base):
        return
    suffix = base.rsplit(".", 1)[1]
    session = "e2e-hostgit-" + suffix  # no other run uses this name
    repo = base + "/repo-" + suffix
    marks = base + "/marks"
    answered = False  # whether the host answered `new`
    try:
        fixture(c, repo, marks)
        try:
            r = subprocess.run([c.cli, "new", c.HOST, "--name", session, "--dir", repo, "--worktree", "--detach", "--", "sleep", "600"],
                               env=envx, capture_output=True, text=True, timeout=120)
            said = (r.stdout + r.stderr).strip()
        except subprocess.TimeoutExpired:
            said = "new did not answer"
        m = re.search(r"Started [^/]+/(\w+)", said)
        if re.search(r"git version 2\.54\b", version):
            answered = not m and "2.55 or later" in said
            ran = c.host_ssh("ls %s" % q(marks)).stdout.decode().split()
            c.ok(answered and not ran, "host git: %s refuses a worktree, and runs no hook (%s)" % (version, said[-80:]))
            return
        if not c.ok(m, "host git: new --worktree in a repository with hooks, config hooks, an fsmonitor and a clean filter (%s)" % said[-80:]):
            return
        answered = True
        sid = m.group(1)
        cwd = session_cwd(c, envx, sid) or ""
        if not c.ok(("/worktrees/%s/" % os.path.basename(repo)) in cwd, "host git: the session works in a worktree of this run's repository (%s)" % cwd):
            return
        # New stat data and the same content: git status must hash README
        # again, which runs the clean filter.
        c.host_ssh("cd %s && touch -m -d \"@$(( $(date +%%s) + 5 ))\" README" % q(cwd))
        said = kill(c, envx, sid)
        removed = c.ok(delete_sessions(c, envx, session) and "removed the clean worktree" in said.lower(),
                       "host git: kill removes the clean worktree (%s)" % said[:80])
        ran = c.host_ssh("ls %s" % q(marks)).stdout.decode().split()
        hooks = [m for m in ran if m != "filter"]
        c.ok(removed and not hooks, "host git: no hook, config hook or fsmonitor of the repository ran, with %s (%s)" % (version, ", ".join(hooks) or "none ran"))
        cgroups = c.host_ssh("cat %s/filter 2>/dev/null; true" % q(marks)).stdout.decode().split()
        if not systemd:
            print("SKIP host git: no systemd user manager (%s), so git runs without a scope" % (state or "none"))
        elif c.ok(cgroups, "host git: git status ran the clean filter, on a host with a systemd user manager"):
            outside = [g for g in cgroups if ("/%s.slice/" % c.NAME) not in g]
            c.ok(not outside, "host git: the clean filter ran under %s.slice, %d times (%s)" % (c.NAME, len(cgroups), (outside or cgroups)[0]))
        # The fixture is sound: plain git runs its hooks and its fsmonitor.
        c.host_ssh("rm -f %s/* && cd %s && git worktree add -q -b e2e-plain %s/plain && git -C %s/plain status --porcelain >/dev/null"
                   % (q(marks), q(repo), q(base), q(base)))
        ran = set(c.host_ssh("ls %s" % q(marks)).stdout.decode().split())
        want = {"fsmonitor"} | set(HOOKS) | ({"config-hook"} if at_least(version, 2, 54) else set())
        c.ok(want <= ran, "host git: plain git runs the fixture's %s, so the checks above mean something (%s)" % (", ".join(sorted(want)), ", ".join(sorted(ran))))
    finally:
        cleanup(c, envx, session, base, answered)


def host_sessions(c, envx):
    """The run's host's sessions, as (id, name, cwd), or None when `ls
    --json` does not show that host without an error."""
    try:
        r = subprocess.run([c.cli, "ls", "--json", "--host", c.HOST], env=envx, capture_output=True, text=True, timeout=60)
        rows = json.loads(r.stdout or "null") if r.returncode == 0 else None
    except (subprocess.TimeoutExpired, ValueError):
        return None
    for h in rows if isinstance(rows, list) else []:
        if isinstance(h, dict) and h.get("host") == c.HOST:
            if h.get("error"):
                return None
            return [(s["id"], s.get("name"), s.get("cwd")) for s in (h.get("sessions") or [])]
    return None


def session_cwd(c, envx, sid):
    """The folder of session sid, as the host's list says."""
    return next((cwd for i, _, cwd in host_sessions(c, envx) or [] if i == sid), None)


def kill(c, envx, sid):
    """Deletes session sid once, and returns what the client said."""
    try:
        r = subprocess.run([c.cli, "kill", c.HOST + "/" + sid], env=envx, capture_output=True, text=True, timeout=60)
        return (r.stdout + r.stderr).strip()
    except subprocess.TimeoutExpired:
        return "kill did not answer"


def appeared(c, envx, name):
    """Waits up to 90 seconds for a session called name to appear on the
    host, after a `new` whose answer did not come. Returns whether it did."""
    for _ in range(ATTEMPTS):
        rows = host_sessions(c, envx)
        if rows and any(n == name for _, n, _ in rows):
            return True
        time.sleep(2)
    return False


def delete_sessions(c, envx, name):
    """Deletes every session called name, as often as it takes, until the
    host lists none, for up to 90 seconds. Returns whether it showed that."""
    for _ in range(ATTEMPTS):
        rows = host_sessions(c, envx)
        if rows is not None:
            ids = [i for i, n, _ in rows if n == name]
            if not ids:
                return True
            for i in ids:
                kill(c, envx, i)
        time.sleep(2)
    return False


def cleanup(c, envx, session, base, answered):
    """Deletes the run's sessions, and then removes the run's own folder,
    base, and nothing else. When the host did not answer `new`, the session
    may still come: it waits for it first. When it cannot show that the
    run's sessions are gone, it keeps the folder, and says where."""
    if not answered and not appeared(c, envx, session):
        print("WARN host git: new gave no clear answer, and no session called %s appeared, so %s stays on the host" % (session, base))
        return
    if not delete_sessions(c, envx, session):
        print("WARN host git: a session called %s may still run, so %s stays on the host" % (session, base))
        return
    if own_base(base):
        c.host_rc("rm -rf -- %s" % q(base))


def fixture(c, repo, marks):
    """The repository: a README with a clean filter, an fsmonitor, three hook
    scripts and a hook that git's config defines (git 2.54 and later), each
    of which writes a marker. The filter writes its cgroup."""
    git = "git -c user.name=e2e -c user.email=e2e@example.com -c commit.gpgsign=false"
    steps = [
        "mkdir -p %s && git init -q -b main %s && cd %s" % (q(marks), q(repo), q(repo)),
        "echo app > README && echo 'README filter=e2emark' > .gitattributes",
        "%s add README .gitattributes && %s commit -q -m base" % (git, git),
        "git config filter.e2emark.clean %s" % q("cat /proc/self/cgroup >> %s/filter; cat" % marks),
        "printf '#!/bin/sh\\ntouch %s/fsmonitor\\nexit 1\\n' > .git/fsmonitor.sh && chmod +x .git/fsmonitor.sh" % marks,
        "git config core.fsmonitor \"$PWD/.git/fsmonitor.sh\"",
        "git config hook.e2e.command %s" % q("touch %s/config-hook" % marks),
    ]
    for hook in HOOKS:
        steps.append("printf '#!/bin/sh\\ntouch %s/%s\\n' > .git/hooks/%s && chmod +x .git/hooks/%s" % (marks, hook, hook, hook))
        steps.append("git config --add hook.e2e.event %s" % hook)
    c.host_ssh(" && ".join(steps))
