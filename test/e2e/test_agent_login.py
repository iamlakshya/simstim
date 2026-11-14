"""Offline tests of the login check in agent_login.py:
python3 test/e2e/test_agent_login.py.

They replace run_cmd, the host's shell and the terminal, so they run no
command and reach no host. The check owns a session only from a successful
`agents login --json` answer with "started": true. A list that failed, an
owner's login, an error whose text names a session, and every other answer
leave the owner's sessions alone: none of them reaches the list of sessions
to delete. The client is closed on every path, also when the detach fails.
"""
import contextlib, io, json, os, re, shutil, subprocess, sys, tempfile, types, unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import agent_login as al  # noqa: E402
import owned as ow  # noqa: E402

from login_fakes import Ctx, DETACHED, FAILED, HOSTILE, Host, LINK, MANIFEST, PROMPT, PROMPT_NO_LISTENER, READY, RM, Run, Term, WARNING_DONE, WARNING_ON, login, ls, rm_of, started  # noqa: E402


class LoginCheck(unittest.TestCase):
    def setUp(self):
        Term.made = []
        al.LOGIN_WAIT, al.PROMPT_WAIT, al.EXIT_WAIT = 0.2, 0.2, 0.1

    def check(self, run, ctx=None):
        c, created = ctx or Ctx([PROMPT, DETACHED]), []
        old, ow.run_cmd = ow.run_cmd, run
        said = io.StringIO()
        try:
            with contextlib.redirect_stdout(said):
                al.login_checks(c, {}, created)
        finally:
            ow.run_cmd = old
            self.said = said.getvalue()
        for argv in run.calls:
            self.assertNotIn("kill", argv)
            self.assertNotIn("rm", argv)
        self.assertEqual(c.host.ran, [])  # the check deletes nothing itself
        for ch in self.said:
            self.assertFalse(ow.hidden(ch), "the check printed %r" % ch)
        return c, created

    def assertSkipped(self, c, created):
        self.assertEqual(created, [])
        self.assertEqual([m for ok, m in c.results if not ok], [])
        self.assertEqual(Term.made, [])  # no attach
        self.assertIn("SKIP login", self.said)

    def test_a_login_of_its_own(self):
        c, created = self.check(Run(start=started("mine1")))
        self.assertEqual(created, ["mine1"])
        t = Term.made[0]
        self.assertEqual(t.argv, ["client", "attach", "--id", "--", "box/mine1"])
        self.assertEqual(t.sent, [b"\x1c"])
        self.assertTrue(t.closed)
        self.assertTrue(all(ok for ok, _ in c.results), c.results)
        self.assertEqual(c.ssh, ["cat ~/.local/state/client/sessions/mine1/manifest.json"])

    def test_a_list_that_failed(self):
        for answer in [(1, "", "Error: cannot reach box"), (0, "not json", ""), (0, "[]", ""), "timeout",
                       ls(login("owner1", "exited"), error="the host did not answer"),
                       (0, json.dumps([{"host": "other", "sessions": []}]), ""),
                       (0, json.dumps([{"host": "box", "sessions": "none"}]), ""),
                       (0, json.dumps([{"host": "box", "sessions": [{"name": "login-claude"}]}]), "")]:
            run = Run(lists=[answer])
            c, created = self.check(run)
            self.assertSkipped(c, created)
            self.assertFalse([a for a in run.calls if a[1] == "agents"], answer)  # no login started

    def test_host_doctor_failed_or_logged_in(self):
        logged = {"agent_checks": [{"kind": "claude", "installed": True, "on_path": True, "login": "yes"}]}
        for doctor in [(1, "", ""), (0, "{", ""), (0, json.dumps(logged), ""), (0, json.dumps({"agent_checks": []}), "")]:
            run = Run(doctor=doctor)
            c, created = self.check(run)
            self.assertSkipped(c, created)
            self.assertFalse([a for a in run.calls if a[1] in ("ls", "agents")])

    def test_an_owners_login_that_runs(self):
        c, created = self.check(Run(lists=[ls(login("owner1"))]))
        self.assertSkipped(c, created)

    def test_an_owners_login_that_starts_while_it_waits(self):
        # The list showed none, then the owner's login started first: the
        # command finds it, and says that it did not start it.
        c, created = self.check(Run(start=started("owner1", started=False)))
        self.assertSkipped(c, created)

    def test_spoofed_starts(self):
        # Answers whose text names a session, or that fail / print malformed
        # JSON or unexpected stderr after preconditions pass: every one fails
        # the check (c.failed > 0) while attaching to or deleting nothing.
        error = {"v": 1, "error": {"code": "busy", "kind": "failed", "message": "host refused: It runs in the session login-claude (box/ownerabc).", "exit": 1}}
        for name, answer in {
            "an error whose text holds the old start phrase": (1, "", "Error: host refused: It runs in the session login-claude (box/ownerabc).\n"),
            "a JSON error on stderr": (1, "", json.dumps(error) + "\n"),
            "a JSON error object with an id": (0, json.dumps({"v": 1, "error": {"message": "x"}, "id": "ownerabc", "started": True, "host": "box", "name": "login-claude", "address": "box/ownerabc"}), ""),
            "a failed command with a start on stdout": (1, started("ownerabc")[1], ""),
            "started false with no concurrent session": started("ownerabc", started=False),
            "started as a string": started("ownerabc", started="true"),
            "no started": started("ownerabc", started=None),
            "an ID that a host does not make": started("x\x1b]52;c;aGk=\x07"),
            "an ID with a slash": started("../owner"),
            "an address of another session": started("mine1", address="box/ownerabc"),
            "another host": started("mine1", host="other"),
            "another name": started("ownerabc", name="api"),
            "no v": started("mine1", v=None),
            "two JSON values": (0, started("mine1")[1] + started("ownerabc")[1], ""),
            "text before the JSON": (0, "It runs in the session login-claude (box/ownerabc).\n" + started("mine1")[1], ""),
            "an error on stderr after a start": (0, started("mine1")[1], json.dumps(error) + "\n"),
            "text on stderr after a start": (0, started("mine1")[1], "It runs in the session login-claude (box/ownerabc).\n"),
            # Only the warnings that the command prints, in their shape.
            "an object with an ID on stderr": (0, started("mine1")[1], json.dumps({"id": "ownerabc"}) + "\n"),
            "a session on stderr": (0, started("mine1")[1], started("ownerabc")[1].replace("\n", "") + "\n"),
            "a warning of another code": (0, started("mine1")[1], json.dumps({"v": 1, "warning": {"code": "old_flag", "message": "m"}}) + "\n"),
            "a warning of another version": (0, started("mine1")[1], json.dumps({"v": 2, "warning": {"code": "debug_incomplete", "message": "m"}}) + "\n"),
            "a warning with more keys": (0, started("mine1")[1], json.dumps({"v": 1, "warning": {"code": "debug_incomplete", "message": "m", "id": "ownerabc"}}) + "\n"),
            "a warning beside more keys": (0, started("mine1")[1], json.dumps({"v": 1, "id": "ownerabc", "warning": {"code": "debug_incomplete", "message": "m"}}) + "\n"),
            "a warning whose message is no string": (0, started("mine1")[1], json.dumps({"v": 1, "warning": {"code": "debug_incomplete", "message": ["box/ownerabc"]}}) + "\n"),
            "a list on stderr": (0, started("mine1")[1], "[]\n"),
            # A version that Python counts as 1, and stderr that a split at
            # every line break, or a blank-line filter, would hide.
            "a warning of version true": (0, started("mine1")[1], json.dumps({"v": True, "warning": {"code": "debug_on", "message": "m"}}) + "\n"),
            "a warning of version 1.0": (0, started("mine1")[1], json.dumps({"v": 1.0, "warning": {"code": "debug_on", "message": "m"}}) + "\n"),
            "a session of version true": started("mine1", v=True),
            "a form feed alone on stderr": (0, started("mine1")[1], "\x0c"),
            "a form feed line on stderr": (0, started("mine1")[1], "\x0c\n"),
            "a vertical tab on stderr": (0, started("mine1")[1], "\x0b\n"),
            "spaces on a line of their own": (0, started("mine1")[1], "   \n"),
            "a blank line among warnings": (0, started("mine1")[1], WARNING_ON + "\n\n" + WARNING_DONE + "\n"),
            "a warning with a carriage return": (0, started("mine1")[1], WARNING_ON + "\r\n"),
            "a warning with a space before it": (0, started("mine1")[1], " " + WARNING_ON + "\n"),
            "a warning split at a line separator": (0, started("mine1")[1], WARNING_ON + "\u2028" + json.dumps({"id": "ownerabc"}) + "\n"),
            "a warning with a control character in it": (0, started("mine1")[1], WARNING_ON.replace("debug on", "debug\x1b on") + "\n"),
            "an array": (0, "[" + started("mine1")[1] + "]", ""),
        }.items():
            with self.subTest(name):
                Term.made = []
                c, created = self.check(Run(start=answer, after=ls()))
                self.assertEqual(created, [])
                self.assertEqual(Term.made, [])
                self.assertGreater(c.failed, 0, c.results)
                self.assertNotIn("SKIP login", self.said)

    def test_a_start_that_fails_is_a_failed_check(self):
        # A start that did not end, or printed what is not UTF-8, is a
        # failed check, never a skip. Its ownership stays as careful: a
        # login-claude session that came after it may be the owner's, so
        # nothing is attached to, owned or deleted.
        for answer in ["timeout", "not utf-8"]:
            with self.subTest(answer):
                Term.made = []
                host = Host({"late1": "login-claude"})
                c, created = self.check(Run(start=answer, after=ls(login("late1"))), Ctx([PROMPT, DETACHED], host=host))
                self.assertEqual(created, [])
                self.assertEqual(Term.made, [])
                self.assertEqual(host.sessions, {"late1": "login-claude"})
                failed = [m for ok, m in c.results if not ok]
                self.assertEqual(len(failed), 1, c.results)
                self.assertIn("login: agents login claude --json starts a login session of its own", failed[0])
                self.assertNotIn("SKIP login", self.said)
                self.assertIn("FAILED START login-claude", self.said)
                self.assertIn("late1", self.said)  # named, and left alone

    def test_the_warnings_that_the_command_prints_are_no_failure(self):
        # A debug file in the environment turns the client's debug mode on:
        # the command then prints debug_on, and maybe debug_incomplete. The
        # start stays the check's own, and the clean-up deletes it by ID.
        for err in [WARNING_ON + "\n", WARNING_DONE + "\n", WARNING_ON + "\n" + WARNING_DONE + "\n", WARNING_ON, ""]:
            with self.subTest(err=err):
                Term.made = []
                host = Host({"mine1": "login-claude"})
                c, created = self.check(Run(start=(0, started("mine1")[1], err)), Ctx([PROMPT, DETACHED], host=host))
                self.assertEqual(created, ["mine1"])
                self.assertEqual(len(Term.made), 1)
                self.clean_up(c, created)
                self.assertEqual(host.ran, ["~/helper rm mine1"])
                self.assertEqual(host.sessions, {})

    def test_a_manifest_that_does_not_read(self):
        # run.py's host_ssh raises when cat fails. When the session went
        # away, the check fails without attaching. When it is there, the check
        # fails that one line and goes on. The suite never stops, and the
        # clean-up by ID runs after it.
        failed = subprocess.CalledProcessError(1, ["ssh", "box", "cat"])
        for after, gone in [(ls(), True), (ls({"id": "other123", "name": "mine1", "state": "idle"}), True),
                            (ls(login("mine1", "starting")), False)]:
            with self.subTest(after=after):
                Term.made = []
                host = Host({"mine1": "login-claude"} if not gone else {"other123": "mine1"})
                c, created = self.check(Run(after=after), Ctx([PROMPT, DETACHED], manifest=failed, host=host))
                self.assertEqual(created, ["mine1"])
                if gone:
                    self.assertEqual(Term.made, [])
                    self.assertTrue(any("went away" in m for ok, m in c.results if not ok), c.results)
                    self.assertNotIn("SKIP login", self.said)
                    self.assertGreater(c.failed, 0, c.results)
                    self.assertEqual([ok for ok, _ in c.results], [True, False])
                else:
                    self.assertEqual(len(Term.made), 1)
                    self.assertEqual([ok for ok, _ in c.results], [True, False, True, True])
                self.clean_up(c, created)
                self.assertEqual(host.ran, ["~/helper rm mine1"])
                self.assertNotIn("mine1", host.sessions)
                if gone:
                    self.assertEqual(host.sessions, {"other123": "mine1"})

    def test_the_owned_session_is_gone_and_another_is_named_like_it(self):
        # mine1 started, then went before the attach. other123 is named
        # mine1, so box/mine1 would now reach it. The check fails without
        # attaching, and the clean-up, by ID alone, leaves other123 alone.
        host = Host({"other123": "mine1"})
        c, created = self.check(Run(after=ls({"id": "other123", "name": "mine1", "state": "idle"})), Ctx([PROMPT], host=host))
        self.assertEqual(created, ["mine1"])
        self.assertEqual(Term.made, [])
        self.assertGreater(c.failed, 0, c.results)
        self.assertNotIn("SKIP login", self.said)
        self.clean_up(c, created)
        self.assertEqual(host.ran, ["~/helper rm mine1"])
        self.assertEqual(host.sessions, {"other123": "mine1"})

    def clean_up(self, c, created):
        """Runs ow.clean_up, with a client that must not run: the clean-up
        never goes through the client's rm or kill, or an address."""
        calls = []
        old, ow.run_cmd = ow.run_cmd, lambda argv, env=None, timeout=60: calls.append(argv) or (1, "", "")
        try:
            ow.clean_up(c, created)
        finally:
            ow.run_cmd = old
        self.assertEqual(calls, [])

    def test_no_attach_unless_the_address_is_exact(self):
        # The session is there, but so is another named like its ID; the ID
        # is there under another name; or the list before the attach fails:
        # each fails the check without attaching.
        for after in [ls(login("mine1", "starting"), {"id": "other123", "name": "mine1", "state": "idle"}),
                      ls({"id": "mine1", "name": "api", "state": "idle"}),
                      (1, "", "Error: cannot reach box"), ls(login("mine1"), error="the host did not answer"), ls()]:
            with self.subTest(after=after):
                Term.made = []
                c, created = self.check(Run(after=after))
                self.assertEqual(created, ["mine1"])
                self.assertEqual(Term.made, [])
                self.assertGreater(c.failed, 0, c.results)

    def test_a_start_that_names_a_session_that_ran_before(self):
        # The start says "started": true, but its ID was in the list before
        # it: an ended login of the owner's, or a session of the owner's of
        # another name. The check fails, and neither attaches nor deletes it.
        before = ls(login("old1", "exited"), {"id": "owner1", "name": "api", "state": "idle"})
        for sid in ["old1", "owner1"]:
            with self.subTest(sid=sid):
                Term.made = []
                host = Host({"old1": "login-claude", "owner1": "api"})
                c, created = self.check(Run(lists=[before], start=started(sid), after=before), Ctx([PROMPT], host=host))
                self.assertEqual(created, [])
                self.assertEqual(Term.made, [])
                self.assertGreater(c.failed, 0, c.results)
                self.assertTrue(any("ran before" in m for ok, m in c.results if not ok), c.results)
                self.assertNotIn("SKIP login", self.said)
                self.clean_up(c, created)
                self.assertEqual(host.ran, [])
                self.assertEqual(host.sessions, {"old1": "login-claude", "owner1": "api"})

    def test_clean_up_by_id_alone(self):
        # gone1234 went before the clean-up, and other123 is named like it:
        # other123 stays. An ID that a host does not make is never sent.
        host = Host({"keep1111": "e2e-secret", "other123": "gone1234"})
        c = Ctx(host=host)
        self.clean_up(c, ["gone1234", "keep1111", "bad id; rm -rf ~", "../x"])
        self.assertEqual(host.ran, ["~/helper rm gone1234", "~/helper rm keep1111"])
        self.assertEqual(host.sessions, {"other123": "gone1234"})

    def test_a_prompt_that_never_shows(self):
        # The attach shows nothing, and never ends: the check detaches,
        # kills its client, and the session is still its own.
        c, created = self.check(Run(), Ctx(["connecting...\x1b]52;c;aGk=\x07\u009b".encode()], code=None))
        self.assertEqual(created, ["mine1"])
        t = Term.made[0]
        self.assertEqual(t.sent, [b"\x1c"])
        self.assertTrue(t.closed)
        self.assertEqual([ok for ok, _ in c.results], [True, True, False, False])
        self.assertIn("connecting...?]52;c;aGk=??", self.said)

    def test_a_detach_that_fails(self):
        # An error from the detach key, or from the wait, still closes the
        # client. The session stays the check's own, for the caller to delete.
        for fail in ["send", "exit_code"]:
            with self.subTest(fail):
                Term.made = []
                created = []
                old, ow.run_cmd = ow.run_cmd, Run()
                try:
                    with contextlib.redirect_stdout(io.StringIO()), self.assertRaises(OSError):
                        al.login_checks(Ctx([PROMPT], fail=fail), {}, created)
                finally:
                    ow.run_cmd = old
                self.assertEqual(created, ["mine1"])
                self.assertTrue(Term.made[0].closed)

    def test_a_manifest_without_the_login(self):
        c, created = self.check(Run(), Ctx([PROMPT, DETACHED], manifest=b"not json" + HOSTILE.encode()))
        self.assertEqual(created, ["mine1"])
        self.assertEqual([ok for ok, _ in c.results], [True, False, True, True])

    def test_safe_text(self):
        got = ow.safe_text(("a" + HOSTILE + "\nb").encode())
        self.assertEqual(got, "a?]52;c;aGk=? ?2J ? ? ? ? ? ? ?\nb")
        for ch in got:
            self.assertFalse(ow.hidden(ch))
        self.assertEqual(ow.safe_text("plain é ü"), "plain é ü")
        self.assertEqual(ow.safe_text(b"\xff"), "�")


if __name__ == "__main__":
    unittest.main()
