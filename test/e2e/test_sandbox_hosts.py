"""Offline tests of the session cleanup in sandbox_hosts.py:
python3 test/e2e/test_sandbox_hosts.py.

They replace run_cmd, so they run no command and reach no host. A cleanup
passes only when the host confirms that the session is gone. A timeout, a
failed command, an answer that is not JSON and an error for the host all
fail it, and keep the throwaway folder.
"""
import contextlib, io, json, os, shutil, subprocess, sys, tempfile, unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import sandbox_hosts as sh  # noqa: E402

NAME = "e2e-forward-0a1b2c3d"


class Fake:
    """Answers each call of run_cmd with the next answer: a tuple
    (exit code, stdout, stderr), or "timeout"."""
    def __init__(self, *answers):
        self.answers = list(answers)
        self.calls = []

    def __call__(self, argv, env=None, timeout=60):
        self.calls.append(argv)
        answer = self.answers.pop(0) if self.answers else (0, "", "")
        if answer == "timeout":
            raise subprocess.TimeoutExpired(argv, timeout)
        return answer


def ls(*sessions, error=None):
    """The answer of `ls --json --host box`."""
    row = {"host": "box", "sessions": list(sessions) or None}
    if error:
        row["error"] = error
    return (0, json.dumps([row]), "")


def raw(rows):
    """An answer of ls whose rows are given as they are."""
    return (0, json.dumps(rows), "")


class Ctx:
    cli, HOST, NAME, CONTROL_PREFIX = "client", "box", "client", "nf-"

    def __init__(self):
        self.results = []

    def ok(self, cond, msg):
        self.results.append((bool(cond), msg))
        return cond


class FindSession(unittest.TestCase):
    def find(self, *answers):
        old, sh.run_cmd = sh.run_cmd, Fake(*answers)
        try:
            return sh.find_session(Ctx(), {}, NAME)
        finally:
            sh.run_cmd = old

    def test_found(self):
        self.assertEqual(self.find(ls({"name": NAME, "id": "abc"})), ("abc", None))

    def test_confirmed_absent(self):
        self.assertEqual(self.find(ls({"name": "other", "id": "x"})), (None, None))
        self.assertEqual(self.find(ls()), (None, None))  # sessions: null
        self.assertEqual(self.find(raw([{"host": "box", "sessions": []}])), (None, None))

    def test_could_not_tell(self):
        for answer in ["timeout", (1, "", "ssh: connect to host box port 22: Connection refused"), (0, "not json", ""),
                       (0, "[]", ""), ls(error="offline"), (0, json.dumps({"host": "box"}), ""),
                       ls({"name": NAME}),
                       # Shapes that cannot say that a session is absent.
                       raw([{"host": "box", "sessions": {}}]), raw([{"host": "box"}]),
                       raw([{"host": "box", "sessions": [None]}]), raw([{"host": "box", "sessions": "none"}]),
                       raw([None, {"host": "box", "sessions": []}]),
                       # Sessions whose name or ID is not a string.
                       ls({"id": "abc"}), ls({"name": NAME, "id": 123}), ls({"name": "", "id": "abc"})]:
            sid, why = self.find(answer)
            self.assertIsNone(sid, answer)
            self.assertTrue(why, answer)


class Cleanup(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="e2e-test-")
        self.addCleanup(shutil.rmtree, self.tmp, True)

    def clean(self, sid, *answers, tried=True):
        c, fake = Ctx(), Fake(*answers)
        old, sh.run_cmd = sh.run_cmd, fake
        try:
            with contextlib.redirect_stdout(io.StringIO()) as out:
                sh.cleanup(c, {"ssh": "box"}, self.tmp, {}, NAME, sid, None, tried)
        finally:
            sh.run_cmd = old
        return c.results, fake.calls, out.getvalue()

    def assert_failed(self, results, out):
        self.assertEqual(len(results), 1)
        self.assertFalse(results[0][0], results)
        self.assertTrue(os.path.isdir(self.tmp), "the throwaway folder was removed")
        self.assertIn("client kill box/", out)

    def test_confirmed_deleted(self):
        results, calls, _ = self.clean("abc", (0, "", ""), ls())
        self.assertEqual([r[0] for r in results], [True])
        self.assertEqual(calls[0], ["client", "kill", "box/abc"])
        self.assertFalse(os.path.exists(self.tmp))

    def test_kill_fails(self):
        results, calls, out = self.clean("abc", (1, "", "no session abc"))
        self.assert_failed(results, out)
        self.assertEqual(len(calls), 1)

    def test_kill_times_out(self):
        results, _, out = self.clean("abc", "timeout")
        self.assert_failed(results, out)

    def test_listing_after_kill_times_out(self):
        results, _, out = self.clean("abc", (0, "", ""), "timeout")
        self.assert_failed(results, out)

    def test_listing_after_kill_fails(self):
        results, _, out = self.clean("abc", (0, "", ""), (1, "", "ssh: Could not resolve hostname box"))
        self.assert_failed(results, out)

    def test_host_error_after_kill(self):
        results, _, out = self.clean("abc", (0, "", ""), ls(error="offline"))
        self.assert_failed(results, out)

    def test_shapes_that_cannot_say(self):
        for answer in [raw([{"host": "box", "sessions": {}}]), raw([{"host": "box"}]),
                       raw([{"host": "box", "sessions": [None]}]), ls({"id": "abc"}), ls({"name": NAME, "id": 123})]:
            self.setUp()
            results, _, out = self.clean("abc", (0, "", ""), answer)
            self.assert_failed(results, out)

    def test_an_id_that_is_not_a_string(self):
        # new timed out, and ls lists the session with an ID of another type.
        results, calls, out = self.clean(None, ls({"name": NAME, "id": 123}))
        self.assert_failed(results, out)
        self.assertEqual(len(calls), 1, "it ran kill with that ID")

    def test_still_listed(self):
        results, _, out = self.clean("abc", (0, "", ""), ls({"name": NAME, "id": "abc"}))
        self.assert_failed(results, out)

    def test_unknown_id_and_no_answer(self):
        # new timed out, and the lookup cannot tell whether it made a session.
        results, calls, out = self.clean(None, "timeout")
        self.assert_failed(results, out)
        self.assertEqual(len(calls), 1, "it ran kill without an ID")

    def test_unknown_id_found_then_deleted(self):
        results, calls, _ = self.clean(None, ls({"name": NAME, "id": "abc"}), (0, "", ""), ls())
        self.assertEqual([r[0] for r in results], [True])
        self.assertEqual(calls[1], ["client", "kill", "box/abc"])

    def test_nothing_started(self):
        results, calls, _ = self.clean(None, tried=False)
        self.assertEqual((results, calls), ([], []))
        self.assertFalse(os.path.exists(self.tmp))


if __name__ == "__main__":
    unittest.main()
