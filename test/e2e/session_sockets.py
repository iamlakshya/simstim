"""End-to-end checks for the session sockets: a program inside one session
cannot type into, read or attach to another session through its socket, and
an agent's hook reaches its own session only.

run.py calls run() with its helpers. `E2E_ONLY=session-sockets just e2e` runs
only these. They start two sessions of their own, type only into the first,
and delete both. They touch no other session. On a host without a systemd
user manager, they check only that `host doctor` reports the sockets as weak.
"""
import base64, json, re, struct, subprocess, time


class Ctx:
    """What the checks need from run.py."""
    def __init__(self, **kw):
        self.__dict__.update(kw)


HELLO, REQUEST, ATTACH = 0x01, 0x10, 0x20


def frame(kind, obj):
    """One frame of the protocol: its length (u32, little-endian), its type
    (u8), then its JSON."""
    p = json.dumps(obj).encode()
    return struct.pack("<IB", len(p) + 1, kind) + p


def raw_probe(name):
    """A Python script for the host: it dials the socket that its argument
    names, asks it to type, and prints RAW- and the answer's error code."""
    return ("""import json, socket, struct, sys
s = socket.socket(socket.AF_UNIX)
s.connect(sys.argv[1])
def send(kind, obj):
    p = json.dumps(obj).encode()
    s.sendall(struct.pack("<IB", len(p) + 1, kind) + p)
def read():
    h = b""
    while len(h) < 5:
        h += s.recv(5 - len(h))
    n, kind = struct.unpack("<IB", h)
    p = b""
    while len(p) < n - 1:
        p += s.recv(n - 1 - len(p))
    return kind, p
send(%d, {"magic": %r, "protocol": "1.0", "version": "e2e", "role": "bridge"})
read()
send(%d, {"id": 1, "op": "send", "args": {"text": "echo E2E-TYPED", "enter": True}})
kind, p = read()
print("RAW-" + ((json.loads(p).get("error") or {}).get("code") or "ok"))
""" % (HELLO, name, REQUEST)).encode()


def probe_line(c, b):
    """The line typed into the first session: it runs raw_probe on session
    b's socket with python3, or prints the marker RAW-NONE without it. The
    shell builds each marker, so the echo of the typed line holds none."""
    sock = c.NAME.upper() + "_HOOK_SOCK"
    return ("if command -v python3 >/dev/null; then echo %s | base64 -d | python3 - \"${%s%%/h/*}/s/%s.sock\"; "
            "else printf 'RAW-%%s\\n' NONE; fi; echo RAWDONE-$((40+2))\r"
            % (base64.b64encode(raw_probe(c.NAME)).decode(), sock, b)).encode()


def probe_answers(buf):
    """The markers that the probe printed: the output lines that hold only
    RAW- and a word, never the echo of the typed line."""
    return [m.decode() for m in re.findall(rb"(?m)^RAW-([A-Za-z_]+)\r?$", buf)]


def probe_step(c, t, b):
    """Types probe_line into the first session's terminal t, and checks the
    answer of session b's socket: forbidden, or a skip without python3."""
    t.buf = b""
    t.send(probe_line(c, b))
    c.ok(t.wait_for(rb"(?m)^RAWDONE-42", 30), "sockets: the probe of the second session's socket ran")
    answers = probe_answers(t.buf)
    if answers == ["NONE"]:
        print("SKIP sockets: the host has no python3 for the probe of a session's socket")
        return
    c.ok(answers == ["forbidden"], "sockets: the second session's socket refuses a program in the first (%s)" % answers)


def doctor_checks(c):
    """The lines of host doctor, by name."""
    r = subprocess.run([c.cli, "host", "doctor", c.HOST, "--json"], capture_output=True, text=True, timeout=120)
    return {x["name"]: x for x in json.loads(r.stdout or "{}").get("checks") or []}


def rows(c):
    r = subprocess.run([c.cli, "ls", "--json", "--host", c.HOST], capture_output=True, text=True, timeout=60)
    return {s["id"]: s for h in json.loads(r.stdout or "[]") for s in (h.get("sessions") or [])}


def run(c):
    lines = doctor_checks(c)
    check = lines.get("session sockets")
    if not c.ok(check is not None, "sockets: host doctor reports the session sockets"):
        return
    # Whether the host has a systemd user manager comes from its own line.
    # Sessions that an older helper started make the sockets line weak too,
    # and must not stop the checks of the two new sessions below.
    if not (lines.get("systemd user manager") or {}).get("ok"):
        c.ok(not check.get("ok") and check.get("detail", "").startswith("weak") and check.get("fix"),
             "sockets: without a systemd user manager, host doctor reports them as weak, with a hint")
        print("SKIP sockets: the host has no systemd user manager, so its holders keep the role check alone")
        return
    if not check.get("ok"):
        print("NOTE sockets: host doctor says %s" % check.get("detail"))
    created = []
    try:
        checks(c, created)
    finally:
        for s in created:
            subprocess.run([c.cli, "kill", c.HOST + "/" + s], capture_output=True)


def checks(c, created):
    a = c.start("e2e-sock-a", *c.BASH_I)
    b = c.start("e2e-sock-b", "/bin/sh", "-c", "sleep 600")
    created += [s for s in (a, b) if s]
    if not c.ok(a and b, "sockets: two sessions start"):
        return
    t = c.Term([c.cli, "attach", c.HOST + "/" + a], env=c.env)
    if not c.ok(t.wait_for(rb"\$( |\x1b)", 30), "sockets: the first session's shell is ready"):
        return
    t.send(b" unset HISTFILE; { set +o history; history -c; } 2>/dev/null\r")

    # 1. The helper's bridge, run in the first session, asks to type into
    #    the second session, to show its screen, to delete it, and to attach
    #    to it. Each answer must be forbidden. The marker is computed, so the
    #    typed line does not hold it.
    stream = (frame(HELLO, {"magic": c.NAME, "protocol": "1.0", "version": "e2e", "role": "client"})
              + frame(REQUEST, {"id": 1, "op": "send", "args": {"id": b, "text": "echo E2E-TYPED", "enter": True}})
              + frame(REQUEST, {"id": 2, "op": "peek", "args": {"id": b, "bytes": 100}})
              + frame(REQUEST, {"id": 3, "op": "delete", "args": {"id": b}})
              + frame(ATTACH, {"session": b, "cols": 80, "rows": 24, "client": "e2e", "last_offset": -1}))
    line = ("(echo %s | base64 -d; sleep 4) | %s bridge | tr -c '[:print:]' '\\n' | grep -o 'code.:.[a-z_]*'; echo SOCK-$((40+2))\r"
            % (base64.b64encode(stream).decode(), c.HELPER))
    t.buf = b""
    t.send(line.encode())
    c.ok(t.wait_for(rb"SOCK-42", 30), "sockets: the bridge in the first session answers")
    got = re.findall(rb'code":"([a-z_]+)', t.buf)
    c.ok(got.count(b"forbidden") == 4,
         "sockets: send, peek, delete and attach from inside a session are forbidden (%s)" % b", ".join(got).decode())

    # 2. The second session's socket itself, dialled from the first session,
    #    refuses to type. This needs python3 on the host.
    probe_step(c, t, b)

    # 3. A hook in the first session that names the second session's hook
    #    socket is refused. The first session's own hook is taken.
    hook = c.NAME.upper() + "_HOOK_SOCK"
    t.send(("env %s=\"${%s%%/*}/%s.sock\" %s hook notification '{\"message\":\"e2e-cross-hook\"}'\r"
            % (hook, hook, b, c.HELPER)).encode())
    t.send(("%s hook notification '{\"message\":\"e2e-own-hook\"}'; echo HOOKS-$((40+2))\r" % c.HELPER).encode())
    c.ok(t.wait_for(rb"HOOKS-42", 30), "sockets: the hooks ran")
    t.send(b"\x1c")
    t.exit_code(10)
    time.sleep(2)
    r = rows(c)
    c.ok(r.get(a, {}).get("reason") == "e2e-own-hook" and r.get(a, {}).get("state") == "needs_input",
         "sockets: a hook reaches its own session (%s, %r)" % (r.get(a, {}).get("state"), r.get(a, {}).get("reason")))
    c.ok(r.get(b, {}).get("reason") != "e2e-cross-hook" and r.get(b, {}).get("alive"),
         "sockets: a hook cannot reach another session (%s, %r)" % (r.get(b, {}).get("state"), r.get(b, {}).get("reason")))

    # 4. Nothing reached the second session's terminal.
    t = c.Term([c.cli, "attach", c.HOST + "/" + b], env=c.env)
    t.read(3)
    t.send(b"\x1c")
    t.exit_code(10)
    c.ok(b"E2E-TYPED" not in t.buf, "sockets: nothing was typed into the second session")
