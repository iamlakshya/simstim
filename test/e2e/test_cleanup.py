"""Offline tests of the e2e run's lock, its own folder, and its last step
(owned.py, run_parts.finish): python3 test/e2e/test_cleanup.py. The
harness's own shell commands run on a local folder that stands for the
host's home, and a child run is ended by signals. The owner's files
survive, and only a lock with the run's token is given up.
"""
import contextlib, io, json, os, shutil, signal, subprocess, sys, tempfile, time, types, unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fakes import HERE, MODULES, OWNER, Ctx, Host, T, deleted, fake_client, local_shell, read, source, write  # noqa: E402
import owned as ow  # noqa: E402
import run_parts as rp  # noqa: E402


class OnTheHost(unittest.TestCase):
    """The lock, the run's folder and the alerts file, with the harness's
    own shell commands on a local folder. The owner's files survive."""
    def setUp(self):
        self.home = tempfile.mkdtemp(prefix="e2e-home-")
        self.addCleanup(shutil.rmtree, self.home, True)
        for rel, text in OWNER.items():
            write(os.path.join(self.home, rel), text)
        self.addCleanup(setattr, ow, "RUN", ow.RUN)
        self.addCleanup(setattr, ow, "NONCE", ow.NONCE)
        ow.NONCE = "n" * 32
        self.alerts_file = os.path.join(self.home, "xdg", "client", "alerts.toml")

    def ctx(self, run, sessions=None, fail=None):
        ow.RUN = run
        host = Host(sessions, alerts=lambda: {"path": "~/xdg/client/alerts.toml", "exists": os.path.exists(self.alerts_file)})

        def answer(cmd, data=None):
            # The helper's rm goes to the fake host's sessions.
            r = (fail(cmd, data) if fail.__code__.co_argcount >= 2 else fail(cmd)) if fail else None
            return host.host_rc(cmd, data) if r is None and cmd.startswith("~/helper rm ") else r
        c = Ctx(host)
        c.host_rc, c.host_out = local_shell(self.home, fail=answer)
        return c, host

    def assertOwnerUntouched(self):
        for rel, text in OWNER.items():
            self.assertEqual(read(os.path.join(self.home, rel)), text, rel)

    def finish(self, c, host, state, created=()):
        with fake_client(host) as out:
            clean = rp.finish(c, list(created), state)
        return clean, out.getvalue()

    def test_two_runs(self):
        a, host = self.ctx("aaaaaa")
        self.assertEqual(ow.take_lock(a), (True, "aaaaaa"))
        b, _ = self.ctx("bbbbbb")
        self.assertEqual(ow.take_lock(b), (False, "aaaaaa"))
        self.assertFalse(ow.release_lock(b))  # not B's to give up
        self.assertTrue(os.path.isdir(os.path.join(self.home, ".e2e-lock")))
        a, host = self.ctx("aaaaaa")
        clean, out = self.finish(a, host, {"locked": True})
        self.assertTrue(clean, out)
        self.assertFalse(os.path.exists(os.path.join(self.home, ".e2e-lock")))
        self.assertOwnerUntouched()

    def test_a_lost_answer_of_the_lock(self):
        # The host made the lock, and the answer was lost: the token in the
        # lock tells that the lock is the run's own.
        def lost(cmd, data=None):
            if cmd.startswith("mkdir ~/.e2e-lock"):
                subprocess.run(["sh", "-c", cmd], env={"HOME": self.home, "PATH": os.environ.get("PATH", "")},
                               input=data, stdin=None if data is not None else subprocess.DEVNULL,
                               capture_output=True, timeout=10)
                return 255
            return None
        c, _ = self.ctx("aaaaaa", fail=lost)
        self.assertEqual(ow.take_lock(c), (True, "aaaaaa"))

    def test_the_lock_message_and_its_commands(self):
        c, _ = self.ctx("aaaaaa")
        ow.take_lock(c)
        text = ow.leftover_help(c, "aaaaaa")
        for want in ["Only when no run with the token aaaaaa goes on", "e2e-*-aaaaaa", "list them on the laptop: client ls --host box.",
                     "~/.e2e-run-aaaaaa", "~/helper rm <id>", ow.UNLOCK]:
            self.assertIn(want, text)
        # The safe command removes the lock, and nothing else: a lock that
        # holds anything more stays.
        write(os.path.join(self.home, ".e2e-lock", "other"), "x")
        self.assertNotEqual(c.host_rc(ow.UNLOCK), 0)
        self.assertTrue(os.path.isdir(os.path.join(self.home, ".e2e-lock")))
        os.remove(os.path.join(self.home, ".e2e-lock", "other"))
        self.assertEqual(c.host_rc(ow.UNLOCK), 0)
        self.assertFalse(os.path.exists(os.path.join(self.home, ".e2e-lock")))

    def test_a_run_file_that_is_no_token(self):
        # The lock's run file holds text that is no token: the run never
        # repeats it, and its message gives no command but the unlock.
        crafted = "x ] || [ x ] && rm -f ~/precious.txt || [ x"
        write(os.path.join(self.home, ".e2e-lock", "run"), crafted)
        write(os.path.join(self.home, "precious.txt"), "mine\n")
        c, _ = self.ctx("aaaaaa")
        locked, holder = ow.take_lock(c)
        self.assertEqual((locked, holder), (False, "unknown"))
        text = ow.leftover_help(c, holder)
        self.assertNotIn("precious", text)
        self.assertIn("cannot be read", text)
        commands = [line.split(": ", 1)[1] for line in text.splitlines() if ": " in line]
        self.assertEqual(commands, [ow.UNLOCK])
        self.assertEqual(read(os.path.join(self.home, "precious.txt")), "mine\n")

    def test_a_session_that_is_already_gone(self):
        # The helper's rm fails, as for an ID with no session: the session's
        # folder in the helper's state folder tells. Gone counts as deleted;
        # a session whose folder is there and that the rm did not delete is
        # not.
        write(os.path.join(self.home, "helper"), "#!/bin/sh\necho 'Error: no session' >&2\nexit 1\n")
        os.chmod(os.path.join(self.home, "helper"), 0o755)
        write(os.path.join(self.home, ".local", "state", "client", "sessions", "stay1234", "manifest.json"), "{}")
        host_rc, host_out = local_shell(self.home)
        c = Ctx(Host())
        c.host_rc = host_rc
        created = ["gone1234", "stay1234"]
        self.assertEqual(ow.clean_up(c, created), ["stay1234"])
        self.assertEqual(created, ["stay1234"])

    def test_an_early_exit(self):
        # As E2E_ONLY=guided-setup: the run takes the lock, makes its folder,
        # and ends. The clean-up removes the run's folder and the lock, and
        # none of the owner's files at the old fixed paths.
        c, host = self.ctx("aaaaaa")
        ow.take_lock(c)
        state = {"before": set()}
        self.assertTrue(ow.make_run_dir(c))
        self.assertEqual(read(os.path.join(self.home, ".e2e-run-aaaaaa", ".e2e-run")), "aaaaaa")
        clean, out = self.finish(c, host, state)
        self.assertTrue(clean, out)
        self.assertFalse(os.path.exists(os.path.join(self.home, ".e2e-run-aaaaaa")))
        self.assertFalse(os.path.exists(os.path.join(self.home, ".e2e-lock")))
        self.assertOwnerUntouched()

    def test_a_pull_run(self):
        # The pull check makes its repository in the run's folder, with
        # git, and the clean-up removes it with that folder. The owner's
        # ~/e2e-pull stays.
        if shutil.which("git") is None:
            self.skipTest("no git")
        c, host = self.ctx("aaaaaa")
        ow.take_lock(c)
        state = {"before": set()}
        self.assertTrue(ow.make_run_dir(c))
        created = []
        c.root = types.SimpleNamespace(path=self.home)
        with fake_client(host):
            rp.pull_check(c, {}, created)
        self.assertTrue(all(ok for ok, _ in c.results), c.results)
        self.assertTrue(os.path.isdir(os.path.join(self.home, ".e2e-run-aaaaaa", "pull", ".git")))
        clean, out = self.finish(c, host, state, created)
        self.assertTrue(clean, out)
        self.assertFalse(os.path.exists(os.path.join(self.home, ".e2e-run-aaaaaa")))
        self.assertOwnerUntouched()

    def test_a_folder_of_the_runs_name_that_is_there(self):
        # A folder of the run's name that the run did not make, even when
        # .e2e-nonce holds the public run token (item 9.3): mkdir refuses it,
        # and only the secret 32-hex NONCE adopts or removes it.
        write(os.path.join(self.home, ".e2e-run-aaaaaa", "theirs.txt"), "x")
        write(os.path.join(self.home, ".e2e-run-aaaaaa", ".e2e-run"), "aaaaaa")
        write(os.path.join(self.home, ".e2e-run-aaaaaa", ".e2e-nonce"), "aaaaaa")
        c, host = self.ctx("aaaaaa")
        self.assertFalse(ow.make_run_dir(c))
        clean, out = self.finish(c, host, {"run_dir": False})
        self.assertTrue(clean, out)
        self.assertEqual(read(os.path.join(self.home, ".e2e-run-aaaaaa", "theirs.txt")), "x")
        self.assertEqual(read(os.path.join(self.home, ".e2e-run-aaaaaa", ".e2e-nonce")), "aaaaaa")
        self.assertTrue(ow.remove_run_dir(c))
        self.assertEqual(read(os.path.join(self.home, ".e2e-run-aaaaaa", "theirs.txt")), "x")

    def set_alerts(self, token):
        """What `host alerts set` writes for the run with token: the helper's
        file, at the path it uses, with the run's folder in its command."""
        write(self.alerts_file, "[[destination]]\ntype = 'command'\nrun = 'cat >> \"$HOME/.e2e-run-%s/alerts.jsonl\"'\n" % token)

    def test_a_lost_answer_of_the_run_folder(self):
        # The host made the run's folder, and the answer was lost: the nonce
        # in it tells that it is the run's, and the clean-up removes it.
        for lost in [255, OSError("the host did not answer")]:
            with self.subTest(lost=lost):
                def after(cmd, data=None):
                    if cmd.startswith("mkdir ~/.e2e-run-"):
                        subprocess.run(["sh", "-c", cmd], env={"HOME": self.home, "PATH": os.environ.get("PATH", "")},
                                       input=data, stdin=None if data is not None else subprocess.DEVNULL,
                                       capture_output=True, timeout=10)
                        if isinstance(lost, Exception):
                            raise lost
                        return lost
                    return None
                c, host = self.ctx("aaaaaa", fail=after)
                with contextlib.redirect_stdout(io.StringIO()):
                    self.assertTrue(ow.make_run_dir(c))
                clean, out = self.finish(c, host, {})
                self.assertTrue(clean, out)
                self.assertFalse(os.path.exists(os.path.join(self.home, ".e2e-run-aaaaaa")))

    def test_a_run_folder_that_stops_part_way(self):
        # A removal that stops part way keeps the nonce file, so the next
        # try finds the folder the run's own; broken symlinks go too.
        c, host = self.ctx("aaaaaa")
        self.assertTrue(ow.make_run_dir(c))
        folder = os.path.join(self.home, ".e2e-run-aaaaaa")
        write(os.path.join(folder, ".hidden", "x"), "x")
        write(os.path.join(folder, "keep", "x"), "x")
        write(os.path.join(folder, "seq.bin"), "x")
        os.symlink("/no/such/target", os.path.join(folder, "broken"))
        bin_dir = os.path.join(self.home, "bin")
        write(os.path.join(bin_dir, "rm"), '#!/bin/sh\nfor a in "$@"; do case "$a" in */keep) exit 1;; esac; done\nexec /bin/rm "$@"\n')
        os.chmod(os.path.join(bin_dir, "rm"), 0o755)
        failing, _ = local_shell(self.home, path=bin_dir)
        self.assertFalse(ow.remove_run_dir(types.SimpleNamespace(host_rc=failing)))
        self.assertEqual(read(os.path.join(folder, ".e2e-nonce")), ow.NONCE)
        self.assertTrue(os.path.isdir(os.path.join(folder, "keep")))
        self.assertTrue(ow.remove_run_dir(c))
        self.assertFalse(os.path.exists(os.path.join(self.home, ".e2e-run-aaaaaa")))

    def test_a_nonce_file_that_does_not_read(self):
        # A folder of the run's name whose nonce file the shell cannot read:
        # the removal cannot tell that it is not the run's, and fails.
        nonce = os.path.join(self.home, ".e2e-run-aaaaaa", ".e2e-nonce")
        write(nonce, ow.NONCE)
        os.chmod(nonce, 0)
        self.addCleanup(os.chmod, nonce, 0o600)
        c, host = self.ctx("aaaaaa")
        if os.access(nonce, os.R_OK):
            self.skipTest("the tests run as a user who reads every file")
        self.assertFalse(ow.remove_run_dir(c))
        self.assertTrue(os.path.exists(nonce))

    def test_eio_on_stdout_does_not_stop_cleanup(self):
        # After SIGHUP every print raises EIO (items AB & 9.1): owned.say runs
        # before rm (for late5678), and finish still deletes both sessions,
        # removes the run folder, and releases the lock.
        c, host = self.ctx("aaaaaa", sessions={"mine1234": "e2e-x-aaaaaa", "late5678": "e2e-late-aaaaaa"})
        ow.take_lock(c)
        self.assertTrue(ow.make_run_dir(c))

        class BrokenPipeOut:
            def write(self, _s): raise OSError(5, "Input/output error")
            def flush(self): raise OSError(5, "Input/output error")
        old_run, ow.run_cmd = ow.run_cmd, host.run_cmd
        old_out, sys.stdout = sys.stdout, BrokenPipeOut()
        try:
            clean = rp.finish(c, ["mine1234"], {"before": set()})
        finally:
            sys.stdout, ow.run_cmd = old_out, old_run
        self.assertTrue(clean)
        self.assertEqual(sorted(deleted(host)), ["late5678", "mine1234"])
        self.assertFalse(os.path.exists(os.path.join(self.home, ".e2e-run-aaaaaa")))
        self.assertFalse(os.path.exists(os.path.join(self.home, ".e2e-lock")))


class Finish(unittest.TestCase):
    """run.py's last step runs every part, also when one part fails, checks
    each exit code, and run.py calls it in a finally around the whole suite."""
    def test_every_step_runs(self):
        host = Host({"mine1234": "e2e-x-aaaaaa"})
        c = Ctx(host)
        calls = []

        def host_rc(cmd, data=None):
            calls.append(cmd)
            if cmd.startswith("~/helper rm"):
                raise OSError("the host did not answer")
            return 0
        c.host_rc = host_rc
        c.lock = ow.RUN
        with fake_client(host) as out:
            clean = rp.finish(c, ["mine1234"], {})
        self.assertFalse(clean)
        self.assertIn("CLEAN-UP FAILED: the run's sessions mine1234 are not deleted. Delete each by its ID on the host: ~/helper rm mine1234", out.getvalue())
        self.assertTrue(any(".e2e-run-" in cmd and "rm -rf" in cmd for cmd in calls), calls)
        # A clean-up that failed keeps the lock, and says how to remove it.
        self.assertFalse([cmd for cmd in calls if ".e2e-lock" in cmd], calls)
        self.assertIn("the lock ~/.e2e-lock stays", out.getvalue())
        self.assertIn(ow.UNLOCK, out.getvalue())

    def test_each_exit_code(self):
        # Every command fails: finish says so for each, with its command,
        # and returns False.
        host = Host({"mine1234": "e2e-x-aaaaaa"})
        c = Ctx(host)
        c.host_rc = lambda cmd, data=None: 1
        c.lock = ow.RUN
        with fake_client(host) as out:
            clean = rp.finish(c, ["mine1234"], {})
        self.assertFalse(clean)
        text = out.getvalue()
        for want in ["~/helper rm mine1234", "rm -rf %s" % ow.run_dir(), ow.UNLOCK]:
            self.assertIn(want, text)

    def test_a_second_clean_up_finds_nothing_left(self):
        # agent_setup's own finally deletes its sessions from run.py's list:
        # clean_up takes each deleted ID off it, and keeps the one that the
        # helper did not delete, so run.py's last step tries only that one.
        host = Host({"mine1234": "e2e-x-aaaaaa", "stuck123": "e2e-y-aaaaaa"})
        c = Ctx(host)
        stuck = [True]

        def host_rc(cmd, data=None):
            if cmd.startswith("~/helper rm stuck123") and stuck[0]:
                host.ran.append(cmd)
                return 1  # the session is there, and the rm failed
            return host.host_rc(cmd, data)
        c.host_rc = host_rc
        created = ["mine1234", "stuck123"]
        self.assertEqual(ow.clean_up(c, created), ["stuck123"])
        self.assertEqual(created, ["stuck123"])
        stuck[0] = False
        with fake_client(host):
            self.assertTrue(rp.finish(c, created, {}))
        self.assertEqual(deleted(host), ["mine1234", "stuck123", "stuck123"])

    def test_a_signal_or_ctrl_c_in_a_step(self):
        # A KeyboardInterrupt in the delete of the sessions: every later step
        # still runs, and the clean-up says that it failed.
        host = Host({"mine1234": "e2e-x-aaaaaa"})
        c = Ctx(host, lock=ow.RUN)
        calls = []

        def host_rc(cmd, data=None):
            calls.append(cmd)
            if cmd.startswith("~/helper rm"):
                raise KeyboardInterrupt
            return 0
        c.host_rc = host_rc
        with fake_client(host) as out:
            self.assertFalse(rp.finish(c, ["mine1234"], {}))
        self.assertIn("CLEAN-UP FAILED: delete the run's sessions by ID: KeyboardInterrupt()", out.getvalue())
        self.assertTrue(any(".e2e-run-" in cmd for cmd in calls), calls)
        self.assertIn("the lock ~/.e2e-lock stays", out.getvalue())

    def test_a_list_that_fails(self):
        # The last list fails each time: an exit code, a timeout, or a list
        # that is no list. The clean-up fails, says how to list and delete
        # the run's sessions by its token, and keeps the lock.
        for name, answer in [("an exit code", (1, "", "Error")), ("a timeout", "timeout"), ("a list that is no list", (0, '{"v": 1}', ""))]:
            with self.subTest(name):
                host = Host({"late1234": ow.e2e_name("e2e-secret")})
                tries = []

                def run(argv, env=None, timeout=60, cwd=None):
                    tries.append(argv)
                    if answer == "timeout":
                        raise subprocess.TimeoutExpired(argv, timeout)
                    return answer
                host.run_cmd = run
                c = Ctx(host, lock=ow.RUN)
                with fake_client(host) as out:
                    clean = rp.finish(c, [], {})
                self.assertFalse(clean)
                self.assertEqual(len(tries), rp.LIST_TRIES)
                self.assertGreater(len(tries), 1)  # it tries again
                self.assertEqual(deleted(host), [])
                text = out.getvalue()
                for want in ["list its sessions on the laptop: client ls --host box.", "e2e-*-" + ow.RUN, "~/helper rm <id>", ow.UNLOCK]:
                    self.assertIn(want, text)
                self.assertFalse([cmd for cmd in host.ran if ".e2e-lock" in cmd])

    def test_a_list_with_a_malformed_collection(self):
        # The host's list has no list of sessions in it, but a {}, "", false,
        # 0, a missing key, or a session that is no object or has no ID: the
        # list failed. The clean-up fails, deletes nothing, and keeps the lock.
        late = {"id": "late1234", "name": ow.e2e_name("e2e-secret"), "state": "idle"}
        for name, sessions in [("null", None), ("an object", {}), ("a string", ""), ("false", False), ("0", 0), ("no key", "missing"),
                               ("a session that is a list", [["late1234"]]), ("a session with no ID", [{"name": late["name"]}]),
                               ("an ID that a host does not make", [dict(late, id="../x")])]:
            with self.subTest(name):
                host = Host({"late1234": late["name"]})
                row = {"v": 1, "host": "box"}
                if sessions != "missing":
                    row["sessions"] = sessions
                host.run_cmd = lambda argv, env=None, timeout=60, cwd=None: (0, json.dumps([row]), "")
                c = Ctx(host, lock=ow.RUN)
                with fake_client(host) as out:
                    self.assertFalse(rp.finish(c, [], {}))
                self.assertEqual(deleted(host), [])
                self.assertFalse([cmd for cmd in host.ran if ".e2e-lock" in cmd])
                self.assertIn("the lock ~/.e2e-lock stays", out.getvalue())
        # [] is the list of a host with no session: the client never prints null.
        host = Host()
        host.run_cmd = lambda argv, env=None, timeout=60, cwd=None: (0, json.dumps([{"v": 1, "host": "box", "sessions": []}]), "")
        with fake_client(host):
            self.assertTrue(rp.finish(Ctx(host, lock=ow.RUN), [], {}))

    def test_a_signal_in_the_wait_between_two_lists(self):
        # The first list fails, and a signal ends the wait before the second:
        # the clean-up goes on, lists again, deletes the run's session, and
        # gives up the lock.
        for ending in [SystemExit(143), KeyboardInterrupt()]:
            with self.subTest(ending=repr(ending)):
                host = Host({"mine1234": ow.e2e_name("e2e-x")})
                lists = []
                good = host.run_cmd

                def run(argv, env=None, timeout=60, cwd=None):
                    lists.append(argv)
                    return (1, "", "Error") if len(lists) == 1 else good(argv, env, timeout, cwd)
                host.run_cmd = run

                def wait(seconds):
                    raise ending
                c = Ctx(host, lock=ow.RUN)
                old, rp.time.sleep = rp.time.sleep, wait
                try:
                    with fake_client(host):
                        self.assertTrue(rp.finish(c, ["mine1234"], {}))
                finally:
                    rp.time.sleep = old
                self.assertEqual(len(lists), 2)
                self.assertEqual(deleted(host), ["mine1234"])
                self.assertTrue(any(".e2e-lock" in cmd and "rmdir" in cmd for cmd in host.ran))

    def test_a_signal_during_the_clean_up_is_ignored(self):
        # SIGTERM comes while a step runs. As run.py does, the run turns it
        # into an exit, but the clean-up ignores it until it ends: the step
        # finishes, and so does every step after it.
        host = Host({"mine1234": ow.e2e_name("e2e-x")})
        c = Ctx(host, lock=ow.RUN)
        real = host.host_rc

        def host_rc(cmd, data=None):
            if cmd.startswith("~/helper rm"):
                os.kill(os.getpid(), signal.SIGTERM)
                time.sleep(0.1)
            return real(cmd, data)
        c.host_rc = host_rc

        def stop(signum, frame):
            sys.exit(128 + signum)
        old = signal.signal(signal.SIGTERM, stop)
        try:
            with fake_client(host):
                self.assertTrue(rp.finish(c, ["mine1234"], {}))
            self.assertIs(signal.getsignal(signal.SIGTERM), stop)  # put back after the clean-up
        finally:
            signal.signal(signal.SIGTERM, old)
        self.assertEqual(deleted(host), ["mine1234"])
        self.assertTrue(any(".e2e-lock" in cmd and "rmdir" in cmd for cmd in host.ran))

    def test_the_lock_by_its_token(self):
        # The last step gives up the lock only when it holds the run's token.
        for lock, released in [(ow.RUN, True), ("dddddd", False), ("none", False)]:
            with self.subTest(lock=lock):
                host = Host()
                c = Ctx(host, lock=lock)
                with fake_client(host):
                    self.assertTrue(rp.finish(c, [], {}))
                self.assertEqual(any(".e2e-lock" in cmd and "rmdir" in cmd for cmd in host.ran), released)

    def test_a_session_that_came_late(self):
        # A slow host made a session of the run's after the list of its
        # start: finish finds it by the run's token in its name and its
        # newness, and deletes it by ID. Nothing else goes.
        mine = ow.e2e_name("e2e-secret")
        host = Host({"late1234": mine, "old12345": ow.e2e_name("e2e-old"), "runb1234": "e2e-secret-bbbbbb", "owner123": "api",
                     "known123": ow.e2e_name("e2e-x")})
        c = Ctx(host)
        with fake_client(host):
            clean = rp.finish(c, ["known123"], {"before": {"old12345", "runb1234", "owner123"}})
        self.assertTrue(clean)
        self.assertEqual(sorted(deleted(host)), ["known123", "late1234"])
        self.assertEqual(sorted(host.sessions), ["old12345", "owner123", "runb1234"])

    def test_no_session_adopted_when_before_is_none(self):
        # Item 6: when initial ls failed (before is None), finish never adopts
        # token-matching sessions that are not in created.
        name = ow.e2e_name("e2e-x")
        host = Host({"owner123": name})
        c = Ctx(host, lock=ow.RUN)
        created = []
        with fake_client(host) as out:
            clean = rp.finish(c, created, {"before": None})
        self.assertFalse(clean)
        self.assertEqual(deleted(host), [])
        self.assertEqual(created, [])
        self.assertEqual(host.sessions, {"owner123": name})
        self.assertIn("the list before the run failed", out.getvalue())
        self.assertFalse([cmd for cmd in host.ran if ".e2e-lock" in cmd])


if __name__ == "__main__":
    unittest.main()
