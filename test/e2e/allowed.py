"""The commands, paths, flags and keys the e2e run may use, as data.

boundary.py enforces these before each owner-host call. allowed_scan reads
all harness source and rejects unlisted commands, unused entries, hidden
calls and disposable-only actions outside the authenticated guard. Each
entry states its ownership rule or why it needs a disposable host.
"""
import ast, itertools, os, re, conformance_allowed

HERE = os.path.dirname(os.path.abspath(__file__))

# Harness modules and recognised authenticated-disposable guards.
OWNER_MODULES = ["joined_checks.py", "run.py", "run_parts.py", "owned.py", "agent_setup.py"]
DISPOSABLE_MODULES = ["disposable.py", "disposable_checks.py", "disposable_setup.py", "agent_login.py", "guided_setup.py",
                      "host_commands.py", "host_git.py", "sandbox_hosts.py", "session_sockets.py", "session_verbs.py"]
BOUNDARY = ["boundary.py", "harness.py"]
SUPPORT = ["allowed.py", "allowed_scan.py", "fakes.py", "login_fakes.py", "conformance_evidence.py", "conformance_allowed.py"]
MODULES = OWNER_MODULES
GUARDS = {"DISPOSABLE", "c.DISPOSABLE", "R.DISPOSABLE", "state.get('disposable')"}

# A session's program runs after env -i, with only these variables, so the
# host's BASH_ENV, ENV and exported functions never reach it, and bash
# reads no rc file (owned.program).
PROGRAM_ENV = '/usr/bin/env -i HOME="$HOME" PATH="$PATH" TERM="$TERM" LANG=C.UTF-8 INPUTRC=/dev/null'
SHELLS = {"/bin/sh": "/bin/sh", "/bin/bash": "/bin/bash --norc --noprofile"}
HOLDER_VAR = re.compile(r"[A-Z]+_(?:HOOK_SOCK|SOCK|SESSION)")


def program(shell, script, wait=False, keep=()):
    """The argv of a session that runs script with shell, after env -i. The
    outer /bin/sh runs only the absolute /usr/bin/env, so nothing of the
    host's environment runs in it. It execs env, unless wait: then it stays
    the session's first process, with the session's own environment, such
    as a secret, while script runs without it, and no command line holds
    a variable's value. keep forwards only holder socket/session vars."""
    for v in keep:
        if not HOLDER_VAR.fullmatch(v):
            raise ValueError("program() forwards only holder socket/session vars: %r" % (v,))
    extra = "".join(' ${%s+"%s=$%s"}' % (v, v, v) for v in keep)
    return ["/bin/sh", "-c", ("" if wait else "exec ") + PROGRAM_ENV + extra + " " + SHELLS[shell] + ' -c "$1"', "sh", script]


def isolated(cmd, allow_holder=False):
    """Whether cmd, a session's program, is one that program() made."""
    if len(cmd) != 5 or list(cmd[:2]) != ["/bin/sh", "-c"] or cmd[3] != "sh":
        return False
    shells = "|".join(re.escape(v) for v in SHELLS.values())
    extra = r'(?: \$\{([A-Z]+_(?:HOOK_SOCK|SOCK|SESSION))\+"\1=\$\1"\})*' if allow_holder else ""
    return re.fullmatch(r'(?:exec )?' + re.escape(PROGRAM_ENV) + extra + r' (?:%s) -c "\$1"' % shells, cmd[2]) is not None


# git on the laptop and on the host: no hook, signing or fsmonitor of the
# user's or the owner's, and the run's own name. owned.GIT adds the
# variables that turn off the global and the system config.
GIT_FLAGS = ["-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false",
             "-c", "core.fsmonitor=false", "-c", "user.name=e2e", "-c", "user.email=e2e@example.com"]

START, READ, ATTACH, DELETE, OWN_FOLDER = (
    "1: starts a session of its own, with the run's token in its name",
    "2: only reads",
    "3: attaches, and sends nothing but the detach key",
    "4: deletes a session of its own by ID, through the helper's rm",
    "5: makes or removes files only in the run's own folder")
LOCK = "the run's own lock, ~/.e2e-lock, with its token: one run at a time on a host"
HOOKS = " (exception E: the host's own setup and resume hooks run in it, as in every session there)"
STATUS = " (exception F: host doctor runs each agent's --version and status command, which may touch the agent's own files)"
LOGIN = " (exception H: the helper runs the user's login shell once, for the environment, as at every start and doctor there)"

CLIENT = {
    ("new", "--name", "--detach"): START + ": at a terminal, where new prints its own Started line" + HOOKS + LOGIN,
    ("new", "--name", "--detach", "--json"): START + ": owned by the new ID in its JSON" + HOOKS + LOGIN,
    ("new", "--name", "--json", "--"): START + ": with no --detach it is refused, and the run's name finds any session that it would make",
    ("attach", "--id", "--"): ATTACH,
    ("ls", "--json", "--host"): READ + ": the host's sessions",
    ("version", "--json"): READ + ": the client's own version, on the laptop: it reaches no host",
    ("host", "doctor"): READ + STATUS + LOGIN,
    ("host", "doctor", "--json"): READ + STATUS + LOGIN,
    ("host", "doctor", "--fix"): READ + ": the check answers no to every fix" + STATUS + LOGIN,
    ("host", "alerts", "--json"): READ + ": before the first start too, which stops a run on a host whose alerts are on",
    ("pull", "--id", "--"): READ + ": the branch of the run's own repository comes to a local folder, with host git config off",
    ("pull", "--id", "--into", "mine", "--"): READ + ": refused, as the branch is checked out on the laptop",
}

START_FLAGS = {
    "--": START + ": the program that the session runs, after env -i (owned.program)",
    "--dir": START + ": the session starts in a folder of the run's own",
    "--secret": START + ": the secret goes to the session's environment from the throwaway config",
    "--setup": START + ": the setup script of the run's own repository runs in the session",
}

SENDS = {
    b"\x1c": ATTACH + ": the detach key, the default one, from the run's own config",
    b"y\r": START + ": the answer to the client's own question about the setup script of the run's repository, at the local terminal",
    b"n\r": READ + ": the no to each fix of host doctor --fix, at the local terminal",
}

# What runs only on a disposable host, one that the run made (run.py's
# DISPOSABLE): inside an `if` of GUARDS, or in a module of DISPOSABLE_MODULES.
# check() fails when one of these runs anywhere else.
ONLY = "only on a disposable host, which `host create` made for the run: "
DISPOSABLE_ONLY = {
    "client": {
        ("kill",): ONLY + "deletes a session of its own by its address, with the client's kill",
        ("--debug", "--debug-file", "attach", "--id", "--"): ONLY + "the bridge's debug writes to the helper's shared debug folder, and prunes it",
        ("debug", "start", "--for", "5m"): ONLY + "debug start on a session of its own",
        ("debug", "save"): ONLY + "debug save on a session of its own",
        ("debug", "profile", "cpu", "--for", "2s"): ONLY + "debug profile on a session of its own",
        ("debug", "stop"): ONLY + "debug stop on a session of its own",
        ("resume", "--memory-max", "300M", "--detach"): ONLY + "runs the agent of a session of its own again",
        ("resume", "--detach"): ONLY + "resumes an ended session, and is refused when the agent runs",
        ("host", "alerts", "set", "--command", "--events", "needs_input,failed", "--delay", "2s"): ONLY + "sets the host's alerts",
        ("host", "alerts", "test"): ONLY + "sends a test alert",
        ("host", "alerts", "log", "-n", "10"): ONLY + "reads the alerts log",
        ("host", "alerts"): ONLY + "reads whether the host's alerts are on",
        ("host", "alerts", "off"): ONLY + "turns off the host's alerts",
        ("agents", "install", "claude", "--host"): ONLY + "an install, also of an agent that is there",
        ("agents", "login", "claude", "--json"): ONLY + "a login session, which has no name of the run's",
        ("setup",): ONLY + "the guided setup, refused with no terminal, or stopped with Ctrl+C",
        ("setup", "--host", "--agents", "no-such-agent", "--yes"): ONLY + "refused, as the agent is unknown",
        ("setup", "--host", "--agents", "shell", "--yes", "--no-login"): ONLY + "turns on linger, the restore unit and terminfo",
        ('host', 'show'): ONLY + 'host_commands.py: exercises this exact client command on its owned disposable host or fresh session ID',
        ('host', 'show', '--json'): ONLY + 'host_commands.py: exercises this exact client command on its owned disposable host or fresh session ID',
        ('host', 'upgrade'): ONLY + 'host_commands.py: exercises this exact client command on its owned disposable host or fresh session ID',
        ('host', 'upgrade', '--json'): ONLY + 'host_commands.py: exercises this exact client command on its owned disposable host or fresh session ID',
        ('host', 'set', 'idl=2h'): ONLY + 'host_commands.py: exercises this exact client command on its owned disposable host or fresh session ID',
        ('host', 'set'): ONLY + 'host_commands.py: exercises this exact client command on its owned disposable host or fresh session ID',
        ('host', 'rm', '--destroy', '--no-input'): ONLY + 'host_commands.py: exercises this exact client command on its owned disposable host or fresh session ID',
        ('host', 'rm'): ONLY + 'host_commands.py: exercises this exact client command on its owned disposable host or fresh session ID',
        ('host', 'set', 'idle=2h'): ONLY + 'host_commands.py: exercises this exact client command on its owned disposable host or fresh session ID',
        ('host', 'test', '--no-input'): ONLY + 'sandbox_hosts.py: exercises this exact client command on its owned disposable host or fresh session ID',
        ('show',): ONLY + 'session_verbs.py: exercises this exact client command on its owned disposable host or fresh session ID',
        ('show', '--json'): ONLY + 'session_verbs.py: exercises this exact client command on its owned disposable host or fresh session ID',
        ('attach', '-'): ONLY + 'session_verbs.py: exercises this exact client command on its owned disposable host or fresh session ID',
        ('rename', 'abcd2345'): ONLY + 'session_verbs.py: exercises this exact client command on its owned disposable host or fresh session ID',
        ('attach', '--no-input', '--id', '--'): ONLY + 'session_verbs.py: exercises this exact client command on its owned disposable host or fresh session ID',
        ('rm',): ONLY + 'session_verbs.py: exercises this exact client command on its owned disposable host or fresh session ID',
        ('rm', '-y'): ONLY + 'session_verbs.py: exercises this exact client command on its owned disposable host or fresh session ID',
    },
    "flags": {
        "--worktree": ONLY + "the client makes a worktree outside the run's folder",
        "--memory-max": ONLY + "a memory cap that the check makes a process go over: an oom event, which alerts",
    },
    "sends": {
        b"\x1c\x1c": ONLY + "the detach key twice, which the client passes to the session's program",
        b"echo MARK-$((40+2))\r": ONLY + "types a command into a shell of its own",
        b"for i in 1 2 3; do echo LINE-$i; done\r": ONLY + "types a command into a shell of its own",
        b"cat -v\r": ONLY + "types a command into a shell of its own",
        b"\x04": ONLY + "ends cat, or the shell's input",
        b"q": ONLY + "ends the host's key tester",
        "<k>": ONLY + "kitty keyboard sequences to the host's key tester",
        b"bind 'set enable-bracketed-paste on' 2>/dev/null; printf '\\033[?2004h'; read -r P; cat $P\r": ONLY + "types a command that reads a pasted path",
        "<b'\\x1b[200~' + local.encode() + b'\\x1b[201~\\r'>": ONLY + "a dropped file, which the client uploads to the host",
        b"\x16": ONLY + "Ctrl+V: the client uploads the test image to the host",
        "<b'y\\r' if yes else b'n\\r'>": ONLY + "a yes only to the fix of the check's own stale socket",
        b"\x03": ONLY + "Ctrl+C at the guided setup's first question, at the local terminal",
        b'\r': ONLY + 'session_verbs.py: sends this exact check input only to an owned disposable session or an isolated local confirmation',
        '<probe_line(c, b)>': ONLY + 'session_sockets.py: sends this exact check input only to an owned disposable session or an isolated local confirmation',
        '<line.encode()>': ONLY + 'session_sockets.py: sends this exact check input only to an owned disposable session or an isolated local confirmation',
        '<(\'env %s="${%s%%/*}/%s.sock" %s hook notification \\\'{"message":"e2e-cross-hook"}\\\'\\r\' % (hook, hook, b, c.HELPER)).encode()>': ONLY + 'session_sockets.py: sends this exact check input only to an owned disposable session or an isolated local confirmation',
        '<(\'%s hook notification \\\'{"message":"e2e-own-hook"}\\\'; echo HOOKS-$((40+2))\\r\' % c.HELPER).encode()>': ONLY + 'session_sockets.py: sends this exact check input only to an owned disposable session or an isolated local confirmation',
    },
    "host": {
        '"cat > %s/notify.bin" % owned.run_dir()': ONLY + "the OSC 777 desktop notification bytes, which raise a needs_input alert event",
        '\'if [ -e %s ]; then grep -qF -- %s %s; s=$?; if [ $s = 0 ]; then rm -f %s || exit 1; elif [ $s != 1 ]; then exit 2; fi; fi; if [ -e %s ]; then grep -qF -- %s %s; [ $? = 1 ]; fi\' % (f, tag, f, f, f, tag, f)':
            ONLY + "removes the alerts file that the run made, while it holds the token",
        "'[ ! -e %s ]' % path": ONLY + "reads whether the helper's alerts file is there",
        "'rm -rf -- %s' % path": ONLY + "removes a path that a check made for the run, with its token",
        "'rm -f -- %s/%s' % (d['folder'], n)": ONLY + "removes a file that came in the debug folder during the debug check",
        "'if [ -d %s ]; then ls -A %s; fi; echo e2e-done' % (folder, folder)": ONLY + "reads the debug folder",
        "'cat ~/.local/state/%s/sessions/%s/debug.jsonl' % (c.NAME, sid)": ONLY + "reads the debug file of a session of its own",
        "'stat -c %a ' + path": ONLY + "reads the alerts file's mode",
        "'grep -qF -- %s %s' % (disposable.alerts_tag(), path)": ONLY + "reads the alerts file",
        '\'cat "%s" 2>/dev/null; true\' % out_file': ONLY + "reads the alerts that came, in the run's folder",
        "VT_KILL % (sid, (R.NAME + '-vt')[:15])": ONLY + "kills the shadow terminal of a session of its own",
        "'pgrep -u \"$(id -u)\" -f \\'sleep 91[3]\\''": ONLY + "reads whether the resumed session's process runs",
        "'systemctl --user show %s -p MemoryMax --value' % shlex.quote(unit)": ONLY + "reads the resumed unit's cap",
        "'systemctl --user show %s.slice -p MemoryMax --value' % R.NAME": ONLY + "reads the sessions' slice limit in the memory check",
        "'sha256sum -- ' + shlex.quote(m.group(1).decode())": ONLY + "reads the pasted image",
        "'test -e ' + shlex.quote(sock)": ONLY + "reads whether the check's socket is gone",
        "'test -S ' + q": ONLY + "reads the check's socket",
        "\"python3 -c 'import socket,sys; socket.socket(socket.AF_UNIX).bind(sys.argv[1])' %s\" % q": ONLY + "makes a stale socket in the helper's runtime folder",
        "'[ ! -e %s ]' % q": ONLY + "reads whether the socket's path is free",
        '(SOCKETS + \'; mkdir -p "$d" && printf %%s "$d"\') % (c.NAME, c.NAME)': ONLY + "the helper's runtime folder for sockets, made when it is not there",
        '(SOCKETS + \'; python3 - "$d"\') % (c.NAME, c.NAME)': ONLY + "reads the stale sockets",
        "'command -v python3 >/dev/null'": ONLY + "reads whether the host has python3",
        "'[ ! -e %s ]' % wt": ONLY + "reads whether the worktree's folder is free",
        "'rm -f %s/* && cd %s && %s worktree add -q -b e2e-plain %s/plain && %s -C %s/plain status --porcelain >/dev/null' % (marks, repo, base_git(), base, base_git(), base)": ONLY + 'host_git.py: uses this exact fixture command on the disposable host, with token-owned paths or an owned session ID',
        '\'expected="%s/%s/worktrees/%s/%s-%s"; [ %s = "$expected" ] && [ ! -L "$expected" ] && [ ! -L "$expected/README" ] && [ -f "$expected/README" ] && [ "$(cd -P "$expected" && pwd)" = "$expected" ] && touch -m -d "@$(( $(date +%%s) + 5 ))" "$expected/README"\' % (owned.xdg_sh(\'XDG_DATA_HOME\', \'.local/share\'), c.NAME, os.path.basename(repo), slug, sid, q(cwd))': ONLY + 'host_git.py: uses this exact fixture command on the disposable host, with token-owned paths or an owned session ID',
        "'systemctl --user is-system-running'": ONLY + 'host_git.py: uses this exact fixture command on the disposable host, with token-owned paths or an owned session ID',
        "'mkdir %s' % base": ONLY + 'host_git.py: uses this exact fixture command on the disposable host, with token-owned paths or an owned session ID',
        "'git --version'": ONLY + 'host_git.py: uses this exact fixture command on the disposable host, with token-owned paths or an owned session ID',
        "'ls %s' % marks": ONLY + 'host_git.py: uses this exact fixture command on the disposable host, with token-owned paths or an owned session ID',
        "'cat %s/filter 2>/dev/null; true' % marks": ONLY + 'host_git.py: uses this exact fixture command on the disposable host, with token-owned paths or an owned session ID',
        '\'env=$(tr \\\'\\\\0\\\' \\\'\\\\n\\\' < /proc/%d/environ) || exit 3; printf \\\'%%s\\\\n\\\' "$env" | grep -q \\\'^PATH=\\\' || exit 4; printf \\\'%%s\\\\n\\\' "$env" | sed -n \\\'s/^SSH_AUTH_SOCK=//p\\\'\' % pid': ONLY + 'sandbox_hosts.py: uses this exact fixture command on the disposable host, with token-owned paths or an owned session ID',
        "'rm -f ' + marker": ONLY + 'session_verbs.py: uses this exact fixture command on the disposable host, with token-owned paths or an owned session ID',
        '\'mkdir -p %s && %s init --template= -q -b main %s && cd %s && mkdir -p .git/hooks && echo app > README && echo \\\'README filter=e2emark\\\' > .gitattributes && %s add README .gitattributes && %s commit -q -m base && %s config filter.e2emark.clean %s && printf \\\'#!/bin/sh\\\\ntouch %s/fsmonitor\\\\nexit 1\\\\n\\\' > .git/fsmonitor.sh && chmod +x .git/fsmonitor.sh && %s config core.fsmonitor "$PWD/.git/fsmonitor.sh" && %s config hook.e2e.command %s && printf \\\'#!/bin/sh\\\\ntouch %s/post-checkout\\\\n\\\' > .git/hooks/post-checkout && chmod +x .git/hooks/post-checkout && printf \\\'#!/bin/sh\\\\ntouch %s/post-index-change\\\\n\\\' > .git/hooks/post-index-change && chmod +x .git/hooks/post-index-change && printf \\\'#!/bin/sh\\\\ntouch %s/reference-transaction\\\\n\\\' > .git/hooks/reference-transaction && chmod +x .git/hooks/reference-transaction && %s config --add hook.e2e.event post-checkout && %s config --add hook.e2e.event post-index-change && %s config --add hook.e2e.event reference-transaction\' % (marks, git, repo, repo, git, git, git, q(\'cat /proc/self/cgroup >> %s/filter; cat\' % marks), marks, git, git, q(\'touch %s/config-hook\' % marks), marks, marks, marks, git, git, git)': ONLY + "host_git.py: creates exactly the token-owned repository, hooks, filter and markers used by the isolation control",
    },
}

HOST = {
    '"set -e; cd %s; printf \'%s\' > .%s/setup; chmod +x .%s/setup; %s add -A; %s commit -qm change" % (repo, changed, c.NAME, c.NAME, owned.GIT, owned.GIT)': OWN_FOLDER + ": changes only the run's own trusted script, to check the hash and renewed question",
    '"mkdir %s && cat > %s/.e2e-nonce && printf %%s %s > %s/.e2e-run" % (d, d, RUN, d)':
        OWN_FOLDER + ": makes it, with the nonce from stdin and the token in it",
    '\'[ "$(cat %s/.e2e-nonce 2>/dev/null)" = "$(cat)" ]\' % d': READ + ": whether the folder holds the run's nonce, after a lost answer",
    '\'n=$(cat); [ -n "$n" ] || exit 1; if [ "$(cat %s 2>/dev/null)" = "$n" ]; then for x in %s/* %s/.[!.]* %s/..?*; do [ "$x" = %s ] || { [ ! -e "$x" ] && [ ! -L "$x" ]; } || rm -rf -- "$x" || exit 1; done; rm -f %s && rmdir %s || exit 1; fi; [ ! -e %s ] || { [ ! -e %s ] && [ ! -L %s ]; } || { [ -r %s ] && [ "$(cat %s)" != "$n" ]; }\' % (f, d, d, d, f, f, d, d, f, f, f, f)':
        OWN_FOLDER + ": removes it, only while it holds the nonce",
    '\'%s rm %s || [ ! -e "%s/%s/sessions/%s" ]\' % (c.HELPER, shlex.quote(sid), xdg_sh(\'XDG_STATE_HOME\', \'.local/state\'), c.NAME, sid)':
        DELETE + ", the helper of the version that the run checked; a session that is already gone counts as deleted",
    '"mkdir %s && printf %%s %s > %s/run" % (LOCK, RUN, LOCK)': LOCK + ": takes it",
    '"cat %s/run 2>/dev/null; true" % LOCK': READ + ": the token of the lock",
    '\'[ "$(cat %s/run 2>/dev/null)" = %s ] && %s\' % (LOCK, RUN, UNLOCK)': LOCK + ": gives it up, only while it holds the token",
    '"if [ -e %s ]; then printf \'lock:\'; cat %s/run 2>/dev/null; else printf none; fi" % (LOCK, LOCK)': READ + ": the lock's token",
    '"cat > %s/seq.bin" % owned.run_dir()': OWN_FOLDER + ": the bytes that a session prints",
    '"command -v git >/dev/null"': READ,
    '\'test -x "$HOME/.local/share/%s/bin/%s/%s-host"\' % (c.NAME, ver, c.NAME)': READ + ": whether this build's helper is on the host, before any client call",
    '\'ls -1A "$HOME/.local/share/%s/bin" 2>/dev/null; printf \\\'current->\\\'; readlink "$HOME/.local/share/%s/bin/current" 2>/dev/null; echo; echo e2e-done\' % (c.NAME, c.NAME)':
        READ + ": the helper versions and current link, at the start and at the end",
    "allowed.SLICE % c.NAME": READ + ": the sessions' slice limit, before the first start and at the end",
    '\'for h in setup resume; do [ -e "%s/%s/hooks/$h" ] && echo "$h"; done; echo e2e-done\' % (owned.xdg_sh(\'XDG_CONFIG_HOME\', \'.config\'), c.NAME)':
        READ + ": which of the host's own hooks are there, for the start notice of exception E",
    '"mkdir %s && %s init --template= -q -b main %s && cd %s && %s commit -q --allow-empty -m base && %s switch -q -c e2e-topic && %s commit -q --allow-empty -m \'from the session\'" % (repo, owned.GIT, repo, repo, owned.GIT, owned.GIT, owned.GIT)':
        OWN_FOLDER + ": the pull check's repository, with git of no user's config",
    '"set -e; mkdir %s; mkdir %s/.%s; cd %s; %s init --template= -q -b main; printf \'%s\' > .%s/setup; chmod +x .%s/setup; %s add -A; %s commit -qm init" % (repo, repo, c.NAME, repo, owned.GIT, script, c.NAME, c.NAME, owned.GIT, owned.GIT)':
        OWN_FOLDER + ": the setup check's repository, with git of no user's config",
    '\'echo 7 > "%s"\' % exit_file': OWN_FOLDER + ": the exit code of the setup script",
    '\'rm -f "%s"\' % exit_file': OWN_FOLDER,
    '"cat ~/.local/state/%s/sessions/%s/manifest.json" % (c.NAME, sid)': READ + ": the manifest of a session of the run's own",
    '"exec 3<&0; tr \'\\\\0\' \'\\\\n\' < /proc/%d/environ | grep -qxF -f /dev/fd/3" % pid': READ + ": a session's environment",
    '"grep -rqF -f - ~/.local/state/%s/" % c.NAME': READ,
    '"exec 3<&0; " + CMDLINES + " | grep -qF -f /dev/fd/3"': READ + ": the host's command lines",
    '"exec 3<&0; systemctl --user show %s | grep -qF -f /dev/fd/3" % shlex.quote(unit)': READ,
}

SLICE = ('f=/sys/fs/cgroup/user.slice/user-$(id -u).slice/user@$(id -u).service/cgroup.controllers; '
         'if [ -r "$f" ] && grep -qw memory "$f"; then systemctl --user show %s.slice -p MemoryMax --value; else echo none; fi')

ID = r"[A-Za-z0-9_-]{1,64}"
VERSION = r"[A-Za-z0-9._-]+"
PART = r"/(?!\.{1,2}(?:/|$))[A-Za-z0-9._+-]+"


def holes(c):
    """The value of each hole of a host template, as a regex: the run's own
    paths, token and lock, the product's name, an ID, and so on."""
    import agent_setup, owned
    q = re.escape
    under = r"(?:~|\$HOME)/\.e2e-run-%s(?:%s)*" % (owned.RUN, PART)
    return {"d": q(owned.run_dir()), "f": q(owned.run_dir() + "/.e2e-nonce"),
            "RUN": q(owned.RUN), "LOCK": q(owned.LOCK), "UNLOCK": q(owned.UNLOCK), "owned.run_dir()": q(owned.run_dir()),
            "owned.GIT": q(owned.GIT), "c.NAME": q(c.NAME), "NAME": q(c.NAME), "c.HELPER": q(c.HELPER) if c.HELPER else "(?!)",
            "sid": ID, "shlex.quote(sid)": ID, "ver": VERSION, "pid": "[0-9]+", "shlex.quote(unit)": r"[A-Za-z0-9@._:-]+",
            "repo": under, "exit_file": under, "script": "[^']*", "changed": "[^']*", "CMDLINES": q(agent_setup.CMDLINES), "SLICE": q(SLICE)}


def template(node, values):
    """The regex of the commands that node, a host command's source, makes."""
    if isinstance(node, ast.Constant) and isinstance(node.value, str):
        return re.escape(node.value)
    if isinstance(node, ast.BinOp) and isinstance(node.op, ast.Add):
        return template(node.left, values) + template(node.right, values)
    if isinstance(node, ast.BinOp) and isinstance(node.op, ast.Mod):
        fmt = SLICE if ast.unparse(node.left) in ("SLICE", "allowed.SLICE") else node.left.value
        args = node.right.elts if isinstance(node.right, ast.Tuple) else [node.right]
        out, i = "", 0
        for part in re.split(r"(%%|%s|%d)", fmt):
            if part in ("%s", "%d"):
                out += "(?:%s)" % template(args[i], values)
                i += 1
            else:
                out += re.escape("%" if part == "%%" else part)
        return out
    if isinstance(node, ast.Call) and ast.unparse(node.func) in ("xdg_sh", "owned.xdg_sh"):
        import owned
        return re.escape(owned.xdg_sh(*[a.value for a in node.args]))
    return values[ast.unparse(node)]


def host_refusal(cmd, c):
    """Why cmd may not run on a host that the run did not make, or None: it
    must be one of HOST's templates, each hole filled with a value of its
    class (holes)."""
    values = holes(c)
    for src in HOST:
        if re.fullmatch(template(ast.parse(src, mode="eval").body, values), cmd, re.S):
            return None
    return "the host command %r is not in allowed.py's HOST table, with values of the run's own" % cmd[:200]


VALUE_FLAGS = {"--name", "--host", "--into", "--dir", "--secret"}


def client_target_ok(lead, pos, vals, c):
    """Whether positional arguments and value-bearing flags stay within the
    run's own host, owned sessions and run folder on a non-disposable host."""
    if c is None:
        return True
    import boundary, owned
    if lead in (["attach"], ["pull"]):
        prefix = c.HOST + "/"
        sid = pos[0][len(prefix):] if pos and pos[0].startswith(prefix) else ""
        return bool(owned.ID.fullmatch(sid) and sid in boundary.OWNED)
    if lead == ["new"]:
        if pos != [c.HOST] or not re.fullmatch(r"e2e-[A-Za-z0-9_-]+-" + re.escape(owned.RUN), vals.get("--name", "")):
            return False
        if "--dir" in vals and not re.fullmatch(r"(?:~|\$HOME)/\.e2e-run-%s(?:%s)*" % (re.escape(owned.RUN), PART), vals["--dir"]):
            return False
        return True
    if lead == ["ls"]:
        return vals.get("--host") == c.HOST
    if lead in (["host", "doctor"], ["host", "alerts"]):
        return pos == [c.HOST]
    return True


def client_allowed(args, c=None):
    """Whether args, the words after the client, are an entry of CLIENT:
    its subcommand words first, every flag of the entry present, no unallowed
    flag or extra positional word, and for `new`, an isolated program after --."""
    has_sep = "--" in args
    head = args[:args.index("--")] if has_sep else args
    for key in CLIENT:
        lead = list(itertools.takewhile(lambda w: not w.startswith("-"), key))
        if args[:len(lead)] != lead:
            continue
        identity_target = lead in (["attach"], ["pull"])
        if lead == ["new"]:
            if not has_sep or not isolated(args[args.index("--") + 1:]):
                continue
            head = args[:args.index("--")]
        elif identity_target:
            if not has_sep:
                continue
            head = args
        elif has_sep:
            continue
        else:
            head = args
        req = {w for w in key if w.startswith("-") and w != "--"}
        allowed_flags = req | (set(START_FLAGS) - {"--"} if lead == ["new"] else set())
        seen, vals, pos, i, rest, bad = set(), {}, [], 0, head[len(lead):], False
        while i < len(rest):
            w = rest[i]
            if w == "--" and identity_target:
                pos.extend(rest[i + 1:])
                break
            if w.startswith("-"):
                if w not in allowed_flags or (w in VALUE_FLAGS and w in vals):
                    bad = True
                    break
                seen.add(w)
                if w in VALUE_FLAGS:
                    if i + 1 >= len(rest) or (w == "--into" and "mine" in key and rest[i + 1] != "mine"):
                        bad = True
                        break
                    vals[w] = rest[i + 1]
                    i += 2
                    continue
            else:
                pos.append(w)
            i += 1
        want_pos = 0 if lead in (["version"], ["ls"]) else 1
        if not bad and req <= seen and len(pos) == want_pos and client_target_ok(lead, pos, vals, c):
            return True
    return False


# Exact laptop pull operations; later configuration overrides are refused.
GIT_INIT = (("init", "--template=", "-q"), ("init", "--template=", "-q", "-b", "mine"))
GIT_LOG = tuple(("log", "--format=%s") + count + (ref,)
                for count in ((), ("-1",)) for ref in ("e2e-topic", "mine"))


def rooted_path(path, c, cwd=None, strict=False):
    """A canonical path within this laptop root, or None. Symlinks count."""
    try:
        root = os.path.realpath(os.fspath(c.root.path))
        actual = os.path.realpath(os.path.join(cwd or root, os.fspath(path)))
        if os.path.isdir(root) and os.path.commonpath((root, actual)) == root and (not strict or actual != root):
            return actual
    except (AttributeError, OSError, TypeError, ValueError):
        pass
    return None


def owned_git_store(path, c, init=False):
    """A normal repository inside the root. Linked worktrees stay out."""
    root = rooted_path(c.root.path, c)
    here = path
    while here and rooted_path(here, c):
        git = os.path.join(here, ".git")
        if os.path.lexists(git):
            return os.path.isdir(git) and rooted_path(git, c) is not None
        if init:
            return True
        if here == root:
            break
        here = os.path.dirname(here)
    return False


def laptop_git_refusal(args, c, cwd):
    """Only init and log on the run's own physical repository may run."""
    base = rooted_path(cwd if cwd is not None else c.root.path, c)
    if base is None or not os.path.isdir(base):
        return "laptop git needs a working directory inside the run's root"
    if tuple(args[:-1]) in GIT_INIT:
        target = rooted_path(args[-1], c, base, strict=True)
        if target is not None and owned_git_store(target, c, init=True):
            return None
        return "laptop git init needs a repository inside the run's root"
    if tuple(args) in GIT_LOG and cwd is not None and owned_git_store(base, c):
        return None
    return "laptop git permits only the run's exact init and log commands"


FORWARD_OPTIONS = ["-o", "BatchMode=yes", "-o", "ControlMaster=no", "-o", "ControlPath=none",
                   "-o", "ForwardX11=no", "-o", "PermitLocalCommand=no", "-o", "GSSAPIDelegateCredentials=no",
                   "-o", "ClearAllForwardings=yes", "-T"]
FORWARD_PROBE = "exec \"$SHELL\" -l -i -c 'printf \"__E2E_SOCK__%s__E2E_SOCK__\" \"${SSH_AUTH_SOCK:-}\"'"
KEY_TYPES = ("ed25519", "ecdsa", "rsa")


def fixture_refusal(argv, c):
    """The exact empty-agent, wrong-key and plain-SSH control operations."""
    import boundary
    if c is not boundary.ctx() or not c.DISPOSABLE:
        return "local SSH fixtures require the canonical authenticated disposable host"
    if not all(isinstance(word, str) and "\0" not in word for word in argv):
        return "local SSH fixture arguments must be plain strings"
    if argv == ["ssh", "-V"]:
        return None
    if len(argv) == 4 and argv[:3] == ["ssh-agent", "-D", "-a"]:
        path = rooted_path(argv[3], c, strict=True)
        if path and os.path.basename(path) == "agent.sock" and not os.path.lexists(path):
            return None
    if len(argv) == 10 and argv[:3] == ["ssh-keygen", "-q", "-t"] and argv[3] in KEY_TYPES \
            and argv[4:9] == ["-N", "", "-C", "", "-f"]:
        path = rooted_path(argv[9], c, strict=True)
        if path and os.path.basename(path) == "wrong_" + argv[3] and not os.path.lexists(path) and not os.path.lexists(path + ".pub"):
            return None
    if len(argv) >= 3 and argv[:2] == ["ssh", "-F"]:
        config = rooted_path(argv[2], c, strict=True)
        target = c.root.entry.get("ssh")
        tails = (FORWARD_OPTIONS + ["--", target, FORWARD_PROBE],
                 FORWARD_OPTIONS + ["-o", "ForwardAgent=no", "--", target, FORWARD_PROBE])
        if config and os.path.basename(config) == "plain.conf" and os.path.isfile(config) and argv[3:] in tails:
            return None
    return "the local SSH fixture is not an exact operation on the run's own files"


def laptop_refusal(argv, c, cwd=None):
    """Why argv may not run on the laptop, or None: the client with an entry
    of CLIENT, or isolated git on its own repository. cwd is the actual
    subprocess directory, with the root used for an init with none."""
    if not argv:
        return "an empty command"
    if not c.DISPOSABLE and cwd is not None and rooted_path(cwd, c) is None:
        return "a laptop working directory must stay inside the run's root"
    if argv[0] == c.cli:
        return None if c.DISPOSABLE or client_allowed(argv[1:], c) else \
            "the client command %r is not in allowed.py's CLIENT table, with targets of the run's own" % (argv[1:8],)
    if argv[0] == "git" and argv[1:1 + len(GIT_FLAGS)] == GIT_FLAGS:
        if not all(isinstance(w, str) and "\0" not in w for w in argv):
            return "laptop git arguments must be strings"
        return None if c.DISPOSABLE else laptop_git_refusal(argv[1 + len(GIT_FLAGS):], c, cwd)
    if argv[0] in ("ssh", "ssh-agent", "ssh-keygen"):
        return fixture_refusal(argv, c)
    return "%r is not a program that the run starts on the laptop" % argv[0]


def keys_for(argv, c):
    """The keys that a client at a terminal may get on a host that the run
    did not make, or None for any: an attach gets the detach key, new the y
    to its own question, and host doctor --fix a no."""
    if c.DISPOSABLE:
        return None
    args = argv[1:]
    if args[:1] == ["attach"]:
        return {b"\x1c"}
    if args[:1] == ["new"]:
        return {b"y\r"}
    if args[:2] == ["host", "doctor"] and "--fix" in args:
        return {b"n\r"}
    return set()


conformance_allowed.extend(DISPOSABLE_ONLY, ONLY, DISPOSABLE_MODULES)
from allowed_scan import (  # noqa: E402, F401 - re-exported for callers
    calls,
    check,
    client_words,
    guarded,
    host_key,
    parsed,
    scan,
    shape_problems,
    table_problems,
    tables,
    walk,
)
