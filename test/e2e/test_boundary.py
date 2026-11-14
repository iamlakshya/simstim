"""Offline tests of boundary.py, laptop Root isolation, drop_link PID scoping,
env -i / fish / git isolation, disposable container markers, and runtime
allow-list refusals: python3 test/e2e/test_boundary.py.
"""
import os, re, shutil, signal, subprocess, sys, tempfile, time, types, unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fakes import read, write  # noqa: E402
import allowed  # noqa: E402
import boundary  # noqa: E402
import owned as ow  # noqa: E402
import run_parts as rp  # noqa: E402


def make_exec(path, text):
    write(path, text)
    os.chmod(path, 0o755)
    return path


class RootAndDetachKey(unittest.TestCase):
    """Part 2 & Item M: the run's client gets a fresh laptop root, never reads
    or changes the user's config.toml, and checks detach_key before sending."""
    def setUp(self):
        self.base = tempfile.mkdtemp(prefix="e2e-broot-")
        self.addCleanup(shutil.rmtree, self.base, True)
        self.addCleanup(setattr, boundary, "CTX", boundary.CTX)
        self.addCleanup(boundary.OWNED.clear)
        self.user_cfg = os.path.join(self.base, "user-cfg", "client")
        write(os.path.join(self.user_cfg, "config.toml"), '[attach]\ndetach_key = "ctrl+]"\n')
        write(os.path.join(self.user_cfg, "hosts.toml"),
              '[[host]]\nname = "box"\nssh = "dev@box"\nssh_config = "~/.ssh/custom_box"\nsecrets = ["API_KEY"]\n\n'
              '[host.machine]\nprovider = "lima"\nid = "vm1"\n\n'
              '[[host]]\nname = "other"\nssh = "dev@other"\n')
        self.cli = make_exec(
            os.path.join(self.base, "fake-cli"),
            "#!%s\nimport sys, tty\ntty.setraw(sys.stdin.fileno())\n"
            "sys.stdout.write('READY\\r\\n'); sys.stdout.flush()\n"
            "b = sys.stdin.buffer.read(1)\nsys.exit(0 if b == b'\\x1c' else 2)\n" % sys.executable)
        self.root = boundary.Root("client", cli=self.cli, hosts=os.path.join(self.user_cfg, "hosts.toml"),
                                  host="box", base=self.base)
        self.addCleanup(self.root.remove)

    def test_root_isolates_laptop_folders_and_strips_host_secrets(self):
        self.assertFalse(os.path.exists(os.path.join(self.root.config, "config.toml")))
        hosts_txt = read(os.path.join(self.root.config, "hosts.toml"))
        self.assertIn('name = "box"', hosts_txt)
        self.assertNotIn("other", hosts_txt)
        self.assertNotIn("API_KEY", hosts_txt)
        self.assertIn(os.path.join(boundary.real_home(), ".ssh/custom_box"), hosts_txt)
        self.assertEqual(self.root.entry["machine"], {"provider": "lima", "id": "vm1"})
        env = self.root.env({"HOME": "/outside", "XDG_CONFIG_HOME": "/outside", "TERM": "xterm"})
        self.assertEqual(env["HOME"], self.root.home)
        self.assertEqual(env["USERPROFILE"], self.root.home)
        self.assertEqual(env["XDG_CONFIG_HOME"], self.root.dirs["XDG_CONFIG_HOME"])
        self.assertEqual(env["GIT_CONFIG_GLOBAL"], os.devnull)
        self.assertEqual(env["GIT_CONFIG_NOSYSTEM"], "1")
        # A sub-folder inside root.path is allowed (for agent_setup's throwaway config).
        sub = os.path.join(self.root.path, "sub-cfg")
        self.assertEqual(self.root.env({"XDG_CONFIG_HOME": sub})["XDG_CONFIG_HOME"], sub)
        # Owner's config.toml was never modified.
        self.assertEqual(read(os.path.join(self.user_cfg, "config.toml")), '[attach]\ndetach_key = "ctrl+]"\n')

    def test_detach_key_check_and_term_send_guard(self):
        self.assertTrue(self.root.detach_key_is_the_default())
        cfg_file = os.path.join(self.root.config, "config.toml")
        for content, want in [
            ('[attach]\ndetach_key = "ctrl+\\\\"\n', True),
            ('[secrets]\nX = "echo 1"\n', True),
            ('[attach]\ndetach_key = "ctrl+]"\n', False),
            ('attach = { detach_key = "ctrl+a" }\n', False),
            ('not = [valid toml\n', False),
        ]:
            with self.subTest(content=content):
                write(cfg_file, content)
                self.assertIs(self.root.detach_key_is_the_default(), want)
        os.remove(cfg_file)

        c = types.SimpleNamespace(cli=self.cli, HOST="box", NAME="client", HELPER='"$HOME/h"',
                                  DISPOSABLE=False, root=self.root)
        boundary.CTX = c
        with self.assertRaises(boundary.Refused):
            boundary.term([self.cli, "attach", "--id", "--", "box/unowned1"])
        boundary.own("sid12345")
        # Sub-config overriding detach_key is refused on Term.send even when root.config has no config.toml.
        sub_xdg = os.path.join(self.root.path, "sub", "config")
        write(os.path.join(sub_xdg, "client", "config.toml"), '[attach]\ndetach_key = "ctrl+]"\n')
        t = boundary.term([self.cli, "attach", "--id", "--", "box/sid12345"], env={"XDG_CONFIG_HOME": sub_xdg})
        try:
            self.assertTrue(t.wait_for(rb"READY", 10))
            with self.assertRaises(boundary.Refused):
                t.send(b"\x1c")
            with self.assertRaises(AttributeError):
                _ = t.fd
        finally:
            t.close()

        # Default detach_key allows single Ctrl+\ and refuses double Ctrl+\ or typing on owner hosts.
        t2 = boundary.term([self.cli, "attach", "--id", "--", "box/sid12345"])
        try:
            self.assertTrue(t2.wait_for(rb"READY", 10))
            with self.assertRaises(boundary.Refused):
                t2.send(b"\x1c\x1c")
            with self.assertRaises(boundary.Refused):
                t2.send(b"ls\r")
            t2.send(b"\x1c")
            self.assertEqual(t2.exit_code(10), 0)
        finally:
            t2.close()


class DropLinkIsolation(unittest.TestCase):
    """Items L & 3: drop_link signals only bridges below a live client started
    by the run, closes only the run's own SSH control socket, and leaves an
    owner's bridge and master untouched on both Linux and macOS."""
    def test_drop_link_touches_only_live_run_client_children(self):
        base = tempfile.mkdtemp(prefix="e2e-drop-")
        self.addCleanup(shutil.rmtree, base, True)
        self.addCleanup(setattr, boundary, "CTX", boundary.CTX)
        self.addCleanup(boundary.OWNED.clear)
        boundary.own("sid12345")
        fake_bin, ssh_log = os.path.join(base, "bin"), os.path.join(base, "ssh.log")
        os.makedirs(fake_bin)
        make_exec(os.path.join(fake_bin, "ssh"),
                  "#!%s\nimport json, sys\nwith open(%r, 'a') as f:\n    f.write(json.dumps(sys.argv[1:]) + '\\n')\n"
                  % (sys.executable, ssh_log))
        old_path = os.environ.get("PATH", "")
        os.environ["PATH"] = fake_bin + os.pathsep + old_path
        self.addCleanup(os.environ.__setitem__, "PATH", old_path)

        hosts = os.path.join(base, "hosts.toml")
        write(hosts, '[[host]]\nname = "box"\nssh = "dev@box"\n')
        bridge_script = make_exec(os.path.join(base, "client-host"), "#!%s\nimport time\ntime.sleep(60)\n" % sys.executable)
        child_rc = os.path.join(base, "child.rc")
        cli_script = make_exec(
            os.path.join(base, "cli"),
            "#!%s\nimport subprocess, sys\n"
            "p = subprocess.Popen([%r, 'bridge'], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)\n"
            "sys.stdout.write('CHILD=' + str(p.pid) + '\\n'); sys.stdout.flush()\n"
            "rc = p.wait()\n"
            "with open(%r, 'w') as f:\n    f.write(str(rc))\n"
            "sys.exit(128 + (-rc) if rc < 0 else rc)\n" % (sys.executable, bridge_script, child_rc))
        root = boundary.Root("client", cli=cli_script, hosts=hosts, host="box", base=base)
        self.addCleanup(root.remove)
        wrapper = os.path.join(root.config, "ssh", "config-box")
        write(wrapper, "Host box\n    HostName 127.0.0.1\n")
        target = boundary.Target(root)
        boundary.CTX = types.SimpleNamespace(cli=cli_script, HOST="box", NAME="client", HELPER='"$HOME/h"',
                                             DISPOSABLE=False, root=root)

        owner_bridge = subprocess.Popen([bridge_script, "bridge"], stdin=subprocess.DEVNULL,
                                        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        owner_master = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(60)", "ssh: master box"],
                                        stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        self.addCleanup(lambda: (owner_bridge.kill(), owner_bridge.wait()))
        self.addCleanup(lambda: (owner_master.kill(), owner_master.wait()))

        # Refuses PIDs not in boundary.LIVE and runs no ssh -O exit for them.
        self.assertEqual(target.drop_link(owner_bridge.pid), [])
        self.assertEqual(target.drop_link(owner_master.pid), [])
        self.assertIsNone(read(ssh_log))
        self.assertIsNone(owner_bridge.poll())
        self.assertIsNone(owner_master.poll())

        t = boundary.term([cli_script, "attach", "--id", "--", "box/sid12345"])
        try:
            self.assertTrue(t.wait_for(rb"CHILD=(\d+)", 10), t.buf)
            child_pid = int(re.search(rb"CHILD=(\d+)", t.buf).group(1))
            killed = target.drop_link(t.pid)
            self.assertEqual(killed, [child_pid])
            self.assertEqual(t.exit_code(10), 128 + int(signal.SIGTERM))
            self.assertEqual(int(read(child_rc)), -int(signal.SIGTERM))
            with self.assertRaises(ProcessLookupError):
                os.kill(child_pid, 0)
            ssh_calls = [ __import__("json").loads(x) for x in (read(ssh_log) or "").splitlines() ]
            self.assertEqual(ssh_calls, [["-F", wrapper, "-o", "ControlPath=" + os.path.join(root.control_dir(), "%C"),
                                          "-O", "exit", "--", "dev@box"]])
            self.assertIsNone(owner_bridge.poll())
            self.assertIsNone(owner_master.poll())
        finally:
            t.close()
        self.assertEqual(target.drop_link(t.pid), [])
        self.assertEqual(len((read(ssh_log) or "").splitlines()), 1)


class HostShellAndGitIsolation(unittest.TestCase):
    """Items N, Y & AF: real command construction isolates BASH_ENV, ENV,
    exported functions, non-POSIX login shells (fish), and git hooks/signing."""
    def test_on_host_and_owned_program_strip_shell_startup_and_work_under_fish_like_shell(self):
        d = tempfile.mkdtemp(prefix="e2e-shell-")
        self.addCleanup(shutil.rmtree, d, True)
        pwned = os.path.join(d, "pwned")
        env_file = write(os.path.join(d, "rc.sh"), "touch %s\n" % pwned) or os.path.join(d, "rc.sh")
        hostile_env = {
            "HOME": d,
            "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
            "TERM": "xterm",
            "BASH_ENV": env_file,
            "ENV": env_file,
            "PROMPT_COMMAND": "touch %s" % pwned,
            "BASH_FUNC_echo%%": "() { touch %s; }" % pwned,
            "SECRET_LEAK": "should-not-pass",
        }
        # 1. boundary.on_host through bash and through a strict fish-like login shell.
        fish_like = make_exec(
            os.path.join(d, "fake-fish"),
            "#!%s\nimport os, re, subprocess, sys\n"
            "assert sys.argv[1] == '-c', sys.argv\n"
            "line = sys.argv[2]\n"
            "# Reject backslashes, unquoted $, &&, ; outside single quotes, as fish would.\n"
            "m = re.fullmatch(r\"/bin/sh -c '([^'\\\\]+)'\", line)\n"
            "assert m is not None, 'not fish-safe: ' + repr(line)\n"
            "sys.exit(subprocess.run(['/bin/sh', '-c', m.group(1)], stdin=subprocess.DEVNULL, timeout=15).returncode)\n"
            % sys.executable)
        cmd = 'echo "OUT=${SECRET_LEAK:-clean}" > "$HOME/out"'
        for sh_bin in ["/bin/sh", fish_like]:
            r = subprocess.run([sh_bin, "-c", boundary.on_host(cmd)], env=hostile_env,
                               stdin=subprocess.DEVNULL, capture_output=True, timeout=15)
            self.assertEqual(r.returncode, 0, r.stderr)
            self.assertEqual(read(os.path.join(d, "out")), "OUT=clean\n")
            self.assertFalse(os.path.exists(pwned))

        # 2. owned.program (both exec and wait=True) strips BASH_ENV, ENV and exported functions.
        for shell in ("/bin/sh", "/bin/bash"):
            for wait in (False, True):
                argv = ow.program(shell, 'echo "P=${SECRET_LEAK:-clean}"', wait=wait)
                env_run = dict(hostile_env, KEEP_SECRET="kept-on-parent")
                r = subprocess.run(argv, env=env_run, stdin=subprocess.DEVNULL, capture_output=True, timeout=15)
                self.assertEqual(r.returncode, 0, r.stderr)
                self.assertEqual(r.stdout.decode(), "P=clean\n")
                self.assertFalse(os.path.exists(pwned))

    def test_real_git_commands_ignore_planted_hooks_templates_signing_and_fsmonitor(self):
        if shutil.which("git") is None:
            self.skipTest("no git")
        d = tempfile.mkdtemp(prefix="e2e-git-")
        self.addCleanup(shutil.rmtree, d, True)
        self.addCleanup(setattr, boundary, "CTX", boundary.CTX)
        pwned = os.path.join(d, "git-pwned")
        hooks = os.path.join(d, "hooks")
        tmpl = os.path.join(d, "template")
        os.makedirs(hooks)
        os.makedirs(os.path.join(tmpl, "hooks"))
        for h in ("pre-commit", "post-commit", "post-checkout", "reference-transaction"):
            make_exec(os.path.join(hooks, h), "#!/bin/sh\ntouch %s\nexit 1\n" % pwned)
            make_exec(os.path.join(tmpl, "hooks", h), "#!/bin/sh\ntouch %s\nexit 1\n" % pwned)
        write(os.path.join(tmpl, "planted"), "from-template\n")
        fsm = make_exec(os.path.join(d, "fsmonitor.sh"), "#!/bin/sh\ntouch %s\nexit 1\n" % pwned)
        gitconfig = os.path.join(d, ".gitconfig")
        sys_gitconfig = os.path.join(d, "etc-gitconfig")
        write(gitconfig,
              "[core]\n\thooksPath = %s\n\tfsmonitor = %s\n"
              "[init]\n\ttemplateDir = %s\n"
              "[commit]\n\tgpgsign = true\n"
              "[tag]\n\tgpgsign = true\n"
              "[gpg]\n\tprogram = /nonexistent/gpg-binary\n"
              "[e2eprobe]\n\tleaked = global\n" % (hooks, fsm, tmpl))
        write(sys_gitconfig, "[e2eprobe]\n\tsysleaked = system\n")

        # 1. Host-side owned.GIT ignores global and system gitconfig (even unmasked keys).
        host_repo = os.path.join(d, "host-repo")
        host_env = {"HOME": d, "GIT_CONFIG_GLOBAL": gitconfig, "GIT_CONFIG_SYSTEM": sys_gitconfig,
                    "PATH": os.environ.get("PATH", "/usr/bin:/bin")}
        script = ("mkdir %s && %s init --template= -q -b main %s && cd %s && "
                  "! %s config --get e2eprobe.leaked && ! %s config --get e2eprobe.sysleaked && "
                  "echo hi > f && %s add -A && %s commit -qm init && %s tag v1"
                  % (host_repo, ow.GIT, host_repo, host_repo, ow.GIT, ow.GIT, ow.GIT, ow.GIT, ow.GIT))
        r = subprocess.run(["/bin/sh", "-c", script], env=host_env,
                           stdin=subprocess.DEVNULL, capture_output=True, timeout=20)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertFalse(os.path.exists(pwned))
        self.assertFalse(os.path.exists(os.path.join(host_repo, ".git", "planted")))

        # 2. Laptop-side boundary.run(["git"] + allowed.GIT_FLAGS + ...) with Root.env().
        hosts = os.path.join(d, "hosts.toml")
        write(hosts, '[[host]]\nname = "box"\nssh = "box"\n')
        old_global, old_sys = os.environ.get("GIT_CONFIG_GLOBAL"), os.environ.get("GIT_CONFIG_SYSTEM")
        os.environ["GIT_CONFIG_GLOBAL"], os.environ["GIT_CONFIG_SYSTEM"] = gitconfig, sys_gitconfig
        try:
            root = boundary.Root("client", cli="client", hosts=hosts, host="box", base=d)
            self.addCleanup(root.remove)
            boundary.CTX = types.SimpleNamespace(cli="client", HOST="box", NAME="client", HELPER='"$HOME/h"',
                                                 DISPOSABLE=False, root=root)
            laptop_repo = os.path.join(root.path, "laptop-repo")
            env = root.env({"GIT_CONFIG_SYSTEM": sys_gitconfig})
            init = ["git"] + allowed.GIT_FLAGS + ["init", "--template=", "-q", "-b", "mine", laptop_repo]
            res = boundary.run(init, timeout=20, env=env)
            self.assertEqual(res.returncode, 0, res.stderr)
            # Fixture-only setup uses direct local git. The production boundary
            # deliberately permits only init and log on owner hosts.
            fixture = ["git"] + allowed.GIT_FLAGS + ["-C", laptop_repo]
            res = subprocess.run(fixture + ["commit", "--allow-empty", "-qm", "laptop"],
                                 timeout=20, env=env, capture_output=True)
            self.assertEqual(res.returncode, 0, res.stderr)
            for key in ("e2eprobe.leaked", "e2eprobe.sysleaked"):
                probe = subprocess.run(fixture + ["config", "--get", key],
                                       timeout=10, env=env, capture_output=True)
                self.assertEqual(probe.returncode, 1, probe.stdout)
            result = boundary.run(["git"] + allowed.GIT_FLAGS + ["log", "--format=%s", "-1", "mine"],
                                  timeout=10, env=env, cwd=laptop_repo)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout.strip(), b"laptop")

        finally:
            for k, old_v in (("GIT_CONFIG_GLOBAL", old_global), ("GIT_CONFIG_SYSTEM", old_sys)):
                if old_v is None:
                    os.environ.pop(k, None)
                else:
                    os.environ[k] = old_v
        self.assertFalse(os.path.exists(pwned))
        self.assertFalse(os.path.exists(os.path.join(laptop_repo, ".git", "planted")))


class DisposableMarkerAndRuntimeRefusals(unittest.TestCase):
    """Items 4 & 8: disposable() authenticates any host holding the 32-hex
    /etc/e2e-disposable marker, and boundary refuses unowned targets at runtime."""
    def test_disposable_marker_authenticates_any_marked_host_and_refuses_unmarked(self):
        good = "a1b2c3d4" * 4
        fake_t = lambda provider, inside: types.SimpleNamespace(
            machine=lambda: {"provider": provider} if provider else {},
            marker=lambda: inside,
        )
        for provider in ("", "lima", "docker"):
            self.assertTrue(rp.disposable(fake_t(provider, good), "1", good), provider)
            self.assertFalse(rp.disposable(fake_t(provider, ""), "1", good), provider)
            self.assertFalse(rp.disposable(fake_t(provider, "f" * 32), "1", good), provider)
            self.assertFalse(rp.disposable(fake_t(provider, good), "", good), provider)
            self.assertFalse(rp.disposable(fake_t(provider, "abcd1234"), "1", "abcd1234"), provider)
            self.assertFalse(rp.disposable(fake_t(provider, "g" * 32), "1", "g" * 32), provider)

        # Real boundary.Target.marker() reads /etc/e2e-disposable on both SSH/Lima and Docker targets.
        d = tempfile.mkdtemp(prefix="e2e-mark-")
        self.addCleanup(shutil.rmtree, d, True)
        fbin, mark_file = os.path.join(d, "bin"), os.path.join(d, "host-marker")
        os.makedirs(fbin)
        for tool in ("ssh", "docker"):
            make_exec(os.path.join(fbin, tool),
                      "#!%s\nimport os, sys\np = %r\n"
                      "if os.path.exists(p):\n    with open(p) as f:\n        sys.stdout.write(f.read())\n" % (sys.executable, mark_file))
        old_path = os.environ.get("PATH", "")
        os.environ["PATH"] = fbin + os.pathsep + old_path
        self.addCleanup(os.environ.__setitem__, "PATH", old_path)
        hosts = os.path.join(d, "hosts.toml")
        write(hosts, '[[host]]\nname = "vm"\nssh = "dev@vm"\n[host.machine]\nprovider = "lima"\nid = "vm"\n'
                     '[[host]]\nname = "ctr"\nssh = ""\n[host.machine]\nprovider = "docker"\nid = "c1"\n')
        for hname in ("vm", "ctr"):
            root = boundary.Root("client", cli="client", hosts=hosts, host=hname, base=d)
            self.addCleanup(root.remove)
            t = boundary.Target(root)
            if os.path.exists(mark_file):
                os.remove(mark_file)
            self.assertFalse(rp.disposable(t, "1", good))
            write(mark_file, good + "\n")
            self.assertTrue(rp.disposable(t, "1", good))

    def test_runtime_refusals_for_laptop_and_host_commands(self):
        d = tempfile.mkdtemp(prefix="e2e-refuse-")
        self.addCleanup(shutil.rmtree, d, True)
        self.addCleanup(setattr, boundary, "CTX", boundary.CTX)
        self.addCleanup(boundary.OWNED.clear)
        for mod_attr in ("subprocess", "os", "harness"):
            self.assertFalse(hasattr(boundary, mod_attr), mod_attr)
        hosts = os.path.join(d, "hosts.toml")
        write(hosts, '[[host]]\nname = "box"\nssh = "box"\n')
        root = boundary.Root("client", cli="client", hosts=hosts, host="box", base=d)
        self.addCleanup(root.remove)
        target = boundary.Target(root)
        c = types.SimpleNamespace(cli="client", HOST="box", NAME="client",
                                  HELPER='"$HOME/.local/share/client/bin/0.3.0/client-host"',
                                  DISPOSABLE=False, root=root)
        boundary.CTX = c
        env = root.env()
        prog = ["--"] + ow.program("/bin/sh", "sleep 1")

        for bad_argv in [
            [],
            ["sh", "-c", "id"],
            ["git", "init", d],
            ["client", "kill", "box/sid1"],
            ["client", "host", "alerts", "off", "box"],
            ["client", "attach", "--id", "--", "box/owner-session"],
            ["client", "pull", "--id", "--", "box/owner-session"],
            ["client", "ls", "--json", "--host", "other"],
            ["client", "host", "doctor", "other"],
            ["client", "new", "box", "--name", "e2e-x", "--detach"] + prog,
            ["client", "new", "box", "--name", ow.e2e_name("e2e-x"), "--dir", "/outside", "--detach"] + prog,
            ["client", "new", "box", "--name", ow.e2e_name("e2e-x"), "--dir", ow.run_dir() + "/../outside", "--detach"] + prog,
            ["client", "new", "box", "--name", ow.e2e_name("e2e-x"), "--detach", "--worktree"] + prog,
            ["client", "new", "box", "--name", ow.e2e_name("e2e-x"), "--detach", "--memory-max", "256M"] + prog,
            ["client", "new", "box", "--name", ow.e2e_name("e2e-x"), "--detach", "--", "/bin/sh", "-c", "sleep 1"],
        ]:
            with self.subTest(bad_argv=bad_argv):
                with self.assertRaises(boundary.Refused):
                    boundary.run(bad_argv, timeout=5, env=env)

        boundary.own("mine1234")
        self.assertIsNone(allowed.laptop_refusal(["client", "attach", "--id", "--", "box/mine1234"], c))
        self.assertIsNone(allowed.laptop_refusal(["client", "pull", "--id", "--", "box/mine1234"], c))
        self.assertIsNone(allowed.laptop_refusal(
            ["client", "new", "box", "--name", ow.e2e_name("e2e-x"), "--dir", ow.run_dir() + "/repo", "--detach"] + prog, c))
        boundary.disown("mine1234")
        self.assertIsNotNone(allowed.laptop_refusal(["client", "attach", "--id", "--", "box/mine1234"], c))

        # User's raw environment (wrong HOME) is refused too.
        with self.assertRaises(boundary.Refused):
            boundary.run(["client", "version", "--json"], timeout=5, env={"HOME": "/tmp"})

        for bad_host in [
            "rm -rf ~/x",
            "cat > ~/.e2e-run-zzzzzz/seq.bin",
            "cat > %s/../outside" % ow.run_dir(),
            'echo 7 > "$HOME/.e2e-run-%s/../outside"' % ow.RUN,
            '"$HOME/.local/share/client/bin/current" rm sid1',
        ]:
            with self.subTest(bad_host=bad_host):
                with self.assertRaises(boundary.Refused):
                    target.rc(bad_host)

        # Valid owner-host templates pass host_refusal, and c.HELPER=None refuses helper rm.
        self.assertIsNone(allowed.host_refusal("command -v git >/dev/null", c))
        self.assertIsNone(allowed.host_refusal("cat > %s/seq.bin" % ow.run_dir(), c))
        c.HELPER = None
        self.assertIsNotNone(allowed.host_refusal(
            'None rm sid1 || [ ! -e "%s/client/sessions/sid1" ]' % ow.xdg_sh("XDG_STATE_HOME", ".local/state"), c))


if __name__ == "__main__":
    unittest.main()
