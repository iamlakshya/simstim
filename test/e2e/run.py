"""End-to-end tests against a real host. Run with `just e2e`.

Needs: a built client, and a host already added with `host add`.
  E2E_HOST        the host's name in the client (required)
The product name is read from internal/brand, so nothing here spells it.
"""
import collections, json, os, re, subprocess, sys, time

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
sys.path.insert(0, HERE)
from harness import Term

NAME = re.search(r'^const Name = "(.*)"', open(os.path.join(ROOT, "internal/brand/brand.go")).read(), re.M).group(1)
cli = os.path.abspath(sys.argv[1] if len(sys.argv) > 1 else os.path.join(ROOT, "bin", NAME))
HOST = os.environ.get("E2E_HOST")
if not HOST:
    sys.exit("set E2E_HOST to the name of a host added with `host add`")
WRAPPER = os.path.join(os.environ.get("XDG_CONFIG_HOME", os.path.expanduser("~/.config")), NAME, "ssh", "config-" + HOST)
HELPER = "~/.local/share/%s/bin/current/%s-host" % (NAME, NAME)
env = {"TERM": "xterm-ghostty", "TERM_PROGRAM": "ghostty", "COLORTERM": "truecolor"}
results = []

def ok(c, m):
    results.append(bool(c)); print(("PASS " if c else "FAIL ") + m); return c

def host_entry():
    import tomllib
    hosts = tomllib.load(open(os.path.join(os.path.dirname(os.path.dirname(WRAPPER)), "hosts.toml"), "rb"))["host"]
    return [h for h in hosts if h["name"] == HOST][0]

def host_machine():
    """The [host.machine] table of a host made by `host create`, or {}."""
    return host_entry().get("machine") or {}

def host_argv(cmd):
    """The command that runs cmd on the host, the way the client reaches it."""
    h = host_entry()
    machine = h.get("machine") or {}
    if machine.get("provider") == "docker":  # made by `host create docker`: no SSH
        return ["docker", "exec", "-i", "-u", machine.get("user", "root"), machine["id"], "sh", "-c", cmd]
    return ["ssh", "-F", WRAPPER, "-o", "BatchMode=yes", h["ssh"], cmd]

def host_ssh(cmd, data=None):
    """Runs cmd on the host, and fails the suite if it fails."""
    return subprocess.run(host_argv(cmd), input=data, capture_output=True, check=True)

def host_rc(cmd, data=None):
    """Runs cmd on the host and returns its exit code; data goes to its stdin."""
    return subprocess.run(host_argv(cmd), input=data, capture_output=True).returncode

def start(name, *argv, flags=()):
    t = Term([cli, "new", HOST, "--name", name, "--detach", *flags, "--"] + list(argv), env=env)
    t.exit_code(60); m = re.search(rb"Started [^/]+/(\w+)", t.buf)
    if not m: print(t.buf.decode(errors="replace"))
    return m.group(1).decode() if m else None

# An interactive bash for the tests to type into. Its HISTFILE is empty, so
# it adds nothing to the host user's history: bash saves history only to a
# named HISTFILE. `bash +o history -i` would not do, as bash before 5.1
# turns history back on for an interactive shell.
BASH_I = ("/usr/bin/env", "HISTFILE=", "/bin/bash", "--norc", "-i")

created = []

# Setting up hosts and sessions (agent_setup.py). E2E_ONLY=agent-setup runs only these.
import agent_setup
def setup_checks():
    agent_setup.run(agent_setup.Ctx(cli=cli, HOST=HOST, NAME=NAME, env=env, ok=ok, Term=Term, host_ssh=host_ssh, host_rc=host_rc))
if os.environ.get("E2E_ONLY") == "agent-setup":
    setup_checks()
    print("RESULT %d/%d passed" % (sum(results), len(results)))
    sys.exit(0 if all(results) else 1)

# The guided setup (guided_setup.py). E2E_ONLY=guided-setup runs only these.
import guided_setup
def guided_checks():
    guided_setup.run(guided_setup.Ctx(cli=cli, HOST=HOST, NAME=NAME, env=env, ok=ok, Term=Term))
if os.environ.get("E2E_ONLY") == "guided-setup":
    guided_checks()
    print("RESULT %d/%d passed" % (sum(results), len(results)))
    sys.exit(0 if all(results) else 1)

# --- Attach, detach, reattach ---
# 1. Start a detached shell session.
t = Term([cli, "new", HOST, "--agent", "shell", "--name", "e2e-attach", "--detach"], env=env)
code = t.exit_code(60); out = t.buf.decode(errors="replace")
m = re.search(r"Started [^/]+/(\w+)", out)
ok(code == 0 and m, "new --detach: " + out.strip().splitlines()[-1])
sid = m.group(1); created.append(sid)
# 2. Attach, type, see the output.
t = Term([cli, "attach", HOST + "/" + sid], env=env)
# The prompt ends in "$" whatever the PS1 (user@host:~$ on most hosts): with
# a space after it when it arrives live, and with the cursor move after it
# when it arrives in the screen snapshot, which drops trailing spaces.
if not ok(t.wait_for(rb"\$( |\x1b)", 30), "attach shows the shell prompt"):
    print("  the screen ended with: %r" % t.buf[-300:])
# This is the host user's own login shell, so the first line turns its
# history off. bash and zsh save history at exit only to $HISTFILE. bash
# also stops and clears its list, as with no HISTFILE a PROMPT_COMMAND that
# runs `history -a` writes to ~/.history. Other shells fail those quietly.
# A line that starts with a space stays out of history where the rc says so.
# MARK has its own line, as dash drops the rest of a line after a set error.
t.send(b" unset HISTFILE; { set +o history; history -c; } 2>/dev/null\r")
t.send(b"echo MARK-$((40+2))\r")
ok(t.wait_for(rb"MARK-42", 10), "typed command runs on the host and its output comes back")
# 3. Detach with Ctrl+\ (legacy byte), wait for the double-tap window.
t.buf = b""
t.send(b"\x1c")
code = t.exit_code(10)
out = t.buf.decode(errors="replace")
ok(code == 0 and "Detached" in out, "Ctrl+\\ detaches and the client exits 0")
ok("\x1bc" not in out, "no RIS (ESC c) is ever sent to the terminal")
# 4. The session kept running: print in the background, then reattach (new client, full replay).
t = Term([cli, "attach", HOST + "/" + sid], env=env)
ok(t.wait_for(rb"MARK-42", 30), "reattach from a new client replays earlier output")
t.send(b"for i in 1 2 3; do echo LINE-$i; done\r")
ok(t.wait_for(rb"LINE-3", 10), "session still interactive after reattach")
# 5. Double Ctrl+\ sends the key to the session instead of detaching.
t.send(b"cat -v\r"); time.sleep(0.5)
t.buf = b""
t.send(b"\x1c\x1c"); time.sleep(0.8)
t.read(0.5)
ok(t.exit_code(0.5) is None, "double Ctrl+\\ does not detach")
t.send(b"\x04")  # end cat
time.sleep(0.5)
t.send(b"\x1c"); code = t.exit_code(10)
ok(code == 0, "single Ctrl+\\ then detaches")

# --- Reliability and fidelity ---
# A. Reconnect after the connection drops: no gap and no duplicate.
# The output starts after the client attaches, so every line arrives live.
sid = start("e2e-counter", "/bin/bash", "-c", "sleep 3; for i in $(seq 1 400); do echo N$i; sleep 0.05; done; sleep 600")
t = Term([cli, "attach", HOST + "/" + sid], env=env)
ok(t.wait_for(rb"N20\r", 30), "counter running")
# Kill the client's bridge ssh (as a network drop would), and the shared master.
subprocess.run(["pkill", "-f", NAME + "-host\"? bridge"], check=False)  # the path may be quoted
subprocess.run(["pkill", "-f", "ControlPath=.*" + NAME + "/cm"], check=False)
t0 = time.time()
ok(t.wait_for(rb"N400\r", 60), "client reconnected by itself and kept receiving (%.1fs)" % (time.time() - t0))
nums = [int(x) for x in re.findall(rb"N(\d+)\r", t.buf)]
c = collections.Counter(nums)
dups = [n for n, k in c.items() if k > 1]
missing = [n for n in range(1, 401) if n not in c]
ok(not missing, "no gap after reconnect (missing: %s)" % missing[:10])
ok(not dups, "no duplicate after reconnect (dups: %s)" % dups[:10])
t.send(b"\x1c"); t.exit_code(10)

# B. Kitty keyboard sequences reach the host unchanged.
sid2 = start("e2e-keys", "/bin/sh", "-c", "sleep 3; exec " + HELPER + " keytest")
t = Term([cli, "attach", HOST + "/" + sid2], env=env)
ok(t.wait_for(rb"Press keys", 30), "host key tester running in a session")
ok(b"\x1b[>31u" in t.buf, "kitty keyboard push reached the local terminal")
keys = [b"\x1b[13;2u", b"\x1b[112;3u", b"\x1b[98;5u", b"\x1b[27u", b"\x1b[99;5:3u"]
for k in keys:
    t.send(k); time.sleep(0.3)
t.read(1)
for k in keys:
    ok(repr(k.decode())[1:-1].encode() in t.buf.replace(b'\\x1b', b'\\x1b') or k.decode().encode('unicode_escape') in t.buf, "key %r arrived unchanged" % k)
t.send(b"q"); time.sleep(0.2); t.send(b"q")
time.sleep(1); t.read(1)

# C. Escape sequences pass byte for byte.
seq = (b"\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\ "
       b"\x1b[38;2;255;100;0mtruecolor\x1b[0m \x1b[4:3mcurly\x1b[0m "
       b"\x1b]777;notify;Title;Body\x1b\\\x1b]52;c;aGVsbG8=\x1b\\\x1b[?2026h\x1b[?2026l END")
host_ssh("cat > ~/e2e-seq.bin", seq)
# Live output must be byte-identical (history on a new client is a snapshot, a redraw).
sid3 = start("e2e-bytes", "/bin/bash", "-c", "sleep 3; cat ~/e2e-seq.bin; sleep 600")
t = Term([cli, "attach", HOST + "/" + sid3], env=env)
ok(t.wait_for(rb" END", 30), "escape test output arrived")
ok(seq in t.buf, "OSC 8, truecolor, styled underline, OSC 777, OSC 52 and sync output are byte-identical")
t.send(b"\x1c"); t.exit_code(10)

# C2. With --debug and every category on, the same bytes pass unchanged
#     (decision 0025). debug start, save, profile and stop work on the
#     running session, and no debug file holds the session's bytes.
dbg_file = os.path.join(HERE, ".e2e-debug.jsonl")
# The debug folders on each side, to remove afterwards what this check made.
laptop_dbg = os.path.join(os.environ.get("XDG_STATE_HOME", "") if os.path.isabs(os.environ.get("XDG_STATE_HOME", ""))
                          else os.path.expanduser("~/.local/state"), NAME, "debug")
host_dbg_dir = ".local/state/%s/debug" % NAME
def listing(path):
    return set(os.listdir(path)) if os.path.isdir(path) else set()
def host_listing():
    return set(host_ssh("ls -1 %s 2>/dev/null; true" % host_dbg_dir).stdout.decode().split())
laptop_before, host_before = listing(laptop_dbg), host_listing()
outside = os.path.join(laptop_dbg, "outside.txt")
outside_before = open(outside).read() if os.path.exists(outside) else None
sid3d = start("e2e-bytes-debug", "/bin/bash", "-c", "sleep 3; cat ~/e2e-seq.bin; sleep 600")
created.append(sid3d)
t = Term([cli, "--debug", "--debug-file", dbg_file, "attach", HOST + "/" + sid3d], env=env)
ok(t.wait_for(rb" END", 30), "escape test output arrived with debug on")
ok(seq in t.buf, "with every debug category on, the attached bytes are byte-identical")
t.send(b"\x1c"); t.exit_code(10)
for args, what in ((["start", "--for", "5m"], "debug start"), (["save"], "debug save"),
                   (["profile", "--for", "2s"], "debug profile"), (["stop"], "debug stop")):
    argv = [cli, "debug", args[0], HOST + "/" + sid3d] + (["cpu"] if args[0] == "profile" else []) + args[1:]
    r = subprocess.run(argv, capture_output=True, text=True)
    ok(r.returncode == 0, "%s works on a running session (%s)" % (what, (r.stdout + r.stderr).strip()[:120]))
dbg = open(dbg_file, "rb").read() if os.path.exists(dbg_file) else b""
for p in (dbg_file, dbg_file + ".1"):
    if os.path.exists(p):
        os.remove(p)
ok(b'"ev":"key"' in dbg or b'"ev":"ping"' in dbg, "the client's debug file has records")
ok(b"truecolor" not in dbg and b"aGVsbG8" not in dbg, "the client's debug file holds none of the session's bytes")
host_dbg = host_ssh("cat ~/.local/state/%s/sessions/%s/debug.jsonl" % (NAME, sid3d)).stdout
ok(b'"ev":"start"' in host_dbg and b"truecolor" not in host_dbg, "the host's debug file has records, and none of the session's bytes")
# Remove what the check made: the client's profiles and its list of files
# outside the folder on the laptop, and the bridges' files on the host. The
# session's own files go with the session.
for f in listing(laptop_dbg) - laptop_before - {"outside.txt"}:
    os.remove(os.path.join(laptop_dbg, f))
if outside_before is None:
    if os.path.exists(outside):
        os.remove(outside)
else:
    open(outside, "w").write(outside_before)
if not laptop_before and os.path.isdir(laptop_dbg) and not os.listdir(laptop_dbg):
    os.rmdir(laptop_dbg)
for f in host_listing() - host_before:
    if re.fullmatch(r"bridge-[0-9][a-z0-9.-]*", f):
        host_rc("rm -f %s/%s" % (host_dbg_dir, f))

# D. A dropped local file is uploaded and its remote path pasted.
local = os.path.join(HERE, ".drop-test.txt")
open(local, "w").write("dropped file content\n")
sid4 = start("e2e-drop", *BASH_I)
t = Term([cli, "attach", HOST + "/" + sid4], env=env)
time.sleep(2); t.read(1)
t.send(b"bind 'set enable-bracketed-paste on' 2>/dev/null; printf '\\033[?2004h'; read -r P; cat $P\r"); time.sleep(1)
t.send(b"\x1b[200~" + local.encode() + b"\x1b[201~\r")
ok(t.wait_for(rb"/paste/[0-9a-f]{12}-drop-test.txt", 20), "dropped file uploaded and its remote path pasted")
t.send(b"\x1c"); t.exit_code(10)

# E. Killing the client leaves the session running.
t = Term([cli, "attach", HOST + "/" + sid4], env=env)
time.sleep(2)
os.kill(t.pid, 9)
time.sleep(1)
ls = subprocess.run([cli, "ls", "--json"], capture_output=True, text=True).stdout
ok('"id": "%s"' % sid4 in ls and '"alive": true' in ls, "session survives a killed client")

# F. Memory cap: a hog that the agent started is OOM-killed and the session
#    marked, while the agent runs on. Another session is unaffected. Then a
#    hog that is the agent itself ends its session out of memory.
#    A container has no systemd, so no memory caps: there the hog would only
#    fill the container, and the check is skipped.
def session_rows():
    ls = subprocess.run([cli, "ls", "--json"], capture_output=True, text=True).stdout
    return {s["id"]: s for h in json.loads(ls) for s in (h["sessions"] or [])}

if host_machine().get("provider") == "docker":
    print("SKIP memory cap: a container has no systemd user manager, so sessions have no memory caps")
    created += [sid, sid2, sid3, sid4]
else:
    # A cap of its own, the smallest there is, makes the check quick, and
    # keeps the hog far below the host's memory, whatever its size.
    sid5 = start("e2e-oom", "/bin/bash", "-c", "head -c 1G /dev/zero | tail > /dev/null; sleep 600", flags=("--memory-max", "256M"))
    deadline = time.time() + 40
    while True:
        rows = session_rows()
        r5 = rows.get(sid5, {})
        if r5.get("oom_killed") or time.time() > deadline:
            break
        time.sleep(2)
    ok(rows.get(sid4, {}).get("alive"), "other sessions keep running")
    print("oom session:", {k: r5.get(k) for k in ("state", "reason", "memory_current", "memory_max")})
    ok(r5.get("memory_max") == 256 << 20, "the session got the cap it asked for (%s)" % r5.get("memory_max"))
    ok(r5.get("oom_killed") is True, "the over-the-cap process was stopped and the session marked (reason: %s)" % r5.get("reason"))
    ok(r5.get("state") in ("working", "idle"), "the agent runs on after its child's kill, and does not need input (state: %s)" % r5.get("state"))
    # All sessions together have a limit too: the host's memory minus a reserve.
    limit = host_ssh("systemctl --user show %s.slice -p MemoryMax --value" % NAME).stdout.decode().strip()
    ok(limit.isdigit() and int(limit) > 256 << 20, "the sessions' slice has a memory limit (%s)" % limit)
    created += [sid, sid2, sid3, sid4, sid5]
    # tail keeps its last line, and /dev/zero has no newline: it grows until the cap stops it.
    # Without a cap it would fill the host's memory, so it runs only under one.
    if not r5.get("memory_max"):
        print("SKIP memory cap on the agent itself: the session has no memory cap")
    else:
        sid_oom = start("e2e-oom-agent", "/bin/sh", "-c", "exec tail /dev/zero", flags=("--memory-max", "256M"))
        created.append(sid_oom)
        r_oom, end = {}, time.time() + 60
        while time.time() < end and r_oom.get("state") not in ("exited", "errored"):
            time.sleep(2)
            r_oom = session_rows().get(sid_oom, {})
        print("oom agent session:", {k: r_oom.get(k) for k in ("state", "reason", "signal", "oom_killed")})
        ok(r_oom.get("oom_killed") is True and r_oom.get("state") == "errored" and r_oom.get("reason") == "oom",
           "the cap stopped the agent itself, and its session ended out of memory (state: %s, reason: %s)" % (r_oom.get("state"), r_oom.get("reason")))

        # After the cap stops the agent, resume starts it again in the same
        # session, with a bigger cap. Its first run leaves a process behind,
        # and fills its cap. The mark makes the second run quiet. --agent ""
        # keeps the command: the shell agent would continue with a login shell.
        # setsid keeps the process that the agent leaves out of the terminal's
        # session, so the end of the agent sends it no SIGHUP.
        mark = "$HOME/.cache/e2e-resume-%d" % os.getpid()
        sid_res = start("e2e-resume", "/bin/sh", "-c",
                        'if [ -e "%s" ]; then exec sleep 600; fi; mkdir -p "$HOME/.cache"; touch "%s"; setsid sleep 913 & exec tail /dev/zero' % (mark, mark),
                        flags=("--agent", "", "--memory-max", "256M"))
        created.append(sid_res)
        r_res, end = {}, time.time() + 60
        while time.time() < end and r_res.get("state") not in ("exited", "errored"):
            time.sleep(2)
            r_res = session_rows().get(sid_res, {})
        ok(r_res.get("oom_killed") is True, "the cap stopped the agent that resume brings back (state: %s)" % r_res.get("state"))
        ok(host_rc("pgrep -u \"$(id -u)\" -f 'sleep 91[3]'") == 0, "the stopped agent left a process in its session")
        t = Term([cli, "resume", HOST + "/" + sid_res, "--memory-max", "300M", "--detach"], env=env)
        code = t.exit_code(90)
        ok(code == 0 and b"Resumed" in t.buf, "resume starts the agent again (exit %s): %s" % (code, t.buf.decode(errors="replace").strip()))
        r_res = session_rows().get(sid_res, {})
        print("resumed session:", {k: r_res.get(k) for k in ("state", "alive", "memory_max", "oom_killed")})
        ok(r_res.get("alive") and r_res.get("state") not in ("exited", "errored", "lost") and not r_res.get("oom_killed"),
           "the resumed agent runs, in the same session (state: %s)" % r_res.get("state"))
        ok(r_res.get("memory_max") == 300 << 20, "the resumed session got the bigger cap (%s)" % r_res.get("memory_max"))
        unit = r_res.get("unit") or ""
        unit_max = host_ssh("systemctl --user show %s -p MemoryMax --value" % unit).stdout.decode().strip() if unit else ""
        ok(unit_max == str(300 << 20), "the new unit %s has the bigger cap (MemoryMax=%s)" % (unit, unit_max))
        ok(host_rc("pgrep -u \"$(id -u)\" -f 'sleep 91[3]'") == 1, "resume stopped what the old agent left in its session")
        t = Term([cli, "resume", HOST + "/" + sid_res, "--detach"], env=env)
        code = t.exit_code(60)
        ok(code not in (0, None) and b"still runs" in t.buf, "a resume of an agent that runs is refused (exit %s)" % code)
        host_rc('rm -f "%s"' % mark)

# G. Laptop sleep: freeze the client and cut its network path; on wake it must
#    be back to live output within 5 s, with what it missed replayed.
import signal
sid6 = start("e2e-wake", "/bin/bash", "-c", "while :; do date +T%s.%N; sleep 0.1; done")
created.append(sid6)
t = Term([cli, "attach", HOST + "/" + sid6], env=env)
t.wait_for(rb"T\d+", 20); time.sleep(1)
os.kill(t.pid, signal.SIGSTOP)
subprocess.run(["pkill", "-f", NAME + "-host\"? bridge"], check=False)  # the path may be quoted
subprocess.run(["pkill", "-f", "ControlPath=.*" + NAME + "/cm"], check=False)
time.sleep(15)
t.buf = b""
woke = time.time()
os.kill(t.pid, signal.SIGCONT)
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

# H. A new client after the ring wrapped gets a snapshot from the shadow
#    terminal: the right screen and the modes set long ago (kitty flags).
sid7 = start("e2e-snapshot", "/bin/bash", "-c",
    'printf "\\033[>1u\\033[?2004h"; for i in $(seq 1 120000); do printf "LINE-%06d padding padding padding padding padding\\n" $i; done; printf "FINAL-SCREEN-MARKER\\n"; sleep 600')
created.append(sid7)
time.sleep(12)
t = Term([cli, "attach", HOST + "/" + sid7], cols=100, rows=30, env=env)
ok(t.wait_for(rb"FINAL-SCREEN-MARKER", 30), "snapshot: a new client sees the final screen after the ring wrapped")
ok(b"\x1b[>1u" in t.buf and b"\x1b[?2004h" in t.buf, "snapshot: kitty flags and bracketed paste set before the ring's start are restored")
ok(b"LINE-000001 " not in t.buf and len(t.buf) < 2 << 20, "snapshot: bounded (%d bytes, not the 8 MiB ring)" % len(t.buf))
t.send(b"\x1c"); t.exit_code(10)

# I. Killing the shadow terminal does not stop the agent; it restarts.
sid8 = start("e2e-vtcrash", "/bin/bash", "-c", 'printf "\\033[>1u"; i=0; while :; do i=$((i+1)); printf "TICK-%d\\n" $i; sleep 0.2; done')
created.append(sid8)
time.sleep(3)
# Through /proc, not pkill: a slim container image has no procps.
host_ssh("for p in /proc/[0-9]*; do [ \"$(cat $p/comm 2>/dev/null)\" = '%s' ] && kill -9 \"${p#/proc/}\"; done; sleep 3" % (NAME + "-vt")[:15])
t = Term([cli, "attach", HOST + "/" + sid8], env=env)
t.read(4)
ticks = [int(x) for x in re.findall(rb"TICK-(\d+)", t.buf)]
ok(ticks and max(ticks) > 20, "shadow terminal killed: the agent kept running (tick %s)" % (max(ticks) if ticks else None))
ok(b"\x1b[>1u" in t.buf, "shadow terminal killed: it restarted, and a new client still got a snapshot")
t.send(b"\x1c"); t.exit_code(10)

# J. Ctrl+V with an image on the clipboard uploads it and pastes the remote
#    path. The test hook stands in for the clipboard, so the user's real
#    clipboard is never touched.
import base64
png = base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==")
img = os.path.join(HERE, ".clip-test.png")
open(img, "wb").write(png)
sid9 = start("e2e-clip", *BASH_I)
created.append(sid9)
env_clip = dict(env); env_clip[NAME.upper() + "_TEST_CLIPBOARD_IMAGE"] = img
t = Term([cli, "attach", HOST + "/" + sid9], env=env_clip)
time.sleep(2); t.read(1)
t.send(b"cat -v\r"); time.sleep(0.5)
t.buf = b""
t.send(b"\x16")  # Ctrl+V
ok(t.wait_for(rb"/paste/[0-9a-f]{12}-clipboard\.png", 20), "image paste: Ctrl+V uploaded the image and pasted its path on the host")
m = re.search(rb"(/[^ \r\n]*/paste/[0-9a-f]{12}-clipboard\.png)", t.buf)
if m:
    out = host_ssh("sha256sum " + m.group(1).decode()).stdout.decode()
    import hashlib
    ok(hashlib.sha256(png).hexdigest() in out, "image paste: the file on the host is the same image")
t.send(b"\x04"); time.sleep(0.3)
t.send(b"\x1c"); t.exit_code(10)
os.remove(img)

# K. The session owns the whole terminal: its PTY is the terminal's size and
#    follows a resize, and the client draws nothing of its own while attached.
sid10 = start("e2e-size", *BASH_I)
created.append(sid10)
t = Term([cli, "attach", HOST + "/" + sid10], cols=101, rows=37, env=env)
time.sleep(2); t.read(1)
t.send(b"stty size\r")
ok(t.wait_for(rb"[\r\n]37 101\r", 15), "attached: the agent's PTY is the terminal's size (37 rows, 101 columns)")
t.resize(120, 40); time.sleep(1)
t.send(b"stty size\r")
ok(t.wait_for(rb"[\r\n]40 120\r", 15), "attached: a resize reaches the agent's PTY (40 rows, 120 columns)")
t.send(b"\x1c"); t.exit_code(10)

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
created.append(sid11)
t = Term([cli, "attach", HOST + "/" + sid11], cols=COLS, rows=ROWS, env=env)
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

# L. Alerts (decision 0019): a command destination on the host gets the
#    session's events as JSON on stdin. A file the user already had is put
#    aside first and put back after.
ALERTS_TOML = "~/.config/%s/alerts.toml" % NAME
ALERTS_OUT = "$HOME/e2e-alerts.jsonl"
saved = host_ssh("if [ -e %s ]; then mv %s %s.e2e-saved && echo saved; fi" % ((ALERTS_TOML,) * 3)).stdout.strip() == b"saved"
host_ssh("rm -f " + ALERTS_OUT)

def alert_events(kind, sid=None, timeout=20):
    end = time.time() + timeout
    while time.time() < end:
        out = host_ssh("cat %s 2>/dev/null; true" % ALERTS_OUT).stdout.decode(errors="replace")
        evs = [json.loads(l) for l in out.splitlines() if l.startswith("{")]
        hits = [e for e in evs if e.get("event") == kind and (sid is None or e.get("session", {}).get("id") == sid)]
        if hits:
            return hits
        time.sleep(1)
    return []

def host_cli(*args):
    return subprocess.run([cli, "host", "alerts"] + list(args), capture_output=True, text=True)

r = host_cli("set", HOST, "--command", "cat >> \"%s\"" % ALERTS_OUT, "--events", "needs_input,failed", "--delay", "2s")
ok(r.returncode == 0 and "are on" in r.stdout, "alerts: set writes a command destination (%s)" % (r.stdout + r.stderr).strip().splitlines()[0])
mode = host_ssh("stat -c %a " + ALERTS_TOML).stdout.decode().strip()
ok(mode == "600", "alerts: the alerts file is mode 0600 (%s)" % mode)
r = host_cli("test", HOST)
ok(r.returncode == 0 and "✓ command" in r.stdout, "alerts: test reports ok for the command")
ok(alert_events("test", timeout=5), "alerts: the test event reached the command")
# An agent hook raises needs_input; it goes out after the 2 s delay.
t0 = time.time()
sid_ask = start("e2e-alert-ask", "/bin/sh", "-c", "sleep 2; echo '{\"message\":\"e2e question\"}' | %s hook notification; sleep 600" % HELPER)
created.append(sid_ask)
hits = alert_events("needs_input", sid_ask, timeout=2 + 2 + 10 + 5)
ok(hits and hits[0].get("v") == 1 and hits[0]["session"]["name"] == "e2e-alert-ask" and "detail" not in hits[0],
   "alerts: needs_input from a hook arrived %.1f s after start, without the detail" % (time.time() - t0))
# A program that ends with code 3 sends failed.
sid_fail = start("e2e-alert-fail", "/bin/sh", "-c", "sleep 2; exit 3")
created.append(sid_fail)
hits = alert_events("failed", sid_fail, timeout=15)
ok(hits and hits[0].get("exit_code") == 3 and "failed (exit 3)" in hits[0].get("summary", ""), "alerts: a session that exits 3 sends failed with exit_code 3")
r = host_cli("log", HOST, "-n", "10")
ok(r.returncode == 0 and "needs_input" in r.stdout and "failed" in r.stdout, "alerts: the log shows the deliveries")
r = host_cli("off", HOST)
ok(r.returncode == 0 and "off" in r.stdout, "alerts: off removes the file")
r = host_cli(HOST)
ok(r.returncode == 0 and "are off" in r.stdout, "alerts: the host says alerts are off")
host_ssh("rm -f " + ALERTS_OUT)
if saved:
    host_ssh("mv %s.e2e-saved %s" % (ALERTS_TOML, ALERTS_TOML))

setup_checks()
guided_checks()
# M. Full screen (roadmap N2): a program on the alternate screen is reported
#    as full screen, and host doctor says so.
sid_fs = start("e2e-fullscreen", "/bin/bash", "-c", 'printf "\\033[?1049hFULL"; sleep 600')
created.append(sid_fs)
time.sleep(4)
ls = subprocess.run([cli, "ls", "--json"], capture_output=True, text=True).stdout
rows = {s["id"]: s for h in json.loads(ls) for s in (h["sessions"] or [])}
ok(rows.get(sid_fs, {}).get("full_screen") is True, "full screen: a program on the alternate screen is reported as full screen")
doc = subprocess.run([cli, "host", "doctor", HOST], capture_output=True, text=True).stdout
ok("e2e-fullscreen" in doc and "full screen" in doc, "full screen: host doctor names the session")

# N. pull (roadmap N7): a session's branch comes to a local repository over
#    the client's SSH, and the local working tree is untouched.
import tempfile
try:
    host_ssh("command -v git >/dev/null")
    host_git = True
except subprocess.CalledProcessError:
    host_git = False
if host_machine().get("provider") == "docker":
    local = tempfile.mkdtemp()
    subprocess.run(["git", "init", "-q", local], check=True)
    r = subprocess.run([cli, "pull", HOST + "/" + sid], capture_output=True, text=True, cwd=local)
    ok(r.returncode != 0 and "needs SSH" in r.stderr, "pull: says it needs SSH on a host without it")
elif not host_git:
    print("SKIP pull: the host has no git")
else:
    host_ssh("rm -rf ~/e2e-pull && git init -q -b main ~/e2e-pull && cd ~/e2e-pull && "
             "git -c user.name=e2e -c user.email=e2e@example.com commit -q --allow-empty -m base && git switch -q -c e2e-topic && "
             "git -c user.name=e2e -c user.email=e2e@example.com commit -q --allow-empty -m 'from the session'")
    t = Term([cli, "new", HOST, "--name", "e2e-pull", "--dir", "~/e2e-pull", "--detach"], env=env)
    t.exit_code(30)
    ls = subprocess.run([cli, "ls", "--json"], capture_output=True, text=True).stdout
    sid_pull = [s["id"] for h in json.loads(ls) for s in (h["sessions"] or []) if s["name"] == "e2e-pull"][0]
    created.append(sid_pull)
    local = tempfile.mkdtemp()
    subprocess.run(["git", "init", "-q", "-b", "mine", local], check=True)
    r = subprocess.run([cli, "pull", HOST + "/" + sid_pull], cwd=local, capture_output=True, text=True, env=dict(os.environ, **env))
    log = subprocess.run(["git", "log", "--format=%s", "e2e-topic"], cwd=local, capture_output=True, text=True).stdout
    ok(r.returncode == 0 and "from the session" in log, "pull: the session's branch reaches the local repository (%s)" % (r.stderr or r.stdout).strip()[:80])
    r2 = subprocess.run([cli, "pull", HOST + "/" + sid_pull, "--into", "mine"], cwd=local, capture_output=True, text=True, env=dict(os.environ, **env))
    ok(r2.returncode != 0 and "checked out here" in r2.stderr, "pull: refuses to fetch into the branch checked out here")
    host_ssh("rm -rf ~/e2e-pull")

# Clean up.
for s in created:
    subprocess.run([cli, "kill", HOST + "/" + s], capture_output=True)
try:
    host_ssh("rm -f ~/e2e-seq.bin")
    os.remove(local)
except Exception:
    pass
print("RESULT %d/%d passed" % (sum(results), len(results)))
sys.exit(0 if all(results) else 1)
