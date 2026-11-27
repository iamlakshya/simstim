"""End-to-end tests against a real host. Run with `just e2e`.

Needs: a built client, and a host already added with `host add`.
  E2E_HOST        the host's name in the client (required)
The product name is read from internal/brand, so nothing here spells it.

A test host is someone else's machine. On it the run does only five kinds
of thing (docs/contributing/testing.md, allowed.py, which boundary.py
checks before each call and test_rules.py in the code):
  1. it starts sessions of its own: a new ID, and the run's token in the name;
  2. it runs commands that only read: lists, doctor, alerts --json, version;
  3. it attaches, and sends nothing but the detach key;
  4. it deletes its own sessions by ID, through the helper's rm;
  5. it makes and removes files only in its own folder, ~/.e2e-run-<token>.
Before its first start it stops on a host whose alerts are on, or whose
sessions' slice limit a start would change. Every other check prints a
SKIP line that names the Go tests that cover it (run_parts.SKIPPED). On a
disposable host made for the run, authenticated by the matching host marker
and E2E_DISPOSABLE_MARKER, those checks run too
(disposable*.py, agent_login.py, guided_setup.py). Every client process
of the run has a root of its own on the laptop (boundary.Root). One run
at a time on a host: the run takes a lock inside the try, and the finally
reads the lock's token back and gives up only a lock of its own, after
its clean-up, also after SIGTERM or SIGHUP.
"""
import collections, json, os, re, signal, sys, tempfile, time, traceback, types

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
sys.path.insert(0, HERE)
import allowed, boundary, joined_checks, owned, run_parts  # noqa: E402

with open(os.path.join(ROOT, "internal/brand/brand.go")) as _f:
    NAME = re.search(r'^const Name = "(.*)"', _f.read(), re.M).group(1)
cli = os.path.abspath(sys.argv[1] if len(sys.argv) > 1 else os.path.join(ROOT, "bin", NAME))
# No client of the run turns its debug on: a bridge's debug writes to the
# helper's shared debug folder on the host, and prunes it.
run_parts.clean_environ(NAME, os.environ)
HOST = os.environ.get("E2E_HOST")
if not HOST:
    sys.exit("set E2E_HOST to the name of a host added with `host add`")
# The run's own root on the laptop: the client never reads the user's
# config.toml, and never shares the user's control sockets.
R = boundary.Root(NAME, cli=cli, hosts=os.path.join(os.environ.get("XDG_CONFIG_HOME") or os.path.expanduser("~/.config"), NAME, "hosts.toml"),
                  host=HOST)
T = boundary.Target(R)
env = {"TERM": "xterm-ghostty", "TERM_PROGRAM": "ghostty", "COLORTERM": "truecolor"}
results = []

def ok(c, m):
    results.append(bool(c)); owned.say(("PASS " if c else "FAIL ") + m); return c

# A disposable host made for this run and destroyed after it. Its harness
# writes E2E_DISPOSABLE_MARKER into /etc/e2e-disposable and sets
# E2E_DISPOSABLE=1. Only an authenticated disposable host runs checks that
# change shared state.
DISPOSABLE = run_parts.disposable(T, os.environ.get("E2E_DISPOSABLE", ""), os.environ.get("E2E_DISPOSABLE_MARKER", ""))

# What the shared helpers need of the run (owned.py, run_parts.py).
envx = R.env(env)
created = []  # the run's own sessions, by ID: the finally deletes them by ID
names = {}    # each session of created, by ID: the name that the run gave it
made = []  # the host paths that a check of a disposable host made, with the run's token
state = {"before": None, "client_ok": False, "disposable": DISPOSABLE, "made": made}
C = types.SimpleNamespace(cli=cli, HOST=HOST, NAME=NAME, HELPER=None, env=env, envx=envx, ok=ok, Term=boundary.term, root=R,
                          host_ssh=T.ssh, host_rc=T.rc, host_out=T.out, created=created)
boundary.CTX = C

def start(base, *argv, flags=()):
    """Starts a session at a terminal, with a name unique to the run, and
    returns its ID, or None. argv is a shell, -c and a script, which runs
    after env -i (owned.program). owned.start_on_terminal owns the session
    only by the client's own last "Started" line, and only when the ID is
    new."""
    name = owned.e2e_name(base)
    sid = owned.start_on_terminal(C, envx, created, name, list(flags) + ["--"] + owned.program(argv[0], argv[2]))
    ok(sid, "%s: new starts a session of its own" % base)
    if sid:
        names[sid] = name
    return sid

class NoTerm:
    """Stands for the attach that the run did not make, to a session that
    is not its own or whose address is not exact: it shows nothing, and the
    checks that read it fail. Its exit code is no number and not None, so
    that a check of a client that still runs fails too."""
    buf, pid = b"", None
    def read(self, timeout=0.5): return self.buf
    def wait_for(self, pattern, timeout=20): return False
    def send(self, b): pass
    def resize(self, cols, rows): pass
    def signal(self, sig): pass
    def exit_code(self, timeout=10): return "no attach"
    def close(self): pass

def exact(sid, what):
    """Whether the run may run a client command on the session sid: it is
    the run's own, and owned.exact() holds. A failure says so."""
    return run_parts.on_session(C, envx, sid, names.get(sid), what)

def attach(sid, environ=None, **kw):
    """Attaches to the session sid by host/ID, only right after exact()
    found it, by ID and name. Returns a Term, or a NoTerm. On a host that
    the run did not make, the boundary lets an attach get only the detach
    key."""
    if not exact(sid, "attach"):
        return NoTerm()
    return boundary.term([cli, "attach", "--id", "--", HOST + "/" + sid], environ or env, **kw)

def session_rows():
    """The host's sessions by ID, or {} when the list failed or did not end."""
    _, ls, _ = run_parts.run_or_timeout([cli, "ls", "--json", "--host", HOST], envx, None)
    try:
        return {s["id"]: s for h in json.loads(ls) for s in h["sessions"]}
    except (ValueError, KeyError, TypeError):
        return {}

C.start, C.attach, C.exact, C.names, C.state, C.made = start, attach, exact, names, state, made
C.host_machine, C.session_rows, C.HERE, C.DISPOSABLE = T.machine, session_rows, HERE, DISPOSABLE
C.target = T

# SIGTERM and SIGHUP end the run as sys.exit does, so that the finally
# below runs: Python's own end on them runs none.
run_parts.exit_on_signals()
try:
    _, C.HELPER = run_parts.helper_path(C, envx)
    # One e2e run at a time on a host: the run takes a lock on it, inside
    # the try, so that the finally runs also when a signal comes while the
    # lock is taken. The finally reads the lock's token back, and gives up
    # only a lock of the run's own. A run that finds the lock held stops.
    locked, holder = owned.take_lock(C)
    if not locked:
        sys.exit("the lock %s on %s is held%s. One e2e run at a time on a host.\n%s"
                 % (owned.LOCK, HOST, " by the run %s" % holder if owned.TOKEN.fullmatch(holder) else "", owned.leftover_help(C, holder)))
    # The run's own folder, for every file that it makes on the host. mkdir
    # refuses a folder that is already there: then the run stops, and its
    # clean-up leaves that folder alone, as it holds no nonce of the run's.
    if not ok(owned.make_run_dir(C), "the run's own folder %s is made on the host" % owned.run_dir()):
        sys.exit(1)
    # The client installs its helper when the host has none of this build,
    # and prunes the old ones: only on a disposable host.
    ready, why = run_parts.helper_ready(C, envx, DISPOSABLE)
    if not ready:
        sys.exit("FAIL " + why)
    state["client_ok"] = True
    # The run's own config sets no detach key: every key that the run sends
    # to detach is the default one.
    if not ok(R.detach_key_is_the_default(), "the run's own client config has the default detach key"):
        sys.exit(1)
    owned.say(run_parts.hook_notice(C))
    if not DISPOSABLE:
        why = run_parts.start_gate(C, envx, state)
        if why:
            sys.exit("FAIL " + why)
    # The sessions before the run: the clean-up deletes only new ones.
    listed = owned.sessions_on(C, envx)
    if not ok(listed is not None, "the list of the host's sessions before the run succeeded"):
        sys.exit(1)
    state["before"] = {s["id"] for s in listed}
    if os.environ.get("E2E_ONLY") in ("microvm", "microvm-measure"):
        joined_checks.microvm_checks(C, os.environ.get("E2E_ONLY") == "microvm-measure")
        owned.say("RESULT %d/%d passed" % (sum(results), len(results)))
        sys.exit(0 if results and all(results) else 1)
    # Setting up hosts and sessions (agent_setup.py). E2E_ONLY=agent-setup runs only these.
    import agent_setup
    def setup_checks():
        agent_setup.run(agent_setup.Ctx(cli=cli, HOST=HOST, NAME=NAME, HELPER=C.HELPER, env=env, envx=envx, ok=ok, Term=boundary.term,
                                        host_ssh=T.ssh, host_rc=T.rc, host_out=T.out, root=R, created=created, made=made, DISPOSABLE=DISPOSABLE))
    if os.environ.get("E2E_ONLY") == "agent-setup":
        setup_checks()
        owned.say("RESULT %d/%d passed" % (sum(results), len(results)))
        sys.exit(0 if all(results) else 1)
    # The guided setup changes the host's linger, restore unit and terminfo:
    # it runs only on a disposable host.
    def guided_checks():
        if DISPOSABLE:
            import guided_setup
            guided_setup.run(guided_setup.Ctx(cli=cli, HOST=HOST, NAME=NAME, env=env, envx=envx, root=R, ok=ok, Term=boundary.term))
        else:
            run_parts.skip("guided setup")
    if os.environ.get("E2E_ONLY") == "guided-setup":
        guided_checks()
        owned.say("RESULT %d/%d passed" % (sum(results), len(results)))
        sys.exit(0 if all(results) else 1)

    def sandbox_host_checks():
        joined_checks.sandbox_host_checks(C, os.environ.get("E2E_ONLY") == "sandbox-hosts")
    if os.environ.get("E2E_ONLY") == "sandbox-hosts":
        sandbox_host_checks()
        owned.say("RESULT %d/%d passed" % (sum(results), len(results)))
        sys.exit(0 if all(results) else 1)

    def socket_checks():
        joined_checks.socket_checks(C, os.environ.get("E2E_ONLY") == "session-sockets")
    if os.environ.get("E2E_ONLY") == "session-sockets":
        socket_checks()
        owned.say("RESULT %d/%d passed" % (sum(results), len(results)))
        sys.exit(0 if all(results) else 1)

    def host_git_checks():
        joined_checks.host_git_checks(C, os.environ.get("E2E_ONLY") == "host-git")
    if os.environ.get("E2E_ONLY") == "host-git":
        host_git_checks()
        owned.say("RESULT %d/%d passed" % (sum(results), len(results)))
        sys.exit(0 if all(results) else 1)

    def verbs_checks():
        joined_checks.verbs_checks(C, os.environ.get("E2E_ONLY") == "session-verbs")
    if os.environ.get("E2E_ONLY") == "session-verbs":
        verbs_checks()
        owned.say("RESULT %d/%d passed" % (sum(results), len(results)))
        sys.exit(0 if all(results) else 1)

    def host_checks():
        joined_checks.host_checks(C, os.environ.get("E2E_ONLY") == "host-commands")
    if os.environ.get("E2E_ONLY") == "host-commands":
        host_checks()
        owned.say("RESULT %d/%d passed" % (sum(results), len(results)))
        sys.exit(0 if all(results) else 1)

    # --- A script: new --detach --json, as the scripting guide shows it ---
    # stdout is one JSON object and nothing else. The check deletes the
    # session by its ID, through the helper's rm, as every check does.
    run_parts.json_check(C, envx, created)
    if DISPOSABLE:
        import disposable_checks as dc
        dc.kill(C)
    else:
        run_parts.skip("kill")
    # With the run's name, so that the clean-up finds a session that it
    # might start by the run's token.
    code, out, err = run_parts.run_or_timeout([cli, "new", HOST, "--name", owned.e2e_name("e2e-json-usage"), "--json", "--"]
                                              + owned.program("/bin/sh", "exec sleep 600"), envx, None)
    try:
        usage = json.loads(err).get("error", {}).get("kind")
    except (ValueError, AttributeError):
        usage = None
    ok(code == 2 and out == "" and usage == "usage", "new --json without --detach fails with exit code 2, as one JSON line on stderr")

    # --- Attach, detach, reattach ---
    # 1. Start a detached session. The run types nothing into it: its program
    #    prints a mark, ignores the detach key's SIGQUIT, and waits.
    sid = start("e2e-attach", "/bin/sh", "-c", 'echo "MARK-$((40+2))"; trap "" QUIT; while :; do sleep 1; done')
    # 2. Attach, and see the output.
    t = attach(sid)
    try:
        ok(t.wait_for(rb"MARK-42", 30), "attach shows the session's output")
        # 3. Detach with Ctrl+\ (legacy byte), wait for the double-tap window.
        t.buf = b""
        t.send(b"\x1c")
        code = t.exit_code(10)
        out = t.buf.decode(errors="replace")
    finally:
        t.close()
    ok(code == 0 and "Detached" in out, "Ctrl+\\ detaches and the client exits 0")
    ok("\x1bc" not in out, "no RIS (ESC c) is ever sent to the terminal")
    # 4. The session kept running: a new client gets its output again.
    t = attach(sid)
    try:
        ok(t.wait_for(rb"MARK-42", 30), "reattach from a new client replays earlier output")
        t.send(b"\x1c")
        ok(t.exit_code(10) == 0, "the reattached client detaches")
    finally:
        t.close()
    if DISPOSABLE:
        import disposable_checks as dc
        dc.double_detach(C, sid)
        dc.typing(C)
        dc.keys(C)
        dc.paste(C)
    else:
        run_parts.skip("double detach")
        run_parts.skip("typing")
        run_parts.skip("kitty keys")
        run_parts.skip("paste")

    # --- Reliability and fidelity ---
    # A. Reconnect after the connection drops: no gap and no duplicate.
    # The output starts after the client attaches, so every line arrives live.
    sid1 = start("e2e-counter", "/bin/bash", "-c", "sleep 3; for i in $(seq 1 400); do echo N$i; sleep 0.05; done; sleep 600")
    t = attach(sid1)
    try:
        ok(t.wait_for(rb"N20\r", 30), "counter running")
        # End the bridge of this client, which the run started, as a network drop
        # would, and the run's own shared connection: never one of the user's.
        pids = T.drop_link(t.pid)
        ok(pids, "the client's bridge to the test host was found and ended (PIDs %s)" % pids)
        t0 = time.time()
        ok(t.wait_for(rb"N400\r", 60), "client reconnected by itself and kept receiving (%.1fs)" % (time.time() - t0))
        nums = [int(x) for x in re.findall(rb"N(\d+)\r", t.buf)]
        c = collections.Counter(nums)
        dups = [n for n, k in c.items() if k > 1]
        missing = [n for n in range(1, 401) if n not in c]
        ok(not missing, "no gap after reconnect (missing: %s)" % missing[:10])
        ok(not dups, "no duplicate after reconnect (dups: %s)" % dups[:10])
        t.send(b"\x1c"); t.exit_code(10)
    finally:
        t.close()

    # C. Escape sequences pass byte for byte. The file is in the run's folder.
    #    OSC 777 raises a needs_input alert event on the host, so its check runs
    #    only on a disposable host.
    seq = (b"\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\ "
           b"\x1b[38;2;255;100;0mtruecolor\x1b[0m \x1b[4:3mcurly\x1b[0m "
           b"\x1b]52;c;aGVsbG8=\x1b\\\x1b[?2026h\x1b[?2026l END")
    C.host_ssh("cat > %s/seq.bin" % owned.run_dir(), seq)
    # Live output must be byte-identical (history on a new client is a snapshot, a redraw).
    sid3 = start("e2e-bytes", "/bin/bash", "-c", "sleep 3; cat %s/seq.bin; sleep 600" % owned.run_dir())
    t = attach(sid3)
    try:
        ok(t.wait_for(rb" END", 30), "escape test output arrived")
        ok(seq in t.buf, "OSC 8, truecolor, styled underline, OSC 52 and sync output are byte-identical")
        t.send(b"\x1c"); t.exit_code(10)
    finally:
        t.close()
    if DISPOSABLE:
        import disposable, disposable_checks as dc
        dc.desktop_notification(C)
        disposable.debug_check(C, envx, start, exact, seq, "%s/seq.bin" % owned.run_dir(), state)
    else:
        run_parts.skip("desktop notification")
        run_parts.skip("debug")

    # E. Killing the client leaves the session running.
    t = attach(sid)
    try:
        time.sleep(2)
        t.signal(signal.SIGKILL)
        time.sleep(1)
    finally:
        t.close()
    ok(session_rows().get(sid, {}).get("alive") is True, "session survives a killed client")

    # F. Memory cap: an oom and a memory event, which the product alerts on.
    if DISPOSABLE:
        import disposable_checks as dc
        dc.memory(C, sid)
        dc.resume(C)
    else:
        run_parts.skip("memory cap")
        run_parts.skip("resume")

    # G. Laptop sleep: freeze the client and cut its network path; on wake it must
    #    be back to live output within 5 s, with what it missed replayed.
    sid6 = start("e2e-wake", "/bin/bash", "-c", "while :; do date +T%s.%N; sleep 0.1; done")
    t = attach(sid6)
    try:
        t.wait_for(rb"T\d+", 20); time.sleep(1)
        t.signal(signal.SIGSTOP)
        pids = T.drop_link(t.pid)
        ok(pids, "sleep: the client's bridge to the test host was found and ended (PIDs %s)" % pids)
        time.sleep(15)
        t.buf = b""
        woke = time.time()
        t.signal(signal.SIGCONT)
        live = False
        while time.time() - woke < 30:
            t.read(0.1)
            stamps = [float(x) for x in re.findall(rb"T(\d+\.\d+)", t.buf)]
            if stamps and max(stamps) > woke - 1:
                live = True
                break
        took = time.time() - woke
        ok(live and took < 5, "after a simulated sleep, live output resumed %.1f s after wake (%d missed lines replayed)" % (took, len(re.findall(rb"T(\d+\.\d+)", t.buf))))
        t.send(b"\x1c"); t.exit_code(10)
    finally:
        t.close()

    # H. A new client after the ring wrapped gets a snapshot from the shadow
    #    terminal: the right screen and the modes set long ago (kitty flags).
    sid7 = start("e2e-snapshot", "/bin/bash", "-c",
        'printf "\\033[>1u\\033[?2004h"; for i in $(seq 1 120000); do printf "LINE-%06d padding padding padding padding padding\\n" $i; done; printf "FINAL-SCREEN-MARKER\\n"; sleep 600')
    time.sleep(12)
    t = attach(sid7, cols=100, rows=30)
    try:
        ok(t.wait_for(rb"FINAL-SCREEN-MARKER", 30), "snapshot: a new client sees the final screen after the ring wrapped")
        ok(b"\x1b[>1u" in t.buf and b"\x1b[?2004h" in t.buf, "snapshot: kitty flags and bracketed paste set before the ring's start are restored")
        ok(b"LINE-000001 " not in t.buf and len(t.buf) < 2 << 20, "snapshot: bounded (%d bytes, not the 8 MiB ring)" % len(t.buf))
        t.send(b"\x1c"); t.exit_code(10)
    finally:
        t.close()
    if DISPOSABLE:
        import disposable_checks as dc
        dc.shadow_terminal(C)
    else:
        run_parts.skip("shadow terminal")

    # K. The session owns the whole terminal: its PTY is the terminal's size and
    #    follows a resize, and the client draws nothing of its own while attached.
    #    The session prints its size at its start and at each resize: the run
    #    types nothing.
    sid10 = start("e2e-size", "/bin/bash", "-c", 'sleep 2; stty size; trap "stty size" WINCH; while :; do sleep 1 & wait $!; done')
    t = attach(sid10, cols=101, rows=37)
    try:
        ok(t.wait_for(rb"37 101\r", 15), "attached: the agent's PTY is the terminal's size (37 rows, 101 columns)")
        t.resize(120, 40)
        ok(t.wait_for(rb"40 120\r", 15), "attached: a resize reaches the agent's PTY (40 rows, 120 columns)")
        t.send(b"\x1c"); t.exit_code(10)
    finally:
        t.close()

    # The session fills its screen, every row and column, from its own PTY size,
    # then sleeps. Everything the client writes must be that and nothing else.
    ROWS, COLS, ABC = 30, 100, b"ABCDEFGHIJKLMNOPQRSTUVWXYZ"
    fill = ("sleep 3; set -- $(stty size); L=ABCDEFGHIJKLMNOPQRSTUVWXYZ; "
            "for r in $(seq 1 $1); do printf '\\033[%d;1H' $r; printf \"%${2}s\" '' | tr ' ' \"${L:$(( (r-1) % 26 )):1}\"; done; "
            "printf '\\033[1;1H'; sleep 600")
    want = b"".join(b"\x1b[%d;1H" % r + bytes([ABC[(r - 1) % 26]]) * COLS for r in range(1, ROWS + 1)) + b"\x1b[1;1H"
    ESCAPES = re.compile(rb"\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1bP[^\x1b]*\x1b\\|\x1b[()][0-9A-Za-z]|\x1b[@-Z\\-_78=>]")
    NOTIFY = re.compile(rb"\x1b\]777;[^\x07\x1b]*(?:\x07|\x1b\\)")  # system notifications draw nothing
    sid11 = start("e2e-fill", "/bin/bash", "-c", fill)
    t = attach(sid11, cols=COLS, rows=ROWS)
    try:
        ok(t.wait_for(re.escape(want[-(COLS + 16):]), 30), "attached: the session filled its %dx%d screen" % (COLS, ROWS))
        t.read(1)
        mark = len(t.buf)
        t.read(6)  # the session is asleep: anything now would be the client's own
        at = t.buf.find(want[:6 + COLS])
        pre = ESCAPES.sub(b"", t.buf[:max(at, 0)])
        ok(at >= 0 and not pre.strip(), "attached: before the session's bytes the client draws no text of its own (%r)" % pre.strip()[:40])
        ok(NOTIFY.sub(b"", t.buf[at:]) == want, "attached: the screen is the session's bytes alone, every row and column, byte for byte")
        ok(not NOTIFY.sub(b"", t.buf[mark:]), "attached: the client writes nothing while the session is idle (6 s)")
        ok(b"\x1b]2;" not in t.buf and b"\x1b]0;" not in t.buf, "attached: the client sets no window title of its own")
        t.send(b"\x1c"); t.exit_code(10)
    finally:
        t.close()

    # L. Alerts (decision 0019), read only.
    info = owned.json_of([cli, "host", "alerts", HOST, "--json"], envx)
    ok(run_parts.alerts_report(info), "alerts: host alerts --json reports the alerts file, at a plain path, and the last deliveries")
    if DISPOSABLE:
        import disposable_checks as dc
        dc.alerts(C)
    else:
        run_parts.skip("alerts")

    setup_checks()
    guided_checks()
    sandbox_host_checks()
    socket_checks()
    host_git_checks()
    verbs_checks()
    host_checks()
    # M. Full screen (roadmap N2): a program on the alternate screen is reported
    #    as full screen, and host doctor says so.
    sid_fs = start("e2e-fullscreen", "/bin/bash", "-c", 'printf "\\033[?1049hFULL"; sleep 600')
    time.sleep(4)
    ok(session_rows().get(sid_fs, {}).get("full_screen") is True, "full screen: a program on the alternate screen is reported as full screen")
    _, doc, _ = run_parts.run_or_timeout([cli, "host", "doctor", HOST], envx, None)
    ok(names.get(sid_fs, "?") in doc and "full screen" in doc, "full screen: host doctor names the session")

    # N. pull (roadmap N7): a session's branch comes to a local repository over
    #    the client's SSH, and the local working tree is untouched. The
    #    repository is in the run's folder; git on the host only reads it.
    if T.machine().get("provider") == "docker":
        local = tempfile.mkdtemp(dir=R.path)
        run_parts.run_or_timeout(["git"] + allowed.GIT_FLAGS + ["init", "--template=", "-q", local], envx, None)
        if exact(sid, "pull"):
            code, out, err = run_parts.run_or_timeout([cli, "pull", "--id", "--", HOST + "/" + sid], envx, local)
            ok(code not in (0, None) and "needs SSH" in err, "pull: says it needs SSH on a host without it")
    elif C.host_rc("command -v git >/dev/null") != 0:
        owned.say("SKIP pull: the host has no git")
    else:
        run_parts.pull_check(C, envx, created)

    owned.say("RESULT %d/%d passed" % (sum(results), len(results)))
    sys.exit(0 if all(results) else 1)
finally:
    # Also after an exception, an early exit, SIGTERM or SIGHUP: the run's
    # sessions by ID, its folder, then the lock, when it holds the run's
    # token. A signal that came before finish blocked the signals ends it
    # before its first step: then it runs once more. A tuple avoids any CALL
    # bytecode before the try block's exception table.
    clean = False
    for _ in (1, 2, 3, 4):
        try:
            run_parts.ignore_signals()
            clean = run_parts.finish(C, created, state)
            if "finished" in state:
                break
        except BaseException:  # noqa: BLE001 - the clean-up must run
            pass
    if state.get("keep_client_root"):
        R.close_controls(envx)
        owned.say("CLEAN-UP FAILED: the isolated client root stays for inspection: %s" % R.path)
    else:
        R.remove()
    joined_checks.finish_conformance(C, clean, created, state)
    if not clean:
        # A run whose clean-up left something on the host fails, also when
        # every check passed. An exception that ended the run is shown first.
        exc = sys.exc_info()[1]
        if isinstance(exc, SystemExit):
            if isinstance(exc.code, str):
                owned.say(exc.code)  # as the refusal of a held lock
        elif exc is not None:
            owned.say("".join(traceback.format_exception(type(exc), exc, exc.__traceback__)).rstrip("\n"))
        owned.say("FAIL the run's clean-up left something on %s: see CLEAN-UP FAILED above" % HOST)
        sys.exit(1)
