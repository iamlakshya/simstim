"""Runs the shell commands in the tutorials and how-to guides, so the docs
cannot drift from the tool.

Each ```sh block in docs/tutorials/ and docs/how-to/ is tested. A comment
just before a block says how:

    <!-- test: skip (why) -->   not run: it installs or removes something
    <!-- test: help (why) -->   each command is checked with --help instead:
                                 it opens a session, or needs something we lack

A block with no comment is run for real when DOCS_HOST names a test host
(and its SSH target in DOCS_HOST_TARGET, its SSH config in
DOCS_HOST_SSH_CONFIG), in a throwaway config folder. Without DOCS_HOST,
every block is checked with --help: the commands, subcommands and flags must
exist.

    python3 scripts/test-docs-commands.py bin/<name>
"""
import os, re, shlex, subprocess, sys, tempfile

root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
with open(os.path.join(root, "internal/brand/brand.go")) as brand_file:
    name = re.search(r'^const Name = "(.*)"', brand_file.read(), re.M).group(1)
binary = os.path.abspath(sys.argv[1] if len(sys.argv) > 1 else os.path.join(root, "bin", name))
host = os.environ.get("DOCS_HOST", "")
target = os.environ.get("DOCS_HOST_TARGET", host)
sshcfg = os.environ.get("DOCS_HOST_SSH_CONFIG", "")

block_re = re.compile(r"(?:<!-- test: (\w+)[^>]*-->\n)?```sh\n(.*?)```", re.S)

def commands(text):
    """Joins continued lines and drops comments and blank lines."""
    out, cur = [], ""
    for line in text.splitlines():
        line = line.rstrip()
        if line.endswith("\\"):
            cur += line[:-1] + " "
            continue
        cur += line
        cmd = re.sub(r"\s+#.*$", "", cur).strip()
        cur = ""
        if cmd and not cmd.startswith("#"):
            out.append(cmd)
    return out

def ours(cmd):
    return cmd.split()[0] == name

def run(cmd, env):
    args = shlex.split(cmd)
    args[0] = binary
    if host:
        args = [host if a == "my-server" else a.replace("my-server/", host + "/") for a in args]
        if args[1:3] == ["host", "add"] and sshcfg:
            args = args[:4] + [target, "--ssh-config", sshcfg] + args[4:]
    return subprocess.run(args, env=env, capture_output=True, text=True, timeout=180)

def check_help(cmd, env):
    args = shlex.split(cmd)
    args[0] = binary
    # After --, Cobra treats --help as a positional word for the command.
    # Insert it before that marker so a help-only check cannot run an action.
    at = args.index("--") if "--" in args else len(args)
    args.insert(at, "--help")
    return subprocess.run(args, env=env, capture_output=True, text=True, timeout=30)

def main():
    tmp = tempfile.mkdtemp()
    env = dict(os.environ, XDG_CONFIG_HOME=tmp + "/config", XDG_CACHE_HOME=tmp + "/cache", XDG_STATE_HOME=tmp + "/state")
    files = []
    for d in ("docs/tutorials", "docs/how-to"):
        for f in sorted(os.listdir(os.path.join(root, d))):
            if f.endswith(".md"):
                files.append(os.path.join(d, f))
    ran = helped = skipped = 0
    failed = []
    for f in files:
        text = open(os.path.join(root, f)).read()
        for mode, body in block_re.findall(text):
            mode = mode or ("run" if host else "help")
            if mode == "skip":
                skipped += 1
                continue
            for cmd in commands(body):
                if not ours(cmd):
                    # Plain shell (git clone, cd, ssh): only the tool's own commands are checked.
                    continue
                if mode == "run":
                    r = run(cmd, env)
                    ran += 1
                else:
                    r = check_help(cmd, env)
                    helped += 1
                if r.returncode != 0:
                    failed.append((f, cmd, mode, (r.stderr or r.stdout).strip()[-300:]))
    for f, cmd, mode, err in failed:
        print(f"FAIL {f}: {cmd} ({mode})\n  {err}")
    print(f"docs commands: {ran} run, {helped} checked with --help, {skipped} blocks skipped, {len(failed)} failed")
    sys.exit(1 if failed else 0)

if __name__ == "__main__":
    main()
