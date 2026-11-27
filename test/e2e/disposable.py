"""The checks that run only on a disposable host: a container that CI makes
for the run with `host create docker`, and destroys after it, with
E2E_DISPOSABLE=1 set (run.py's DISPOSABLE). On someone else's machine they
would do more than the five kinds of thing that run.py allows, so there
run.py skips them (run_parts.SKIPPED): the bridge's debug, which writes to
the helper's shared debug folder and prunes it, and the guided setup
(guided_setup.py), which changes linger, the restore unit and terminfo.
test_rules.py checks that run.py reaches this module only on such a host.
"""
import os, re

import owned


def debug_check(c, envx, start, exact, seq, seq_path, state):
    """With --debug and every category on, the bytes of a session pass
    unchanged (decision 0025). debug start, save, profile and stop work on
    the running session, and no debug file holds the session's bytes. The
    host's debug folder must hold no file before: the bridge's debug
    prunes it. The folder and what it held go in state before the attach,
    so that run_parts.finish removes the run's new files, also when the
    check stops part way, and fails when one stays."""
    folder = '"%s/%s/debug"' % (owned.xdg_sh("XDG_STATE_HOME", ".local/state"), c.NAME)
    before = debug_listing(c, folder)
    if before is None or before:
        owned.say("SKIP debug: the host's debug folder holds files (or did not answer), and the bridge's debug can prune them")
        return
    state["debug"] = {"folder": folder, "before": before}
    dbg_file = os.path.join(c.root.path, ".e2e-debug.jsonl")
    # The run's own debug folder on the laptop, to remove afterwards what the check made there.
    laptop = os.path.join(c.root.dirs["XDG_STATE_HOME"], c.NAME, "debug")
    laptop_before = set(os.listdir(laptop)) if os.path.isdir(laptop) else None
    sid = start("e2e-bytes-debug", "/bin/bash", "-c", "sleep 3; cat %s; sleep 600" % seq_path)
    if not exact(sid, "attach --debug"):
        return
    t = c.Term([c.cli, "--debug", "--debug-file", dbg_file, "attach", "--id", "--", c.HOST + "/" + sid], env=c.env)
    try:
        c.ok(t.wait_for(rb" END", 30), "escape test output arrived with debug on")
        c.ok(seq in t.buf, "with every debug category on, the attached bytes are byte-identical")
        t.send(b"\x1c")
        t.exit_code(10)
    finally:
        t.close()
    addr = c.HOST + "/" + sid
    for argv, what in (([c.cli, "debug", "start", addr, "--for", "5m"], "debug start"), ([c.cli, "debug", "save", addr], "debug save"),
                       ([c.cli, "debug", "profile", addr, "cpu", "--for", "2s"], "debug profile"), ([c.cli, "debug", "stop", addr], "debug stop")):
        if not exact(sid, what):
            continue
        code, out, err = owned.run_cmd(argv, env=c.envx, timeout=120)
        c.ok(code == 0, "%s works on a running session (%s)" % (what, owned.safe_text((out + err).strip()[:120])))
    dbg = b""
    if os.path.exists(dbg_file):
        with open(dbg_file, "rb") as f:
            dbg = f.read()
    for p in (dbg_file, dbg_file + ".1"):
        if os.path.exists(p):
            os.remove(p)
    c.ok(b'"ev":"key"' in dbg or b'"ev":"ping"' in dbg, "the client's debug file has records")
    c.ok(b"truecolor" not in dbg and b"aGVsbG8" not in dbg, "the client's debug file holds none of the session's bytes")
    host_dbg = c.host_out("cat ~/.local/state/%s/sessions/%s/debug.jsonl" % (c.NAME, sid)).encode()
    c.ok(b'"ev":"start"' in host_dbg and b"truecolor" not in host_dbg, "the host's debug file has records, and none of the session's bytes")
    for f in (set(os.listdir(laptop)) if os.path.isdir(laptop) else set()) - (laptop_before or set()):
        os.remove(os.path.join(laptop, f))
    if laptop_before is None and os.path.isdir(laptop) and not os.listdir(laptop):
        os.rmdir(laptop)
    left = remove_debug(c, state)
    c.ok(left == [], "the run's debug files are removed from the host's debug folder (%s)" % owned.safe_text(", ".join(left or [])))


def debug_listing(c, folder):
    """The names in the host's debug folder, folder (a shell word): an empty
    set when it is not there, or None when the host did not answer. Each
    name is on a line of its own, so a name of spaces is a name too."""
    out = c.host_out('if [ -d %s ]; then ls -A %s; fi; echo e2e-done' % (folder, folder))
    lines = out.split("\n")
    if "e2e-done" not in lines:
        return None
    return set(line for line in lines[:lines.index("e2e-done")] if line != "")


def remove_debug(c, state):
    """Removes from the host's debug folder each file that came during the
    debug check (state's "debug": the folder and what it held before), by
    name. It runs only on a disposable host. Returns the names that are
    still there, or None when the host did not answer."""
    d = state.get("debug")
    if not d:
        return []
    now = debug_listing(c, d["folder"])
    if now is None:
        return None
    left = []
    for n in sorted(now - d["before"]):
        if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", n) or c.host_rc("rm -f -- %s/%s" % (d["folder"], n)) != 0:
            left.append(n)
    if not left:
        state.pop("debug")
    return left


def remove_made(c, made):
    """Removes the host paths in made: those that a check of a disposable
    host made for the run outside its folder, such as a worktree or a
    socket, recorded before they were made. Each has the run's token in
    its last part: a path without it is never removed. It takes each path
    that is gone out of made, and returns those that are not."""
    failed = []
    for path in list(made):
        if ("-" + owned.RUN) not in path.rstrip("'\"").rsplit("/", 1)[-1]:
            made.remove(path)
            continue
        try:
            rc = c.host_rc("rm -rf -- %s" % path)
        except Exception as e:  # noqa: BLE001 - the next path must be tried
            owned.say("CLEAN-UP: rm -rf %s failed: %s" % (owned.safe_text(path), owned.safe_text(e)))
            rc = None
        if rc == 0:
            made.remove(path)
        else:
            failed.append(path)
    return failed


# An alerts file path as the helper names it: in the home folder, or
# absolute, with no character that a shell reads.
ALERTS_PATH = re.compile(r"(~/|/)([A-Za-z0-9._+-]+/)*alerts\.toml")


def alerts_tag():
    """What the run's own alerts file holds, and no other file does: the
    run's folder, which its command destination writes to."""
    return ".e2e-run-" + owned.RUN


def alerts_ready(c, envx, state):
    """The path of the alerts file that the alerts check may make, or None.
    The check never touches a file that is there: when the helper names
    one, the check skips. Else the path goes in state before the check
    makes the file, so that cleanup knows it, also after a crash."""
    info = owned.json_of([c.cli, "host", "alerts", c.HOST, "--json"], envx)
    a = info.get("alerts") if isinstance(info, dict) else None
    path, exists = (a.get("path"), a.get("exists")) if isinstance(a, dict) else (None, None)
    if not c.ok(isinstance(path, str) and isinstance(exists, bool) and ALERTS_PATH.fullmatch(path) and "/../" not in path,
                "alerts: host alerts --json names the host's alerts file"):
        return None
    if exists or c.host_rc("[ ! -e %s ]" % path) != 0:
        owned.say("SKIP alerts: the host has its own alerts file, %s; the alerts check needs a host without one" % owned.safe_text(path))
        return None
    state["alerts_file"] = path
    return path


def remove_alerts_file(c, state):
    """Removes the alerts file that the run made, only while it holds the
    run's token (alerts_tag). A file there that does not hold it is not the
    run's, and stays. A grep that cannot read the file is a failure, never
    a file that is not the run's. Returns whether no file of the run's is
    left: on a failure the state keeps the path, for the next try."""
    f = state.get("alerts_file")
    if not f:
        return True
    tag = alerts_tag()
    try:
        rc = c.host_rc("if [ -e %s ]; then grep -qF -- %s %s; s=$?; if [ $s = 0 ]; then rm -f %s || exit 1; elif [ $s != 1 ]; then exit 2; fi; fi; "
                       "if [ -e %s ]; then grep -qF -- %s %s; [ $? = 1 ]; fi" % (f, tag, f, f, f, tag, f))
    except Exception as e:  # noqa: BLE001 - a failed try is reported
        owned.say("ALERTS: the removal of the run's alerts file failed: %s" % owned.safe_text(e))
        return False
    if rc != 0:
        owned.say("ALERTS: the removal of the run's alerts file failed (exit %s)" % owned.safe_text(rc))
        return False
    state["alerts_file"] = None
    return True


def cleanup(c, state):
    """The clean-up of what the checks of a disposable host made, in
    run_parts.finish: the debug check's files, the paths recorded in
    state's "made", and the alerts file. Returns the problems, each with
    the command that removes what is left."""
    problems = []
    if state.get("debug"):
        left = remove_debug(c, state)
        if left is None or left:
            problems.append("the debug check's files %s are still in the host's debug folder %s"
                            % (owned.safe_text(", ".join(left or ["(the host did not answer)"])), owned.safe_text(state["debug"]["folder"])))
    left = remove_made(c, state.get("made") or [])
    if left:
        problems.append("the paths %s that a check made for the run are still there. Remove each on the host: rm -rf -- <path>"
                        % ", ".join(owned.safe_text(p) for p in left))
    if state.get("alerts_file") and not remove_alerts_file(c, state):
        f = state["alerts_file"]
        problems.append("the alerts file %s that the run made is still there. Remove it on the host: grep -qF -- %s %s && rm -f %s"
                        % tuple(owned.safe_text(v) for v in (f, alerts_tag(), f, f)))
    return problems
