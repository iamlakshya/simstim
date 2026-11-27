"""Offline tests of what the checks of a disposable host make outside the
run's folder, and of its clean-up (disposable.py, disposable_setup.py):
python3 test/e2e/test_disposable_files.py. The harness's own shell commands
run on a local folder that stands for the host's home, with the XDG
folders set in it. A path is recorded before it is made, one that is
already there is never used or removed, and the alerts file goes only
while it holds the run's token.
"""
import contextlib, io, os, shutil, sys, tempfile, unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fakes import Ctx, Host, fake_client, local_shell, read, write  # noqa: E402
import disposable  # noqa: E402
import disposable_setup as ds  # noqa: E402
import owned as ow  # noqa: E402
import run_parts as rp  # noqa: E402

ALERTS = "~/xdg/client/alerts.toml"


class OnTheHost(unittest.TestCase):
    def setUp(self):
        self.home = tempfile.mkdtemp(prefix="e2e-home-")
        self.addCleanup(shutil.rmtree, self.home, True)
        self.addCleanup(setattr, ow, "RUN", ow.RUN)
        ow.RUN = "aaaaaa"
        self.alerts_file = os.path.join(self.home, "xdg", "client", "alerts.toml")
        write(os.path.join(self.home, ".config", "client", "alerts.toml"), "owner = true\n")
        self.host = Host(alerts=lambda: {"path": ALERTS, "exists": os.path.exists(self.alerts_file)})
        self.c = Ctx(self.host, lock="aaaaaa")
        self.c.host_rc, self.c.host_out = local_shell(self.home)
        self.wt = os.path.join(self.home, "data", "client", "worktrees", "e2e-setup-repo-aaaaaa")

    def worktree(self, start):
        """worktree_check with a start that runs start(argv) when the client
        would start the session; its other starts must not come."""
        made, created = [], []
        old, ow.start_new = ow.start_new, lambda c, envx, created, name, *args: start(args)
        try:
            with contextlib.redirect_stdout(io.StringIO()):
                ds.worktree_check(self.c, {}, created, made, "~/.e2e-run-aaaaaa/e2e-setup-repo-aaaaaa", "e2e-setup-repo-aaaaaa",
                                  ["--"] + ow.program("/bin/sh", "true"), lambda *a: b"")
        finally:
            ow.start_new = old
        return made

    def test_a_worktree_is_recorded_before_its_start(self):
        # The start makes the worktree, and an interrupt then ends the check:
        # the path was recorded before the start began, so the clean-up
        # removes it.
        seen = []

        def start(args):
            seen.append(list(made_now))
            os.makedirs(os.path.join(self.wt, "wt-1"))
            raise KeyboardInterrupt
        made_now = []
        old, ow.start_new = ow.start_new, lambda c, envx, created, name, *args: start(args)
        try:
            with self.assertRaises(KeyboardInterrupt), contextlib.redirect_stdout(io.StringIO()):
                ds.worktree_check(self.c, {}, [], made_now, "~/x", "e2e-setup-repo-aaaaaa", ["--"] + ow.program("/bin/sh", "true"), lambda *a: b"")
        finally:
            ow.start_new = old
        self.assertEqual(len(seen[0]), 1, "the path is recorded when the start begins")
        self.assertEqual(disposable.remove_made(self.c, made_now), [])
        self.assertFalse(os.path.exists(self.wt))

    def test_a_worktree_folder_that_is_there(self):
        write(os.path.join(self.wt, "theirs"), "x")
        made = self.worktree(lambda args: self.fail("the check started a worktree in a folder that is there"))
        self.assertEqual(made, [])
        self.assertEqual(read(os.path.join(self.wt, "theirs")), "x")

    def test_only_paths_with_the_runs_token(self):
        write(os.path.join(self.home, "w-aaaaaa", "x"), "run's\n")
        write(os.path.join(self.home, "e2e-setup-repo", "x"), "owner's\n")
        made = ["~/e2e-setup-repo", "~/w-aaaaaa"]
        self.assertEqual(disposable.remove_made(self.c, made), [])
        self.assertEqual(made, [])
        self.assertFalse(os.path.exists(os.path.join(self.home, "w-aaaaaa")))
        self.assertEqual(read(os.path.join(self.home, "e2e-setup-repo", "x")), "owner's\n")

    def ready(self, state):
        with fake_client(self.host) as out:
            return disposable.alerts_ready(self.c, {}, state), out.getvalue()

    def set_alerts(self, token):
        """What `host alerts set` writes for the run with token: the helper's
        file, at the path it uses, with the run's folder in its command."""
        write(self.alerts_file, "run = 'cat >> \"$HOME/.e2e-run-%s/alerts.jsonl\"'\n" % token)

    def test_a_file_that_is_there_is_never_touched(self):
        write(self.alerts_file, "owner = 'xdg'\n")
        state = {}
        path, said = self.ready(state)
        self.assertIsNone(path)
        self.assertIn("SKIP alerts: the host has its own alerts file", said)
        self.assertEqual(disposable.cleanup(self.c, state), [])
        self.assertEqual(read(self.alerts_file), "owner = 'xdg'\n")

    def test_a_path_that_a_shell_reads(self):
        for path in ["~/x;rm -rf ~/y/alerts.toml", "~/$(id)/alerts.toml", "~/../alerts.toml", "x/alerts.toml"]:
            with self.subTest(path):
                self.host.alerts = lambda: {"path": path, "exists": False}
                self.assertIsNone(self.ready({})[0])

    def test_a_crash_between_set_and_off(self):
        # The run made its alerts file, and stopped before off: its clean-up
        # removes the file by its token, and another run's leaves it.
        state = {"disposable": True}
        self.assertEqual(self.ready(state)[0], ALERTS)
        self.set_alerts("aaaaaa")
        ow.RUN = "bbbbbb"
        self.assertTrue(disposable.remove_alerts_file(self.c, {"alerts_file": ALERTS}))
        self.assertIsNotNone(read(self.alerts_file))
        ow.RUN = "aaaaaa"
        with fake_client(self.host):
            self.assertTrue(rp.finish(self.c, [], state))
        self.assertIsNone(read(self.alerts_file))
        self.assertEqual(read(os.path.join(self.home, ".config", "client", "alerts.toml")), "owner = true\n")

    def test_a_file_that_lost_the_token_stays(self):
        write(self.alerts_file, "owner = 'new'\n")
        self.assertTrue(disposable.remove_alerts_file(self.c, {"alerts_file": ALERTS}))
        self.assertEqual(read(self.alerts_file), "owner = 'new'\n")

    def test_a_file_that_does_not_read(self):
        # grep cannot read the file: a failure, never a file that is not the
        # run's. The clean-up fails, and keeps the lock.
        self.set_alerts("aaaaaa")
        os.chmod(self.alerts_file, 0)
        self.addCleanup(os.chmod, self.alerts_file, 0o600)
        if os.access(self.alerts_file, os.R_OK):
            self.skipTest("the tests run as a user who reads every file")
        ow.take_lock(self.c)
        state = {"disposable": True, "alerts_file": ALERTS}
        with fake_client(self.host) as out:
            self.assertFalse(rp.finish(self.c, [], state))
        self.assertIn("the alerts file %s that the run made is still there" % ALERTS, out.getvalue())
        self.assertTrue(os.path.isdir(os.path.join(self.home, ".e2e-lock")))

    def test_the_clean_up_of_a_disposable_host(self):
        # A path recorded in made, and the alerts file: both go, and so does
        # the lock.
        ow.take_lock(self.c)
        write(os.path.join(self.home, "w-aaaaaa", "x"), "x")
        self.set_alerts("aaaaaa")
        state = {"disposable": True, "made": ["~/w-aaaaaa"], "alerts_file": ALERTS}
        with fake_client(self.host) as out:
            self.assertTrue(rp.finish(self.c, [], state), out.getvalue())
        self.assertFalse(os.path.exists(os.path.join(self.home, "w-aaaaaa")))
        self.assertIsNone(read(self.alerts_file))
        self.assertFalse(os.path.exists(os.path.join(self.home, ".e2e-lock")))


if __name__ == "__main__":
    unittest.main()
