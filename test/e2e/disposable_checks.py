"""The checks of run.py that run only on a disposable host (run.py's
DISPOSABLE): they type into a session, upload, resume, kill a process on
the host, delete with the client's kill, set the host's alerts, send the
detach key twice, and make a process go over a memory cap. On a host that
the run did not make, run.py prints their SKIP lines instead, with the Go
tests that cover them (run_parts.SKIPPED). R is run.py's namespace: its
client, host, helpers and lists.
"""
import base64, hashlib, json, os, re, shlex, time

import disposable, owned

# The shell that the typing checks type into: env -i, so that no rc file, no
# ~/.inputrc, no PROMPT_COMMAND and no exported function of the host user's
# reaches it, and the result does not hang on the container's files.
TYPING_SHELL = ("/bin/sh", "-c", 'exec env -i HOME="$HOME" PATH="$PATH" TERM="$TERM" LANG=C.UTF-8 INPUTRC=/dev/null HISTFILE= '
                "/bin/bash --norc --noprofile -i")


def double_detach(R, sid):
    """Double Ctrl+\\ sends the key to the session sid instead of
    detaching: the program, which traps QUIT, gets it. Then a single one
    detaches."""
    t = R.attach(sid)
    try:
        t.wait_for(rb"MARK-42", 30)
        t.send(b"\x1c\x1c")
        time.sleep(0.8)
        t.read(0.5)
        R.ok(t.exit_code(0.5) is None, "double Ctrl+\\ does not detach")
        time.sleep(0.5)
        t.send(b"\x1c")
        R.ok(t.exit_code(10) == 0, "single Ctrl+\\ then detaches")
    finally:
        t.close()


def desktop_notification(R):
    """OSC 777 desktop notification bytes pass byte for byte. On a host that
    the run did not make, OSC 777 raises a needs_input alert event
    (internal/host/scan.go), so this runs only on a disposable host."""
    seq = b"\x1b]777;notify;Title;Body\x1b\\ END"
    R.host_ssh("cat > %s/notify.bin" % owned.run_dir(), seq)
    sid = R.start("e2e-notify", "/bin/bash", "-c", "sleep 3; cat %s/notify.bin; sleep 600" % owned.run_dir())
    t = R.attach(sid)
    try:
        R.ok(t.wait_for(rb" END", 30), "desktop notification output arrived")
        R.ok(seq in t.buf, "OSC 777 desktop notification is byte-identical")
        t.send(b"\x1c")
        t.exit_code(10)
    finally:
        t.close()


def has_memory_caps(R):
    """Whether host doctor reports an active systemd user manager with memory caps."""
    doctor = owned.json_of([R.cli, "host", "doctor", R.HOST, "--json"], R.envx)
    checks = doctor.get("checks") if isinstance(doctor, dict) else None
    passed = {c.get("name") for c in checks or [] if isinstance(c, dict) and c.get("ok") is True}
    return {"systemd user manager", "memory caps"} <= passed


def memory(R, sid):
    """Memory cap: a hog that the agent started is OOM-killed and the session
    marked, while the agent runs on. Another session, sid, is unaffected.
    Then a hog that is the agent itself ends its session out of memory.
    These raise oom and memory events, which alert. Where host doctor reports
    no systemd user manager with memory caps, the hog would only fill the
    host's memory, so the check is skipped."""
    if not has_memory_caps(R):
        owned.say("SKIP memory cap: host doctor reports no systemd user manager with memory caps")
        return
    # A cap of its own, the smallest there is, makes the check quick, and
    # keeps the hog far below the host's memory, whatever its size.
    sid5 = R.start("e2e-oom", "/bin/bash", "-c", "head -c 1G /dev/zero | tail > /dev/null; sleep 600", flags=("--memory-max", "256M"))
    deadline = time.time() + 40
    while True:
        rows = R.session_rows()
        r5 = rows.get(sid5, {})
        if r5.get("oom_killed") or time.time() > deadline:
            break
        time.sleep(2)
    R.ok(rows.get(sid, {}).get("alive"), "other sessions keep running")
    owned.say("oom session: %s" % owned.safe_text({k: r5.get(k) for k in ("state", "reason", "memory_current", "memory_max")}))
    R.ok(r5.get("memory_max") == 256 << 20, "the session got the cap it asked for (%s)" % owned.safe_text(r5.get("memory_max")))
    R.ok(r5.get("oom_killed") is True, "the over-the-cap process was stopped and the session marked (reason: %s)" % owned.safe_text(r5.get("reason")))
    R.ok(r5.get("state") in ("working", "idle"), "the agent runs on after its child's kill, and does not need input (state: %s)" % owned.safe_text(r5.get("state")))
    # All sessions together have a limit too: the host's memory minus a reserve.
    limit = R.host_out("systemctl --user show %s.slice -p MemoryMax --value" % R.NAME).strip()
    R.ok(limit.isdigit() and int(limit) > 256 << 20, "the sessions' slice has a memory limit (%s)" % owned.safe_text(limit))
    # tail keeps its last line, and /dev/zero has no newline: it grows until the cap stops it.
    # Without a cap it would fill the host's memory, so it runs only under one.
    if not r5.get("memory_max"):
        owned.say("SKIP memory cap on the agent itself: the session has no memory cap")
        return
    sid_oom = R.start("e2e-oom-agent", "/bin/sh", "-c", "exec tail /dev/zero", flags=("--memory-max", "256M"))
    r_oom, end = {}, time.time() + 60
    while time.time() < end and r_oom.get("state") not in ("exited", "errored"):
        time.sleep(2)
        r_oom = R.session_rows().get(sid_oom, {})
    owned.say("oom agent session: %s" % owned.safe_text({k: r_oom.get(k) for k in ("state", "reason", "signal", "oom_killed")}))
    R.ok(r_oom.get("oom_killed") is True and r_oom.get("state") == "errored" and r_oom.get("reason") == "oom",
         "the cap stopped the agent itself, and its session ended out of memory (state: %s, reason: %s)" % (owned.safe_text(r_oom.get("state")), owned.safe_text(r_oom.get("reason"))))


def typing(R):
    """Attach, type, see the output; detach, reattach and type again; and a
    double detach key reaches the program instead of detaching."""
    sid = R.start("e2e-type", *TYPING_SHELL)
    t = R.attach(sid)
    try:
        # The prompt ends in "$": with a space after it when it arrives live, and
        # with the cursor move after it in a screen snapshot.
        if not R.ok(t.wait_for(rb"\$( |\x1b)", 30), "typing: attach shows the shell prompt"):
            owned.say("  the screen ended with: %s" % owned.safe_text(t.buf[-300:]))
        t.send(b"echo MARK-$((40+2))\r")
        R.ok(t.wait_for(rb"MARK-42", 10), "typing: a typed command runs on the host and its output comes back")
        t.buf = b""
        t.send(b"\x1c")
        R.ok(t.exit_code(10) == 0 and b"Detached" in t.buf, "typing: Ctrl+\\ detaches and the client exits 0")
    finally:
        t.close()
    t = R.attach(sid)
    try:
        R.ok(t.wait_for(rb"MARK-42", 30), "typing: reattach from a new client replays earlier output")
        t.send(b"for i in 1 2 3; do echo LINE-$i; done\r")
        R.ok(t.wait_for(rb"LINE-3", 10), "typing: the session is still interactive after reattach")
        t.send(b"cat -v\r")
        time.sleep(0.5)
        t.buf = b""
        t.send(b"\x1c\x1c")
        time.sleep(0.8)
        t.read(0.5)
        R.ok(t.exit_code(0.5) is None, "typing: double Ctrl+\\ does not detach")
        t.send(b"\x04")  # end cat
        time.sleep(0.5)
        t.send(b"\x1c")
        R.ok(t.exit_code(10) == 0, "typing: a single Ctrl+\\ then detaches")
    finally:
        t.close()


def keys(R):
    """Kitty keyboard sequences reach the host unchanged."""
    sid = R.start("e2e-keys", "/bin/sh", "-c", "sleep 3; exec " + R.HELPER + " keytest")
    t = R.attach(sid)
    try:
        R.ok(t.wait_for(rb"Press keys", 30), "keys: the host key tester runs in a session")
        R.ok(b"\x1b[>31u" in t.buf, "keys: the kitty keyboard push reached the local terminal")
        sent = [b"\x1b[13;2u", b"\x1b[112;3u", b"\x1b[98;5u", b"\x1b[27u", b"\x1b[99;5:3u"]
        for k in sent:
            t.send(k)
            time.sleep(0.3)
        t.read(1)
        for k in sent:
            R.ok(repr(k.decode())[1:-1].encode() in t.buf.replace(b"\\x1b", b"\\x1b") or k.decode().encode("unicode_escape") in t.buf,
                 "keys: %r arrived unchanged" % k)
        t.send(b"q")
        time.sleep(0.2)
        t.send(b"q")
        time.sleep(1)
        t.read(1)
    finally:
        t.close()


def paste(R):
    """A dropped local file is uploaded and its remote path pasted; Ctrl+V
    with an image on the clipboard uploads it and pastes its path. The test
    hook stands in for the clipboard, so the user's own is never read."""
    local = os.path.join(R.root.path, ".drop-test.txt")
    img = os.path.join(R.root.path, ".clip-test.png")
    try:
        with open(local, "w") as f:
            f.write("dropped file content\n")
        sid = R.start("e2e-drop", *TYPING_SHELL)
        t = R.attach(sid)
        try:
            time.sleep(2)
            t.read(1)
            t.send(b"bind 'set enable-bracketed-paste on' 2>/dev/null; printf '\\033[?2004h'; read -r P; cat $P\r")
            time.sleep(1)
            t.send(b"\x1b[200~" + local.encode() + b"\x1b[201~\r")
            R.ok(t.wait_for(rb"/paste/[0-9a-f]{12}-drop-test.txt", 20), "paste: a dropped file is uploaded and its remote path pasted")
            t.send(b"\x1c")
            t.exit_code(10)
        finally:
            t.close()

        png = base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==")
        with open(img, "wb") as f:
            f.write(png)
        sid = R.start("e2e-clip", *TYPING_SHELL)
        t = R.attach(sid, environ=dict(R.env, **{R.NAME.upper() + "_TEST_CLIPBOARD_IMAGE": img}))
        try:
            time.sleep(2)
            t.read(1)
            t.send(b"cat -v\r")
            time.sleep(0.5)
            t.buf = b""
            t.send(b"\x16")  # Ctrl+V
            R.ok(t.wait_for(rb"/paste/[0-9a-f]{12}-clipboard\.png", 20), "paste: Ctrl+V uploaded the image and pasted its path on the host")
            m = re.search(rb"(/[^ \r\n]*/paste/[0-9a-f]{12}-clipboard\.png)", t.buf)
            if m:
                out = R.host_out("sha256sum -- " + shlex.quote(m.group(1).decode()))
                R.ok(hashlib.sha256(png).hexdigest() in out, "paste: the file on the host is the same image")
            t.send(b"\x04")
            time.sleep(0.3)
            t.send(b"\x1c")
            t.exit_code(10)
        finally:
            t.close()
    finally:
        for f in (local, img):
            if os.path.exists(f):
                os.remove(f)


def resume(R):
    """Resumes an exited session in place on every disposable host (and checks
    that resuming a running session is refused). When host doctor also reports
    systemd memory caps, checks that an OOM-stopped session resumes with a
    larger cap in a new unit and stops any process the old agent left."""
    exit_mark = owned.run_dir("$HOME") + "/resume-exit-mark"
    sid_ex = R.start("e2e-resume-exit", "/bin/sh", "-c",
                     'if [ -e "%s" ]; then exec sleep 600; fi; touch "%s"; exit 0' % (exit_mark, exit_mark))
    row, end = {}, time.time() + 30
    while time.time() < end and row.get("state") not in ("exited", "errored"):
        time.sleep(0.5)
        row = R.session_rows().get(sid_ex, {})
    R.ok(row.get("state") == "exited", "resume: the session exited cleanly before resume (state: %s)" % owned.safe_text(row.get("state")))
    if R.exact(sid_ex, "resume"):
        t = R.Term([R.cli, "resume", R.HOST + "/" + sid_ex, "--detach"], env=R.env)
        try:
            code = t.exit_code(60)
        finally:
            t.close()
        R.ok(code == 0 and b"Resumed" in t.buf, "resume: an exited session starts again (exit %s)" % code)
        row = R.session_rows().get(sid_ex, {})
        R.ok(row.get("alive") and row.get("state") not in ("exited", "errored", "lost"),
             "resume: the exited session runs again in place (state: %s)" % owned.safe_text(row.get("state")))
    if R.exact(sid_ex, "resume"):
        t = R.Term([R.cli, "resume", R.HOST + "/" + sid_ex, "--detach"], env=R.env)
        try:
            code = t.exit_code(60)
        finally:
            t.close()
        R.ok(code not in (0, None) and b"still runs" in t.buf, "resume: a resume of an agent that runs is refused (exit %s)" % code)
    if not has_memory_caps(R):
        owned.say("SKIP resume memory cap: host doctor reports no systemd user manager with memory caps")
        return
    mark = owned.run_dir("$HOME") + "/resume-mark"
    sid = R.start("e2e-resume", "/bin/sh", "-c",
                  'if [ -e "%s" ]; then exec sleep 600; fi; touch "%s"; setsid sleep 913 & exec tail /dev/zero' % (mark, mark),
                  flags=("--memory-max", "256M"))
    row, end = {}, time.time() + 60
    while time.time() < end and row.get("state") not in ("exited", "errored"):
        time.sleep(2)
        row = R.session_rows().get(sid, {})
    R.ok(row.get("oom_killed") is True, "resume: the cap stopped the agent (state: %s)" % owned.safe_text(row.get("state")))
    R.ok(R.host_rc("pgrep -u \"$(id -u)\" -f 'sleep 91[3]'") == 0, "resume: the stopped agent left a process in its session")
    if not R.exact(sid, "resume"):
        return
    t = R.Term([R.cli, "resume", R.HOST + "/" + sid, "--memory-max", "300M", "--detach"], env=R.env)
    try:
        code = t.exit_code(90)
    finally:
        t.close()
    R.ok(code == 0 and b"Resumed" in t.buf, "resume: it starts the agent again with a bigger cap (exit %s)" % code)
    row = R.session_rows().get(sid, {})
    R.ok(row.get("alive") and row.get("state") not in ("exited", "errored", "lost") and not row.get("oom_killed"),
         "resume: the agent runs, in the same session (state: %s)" % owned.safe_text(row.get("state")))
    R.ok(row.get("memory_max") == 300 << 20, "resume: the session got the bigger cap (%s)" % owned.safe_text(row.get("memory_max")))
    unit = row.get("unit") or ""
    unit_max = R.host_out("systemctl --user show %s -p MemoryMax --value" % shlex.quote(unit)).strip() if unit else ""
    R.ok(unit_max == str(300 << 20), "resume: the new unit has the bigger cap (MemoryMax=%s)" % owned.safe_text(unit_max))
    R.ok(R.host_rc("pgrep -u \"$(id -u)\" -f 'sleep 91[3]'") == 1, "resume: it stopped what the old agent left in its session")


# Kills the shadow terminal of the session sid and nothing else: the holder
# runs as `<helper> holder <id>`, and the shadow terminal is its child.
# Through /proc, not pkill: a slim container image has no procps.
VT_KILL = r"""sid=%s vt=%s holder=
for p in /proc/[0-9]*; do
  [ "$(tr '\0' '\n' 2>/dev/null < "$p/cmdline" | tail -n 2 | tr '\n' ' ')" = "holder $sid " ] && holder=${p#/proc/}
done
if [ -z "$holder" ]; then echo "found no holder of session $sid"; else
  for p in /proc/[0-9]*; do
    [ "$(cat "$p/comm" 2>/dev/null)" = "$vt" ] || continue
    [ "$(sed -n 's/^PPid:[[:space:]]*//p' "$p/status" 2>/dev/null)" = "$holder" ] || continue
    kill -9 "${p#/proc/}" && echo "killed $vt ${p#/proc/}, the child of holder $holder"
  done
fi
sleep 3"""


def shadow_terminal(R):
    """Killing the shadow terminal does not stop the agent; it restarts,
    and a new client still gets a snapshot."""
    sid = R.start("e2e-vtcrash", "/bin/bash", "-c", 'printf "\\033[>1u"; i=0; while :; do i=$((i+1)); printf "TICK-%d\\n" $i; sleep 0.2; done')
    time.sleep(3)
    if not owned.ID.fullmatch(sid or ""):
        R.ok(False, "shadow terminal: a session of its own to kill the shadow terminal of")
        return
    out = R.host_out(VT_KILL % (sid, (R.NAME + "-vt")[:15])).strip()
    owned.say("  " + owned.safe_text(out).replace("\n", "\n  "))
    R.ok("killed " in out, "shadow terminal: the session's own was found and killed")
    t = R.attach(sid)
    try:
        t.read(4)
        ticks = [int(x) for x in re.findall(rb"TICK-(\d+)", t.buf)]
        R.ok(ticks and max(ticks) > 20, "shadow terminal: the agent kept running (tick %s)" % (max(ticks) if ticks else None))
        R.ok(b"\x1b[>1u" in t.buf, "shadow terminal: it restarted, and a new client still got a snapshot")
        t.send(b"\x1c")
        t.exit_code(10)
    finally:
        t.close()


def kill(R):
    """A script deletes a session with the client's kill, by its address,
    and the confirmation goes to stderr. Only after exact(): the address
    means this session."""
    name = owned.e2e_name("e2e-kill")
    sid, out, err = owned.start_new(R, R.envx, R.created, name, "--", *owned.program("/bin/sh", "exec sleep 600"))
    if not R.ok(sid, "kill: new starts a session of its own"):
        return
    R.names[sid] = name
    if not R.exact(sid, "kill"):
        return
    code, out, err = owned.run_cmd([R.cli, "kill", R.HOST + "/" + sid], env=R.envx, timeout=60)
    R.ok(code == 0 and out == "" and ("Deleted " + name) in err, "kill: the script deletes the session, and the confirmation goes to stderr")
    if code == 0 and sid in R.created:
        R.created.remove(sid)


def alerts(R):
    """A command destination on the host gets the session's events as JSON
    on stdin. The check runs only when the helper's alerts file is not
    there. The file that it makes names the run's folder, so it holds the
    run's token; disposable.cleanup removes it while it does."""
    path = disposable.alerts_ready(R, R.envx, R.state)
    if not path:
        return
    out_file = owned.run_dir("$HOME") + "/alerts.jsonl"

    def events(kind, sid=None, timeout=20):
        end = time.time() + timeout
        while time.time() < end:
            text = R.host_out("cat \"%s\" 2>/dev/null; true" % out_file)
            evs = [json.loads(line) for line in text.splitlines() if line.startswith("{")]
            hits = [e for e in evs if e.get("event") == kind and (sid is None or e.get("session", {}).get("id") == sid)]
            if hits:
                return hits
            time.sleep(1)
        return []

    code, out, err = owned.run_cmd([R.cli, "host", "alerts", "set", R.HOST, "--command", "cat >> \"%s\"" % out_file,
                                    "--events", "needs_input,failed", "--delay", "2s"], env=R.envx, timeout=120)
    R.ok(code == 0 and "are on" in out, "alerts: set writes a command destination")
    R.ok(R.host_rc("grep -qF -- %s %s" % (disposable.alerts_tag(), path)) == 0, "alerts: the alerts file that the check made holds the run's token")
    mode = R.host_out("stat -c %a " + path).strip()
    R.ok(mode == "600", "alerts: the alerts file is mode 0600 (%s)" % owned.safe_text(mode))
    code, out, err = owned.run_cmd([R.cli, "host", "alerts", "test", R.HOST], env=R.envx, timeout=120)
    R.ok(code == 0 and "✓ command" in err, "alerts: test reports ok for the command, on stderr")
    R.ok(events("test", timeout=5), "alerts: the test event reached the command")
    t0 = time.time()
    ask_name = owned.e2e_name("e2e-alert-ask")
    sid, _, _ = owned.start_new(
        R, R.envx, R.created, ask_name, "--",
        *owned.program("/bin/bash", 'sleep 2; echo \'{"message":"e2e question"}\' | %s hook notification; sleep 600' % R.HELPER,
                       keep=(R.NAME.upper() + "_SOCK", R.NAME.upper() + "_SESSION")))
    if R.ok(sid, "alerts: e2e-alert-ask starts a session of its own"):
        R.names[sid] = ask_name
    hits = events("needs_input", sid, timeout=2 + 2 + 10 + 5)
    R.ok(hits and hits[0].get("v") == 1 and hits[0]["session"]["name"] == R.names.get(sid) and "detail" not in hits[0],
         "alerts: needs_input from a hook arrived %.1f s after start, without the detail" % (time.time() - t0))
    sid = R.start("e2e-alert-fail", "/bin/sh", "-c", "sleep 2; exit 3")
    hits = events("failed", sid, timeout=15)
    R.ok(hits and hits[0].get("exit_code") == 3 and "failed (exit 3)" in hits[0].get("summary", ""), "alerts: a session that exits 3 sends failed with exit_code 3")
    code, out, err = owned.run_cmd([R.cli, "host", "alerts", "log", R.HOST, "-n", "10"], env=R.envx, timeout=120)
    R.ok(code == 0 and "needs_input" in out and "failed" in out, "alerts: the log shows the deliveries")
    code, out, err = owned.run_cmd([R.cli, "host", "alerts", "off", R.HOST], env=R.envx, timeout=120)
    R.ok(code == 0 and "off" in err, "alerts: off removes the file, and says so on stderr")
    code, out, err = owned.run_cmd([R.cli, "host", "alerts", R.HOST], env=R.envx, timeout=120)
    R.ok(code == 0 and "are off" in out, "alerts: the host says alerts are off")
    R.ok(disposable.remove_alerts_file(R, R.state), "alerts: no alerts file of the run's is left on the host")
