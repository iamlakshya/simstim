"""Check the host's git isolation on an authenticated disposable host.

The repository and its markers live in the run's nonce-owned folder. Each
session is owned by a fresh JSON ID, never by its name. Cleanup retries the
helper's exact-ID removal and keeps the folder when the host cannot prove
that the sessions are gone. The plain-git control deliberately runs this
fixture's hooks, config hook and fsmonitor, with global and system config off.
"""
import json, os, re, shlex, time

import boundary, owned

HOOKS = ("post-checkout", "post-index-change", "reference-transaction")
ATTEMPTS = 45


class Ctx:
    """What the checks need from the canonical run context."""
    def __init__(self, **kw):
        self.__dict__.update(kw)


def q(s):
    """Quotes a fixture path for the host's shell."""
    return shlex.quote(s)


def at_least(version, major, minor):
    """Whether git --version says major.minor or later."""
    m = re.search(r"(\d+)\.(\d+)", version)
    return bool(m) and (int(m.group(1)), int(m.group(2))) >= (major, minor)


def own_base(base):
    """Whether base is this run's fixed child of its nonce-owned folder."""
    return base == owned.run_dir() + "/host-git"


def run(c):
    boundary.require_disposable(c)
    if c.host_rc("command -v git >/dev/null") != 0:
        owned.say("SKIP host git: the host has no git")
        return
    state = c.host_out("systemctl --user is-system-running").strip()
    systemd = state in ("running", "degraded", "starting")
    version = c.host_ssh("git --version").stdout.decode().strip()
    base = owned.run_dir() + "/host-git"
    if not c.ok(c.host_rc("mkdir %s" % base) == 0,
                "host git: a folder of this run's own on the host (%s)" % base):
        return
    session = owned.e2e_name("e2e-hostgit")
    repo = owned.run_dir() + "/host-git/repo-" + owned.RUN
    marks = base + "/marks"
    made = []
    before = owned.sessions_on(c, c.envx)
    if before is None:
        c.ok(False, "host git: the host's list before the start succeeded")
        c.state["keep_run_dir"] = True
        owned.say("WARN host git: the host did not answer, so %s stays on the host" % base)
        return
    answered = False
    try:
        fixture(c, repo, marks)
        sid, out, err = owned.start_new(c, c.envx, c.created, session, "--dir", repo, "--worktree", "--",
                                        *owned.program("/bin/sh", "exec sleep 600"))
        made.extend(i for i in c.created if i not in {s["id"] for s in before} and
                    owned.exact(c, c.envx, i, session))
        said = (out + err).strip()
        if re.search(r"git version 2\.54\b", version):
            answered = not sid and "2.55 or later" in said
            ran = c.host_ssh("ls %s" % marks).stdout.decode().split()
            c.ok(answered and not ran, "host git: %s refuses a worktree, and runs no hook (%s)" %
                 (owned.safe_text(version), owned.safe_text(said[-80:])))
            return
        if not c.ok(sid, "host git: new --worktree in a repository with hooks, config hooks, an fsmonitor and a clean filter (%s)" %
                    owned.safe_text(said[-80:])):
            return
        answered = True
        if sid not in made:
            made.append(sid)
        cwd = session_cwd(c, c.envx, sid) or ""
        if not c.ok(touch_worktree(c, sid, session, repo, cwd),
                    "host git: the session works in a worktree of this run's repository (%s)" % owned.safe_text(cwd)):
            return
        said = kill(c, c.envx, sid, session)
        removed = c.ok(delete_sessions(c, made) and "removed the clean worktree" in said.lower(),
                       "host git: kill removes the clean worktree (%s)" % owned.safe_text(said[:80]))
        ran = c.host_ssh("ls %s" % marks).stdout.decode().split()
        hooks = [m for m in ran if m != "filter"]
        c.ok(removed and not hooks, "host git: no hook, config hook or fsmonitor of the repository ran, with %s (%s)" %
             (owned.safe_text(version), owned.safe_text(", ".join(hooks) or "none ran")))
        cgroups = c.host_ssh("cat %s/filter 2>/dev/null; true" % marks).stdout.decode().split()
        if not systemd:
            owned.say("SKIP host git: no systemd user manager (%s), so git runs without a scope" % owned.safe_text(state or "none"))
        elif c.ok(cgroups, "host git: git status ran the clean filter, on a host with a systemd user manager"):
            outside = [g for g in cgroups if ("/%s.slice/" % c.NAME) not in g]
            c.ok(not outside, "host git: the clean filter ran under %s.slice, %d times (%s)" %
                 (c.NAME, len(cgroups), owned.safe_text((outside or cgroups)[0])))
        # This control deliberately leaves the fixture's own config enabled.
        # System/global config and inherited git redirects remain disabled.
        c.host_ssh("rm -f %s/* && cd %s && %s worktree add -q -b e2e-plain %s/plain && %s -C %s/plain status --porcelain >/dev/null" %
                   (marks, repo, base_git(), base, base_git(), base))
        ran = set(c.host_ssh("ls %s" % marks).stdout.decode().split())
        want = {"fsmonitor"} | set(HOOKS) | ({"config-hook"} if at_least(version, 2, 54) else set())
        c.ok(want <= ran, "host git: plain git runs the fixture's %s, so the checks above mean something (%s)" %
             (", ".join(sorted(want)), owned.safe_text(", ".join(sorted(ran)))))
    finally:
        cleanup(c, session, base, answered, before, made)


def host_sessions(c, envx):
    """The host's validated session rows, or None when its list failed."""
    return owned.sessions_on(c, envx)


def session_cwd(c, envx, sid):
    """The folder of an owned session, as the host's list says."""
    if sid not in c.created or sid not in boundary.OWNED:
        return None
    return next((s.get("cwd") for s in host_sessions(c, envx) or [] if s["id"] == sid), None)


def touch_worktree(c, sid, name, repo, cwd):
    """Retimes only the owned session's expected physical worktree README.

    A substring of a host-supplied cwd proves nothing. Check the complete
    expected path, the ID/name pair and physical directory before touching.
    """
    if sid not in c.created or sid not in boundary.OWNED or not owned.exact(c, c.envx, sid, name):
        return False
    slug = re.sub(r"[^a-z0-9]+", "-", name.lower()).strip("-")[:40].rstrip("-") or "task"
    tail = "/%s/worktrees/%s/%s-%s" % (c.NAME, os.path.basename(repo), slug, sid)
    if not isinstance(cwd, str) or not re.fullmatch(r"/[A-Za-z0-9._/+:-]+", cwd) or not cwd.endswith(tail):
        return False
    # The expected data directory comes from the helper's XDG rule, not cwd.
    return c.host_rc('expected="%s/%s/worktrees/%s/%s-%s"; [ %s = "$expected" ] && '
                     '[ ! -L "$expected" ] && [ ! -L "$expected/README" ] && [ -f "$expected/README" ] && '
                     '[ "$(cd -P "$expected" && pwd)" = "$expected" ] && '
                     'touch -m -d "@$(( $(date +%%s) + 5 ))" "$expected/README"' %
                     (owned.xdg_sh("XDG_DATA_HOME", ".local/share"), c.NAME, os.path.basename(repo), slug, sid, q(cwd))) == 0


def kill(c, envx, sid, name):
    """Exercise the old client verb only on this run's exact owned ID."""
    if not re.fullmatch(r"(?=[a-z2-7]*[2-7])[a-z2-7]{8}", sid) or sid not in c.created or sid not in boundary.OWNED or not owned.exact(c, envx, sid, name):
        return "the session is not this run's exact owned ID"
    try:
        _, out, err = owned.run_cmd([c.cli, "kill", c.HOST + "/" + sid], env=envx, timeout=60)
        return (out + err).strip()
    except boundary.TimeoutExpired:
        return "kill did not answer"


def appeared(c, name, before, made):
    """Recover only fresh IDs with this run's token after an unanswered start."""
    for _ in range(ATTEMPTS):
        owned.leftovers(c, c.envx, name, before, c.created, "waiting for the host's answer")
        rows = host_sessions(c, c.envx)
        if rows is not None:
            found = [s["id"] for s in rows if s["name"] == name and owned.new_id(s["id"], before)
                     and s["id"] in c.created and s["id"] in boundary.OWNED]
            if found:
                made.extend(i for i in found if i not in made)
                return True
        time.sleep(2)
    return False


def delete_sessions(c, made):
    """Retry exact-ID helper cleanup; a name never authorises a deletion."""
    for _ in range(ATTEMPTS):
        remaining = [sid for sid in made if sid in c.created]
        owned.clean_up(c, remaining)
        for sid in made:
            if sid not in remaining and sid in c.created:
                c.created.remove(sid)
        rows = host_sessions(c, c.envx)
        if rows is not None and not any(s["id"] in made for s in rows):
            return True
        time.sleep(2)
    return False


def cleanup(c, session, base, answered, before, made):
    """Keep all files when the host cannot confirm that the owned IDs went."""
    if not answered and not made and not appeared(c, session, before, made):
        owned.say("WARN host git: new gave no clear answer, and no session called %s appeared, so %s stays on the host" % (session, base))
        c.state["keep_run_dir"] = True
        return
    if not delete_sessions(c, made):
        owned.say("WARN host git: a session called %s may still run, so %s stays on the host" % (session, base))
        c.state["keep_run_dir"] = True
        return
    # The canonical finally removes the nonce-owned run folder. No extra rm.
    c.ok(own_base(base), "host git: cleanup names only the run's own folder")


def base_git():
    """Git with inherited redirects, system/global config and signing off.

    The fixture's repository config stays enabled for its positive control.
    """
    return ('env -i HOME="$HOME" PATH="$PATH" GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null '
            'git -c user.name=e2e -c user.email=e2e@example.com -c commit.gpgsign=false -c tag.gpgsign=false')


def fixture(c, repo, marks):
    """Make only the nonce-owned fixture, including hooks and a clean filter."""
    git = base_git()
    c.host_ssh("mkdir -p %s && %s init --template= -q -b main %s && cd %s && mkdir -p .git/hooks && "
               "echo app > README && echo 'README filter=e2emark' > .gitattributes && "
               "%s add README .gitattributes && %s commit -q -m base && "
               "%s config filter.e2emark.clean %s && "
               "printf '#!/bin/sh\\ntouch %s/fsmonitor\\nexit 1\\n' > .git/fsmonitor.sh && chmod +x .git/fsmonitor.sh && "
               '%s config core.fsmonitor "$PWD/.git/fsmonitor.sh" && '
               "%s config hook.e2e.command %s && "
               "printf '#!/bin/sh\\ntouch %s/post-checkout\\n' > .git/hooks/post-checkout && chmod +x .git/hooks/post-checkout && "
               "printf '#!/bin/sh\\ntouch %s/post-index-change\\n' > .git/hooks/post-index-change && chmod +x .git/hooks/post-index-change && "
               "printf '#!/bin/sh\\ntouch %s/reference-transaction\\n' > .git/hooks/reference-transaction && chmod +x .git/hooks/reference-transaction && "
               "%s config --add hook.e2e.event post-checkout && %s config --add hook.e2e.event post-index-change && "
               "%s config --add hook.e2e.event reference-transaction" %
               (marks, git, repo, repo, git, git, git, q("cat /proc/self/cgroup >> %s/filter; cat" % marks),
                marks, git, git, q("touch %s/config-hook" % marks), marks, marks, marks, git, git, git))
