"""Offline tests of how the e2e harness owns the sessions that it starts
(owned.py, run_parts.py): python3 test/e2e/test_sessions.py. A session is
the run's own only by a new ID, and only its own sessions are attached to,
pulled from or deleted, by ID.
"""
import ast, contextlib, io, json, os, re, shutil, signal, subprocess, sys, tempfile, types, unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fakes import HERE, MODULES, OWNER, Ctx, Host, T, deleted, fake_client, local_shell, read, source, write  # noqa: E402
import owned as ow  # noqa: E402
import run_parts as rp  # noqa: E402


class Pull(unittest.TestCase):
    """run.py's pull check (run_parts.pull_check) owns only its new session,
    and its clean-up deletes only that session, by ID."""
    def run_pull(self, host):
        c, created = Ctx(host), []
        with tempfile.TemporaryDirectory(prefix="e2e-pull-root-") as laptop:
            c.root = types.SimpleNamespace(path=laptop)
            with fake_client(host) as out:
                rp.pull_check(c, {}, created)
                owned_ids = list(created)
                ow.clean_up(c, created)
        return c, owned_ids, out.getvalue()

    def test_two_runs_at_once(self):
        # Run B's e2e-pull session, and an older one, are on the host.
        other = {"runb5678": "e2e-pull-bbbbbb", "old45678": "e2e-pull-oooooo", "plain123": "e2e-pull"}
        host = Host(other)
        c, created, out = self.run_pull(host)
        self.assertEqual(created, ["mine1234"])
        self.assertEqual(deleted(host), ["mine1234"])
        for sid in other:
            self.assertIn(sid, host.sessions)
        self.assertTrue(all(ok for ok, _ in c.results), c.results)
        pulls = [a for a in host.calls if a[1] == "pull"]
        self.assertTrue(pulls and all(a[a.index("--") + 1] == "box/mine1234" for a in pulls), pulls)
        # The repository is in the run's own folder.
        self.assertTrue(any(cmd.startswith("mkdir %s/pull && " % ow.run_dir()) for cmd in host.ran), host.ran)

    def test_a_slow_start(self):
        # The start ends after its time. Only older e2e-pull sessions are
        # listed: none is the run's own, and none is deleted.
        host = Host({"old45678": "e2e-pull-oooooo", "plain123": "e2e-pull"}, start="never")
        c, created, out = self.run_pull(host)
        self.assertEqual((created, deleted(host)), ([], []))
        self.assertFalse(c.results[-1][0])
        self.assertIn("FAILED START " + ow.e2e_name("e2e-pull"), out)
        self.assertNotIn("pull", [a[1] for a in host.calls])

    def test_a_slow_start_that_made_its_session(self):
        # The start ends after its time, but the host made the session: the
        # run finds it by its own unique name and newness, says so, and
        # deletes it by ID. The older sessions stay.
        host = Host({"old45678": "e2e-pull-oooooo"}, start="timeout")
        c, created, out = self.run_pull(host)
        self.assertEqual((created, deleted(host)), (["mine1234"], ["mine1234"]))
        self.assertIn("old45678", host.sessions)
        self.assertIn("FAILED START " + ow.e2e_name("e2e-pull"), out)
        self.assertIn("mine1234", out)

    def test_no_pull_unless_the_address_is_exact(self):
        # After the start, the run's session is gone, and another is named
        # like its ID: the address would reach that other session. The
        # check runs no pull, fails, and its clean-up deletes by ID alone.
        host = Host({"old45678": "e2e-pull-oooooo"})
        new = host.run_cmd

        def run(argv, env=None, timeout=60, cwd=None):
            r = new(argv, env, timeout, cwd)
            if argv[1] == "new":
                host.sessions = {"old45678": "e2e-pull-oooooo", "other123": "mine1234"}
            return r
        host.run_cmd = run
        c, created, out = self.run_pull(host)
        self.assertNotIn("pull", [a[1] for a in host.calls])
        self.assertIn((False, "pull: box/mine1234 is the run's own session, and its address is exact"), c.results)
        self.assertEqual(deleted(host), ["mine1234"])
        self.assertEqual(sorted(host.sessions), ["old45678", "other123"])

    def test_never_by_name_from_a_list(self):
        # The start answers with the ID of a session that ran before: the
        # check owns nothing, pulls nothing and deletes nothing.
        host = Host({"old45678": ow.e2e_name("e2e-pull")}, made_id="old45678")
        c, created, out = self.run_pull(host)
        self.assertEqual((created, deleted(host)), ([], []))
        self.assertNotIn("pull", [a[1] for a in host.calls])

    def test_pull_check_isolates_host_git_config(self):
        # pull_check and client.SessionRepo/FetchArgs must ignore a planted
        # host $HOME/.gitconfig (core.fsmonitor, hooksPath, packObjectsHook).
        pull_go = read(os.path.join(HERE, "..", "..", "internal", "client", "pull.go"))
        rgit = re.search(r'remoteGit\s*=\s*"([^"]+)"', pull_go)
        rupload = re.search(r'remoteUploadPack\s*=\s*"([^"]+)"', pull_go)
        rcmd = re.search(r'fmt\.Sprintf\(`([^`]+)`', pull_go)
        self.assertTrue(rgit and rupload and rcmd, "pull.go must define remoteGit, remoteUploadPack and SessionRepo cmd")
        d = tempfile.mkdtemp(prefix="e2e-pull-iso-")
        try:
            host_home = os.path.join(d, "host")
            os.makedirs(os.path.join(host_home, ".e2e-run-" + ow.RUN), mode=0o700)
            marker = os.path.join(d, "gitconfig-ran")
            probe = os.path.join(d, "probe.sh")
            write(probe, "#!/bin/sh\necho ran > '%s'\nexit 1\n" % marker)
            os.chmod(probe, 0o755)
            write(os.path.join(host_home, ".gitconfig"),
                  "[core]\n\tfsmonitor = %s\n\thooksPath = %s/hooks\n[uploadpack]\n\tpackObjectsHook = %s\n"
                  % (probe, host_home, probe))
            fake_ssh = os.path.join(d, "fake-ssh.sh")
            write(fake_ssh, "#!/bin/sh\nfor a; do last=$a; done\nHOME='%s' exec sh -c \"$last\"\n" % host_home)
            os.chmod(fake_ssh, 0o755)
            rc_fn, out_fn = local_shell(host_home)
            host = Host()
            base_run = host.run_cmd
            repo_path = os.path.join(host_home, ".e2e-run-" + ow.RUN, "pull")

            def run_cmd(argv, env=None, timeout=60, cwd=None):
                if argv[0] == "git":
                    p = subprocess.run(argv, env=env, cwd=cwd, stdin=subprocess.DEVNULL,
                                       capture_output=True, text=True, timeout=30)
                    return p.returncode, p.stdout, p.stderr
                if len(argv) > 1 and argv[1] == "pull":
                    host.calls.append(argv)
                    if "--into" in argv:
                        return 1, "", "Error: mine is the branch you have checked out here"
                    info = out_fn(rcmd.group(1) % ("'" + repo_path + "'", rgit.group(1), rgit.group(1), rgit.group(1)))
                    lines = [ln.strip() for ln in info.strip().splitlines()]
                    if len(lines) < 3:
                        return 1, "", "SessionRepo failed"
                    fenv = dict(env or os.environ)
                    fenv["GIT_SSH_COMMAND"] = fake_ssh
                    spec = "refs/heads/%s:refs/heads/%s" % (lines[1], lines[1])
                    p = subprocess.run(["git", "fetch", "--upload-pack=" + rupload.group(1), "--no-tags",
                                        "box:" + lines[0], spec],
                                       env=fenv, cwd=cwd, stdin=subprocess.DEVNULL,
                                       capture_output=True, text=True, timeout=30)
                    return p.returncode, p.stdout, p.stderr
                return base_run(argv, env, timeout, cwd)

            host.run_cmd = run_cmd
            c, created = Ctx(host), []
            c.host_rc, c.host_out = rc_fn, out_fn
            envx = {"HOME": os.path.join(d, "client"), "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
                    "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null"}
            os.makedirs(envx["HOME"], mode=0o700)
            c.root = types.SimpleNamespace(path=envx["HOME"])
            with fake_client(host):
                rp.pull_check(c, envx, created)
            self.assertTrue(all(ok for ok, _ in c.results), c.results)
            self.assertFalse(os.path.exists(marker), "host .gitconfig probe executed during pull_check")
        finally:
            shutil.rmtree(d, ignore_errors=True)


class Script(unittest.TestCase):
    """run.py's script check (run_parts.json_check) deletes its session by
    ID, through the helper's rm: never by the client's kill, whose address
    could reach another session named like the ID."""
    def test_a_delete_by_id(self):
        host = Host({"owner123": "api"})
        c, created = Ctx(host), []
        with fake_client(host):
            rp.json_check(c, {}, created)
        self.assertEqual((deleted(host), created), (["mine1234"], []))
        self.assertTrue(all(ok for ok, _ in c.results), c.results)
        self.assertEqual(host.sessions, {"owner123": "api"})

    def test_the_session_goes_between_the_guard_and_the_delete(self):
        # The session goes away after the start, and the owner's session
        # named like its ID comes: the delete by ID reaches nothing of the
        # owner's, and the ID stays on the list for the last clean-up.
        host = Host({"owner123": "api"})
        new = host.run_cmd

        def run(argv, env=None, timeout=60, cwd=None):
            r = new(argv, env, timeout, cwd)
            if argv[1] == "new":
                host.sessions = {"owner123": "api", "owner456": "mine1234"}
            return r
        host.run_cmd = run
        c, created = Ctx(host), []
        with fake_client(host):
            rp.json_check(c, {}, created)
        self.assertEqual(deleted(host), ["mine1234"])
        self.assertEqual(host.sessions, {"owner123": "api", "owner456": "mine1234"})
        self.assertEqual(created, [])  # gone: it counts as deleted
        self.assertFalse([a for a in host.calls if a[1] in ("kill", "rm")])



class Starts(unittest.TestCase):
    """The shared starts: a start that does not answer as expected is
    named, and what it made is found by the run's unique name and newness."""
    def test_a_start_that_ends_after_its_time_or_is_not_utf8(self):
        for start in ["timeout", "not utf-8"]:
            with self.subTest(start):
                name = ow.e2e_name("e2e-secret")
                host = Host({"older123": name, "other123": "e2e-secret-zzzzzz"}, start=start)
                c, created = Ctx(host), []
                with fake_client(host) as out:
                    sid, _, why = ow.start_new(c, {}, created, name, "--", *ow.program("/bin/sh", "true"))
                self.assertIsNone(sid)
                self.assertEqual(created, ["mine1234"])  # new, and of the run's own name
                self.assertIn("FAILED START " + name, out.getvalue())
                self.assertIn("mine1234", out.getvalue())

    def test_a_name_that_is_not_the_runs_own_is_named_not_deleted(self):
        # A login-claude session appeared after the start: it may be the
        # owner's, so the check names it and deletes nothing.
        host = Host({"owner123": "api", "new12345": "login-claude"})
        c, created = Ctx(host), []
        with fake_client(host) as out:
            made = ow.leftovers(c, {}, "login-claude", [{"id": "owner123", "name": "api"}], created, "agents login --json did not end")
        self.assertEqual((made, created, deleted(host)), (["new12345"], [], []))
        self.assertIn("may be the owner's", out.getvalue())

    def at_terminal(self, buf, code=0, made=True):
        """start_on_terminal with a client that prints buf and ends with
        code. The host makes the session mine5678 while new runs, when made."""
        name = ow.e2e_name("e2e-counter")
        host = Host({"owner123": "api"}, start="never")
        c, created = Ctx(host), []

        def term(argv, env=None):
            if made:
                host.sessions["mine5678"] = name
            return T(buf, code)
        c.Term = term
        with fake_client(host) as out:
            sid = ow.start_on_terminal(c, {}, created, name, ["--"] + ow.program("/bin/sh", "sleep 60"))
        return sid, created, out.getvalue()

    def own_line(self, sid="mine5678"):
        return ("Started box/%s (%s, custom).\r\n" % (sid, ow.e2e_name("e2e-counter"))).encode()

    def test_start_on_terminal_takes_the_clients_own_last_line(self):
        # run.py's start(): a repository path that holds "Started box/owner123"
        # comes before the client's own line. The check owns only the new ID
        # in that last line.
        spoof = b"The repository /srv/x\r\n" + self.own_line("owner123") + b" has a setup script:\r\n"
        sid, created, out = self.at_terminal(spoof + self.own_line())
        self.assertEqual((sid, created), ("mine5678", ["mine5678"]))
        sid, created, out = self.at_terminal(spoof, made=False)
        self.assertEqual((sid, created), (None, []))

    def test_bytes_that_are_not_utf8_at_the_terminal(self):
        # The client's own line is there, but the terminal holds a byte that
        # is not UTF-8: a failed start, named, and its session found by its
        # unique name and newness, for the delete by ID.
        sid, created, out = self.at_terminal(b"\xff" + self.own_line())
        self.assertIsNone(sid)
        self.assertIn("FAILED START %s: new printed bytes that are not UTF-8" % ow.e2e_name("e2e-counter"), out)
        self.assertEqual(created, ["mine5678"])

    def test_a_client_that_did_not_end_with_0(self):
        # The client printed its own line of a new ID, and ended with 1: the
        # start owns nothing by that line, and goes to leftovers.
        sid, created, out = self.at_terminal(self.own_line(), code=1)
        self.assertIsNone(sid)
        self.assertIn("FAILED START %s: new ended with 1" % ow.e2e_name("e2e-counter"), out)
        self.assertEqual(created, ["mine5678"])  # by leftovers, not by the line


if __name__ == "__main__":
    unittest.main()
