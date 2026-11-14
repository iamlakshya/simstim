"""Host command scenarios with an isolated root and the C8 fake-process seam."""
import os, shutil, sys, tempfile, types, unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import boundary, host_commands, owned  # noqa: E402
from fakes import write  # noqa: E402


class HostCommands(unittest.TestCase):
    def check(self, machine, wrong_upgrade=False):
        tmp = tempfile.mkdtemp(prefix="e2e-host-command-test-")
        self.addCleanup(shutil.rmtree, tmp, True)
        hosts = os.path.join(tmp, "hosts.toml")
        write(hosts, '[[host]]\nname = "box"\nssh = "dev@box"\n')
        root = boundary.Root("client", cli="client", hosts=hosts, host="box", base=tmp)
        self.addCleanup(root.remove)
        results, calls, keys = [], [], []
        def ok(cond, text):
            results.append((bool(cond), text))
            return cond
        c = types.SimpleNamespace(cli="client", HOST="box", NAME="client", root=root, env={}, DISPOSABLE=True,
                                  envx=root.env(), ok=ok, host_machine=lambda: machine)
        c.host_ssh = lambda cmd: types.SimpleNamespace(stdout=b"v1\ncurrent->v1\ne2e-done\n")
        def run(argv, env=None, timeout=60, cwd=None):
            calls.append((argv, env))
            words = argv[1:]
            if words[:2] == ["host", "show"]:
                return (0, '{"v":1,"name":"box","reachable":true,"info":{"version":"v1"}}', "") if "--json" in words else \
                    (0, "box\n  Helper     client-host v1\n  Limits     none\n", "")
            if words[:2] == ["host", "upgrade"]:
                return (0, '{"outcome":"installed"}' if wrong_upgrade else '{"outcome":"unchanged","host":"box"}', "") if "--json" in words else \
                    (0, "", "box has helper v1 already.\n")
            if words[:2] == ["host", "set"]:
                if "idl=2h" in words:
                    return 2, "", 'Did you mean "idle"?\nTry: client host set box idle=2h\n'
                if "idle=2h" not in words:
                    return 2, "", "host set needs a setting"
                return (0, "", "box: idle 2h; containers do not stop by themselves") if machine else \
                    (2, "", "box was not made by host create")
            if words[:2] == ["host", "rm"] and "--destroy" in words:
                return (4, "", "Try: client host rm box --destroy --confirm box") if machine else (2, "", "no machine to delete")
            raise AssertionError(argv)
        class Terminal:
            def __init__(self, argv, env):
                self.env, self.closed = env, False
                self.buf = b"box was made by `host create`. It holds sessions. Type box to delete the machine too, or press Enter to keep it: " if machine else b""
            def wait_for(self, pattern, timeout):
                import re
                return re.search(pattern, self.buf) is not None
            def send(self, data):
                keys.append(data)
                if data != b"\r":
                    raise AssertionError("the fixture typed the machine's name")
            def exit_code(self, timeout):
                path = os.path.join(self.env["XDG_CONFIG_HOME"], "client", "hosts.toml")
                write(path, "")
                self.buf += b"Forgot box; the machine stays, with its sessions."
                return 0
            def close(self):
                self.closed = True
        c.Term = Terminal
        with mock.patch.object(boundary, "CTX", c), mock.patch.object(owned, "run_cmd", run):
            host_commands.run(c)
        return results, calls, keys, root

    def test_all_original_host_command_assertions_on_container_and_added_host(self):
        for machine in ({"provider": "docker", "id": "fixture"}, {}):
            with self.subTest(machine=machine):
                results, calls, keys, root = self.check(machine)
                self.assertFalse([text for good, text in results if not good], results)
                self.assertEqual(keys, [b"\r"] if machine else [])
                self.assertTrue(all(env["HOME"] == root.home for _, env in calls))
                with open(os.path.join(root.config, "hosts.toml")) as f:
                    self.assertIn('name = "box"', f.read())

    def test_upgrade_that_changes_the_helper_cannot_claim_unchanged(self):
        results, _, _, _ = self.check({}, wrong_upgrade=True)
        self.assertIn((False, "host upgrade --json: unchanged"), results)


if __name__ == "__main__":
    unittest.main()
