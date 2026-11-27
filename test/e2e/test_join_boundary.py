"""Offline checks of the joined suites' authenticated process boundary."""
import ast, os, shutil, sys, tempfile, types, unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import allowed, boundary, host_commands, host_git, owned, run_parts  # noqa: E402
import sandbox_hosts, session_sockets, session_verbs  # noqa: E402
from fakes import write  # noqa: E402

MODULES = (host_commands, host_git, sandbox_hosts, session_sockets, session_verbs)
MARKER = "ab" * 16


class JoinBoundary(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="e2e-join-test-")
        self.addCleanup(shutil.rmtree, self.tmp, True)
        hosts = os.path.join(self.tmp, "hosts.toml")
        write(hosts, '[[host]]\nname = "box"\nssh = "dev@box"\n')
        self.root = boundary.Root("client", cli="client", hosts=hosts, host="box", base=self.tmp)
        self.addCleanup(self.root.remove)
        self.c = types.SimpleNamespace(cli="client", NAME="client", HOST="box", HELPER="~/helper",
                                       root=self.root, env={}, created=[], state={}, DISPOSABLE=False)
        self.c.envx = self.root.env()
        self.addCleanup(setattr, boundary, "CTX", boundary.CTX)
        self.addCleanup(setattr, boundary, "OWNED", boundary.OWNED)
        boundary.CTX, boundary.OWNED = self.c, set()

    def authenticate(self):
        target = types.SimpleNamespace(marker=lambda: MARKER)
        self.c.DISPOSABLE = run_parts.disposable(target, "1", MARKER)
        self.assertTrue(self.c.DISPOSABLE)
        self.c.envx = self.root.env()

    def test_all_suite_entries_refuse_owner_or_fabricated_contexts(self):
        with mock.patch.object(boundary._subprocess, "run") as run, \
                mock.patch.object(boundary._harness, "Term") as term:
            for module in MODULES:
                with self.subTest(module=module.__name__), self.assertRaises(boundary.Refused):
                    module.run(self.c)
            self.authenticate()
            invented = types.SimpleNamespace(**vars(self.c))
            for module in MODULES:
                with self.subTest(module=module.__name__), self.assertRaises(boundary.Refused):
                    module.run(invented)
            run.assert_not_called()
            term.assert_not_called()

    def test_environment_flags_cannot_authenticate_an_owner_host(self):
        target = types.SimpleNamespace(marker=lambda: "another host's marker")
        self.assertFalse(run_parts.disposable(target, "1", MARKER))
        self.assertFalse(run_parts.disposable(target, "1", ""))
        self.assertFalse(run_parts.disposable(target, "yes", MARKER))
        env = self.root.env({"E2E_DISPOSABLE": "1", "E2E_DISPOSABLE_MARKER": MARKER, "CLIENT_NO_INSTALL": "0"})
        self.assertEqual(env["CLIENT_NO_INSTALL"], "1")
        with self.assertRaises(boundary.Refused):
            boundary.require_disposable(self.c)

    def test_local_fixtures_are_exact_and_physically_owned(self):
        self.authenticate()
        folder = tempfile.mkdtemp(dir=self.root.path)
        sock = os.path.join(folder, "agent.sock")
        key = os.path.join(folder, "wrong_ed25519")
        config = os.path.join(folder, "plain.conf")
        write(config, "Host *\n  PermitLocalCommand no\n")
        good = (["ssh", "-V"], ["ssh-agent", "-D", "-a", sock],
                ["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "", "-f", key],
                ["ssh", "-F", config] + allowed.FORWARD_OPTIONS + ["--", "dev@box", allowed.FORWARD_PROBE],
                ["ssh", "-F", config] + allowed.FORWARD_OPTIONS + ["-o", "ForwardAgent=no", "--", "dev@box", allowed.FORWARD_PROBE])
        for argv in good:
            self.assertIsNone(allowed.laptop_refusal(argv, self.c), argv)
            self.assertIsNotNone(allowed.laptop_refusal(argv + ["extra"], self.c), argv)
        outside = os.path.join(self.tmp, "outside")
        os.makedirs(outside)
        link = os.path.join(folder, "escape")
        os.symlink(outside, link)
        bad = (["ssh-agent", "-D", "-a", os.path.join(link, "agent.sock")],
               ["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "", "-f", os.path.join(link, "wrong_ed25519")],
               ["ssh", "-F", config] + allowed.FORWARD_OPTIONS + ["--", "other", allowed.FORWARD_PROBE])
        for argv in bad:
            with mock.patch.object(boundary._subprocess, "run") as run:
                with self.assertRaises(boundary.Refused):
                    boundary.run(argv, timeout=1, env=self.c.envx)
                run.assert_not_called()
        self.c.DISPOSABLE = False
        for argv in good:
            self.assertIsNotNone(allowed.laptop_refusal(argv, self.c), argv)

    def test_disposable_process_cwd_and_xdg_symlinks_cannot_escape(self):
        self.authenticate()
        outside = os.path.join(self.tmp, "outside")
        os.makedirs(outside)
        with mock.patch.object(boundary._subprocess, "run") as run:
            with self.assertRaises(boundary.Refused):
                boundary.run(["ssh", "-V"], timeout=1, env=self.c.envx, cwd=outside)
            link = os.path.join(self.root.path, "escape")
            os.symlink(outside, link)
            env = self.root.env({"XDG_CONFIG_HOME": link})
            with self.assertRaises(boundary.Refused):
                boundary.run(["ssh", "-V"], timeout=1, env=env)
            run.assert_not_called()
            boundary.run(["ssh", "-V"], timeout=1, env=self.c.envx)
            self.assertEqual(run.call_args.kwargs["cwd"], os.path.realpath(self.root.path))
            self.assertEqual(run.call_args.kwargs["timeout"], 1)

    def test_empty_agent_uses_the_existing_owned_terminal(self):
        self.authenticate()
        sock = os.path.join(self.root.path, "agent.sock")
        with mock.patch.object(boundary._harness, "Term") as raw:
            term = boundary.term(["ssh-agent", "-D", "-a", sock], self.c.envx)
            try:
                self.assertFalse(raw.call_args.kwargs["inherit"])
                self.assertEqual(raw.call_args.kwargs["cwd"], self.root.path)
                self.assertEqual(raw.call_args.kwargs["env"]["HOME"], self.root.home)
            finally:
                term.close()

    def test_forwarding_program_does_not_erase_its_observed_environment(self):
        # The fixture's first process must retain SSH_AUTH_SOCK; otherwise
        # env -i would make a broken SSH hardening path appear to pass.
        command = owned.program("/bin/sh", "exec sleep 600", wait=True)
        self.assertFalse(command[2].startswith("exec "))
        self.assertTrue(allowed.isolated(command))
        src = ast.parse(self.read("sandbox_hosts.py"))
        calls = [n for n in ast.walk(src) if isinstance(n, ast.Call) and ast.unparse(n.func) == "owned.program"]
        self.assertTrue(any(any(k.arg == "wait" and isinstance(k.value, ast.Constant) and k.value.value is True
                                for k in call.keywords) for call in calls))

    def test_hook_environment_is_the_existing_holder_environment_only(self):
        command = owned.program("/bin/sh", "exec sleep 600", keep=("CLIENT_HOOK_SOCK", "CLIENT_SESSION"))
        self.assertTrue(allowed.isolated(command, allow_holder=True))
        self.assertFalse(allowed.isolated(command))
        with self.assertRaises(ValueError):
            owned.program("/bin/sh", "exec sleep 600", keep=("BASH_ENV",))

    def test_unowned_or_foreign_worktree_is_never_touched(self):
        calls = []
        self.c.host_rc = lambda cmd, data=None: calls.append(cmd) or 0
        self.assertFalse(host_git.touch_worktree(self.c, "abcd2345", "name", "repo", "/tmp/foreign"))
        self.c.created.append("abcd2345")
        boundary.own("abcd2345")
        with mock.patch.object(owned, "exact", return_value=True):
            self.assertFalse(host_git.touch_worktree(self.c, "abcd2345", "name", "repo", "/tmp/foreign"))
        self.assertEqual(calls, [])

    def read(self, name):
        with open(os.path.join(os.path.dirname(__file__), name)) as f:
            return f.read()


if __name__ == "__main__":
    unittest.main()
