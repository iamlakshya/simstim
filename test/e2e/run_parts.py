"""The parts of run.py that touch the host, so that the offline tests can
test them: the signals that end a run, the checks that run.py skips, the
script check of new --json and its delete, the pull check, the read-only
alerts check, and the run's last step, which deletes the run's sessions
by ID, removes the run's folder, and gives up the lock. run.py calls the
last step in a finally, so it runs also when a check fails with an
exception, after an early exit, and after SIGTERM or SIGHUP, also when one
of these comes while the run takes its lock.

On a test host, which is someone else's machine, the run does only five
kinds of thing (run.py, and test_rules.py's allow list): it starts sessions
of its own, runs commands that only read, attaches and sends only the
detach key, deletes its own sessions by ID, and makes files only in its
own folder. Each part runs only while the run holds its lock. Before its
first start, the run stops on a host whose alerts are on, or whose
sessions' slice limit a start would change (start_gate).
"""
import json, re, shutil, signal, sys, tempfile, time

import allowed, boundary, owned

# How many times the last step lists the host's sessions, and the seconds
# between two tries. The offline tests set the wait to 0.
LIST_TRIES = 3
LIST_WAIT = 2.0

# An alerts file path as the helper names it: in the home folder, or
# absolute, with no character that a shell reads.
ALERTS_PATH = re.compile(r"(~/|/)([A-Za-z0-9._+-]+/)*alerts\.toml")

# The checks that run only on a disposable host (run.py's DISPOSABLE), as they
# do more than the five kinds of thing: what each would do, and the Go tests
# that cover it in a temporary home, on every other host. docs/contributing/testing.md lists them too, and
# test_rules.py checks that each Go test is there.
SKIPPED = {
    "typing": ("types commands and text into a session",
               ["internal/host.TestAttachInputAndResize"]),
    "kitty keys": ("sends kitty keyboard sequences and quit keys to the host's key tester in a session",
                   ["internal/keys.TestPassesOtherKeys", "internal/host.TestAttachInputAndResize"]),
    "paste": ("drops a file and pastes an image, which types into a session and uploads to the host",
              ["internal/client.TestDroppedFilesAreUploaded", "internal/client.TestImagePaste"]),
    "desktop notification": ("prints an OSC 777 desktop notification in a session, which raises a needs_input alert event",
                             ["internal/host.TestScannerSignals", "internal/client.TestNotifyOthers"]),
    "debug": ("turns the bridge's debug on, which writes to the helper's shared debug folder and prunes it",
              ["internal/host.TestDebugStartAndStopInARunningSession", "internal/host.TestTheBridgeRecordsItsRequests"]),
    "resume": ("resumes a session, which runs its agent again", ["internal/host.TestResumeAnEndedAgent", "internal/host.TestResumeWorksOutTheCap"]),
    "shadow terminal": ("kills a process on the host", ["internal/host.TestVTRestart", "internal/host.TestSnapshotFromTheShadowTerminal"]),
    "worktree": ("makes a worktree outside the run's folder", ["internal/host.TestWorktreeSessions"]),
    "agents install": ("installs an agent on the host", ["internal/cli.TestAgentsInstallNoInput", "internal/cli.TestAgentsOfInstallAndLogin"]),
    "agents login": ("starts a login session that has no name of the run's",
                     ["internal/cli.TestAgentsLoginJSON", "internal/cli.TestRunningLogin", "internal/cli.TestAgentsLoginClaude"]),
    "guided setup": ("changes the host's linger, restore unit and terminfo",
                     ["internal/cli.TestSetupWithFlags", "internal/host.TestBridgeDoctorAndSetup",
                      "internal/cli.TestSetupNeedsATerminalOrFlags", "internal/cli.TestSetupStopsAndSaysWhatWasDone"]),
    "stale socket fix": ("makes a socket in the helper's runtime folder, and applies a fix",
                         ["internal/host.TestFixesFindAndApply", "internal/host.TestFixesApplyOnlyWhatIsAsked"]),
    "alerts": ("sets, tests and turns off the host's alerts", ["internal/host.TestAlertsOps", "internal/host.TestHolderRaisesEvents",
                                                              "internal/cli.TestAlertsCommands"]),
    "kill": ("deletes a session by its address with the client's kill", ["internal/cli.TestSessionCommands"]),
    "double detach": ("sends the detach key twice, which the client passes to the session's program",
                      ["internal/client.TestDetachKeyTwiceGoesToTheAgent"]),
    "memory cap": ("makes a process go over a memory cap: an oom and a memory event, which the product alerts on",
                   ["internal/host.TestOOMChildKillThenAgentOOM", "internal/host.TestOOMAgentKill", "internal/host.TestMemoryWarnsOnce",
                    "internal/host.TestLimitSessions"]),
}


def skip(check):
    """Prints the SKIP line of check, from SKIPPED, on a host that the run
    did not make: the check runs only on a disposable host."""
    what, tests = SKIPPED[check]
    owned.say("SKIP %s: it %s, which the e2e run does only on a disposable host. Covered here by %s." % (check, what, ", ".join(tests)))


# The value of E2E_DISPOSABLE_MARKER written in /etc/e2e-disposable on a host
# created for the run (boundary.MARKER): 32 hex characters or more.
MARKER = re.compile(r"[0-9a-f]{32,}")


def disposable(t, flag, marker=""):
    """Whether the host is disposable: E2E_DISPOSABLE is 1, and the host holds
    marker (E2E_DISPOSABLE_MARKER, 32 hex characters or more) in
    /etc/e2e-disposable, written when CI or a harness created it for the run.
    An owner's host, or E2E_DISPOSABLE=1 set on a laptop alone, never matches."""
    return bool(flag == "1" and marker and MARKER.fullmatch(marker) and t.marker() == marker)


# A helper version as the client names it: no character that a shell reads.
HELPER_VERSION = re.compile(r"[A-Za-z0-9._-]+")


def clean_environ(name, environ):
    """Takes the client's debug variable, <NAME>_DEBUG_FILE, out of environ,
    so that no client of the run turns its debug on: a bridge's debug
    writes to the helper's shared debug folder on the host, and prunes it.
    Returns the value it took, or None."""
    return environ.pop(name.upper() + "_DEBUG_FILE", None)


def helper_path(c, envx):
    """This build's helper version and versioned path on the host, from local
    version --json (which reaches no host), or (None, None)."""
    info = owned.json_of([c.cli, "version", "--json"], envx)
    ver = info.get("helper") if isinstance(info, dict) else None
    if not isinstance(ver, str) or not HELPER_VERSION.fullmatch(ver):
        return None, None
    return ver, '"$HOME/.local/share/%s/bin/%s/%s-host"' % (c.NAME, ver, c.NAME)


def helper_ready(c, envx, may_install):
    """Whether this build's host helper is on the host, by a check that only
    reads, before any client call that could install it: an install, and
    the prune of old versions after it, change the owner's host. Returns
    (True, None), or (False, why, with a Try line). On a disposable host,
    may_install, the client may install it. It sets c.HELPER to that
    version's own path, which every delete of the run uses, never the
    current link."""
    ver, path = helper_path(c, envx)
    if ver is None:
        return False, "the client's version --json names no helper version.\nTry: just build"
    there = c.host_rc('test -x "$HOME/.local/share/%s/bin/%s/%s-host"' % (c.NAME, ver, c.NAME)) == 0
    if there or may_install:
        c.HELPER = path
    if there:
        return True, None
    if may_install:
        owned.say("NOTE: the host has no helper %s: the client installs it on this disposable host" % ver)
        return True, None
    return False, ("the host %s has no helper %s, the one of this build, and the e2e run never installs a helper on someone else's machine.\n"
                   "Try: ask the host's owner to run %s host test %s. It installs the helper %s, points the current link at it, "
                   "and removes the helper versions that nothing uses." % (c.HOST, ver, owned.shown(c.cli), c.HOST, ver))


def helper_versions(c):
    """The helper versions and current link target on the host, a sorted
    list, by a read; or None when the host did not answer."""
    out = c.host_out('ls -1A "$HOME/.local/share/%s/bin" 2>/dev/null; printf \'current->\'; '
                     'readlink "$HOME/.local/share/%s/bin/current" 2>/dev/null; echo; echo e2e-done' % (c.NAME, c.NAME)).split("\n")
    return sorted(v for v in out[:-2] if v) if out[-2:] == ["e2e-done", ""] else None


def slice_limit(c):
    """The sessions' slice limit on the host, by a read: "none" where no
    start can change it, the MemoryMax that systemd has, or None when the
    host did not answer."""
    out = c.host_out(allowed.SLICE % c.NAME).strip()
    return out if re.fullmatch(r"none|infinity|[0-9]+", out) else None


def safe_field(v):
    """v as single-line diagnostic text with terminal and BiDi controls removed."""
    return owned.safe_text(v).replace("\n", "?")


def start_gate(c, envx, state):
    """Before the first start, on a host that the run did not make: what
    stops the run, with a Try line, or None. An alerts file means that the
    run's sessions would send their events to the owner's destinations, and
    write to the alerts log. A slice limit other than the one that this
    build sets means that a start would change it. It records the helper
    versions and the slice limit in state, for end_check."""
    state["helpers"] = helper_versions(c)
    state["slice"] = slice_limit(c)
    if state["helpers"] is None or state["slice"] is None:
        return "the host did not answer the reads of its helper versions and its sessions' slice limit.\nTry: run it again"
    info = owned.json_of([c.cli, "host", "alerts", c.HOST, "--json"], envx)
    if not alerts_report(info):
        return "host alerts --json gave no alerts report, so the run cannot tell whether its sessions would send alerts.\nTry: just build"
    state["alerts"] = info
    if info["alerts"]["exists"]:
        return ("the host has an alerts file, %s: the run's sessions would send their events to its destinations, "
                "and the alerts log would record them.\nTry: ask the host's owner. Run the e2e on a disposable host, or on a host "
                "with no alerts file." % safe_field(info["alerts"]["path"]))
    if state["slice"] == "none":
        return None
    doctor = owned.json_of([c.cli, "host", "doctor", c.HOST, "--json"], envx)
    want = doctor.get("memory_max") if isinstance(doctor, dict) else None
    if type(want) is not int or str(want) != state["slice"]:
        return ("a start would change the sessions' slice limit on %s from %s to %s, this build's.\nTry: ask the host's owner. "
                "Run the e2e on a disposable host, or after a start of the owner's own sets the limit."
                % (safe_field(c.HOST), safe_field(state["slice"]), safe_field(want)))
    return None


def end_check(c, state):
    """At the end, by reads: the problems, when the helper versions or the
    slice limit changed during the run, with what changed and how to put
    the slice limit back."""
    problems = []
    if state.get("helpers") is not None:
        now = helper_versions(c)
        if now != state["helpers"]:
            before_h = ", ".join(safe_field(x) for x in state["helpers"])
            now_h = "?" if now is None else ", ".join(safe_field(x) for x in now)
            problems.append("the helper versions on %s changed during the run: before %s, now %s"
                            % (safe_field(c.HOST), before_h, now_h))
    if state.get("slice") not in (None, "none"):
        now = slice_limit(c)
        if now != state["slice"]:
            problems.append("the sessions' slice limit on %s changed during the run, from %s to %s. Put it back on the host: "
                            "systemctl --user set-property %s.slice MemoryMax=%s"
                            % (safe_field(c.HOST), safe_field(state["slice"]), safe_field(now),
                               safe_field(c.NAME), safe_field(state["slice"])))
    return problems


def host_hooks(c):
    """The host's own hooks, setup and resume, that run in each session the
    run starts, as in every session on the host (testing.md's exception).
    Only reads. Returns their names, or None when the host did not answer."""
    out = c.host_out('for h in setup resume; do [ -e "%s/%s/hooks/$h" ] && echo "$h"; done; echo e2e-done'
                     % (owned.xdg_sh("XDG_CONFIG_HOME", ".config"), c.NAME)).split()
    return [h for h in out if h in ("setup", "resume")] if out[-1:] == ["e2e-done"] else None


def hook_notice(c):
    """The notice that run.py prints before its first start: the host's own
    hooks, which run in each session that the run starts (exception E)."""
    hooks = host_hooks(c)
    if hooks is None:
        return "NOTE: the host did not say which of its own hooks are there"
    return "NOTE: the host's own hooks run in each session that the run starts, as in every session there: %s" % (", ".join(hooks) or "none")


def ignore_signals():
    """Blocks and ignores SIGINT, SIGTERM and SIGHUP so no signal can abort
    the unwind, R.remove() or exit reporting once the run is ending."""
    stop_set = {signal.SIGINT, signal.SIGTERM, signal.SIGHUP}
    while True:
        try:
            try:
                signal.pthread_sigmask(signal.SIG_BLOCK, stop_set)
            except (AttributeError, ValueError, OSError):
                pass
            for s in stop_set:
                try:
                    signal.signal(s, signal.SIG_IGN)
                except (ValueError, OSError):
                    pass
            try:
                signal.pthread_sigmask(signal.SIG_UNBLOCK, stop_set)
            except (AttributeError, ValueError, OSError):
                pass
            return
        except BaseException:  # noqa: BLE001 - ignore all three before proceeding
            pass


def exit_on_signals():
    """Makes SIGTERM and SIGHUP end the run as sys.exit does, with exit code
    128 and the signal's number, so that the finally around the suite runs
    and cleans up. Python's own end on these signals runs no finally. Once
    the first signal arrives, ignore_signals() silences all three."""
    def stop(signum, frame):
        ignore_signals()
        while True:
            try:
                sys.exit(128 + signum)
            except KeyboardInterrupt:
                pass

    for s in (signal.SIGTERM, signal.SIGHUP):
        signal.signal(s, stop)


def alerts_report(info):
    """Whether info, what `host alerts <host> --json` printed, is the
    alerts report: "v": 1, the helper's alerts file with a path that is safe
    as a shell word and whether it is there, and the last deliveries. On a
    host that the run did not make, no check may cause any of the product's
    default alert events (internal/alerts/event.go: needs_input, turn_done,
    failed, oom, memory): owner-host sessions emit no OSC 777/9/99, bell or
    hook notification (no needs_input), never work 60s before going idle (no
    turn_done), sleep until helper rm mutes alerts before SIGHUP (no failed),
    and run without memory caps (no oom or memory). start_gate also stops
    before the first start when alerts.toml exists, so custom events such as
    started or exited cannot fire either."""
    if not isinstance(info, dict) or not owned.version_1(info.get("v")):
        return False
    a, log = info.get("alerts"), info.get("log")
    if not isinstance(a, dict) or not isinstance(log, (list, type(None))):
        return False
    path, exists = a.get("path"), a.get("exists")
    return (isinstance(path, str) and isinstance(exists, bool) and ALERTS_PATH.fullmatch(path) is not None
            and "/../" not in path and "/./" not in path)


def finish(c, created, state):
    """The run's last step, in run.py's finally. Each step runs, also when
    one before it fails. A signal or Ctrl+C during the clean-up does not
    stop it: the clean-up ignores SIGINT, SIGTERM and SIGHUP until it ends,
    and each step, the waits between tries too, catches what ends it.
      1. lists the host, with tries again, and adds the sessions named
         e2e-*-<token> that the run did not know yet, as from a slow start,
         to created. A list that still fails is a failure of the clean-up;
      2. deletes the run's sessions by ID (owned.clean_up);
      3. removes the run's own folder, while it holds the token;
      4. last, reads the lock's token back: a lock with the run's token is
         given up, unless the clean-up failed; a lock with another token,
         or none, is never touched.
    It checks each step's exit code, and prints each failure with the safe
    command that a person runs. A clean-up that failed keeps the lock, so
    that no other run starts before a person looks. Returns whether
    nothing is left."""
    stop = {signal.SIGINT, signal.SIGTERM, signal.SIGHUP}
    blocked = None
    try:
        # Blocked first, all at once: no signal comes between the handlers.
        blocked = signal.pthread_sigmask(signal.SIG_BLOCK, stop)
    except (AttributeError, ValueError, OSError):
        pass
    kept = {}
    try:
        for s in stop:
            try:
                kept[s] = signal.signal(s, signal.SIG_IGN)
            except ValueError:  # not the main thread: the steps still catch them
                pass
        state["finished"] = cleanup(c, created, state)
        return state["finished"]
    finally:
        if blocked is not None:
            signal.pthread_sigmask(signal.SIG_SETMASK, blocked)
        for s, handler in kept.items():
            signal.signal(s, handler)


def step(what, fn):
    """Runs one step of the clean-up, and returns its value, or None when it
    raised: also KeyboardInterrupt or SystemExit, so the next step runs."""
    try:
        return fn()
    except BaseException as e:  # noqa: BLE001 - the next step must run
        owned.say("CLEAN-UP FAILED: %s: %s" % (what, owned.safe_text(repr(e))))
        return None


def cleanup(c, created, state):
    """The steps of finish."""
    problems = []
    if not state.get("client_ok", True):
        # No client call ran: no session of the run's can be on the host,
        # and a client call now could install the helper.
        found = []
    else:
        found = None
    for i in range(LIST_TRIES if found is None else 0):
        found = step("list the host's sessions", lambda: owned.sessions_on(c, getattr(c, "envx", None) or {}))
        if found is not None:
            break
        if i + 1 < LIST_TRIES:
            step("wait before the list is tried again", lambda: time.sleep(LIST_WAIT))
    if found is None:
        problems.append("the list of the host's sessions failed %d times, so the run cannot tell whether a session of its own came late. "
                        "When the host answers, list its sessions on the laptop: %s ls --host %s. Then delete each one named e2e-*-%s "
                        "by its ID on the host: %s rm <id>" % (LIST_TRIES, owned.shown(c.cli), c.HOST, owned.RUN, c.HELPER))
    before, unowned = state.get("before"), []
    for s in found or []:
        sid = s["id"]
        if s["name"].startswith("e2e-") and s["name"].endswith("-" + owned.RUN) and owned.ID.fullmatch(sid) and sid not in created:
            if before is None:
                unowned.append(sid)
            elif sid not in before:
                owned.say("CLEAN-UP: %s/%s (%s) came after the list of its start: deleted by ID" % (safe_field(c.HOST), sid, safe_field(s["name"])))
                owned.own(sid, created)
    if unowned:
        problems.append("sessions %s on %s match e2e-*-%s, and the list before the run failed, so the clean-up leaves them"
                        % (", ".join(unowned), safe_field(c.HOST), owned.RUN))
    failed = step("delete the run's sessions by ID", lambda: owned.clean_up(c, created))
    failed = list(created) if failed is None else failed
    if failed:
        problems.append("the run's sessions %s are not deleted. Delete each by its ID on the host: %s"
                        % (", ".join(failed), "; ".join("%s rm %s" % (c.HELPER, i) for i in failed)))
    if state.get("disposable"):  # only a disposable host runs the checks that make these
        import disposable
        more = step("clean up what the checks of the disposable host made", lambda: disposable.cleanup(c, state))
        problems.extend(more if more is not None else ["the clean-up of what the disposable host's checks made did not finish"])
    if not step("remove the run's folder", lambda: owned.remove_run_dir(c)):
        d = owned.run_dir()
        problems.append('the run\'s folder %s is still there. Remove it on the host: [ "$(cat %s/.e2e-run)" = %s ] && rm -rf %s'
                        % (d, d, owned.RUN, d))
    if state.get("client_ok", True):
        more = step("read the helper versions and the slice limit again", lambda: end_check(c, state))
        problems.extend(more if more is not None else ["the reads of the helper versions and the slice limit at the end did not finish"])

    lock = step("read the lock's token", lambda: owned.lock_state(c))
    if lock == "mine" and problems:
        owned.say("\nFAIL the clean-up left something on %s, so the lock %s stays: no other e2e run starts before you look. "
                  "After you remove what is listed below, remove the lock on the host: %s\n" % (c.HOST, owned.LOCK, owned.UNLOCK))
    elif lock == "mine" and not step("give up the lock", lambda: owned.release_lock(c)):
        problems.append("the lock %s is still there. When no run goes on, remove it on the host: %s" % (owned.LOCK, owned.UNLOCK))
    elif lock not in ("mine", "other", "none"):
        problems.append("the run cannot read the lock %s. When no run goes on, and the lock holds %s, remove it on the host: %s"
                        % (owned.LOCK, owned.RUN, owned.UNLOCK))
    for p in problems:
        owned.say("CLEAN-UP FAILED: " + p)
    return not problems


def on_session(c, envx, sid, name, what):
    """Whether the check may run a client command on the session sid: it is
    the run's own, and owned.exact() holds. A failure says so."""
    if sid and owned.exact(c, envx, sid, name):
        return True
    c.ok(False, "%s: %s/%s is the run's own session, and its address is exact" % (what, c.HOST, owned.safe_text(sid)))
    return False


def json_check(c, envx, created):
    """A script: new --detach --json, as the scripting guide shows it.
    stdout is one JSON object of the new session, and nothing else; stderr
    is empty. The check then deletes the session as every check does: by
    its ID alone, through the helper's own rm (owned.clean_up). The
    client's kill takes an address, which could reach another session
    named like the ID once this one is gone."""
    name = owned.e2e_name("e2e-json")
    # A command that reads no rc file: the host user's own shell could run
    # one that reaches the owner's programs.
    sid, out, err = owned.start_new(c, envx, created, name, "--", *owned.program("/bin/sh", "exec sleep 600"))
    try:
        js = json.loads(out)
    except ValueError:
        js = {}
    js = js if isinstance(js, dict) else {}
    c.ok(sid and js.get("v") == 1 and js.get("host") == c.HOST and js.get("name") == name
         and js.get("address") in (c.HOST + "/" + name, c.HOST + "/" + sid) and err == "",
         "new --detach --json prints one session object of a new session, and nothing on stderr")
    if not sid:
        return
    if not owned.clean_up(c, [sid]):
        created.remove(sid)
    found = owned.sessions_on(c, envx)
    c.ok(found is not None and not any(s["id"] == sid for s in found), "the script's session is deleted by its ID")


def pull_check(c, envx, created):
    """pull (roadmap N7): a session's branch comes to a local repository over
    the client's SSH, and the local working tree is untouched. The session
    is the run's own: owned.start_new starts it, with a name unique to the
    run, and owns it only by the new ID in its JSON. The check runs pull
    only after owned.exact(). The clean-up deletes the session by ID. It
    never picks a session by name from a list, and never deletes by an
    address. The repository is in the run's own folder, and mkdir refuses
    one that is already there."""
    repo = owned.run_dir() + "/pull"
    made = c.host_rc("mkdir %s && %s init --template= -q -b main %s && cd %s && %s commit -q --allow-empty -m base && "
                     "%s switch -q -c e2e-topic && %s commit -q --allow-empty -m 'from the session'"
                     % (repo, owned.GIT, repo, repo, owned.GIT, owned.GIT, owned.GIT))
    if not c.ok(made == 0, "pull: a repository with a topic branch is made on the host, in the run's own folder (exit %d)" % made):
        return
    name = owned.e2e_name("e2e-pull")
    sid, out, err = owned.start_new(c, envx, created, name, "--dir", repo, "--", *owned.program("/bin/sh", "exec sleep 600"))
    if not c.ok(sid, "pull: new starts a session of its own in the repository (%s)" % owned.safe_text(err.strip()[-200:])):
        return
    local = tempfile.mkdtemp(prefix="e2e-pull-", dir=c.root.path)
    try:
        run_or_timeout(["git"] + allowed.GIT_FLAGS + ["init", "--template=", "-q", "-b", "mine", local], envx, None)
        if not on_session(c, envx, sid, name, "pull"):
            return
        code, out, err = run_or_timeout([c.cli, "pull", "--id", "--", c.HOST + "/" + sid], envx, local)
        _, log, _ = run_or_timeout(["git"] + allowed.GIT_FLAGS + ["log", "--format=%s", "e2e-topic"], envx, local)
        c.ok(code == 0 and "from the session" in log, "pull: the session's branch reaches the local repository (%s)" % owned.safe_text((err or out).strip()[:80]))
        if not on_session(c, envx, sid, name, "pull --into"):
            return
        code, out, err = run_or_timeout([c.cli, "pull", "--id", "--into", "mine", "--", c.HOST + "/" + sid], envx, local)
        c.ok(code != 0 and "checked out here" in err, "pull: refuses to fetch into the branch checked out here")
    finally:
        shutil.rmtree(local, ignore_errors=True)


def run_or_timeout(argv, envx, cwd):
    """owned.run_cmd, with an answer of exit code None when argv did not end
    or printed bytes that are not UTF-8: a failed check, never the end of
    the suite."""
    try:
        return owned.run_cmd(argv, env=envx, timeout=120, cwd=cwd)
    except (owned.TimeoutExpired, ValueError, boundary.Refused) as e:
        return None, "", owned.safe_text(e)
