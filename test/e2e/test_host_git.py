"""Offline tests of host_git.py's cleanup: python3 test/e2e/test_host_git.py.

A fake host and a fake client stand in for the real ones. They record the
commands that would run, so each test can check what the run would remove.
Nothing runs on a host.
"""
import io
import json
import os
import subprocess
import sys
import unittest
from contextlib import redirect_stdout
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import host_git  # noqa: E402

BASE = "/tmp/e2e-hostgit.AbC123"
NAME = "tool"
SESSION = "e2e-hostgit-AbC123"


class FakeHost:
    """The host's shell and the client, as host_git.run uses them.

    new: "started", or "timeout" when its answer is lost.
    cwd: the session's folder, as ls says.
    appear_after: how many lists the session stays hidden after `new`.
    stays: kill does not remove the session.
    error: ls --json gives the host with this error.
    """

    def __init__(self, new="started", cwd=None, appear_after=0, stays=False, error=None):
        self.new, self.cwd, self.appear_after, self.stays, self.error = new, cwd, appear_after, stays, error
        self.commands = []  # every command that would run on the host
        self.hidden = []    # sessions that `new` made, not listed yet
        self.listed = []    # the host's sessions: [id, name, cwd]
        self.killed = []
        self.lists = 0
        self.results = []

    def result(self, out):
        return subprocess.CompletedProcess([], 0, stdout=out.encode(), stderr=b"")

    def host_ssh(self, cmd):
        self.commands.append(cmd)
        if cmd.startswith("mktemp"):
            return self.result(BASE + "\n")
        if cmd == "git --version":
            return self.result("git version 2.47.3\n")
        return self.result("")

    def host_rc(self, cmd):
        self.commands.append(cmd)
        return 0

    def host_argv(self, cmd):
        return ["host", cmd]

    def ok(self, cond, msg):
        self.results.append((bool(cond), msg))
        return cond

    def client(self, argv, **kw):
        if argv[0] == "host":
            self.commands.append(argv[1])
            return subprocess.CompletedProcess(argv, 1, stdout="running\n", stderr="")
        verb = argv[1]
        if verb == "new":
            name = argv[argv.index("--name") + 1]
            cwd = self.cwd or "/home/u/.local/share/%s/worktrees/repo-AbC123/%s-s1" % (NAME, name)
            self.hidden.append(["s1", name, cwd])
            if self.new == "timeout":
                raise subprocess.TimeoutExpired(argv, 120)
            return subprocess.CompletedProcess(argv, 0, stdout="Started box/s1 (%s, shell)\n" % name, stderr="")
        if verb == "ls":
            self.lists += 1
            if self.lists > self.appear_after:
                self.listed += self.hidden
                self.hidden = []
            row = {"host": "box", "sessions": [{"id": i, "name": n, "cwd": c} for i, n, c in self.listed]}
            if self.error:
                row = {"host": "box", "sessions": None, "error": self.error}
            return subprocess.CompletedProcess(argv, 0, stdout=json.dumps([row]), stderr="")
        if verb == "kill":
            sid = argv[2].split("/", 1)[1]
            self.killed.append(sid)
            if not self.stays:
                self.listed = [s for s in self.listed if s[0] != sid]
            return subprocess.CompletedProcess(argv, 0, stdout="Deleted.\nRemoved the clean worktree and its branch.\n", stderr="")
        raise AssertionError("an unexpected command: %r" % argv)

    def run(self):
        c = host_git.Ctx(cli="client", HOST="box", NAME=NAME, env={}, ok=self.ok,
                         host_ssh=self.host_ssh, host_rc=self.host_rc, host_argv=self.host_argv)
        out = io.StringIO()
        with mock.patch.object(host_git.subprocess, "run", side_effect=self.client), \
                mock.patch.object(host_git.time, "sleep"), redirect_stdout(out):
            host_git.run(c)
        return out.getvalue()

    def removals(self):
        """Every command that would remove something, but the control
        step's `rm -f <marks>/*`, which clears the run's own markers."""
        return [c for c in self.commands if c.startswith(("rm ", "rmdir ")) and not c.startswith("rm -f '%s/marks'/*" % BASE)]


class CleanupTests(unittest.TestCase):
    def test_removes_only_the_runs_own_folder(self):
        h = FakeHost()
        h.run()
        self.assertEqual(h.removals(), ["rm -rf -- '%s'" % BASE])
        self.assertIn("s1", h.killed)

    def test_never_removes_another_path(self):
        for cwd in ("/srv/unrelated/tool/worktrees/repo-AbC123/valuable-data", "/tmp/unrelated-data"):
            with self.subTest(cwd=cwd):
                h = FakeHost(cwd=cwd)
                h.run()
                self.assertEqual(h.removals(), ["rm -rf -- '%s'" % BASE])
                self.assertFalse([c for c in h.commands if cwd in c and not c.startswith("cd ")])

    def test_a_host_error_keeps_the_files(self):
        h = FakeHost(error="cannot reach box: ssh: connect to host box port 22: Connection refused")
        out = h.run()
        self.assertEqual(h.removals(), [])
        self.assertIn("so %s stays on the host" % BASE, out)

    def test_a_session_that_appears_late_is_found_and_deleted(self):
        h = FakeHost(new="timeout", appear_after=3)
        h.run()
        self.assertEqual(h.killed, ["s1"])
        self.assertEqual(h.removals(), ["rm -rf -- '%s'" % BASE])

    def test_a_session_that_never_appears_keeps_the_files(self):
        h = FakeHost(new="timeout", appear_after=10**6)
        out = h.run()
        self.assertEqual(h.removals(), [])
        self.assertIn("no session called %s appeared, so %s stays on the host" % (SESSION, BASE), out)

    def test_a_session_that_does_not_go_keeps_the_files(self):
        h = FakeHost(stays=True)
        out = h.run()
        self.assertTrue(h.killed)
        self.assertEqual(h.removals(), [])
        self.assertIn("may still run, so %s stays on the host" % BASE, out)

    def test_own_base(self):
        self.assertTrue(host_git.own_base(BASE))
        for base in ("/tmp", "/tmp/e2e-hostgit.AbC123/..", "/tmp/../e2e-hostgit.AbC123", "/tmp/other.AbC123",
                     "tmp/e2e-hostgit.AbC123", "", "/tmp/e2e-hostgit.AbC123 x"):
            self.assertFalse(host_git.own_base(base), base)


if __name__ == "__main__":
    unittest.main()
