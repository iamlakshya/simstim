"""Offline ownership and cleanup checks of the host-git adapter.

The fake client uses the same owned.run_cmd seam as the existing C8 tests.
No client or host command runs. The final folder step is the canonical
nonce-checked removal, never an rm of a path returned by the host.
"""
import io, json, os, sys, unittest
from contextlib import redirect_stdout
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import boundary, host_git, owned  # noqa: E402
from fakes import Ctx, Host  # noqa: E402

BASE = owned.run_dir() + "/host-git"
NAME = "client"
SESSION = owned.e2e_name("e2e-hostgit")
SID = "abcd2345"


class FakeHost(Host):
    """Fresh starts, delayed answers and helper deletion failures, as data."""
    def __init__(self, new="started", cwd=None, appear_after=0, stays=False, error=None):
        super().__init__(made_id=SID)
        self.new, self.cwd, self.appear_after, self.stays, self.error = new, cwd, appear_after, stays, error
        self.commands, self.hidden, self.killed, self.lists, self.results = [], [], [], 0, []
        self.state = {}

    def rows(self):
        rows = super().rows()
        for row in rows:
            row["cwd"] = self.cwd or "/home/u/.local/share/client/worktrees/repo-%s/%s-%s" % (owned.RUN, SESSION, SID)
        return rows

    def host_ssh(self, cmd, data=None):
        self.commands.append(cmd)
        out = b"git version 2.47.3\n" if cmd == "git --version" else b""
        return type("Done", (), {"stdout": out, "returncode": 0})()

    def host_out(self, cmd):
        self.commands.append(cmd)
        return "running\n"

    def host_rc(self, cmd, data=None):
        self.commands.append(cmd)
        if cmd.startswith("~/helper rm "):
            sid = cmd.split()[2]
            self.killed.append(sid)
            if self.stays:
                return 1
            self.sessions.pop(sid, None)
        return 0

    def run_cmd(self, argv, env=None, timeout=60, cwd=None):
        self.calls.append(argv)
        if argv[1] == "new":
            self.hidden.append((SID, argv[argv.index("--name") + 1]))
            if self.new == "timeout":
                raise boundary.TimeoutExpired(argv, timeout)
            return 0, json.dumps({"v": 1, "host": "box", "id": SID, "name": SESSION}), ""
        if argv[1] == "ls":
            self.lists += 1
            if self.lists > self.appear_after:
                self.sessions.update(self.hidden)
                self.hidden.clear()
            row = {"host": "box", "sessions": self.rows()}
            if self.error:
                row["error"] = self.error
            return 0, json.dumps([row]), ""
        if argv[1] == "kill":
            sid = argv[2].split("/", 1)[1]
            self.killed.append(sid)
            if not self.stays:
                self.sessions.pop(sid, None)
            return 0, "", "Deleted.\nRemoved the clean worktree and its branch.\n"
        raise AssertionError(argv)

    def run(self):
        c = Ctx(self)
        c.host_ssh, c.host_out = self.host_ssh, self.host_out
        c.created, c.state, c.DISPOSABLE = [], self.state, True
        out = io.StringIO()
        with mock.patch.object(boundary, "CTX", c), mock.patch.object(boundary, "OWNED", set()), \
                mock.patch.object(owned, "run_cmd", side_effect=self.run_cmd), \
                mock.patch.object(host_git.time, "sleep"), redirect_stdout(out):
            host_git.run(c)
            # The outer finally owns this step. Exercise its exact source
            # against the same fake host, only when the adapter released it.
            if not c.state.get("keep_run_dir"):
                owned.remove_run_dir(c)
        return out.getvalue()

    def removals(self):
        return [cmd for cmd in self.commands if cmd.startswith("n=$(cat)")]


class CleanupTests(unittest.TestCase):
    def assert_owned_folder_removed(self, h):
        self.assertEqual(len(h.removals()), 1)
        self.assertIn(owned.run_dir() + "/.e2e-nonce", h.removals()[0])
        self.assertNotIn("rm -rf -- /tmp", h.removals()[0])
        self.assertFalse(h.state.get("keep_run_dir"))

    def test_removes_only_the_runs_own_folder(self):
        h = FakeHost()
        h.run()
        self.assert_owned_folder_removed(h)
        self.assertIn(SID, h.killed)

    def test_never_removes_another_path(self):
        for cwd in ("/srv/unrelated/client/worktrees/repo-valuable/valuable-data", "/tmp/unrelated-data"):
            with self.subTest(cwd=cwd):
                h = FakeHost(cwd=cwd)
                h.run()
                self.assert_owned_folder_removed(h)
                self.assertFalse([cmd for cmd in h.commands if cwd in cmd])

    def test_a_host_error_keeps_the_files(self):
        h = FakeHost(error="cannot reach box: connection refused")
        out = h.run()
        self.assertEqual(h.removals(), [])
        self.assertIn("so %s stays on the host" % BASE, out)
        # Without a valid pre-start list there is no start and no ownership.
        self.assertFalse(h.killed)

    def test_a_session_that_appears_late_is_found_and_deleted(self):
        h = FakeHost(new="timeout", appear_after=3)
        h.run()
        self.assertEqual(h.killed, [SID])
        self.assert_owned_folder_removed(h)

    def test_a_session_that_never_appears_keeps_the_files(self):
        h = FakeHost(new="timeout", appear_after=10**6)
        out = h.run()
        self.assertEqual(h.removals(), [])
        self.assertTrue(h.state.get("keep_run_dir"))
        self.assertIn("no session called %s appeared, so %s stays on the host" % (SESSION, BASE), out)

    def test_a_session_that_does_not_go_keeps_the_files(self):
        h = FakeHost(stays=True)
        out = h.run()
        self.assertTrue(h.killed)
        self.assertEqual(h.removals(), [])
        self.assertTrue(h.state.get("keep_run_dir"))
        self.assertIn("may still run, so %s stays on the host" % BASE, out)

    def test_own_base(self):
        self.assertTrue(host_git.own_base(BASE))
        for base in ("/tmp", BASE + "/..", BASE + "/other", "~/.e2e-run-000000/host-git", "", BASE + " x"):
            self.assertFalse(host_git.own_base(base), base)

    def test_old_or_named_like_id_sessions_are_never_deleted(self):
        h = FakeHost(new="timeout", appear_after=10**6)
        h.sessions = {"keep2345": SESSION, "other234": SID}
        h.run()
        self.assertEqual(h.sessions, {"keep2345": SESSION, "other234": SID})
        self.assertEqual(h.killed, [])


if __name__ == "__main__":
    unittest.main()
