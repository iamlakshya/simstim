"""End-to-end checks for hosts you add (isolation spec 14.16). For now there
is one, test 2: with ForwardAgent naming a throwaway agent in a throwaway
client SSH config, a session on the host has no SSH_AUTH_SOCK (task H2,
decision 0034).

run.py calls run() with its helpers. `E2E_ONLY=sandbox-hosts just e2e` runs
only these. They use a throwaway client config, state and cache, so the
user's own files and shared connections are never used or changed.

The config forwards a throwaway ssh-agent that holds no key, never the user's
own agent. So even if the client were wrong, the host would get no key. To
show that the check can fail, plain ssh with the same config, and without
the client's generated config and options, forwards that empty agent once,
on a connection of its own. Naming an agent socket in ForwardAgent needs
OpenSSH 8.2 or later on this computer.
"""
import json, os, re, secrets, shutil, subprocess, tempfile, time


class Ctx:
    """What the checks need from run.py."""
    def __init__(self, **kw):
        self.__dict__.update(kw)


def run(c):
    forwarding(c)


def ssh_version():
    """This computer's OpenSSH version as (major, minor), or None."""
    r = subprocess.run(["ssh", "-V"], capture_output=True, text=True)
    m = re.search(r"OpenSSH_(\d+)\.(\d+)", r.stderr + r.stdout)
    return (int(m.group(1)), int(m.group(2))) if m else None


def toml_str(s):
    """s as a TOML basic string. A JSON string is one."""
    return json.dumps(s)


def start_agent(sock):
    """Starts a throwaway ssh-agent with no keys on sock, or returns None."""
    agent = subprocess.Popen(["ssh-agent", "-D", "-a", sock], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    end = time.time() + 5
    while time.time() < end and not os.path.exists(sock):
        time.sleep(0.1)
    if os.path.exists(sock):
        return agent
    agent.terminate()
    agent.wait(5)
    return None


def write_configs(c, h, tmp, sock):
    """Writes a throwaway client config whose one host forwards the agent at
    sock, and a plain SSH config that reads the same files as the client's
    generated one, without its first lines. The host's own SSH config, if it
    has one, still applies. Returns the plain config."""
    fwd = os.path.join(tmp, "forward.conf")
    with open(fwd, "w") as f:
        f.write("# Written by the end-to-end tests: forward a throwaway agent to every host.\n")
        f.write("ForwardAgent %s\n" % sock)
        if h.get("ssh_config"):
            f.write('Include "%s"\n' % os.path.expanduser(h["ssh_config"]))
    cfg = os.path.join(tmp, "config", c.NAME)
    os.makedirs(cfg)
    with open(os.path.join(cfg, "hosts.toml"), "w") as f:
        f.write("[[host]]\nname = %s\nssh = %s\nssh_config = %s\n" % (toml_str(c.HOST), toml_str(h["ssh"]), toml_str(fwd)))
    plain = os.path.join(tmp, "plain.conf")
    with open(plain, "w") as f:
        for p in (fwd, os.path.expanduser("~/.ssh/config"), "/etc/ssh/ssh_config"):
            if os.path.exists(p):
                f.write('Include "%s"\n' % p)
    return plain


# The probes run the same kind of shell as the session's login environment
# (LoginEnv in internal/host/env.go): a login shell that is interactive.
MARK = "__E2E_SOCK__"
PROBE = "exec \"$SHELL\" -l -i -c 'printf \"%s%%s%s\" \"${SSH_AUTH_SOCK:-}\"'" % (MARK, MARK)


def probe_sock(plain, target, agent):
    """SSH_AUTH_SOCK in a login shell on the host, through plain ssh with the
    plain config, on a connection of its own. Only agent forwarding varies:
    agent is "no", or None to leave it to the config. Returns (value, None),
    or (None, why) when the probe could not tell."""
    argv = ["ssh", "-F", plain, "-o", "BatchMode=yes", "-o", "ControlMaster=no", "-o", "ControlPath=none",
            "-o", "ForwardX11=no", "-o", "PermitLocalCommand=no", "-o", "GSSAPIDelegateCredentials=no",
            "-o", "ClearAllForwardings=yes", "-T"]
    if agent:
        argv += ["-o", "ForwardAgent=" + agent]
    try:
        r = subprocess.run(argv + ["--", target, PROBE], stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=60)
    except subprocess.TimeoutExpired:
        return None, "plain ssh took more than 60 s"
    m = re.search(re.escape(MARK) + "(.*?)" + re.escape(MARK), r.stdout)
    if r.returncode != 0 or not m:
        return None, "plain ssh exited %d: %s" % (r.returncode, (r.stderr.strip().splitlines() or ["no output"])[-1])
    return m.group(1), None


def session_sock(c, sid):
    """SSH_AUTH_SOCK in the session's agent process, read on the host. Returns
    (value, None), or (None, why) when the environment could not be read."""
    try:
        man = json.loads(c.host_ssh("cat ~/.local/state/%s/sessions/%s/manifest.json" % (c.NAME, sid)).stdout)
        pid = int(man["runtime"]["agent_pid"])
        # A failed read stops the command, and an environment without PATH
        # is not one that was read.
        out = c.host_ssh("env=$(tr '\\0' '\\n' < /proc/%d/environ) || exit 3; "
                         "printf '%%s\\n' \"$env\" | grep -q '^PATH=' || exit 4; "
                         "printf '%%s\\n' \"$env\" | sed -n 's/^SSH_AUTH_SOCK=//p'" % pid).stdout
    except (subprocess.CalledProcessError, KeyError, ValueError) as e:
        return None, str(e)
    return out.decode().strip(), None


def run_cmd(argv, env=None, timeout=60):
    """Runs argv with no input, and returns (exit code, stdout, stderr). It
    raises subprocess.TimeoutExpired. The offline tests replace it."""
    r = subprocess.run(argv, env=env, stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=timeout)
    return r.returncode, r.stdout, r.stderr


def last_line(text):
    lines = (text or "").strip().splitlines()
    return lines[-1] if lines else "no output"


def find_session(c, envx, name):
    """Looks for the session called name on the host. Returns (ID, None) when
    the host lists it, (None, None) when the host answers and lists no such
    session, and (None, why) when the lookup could not tell: a timeout, a
    failed ls, an answer that is not JSON, or an error for the host."""
    try:
        code, out, err = run_cmd([c.cli, "ls", "--json", "--host", c.HOST], envx, 60)
    except subprocess.TimeoutExpired:
        return None, "ls took more than 60 s"
    if code != 0:
        return None, "ls exited %d: %s" % (code, last_line(err))
    try:
        rows = json.loads(out)
    except ValueError:
        return None, "ls printed no JSON"
    # Check the whole shape first: only an answer that has the expected
    # shape can say that a session is absent.
    if not isinstance(rows, list) or not all(isinstance(r, dict) for r in rows):
        return None, "ls printed JSON of another shape"
    rows = [r for r in rows if r.get("host") == c.HOST]
    if len(rows) != 1:
        return None, "ls has no row for %s" % c.HOST
    if rows[0].get("error"):
        return None, "ls could not reach %s: %s" % (c.HOST, rows[0]["error"])
    if "sessions" not in rows[0]:
        return None, "ls printed no sessions for %s" % c.HOST
    sessions = rows[0]["sessions"]
    if sessions is None:
        sessions = []  # Go writes an empty list as null
    if not isinstance(sessions, list) or not all(isinstance(s, dict) for s in sessions):
        return None, "ls printed sessions of another shape"
    if not all(isinstance(s.get(k), str) and s[k] for s in sessions for k in ("name", "id")):
        return None, "ls printed a session without a name or an ID"
    for row in sessions:
        if row["name"] == name:
            return row["id"], None
    return None, None


def delete_session(c, envx, name, sid):
    """Deletes the session called name, whose ID may be unknown. Returns None
    when the host then confirms that no such session is left, and otherwise
    why it could not confirm it."""
    if not sid:
        sid, why = find_session(c, envx, name)
        if why or not sid:
            return why
    try:
        code, _, err = run_cmd([c.cli, "kill", c.HOST + "/" + sid], envx, 60)
    except subprocess.TimeoutExpired:
        return "kill took more than 60 s"
    if code != 0:
        return "kill exited %d: %s" % (code, last_line(err))
    left, why = find_session(c, envx, name)
    if why:
        return why
    return "ls still lists it" if left else None


def forwarding(c):
    h = c.host_entry()
    if not h.get("ssh"):
        print("SKIP forwarding: %s is not reached over SSH" % c.HOST)
        return
    ver = ssh_version()
    if not ver or ver < (8, 2):
        print("SKIP forwarding: the check needs OpenSSH 8.2 or later on this computer (found %s)" % (ver,))
        return
    # A short folder in /tmp, so that the sockets' paths fit.
    tmp = tempfile.mkdtemp(prefix="e2e-fwd-", dir="/tmp")
    envx = dict(os.environ, **c.env)
    envx.update(XDG_CONFIG_HOME=os.path.join(tmp, "config"), XDG_STATE_HOME=os.path.join(tmp, "state"),
                XDG_CACHE_HOME=os.path.join(tmp, "cache"))
    name = "e2e-forward-" + secrets.token_hex(4)  # this run's own session
    agent, sid, tried = None, None, False
    try:
        sock = os.path.join(tmp, "agent.sock")
        agent = start_agent(sock)
        if not c.ok(agent, "forwarding: a throwaway ssh-agent with no keys starts"):
            return
        plain = write_configs(c, h, tmp, sock)
        out, tried = "", True
        try:
            r = subprocess.run([c.cli, "new", c.HOST, "--name", name, "--detach", "--", "/bin/sh", "-c", "sleep 600"],
                               env=envx, stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=120)
            out = (r.stdout + r.stderr).strip()
            m = re.search(r"Started [^/]+/(\w+)", r.stdout)
            sid = m.group(1) if m else None
        except subprocess.TimeoutExpired:
            out = "new took more than 120 s"
        if not sid:  # a start that timed out may still have made it
            sid, _ = find_session(c, envx, name)
        if not c.ok(sid, "forwarding: new starts %s with ForwardAgent in the client's SSH config (%s)" % (name, out)):
            return
        got, why = session_sock(c, sid)
        if not c.ok(why is None, "forwarding: the session's environment can be read on the host (%s)" % (why or "ok")):
            return
        # The same config without the client's hardening forwards the agent.
        # A host whose login sets SSH_AUTH_SOCK itself sets it without
        # forwarding too.
        forwarded, why1 = probe_sock(plain, h["ssh"], None)
        own, why2 = probe_sock(plain, h["ssh"], "no")
        if why1 or why2:
            print("SKIP forwarding: the plain ssh probe could not tell (%s)" % (why1 or why2))
            return
        if not forwarded or forwarded == own:
            print("SKIP forwarding: %s gives plain ssh no forwarded agent either (sshd's AllowAgentForwarding?), "
                  "so the check would prove nothing" % c.HOST)
            return
        c.ok(got in ("", own), "forwarding: the session has no forwarded SSH_AUTH_SOCK (it has %r, plain ssh got %r)" % (got, forwarded))
    finally:
        cleanup(c, h, tmp, envx, name, sid, agent, tried)


def cleanup(c, h, tmp, envx, name, sid, agent, tried):
    """Ends the throwaway shared connection and agent. When this run tried to
    start its session, it deletes it, and passes only when the host then
    confirms that the session is gone. It removes the throwaway folder only
    then. Otherwise it keeps the folder, fails, and says how to delete the
    session by hand."""
    why = delete_session(c, envx, name, sid) if tried else None
    if tried:
        c.ok(why is None, "forwarding: the test's session %s is deleted (%s)" % (name, why or "the host lists no such session"))
    wrapper = os.path.join(tmp, "config", c.NAME, "ssh", "config-" + c.HOST)
    if os.path.exists(wrapper):
        cm = os.path.join(tmp, "cache", c.NAME, "cm", c.CONTROL_PREFIX + "%C")
        try:
            run_cmd(["ssh", "-F", wrapper, "-o", "ControlPath=" + cm, "-O", "exit", h["ssh"]], None, 10)
        except subprocess.TimeoutExpired:
            pass
    if agent:
        agent.terminate()
        agent.wait(5)
    if why is None:
        shutil.rmtree(tmp, ignore_errors=True)
        return
    which = "%s/%s" % (c.HOST, sid) if sid else "%s/<its ID from: %s ls --host %s>" % (c.HOST, c.cli, c.HOST)
    print("  %s may still be on %s. Check, and delete it with: %s kill %s. The throwaway config is kept in %s."
          % (name, c.HOST, c.cli, which, tmp))
