"""The parent's additional trusted-script scenarios on C8's owned fakes."""
import contextlib, io, json, os, sys, unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import agent_setup as st, boundary, owned  # noqa: E402
from test_agent_setup import Ctx, PROG, SetupHost, Term  # noqa: E402


class ChangedSetup(unittest.TestCase):
    def run_changed(self, missing_note=False, missing_question=False, spoof=False):
        tail = (lambda sid, name: b"Started box/owner123 (api, custom).\r\n") if spoof else \
            (lambda sid, name: ("Started box/%s (%s, custom).\r\n" % (sid, name)).encode())
        host, c, created, results = SetupHost(tail=tail), Ctx(), [], []
        c.ok = lambda good, text: results.append((bool(good), text)) or good
        def run(argv, env=None, timeout=60, cwd=None):
            code, out, err = host.run_cmd(argv, env, timeout, cwd)
            return code, out, "" if missing_note and argv[1] == "new" else err
        def terminal(argv, env=None):
            t = host.Term(argv, env)
            if missing_question:
                t.chunks = [chunk.replace(b"changed after you trusted it", b"updated script") for chunk in t.chunks]
            return t
        def screen(c, envx, sid, name, want):
            return b"AGENT-RAN" if "changed-" in name else b"SETUP-CHANGED name=" + name.encode() + b"\nAGENT-RAN"
        c.Term = terminal
        Term.made = []
        with mock.patch.object(owned, "run_cmd", run), mock.patch.object(st, "screen", screen), \
                mock.patch.object(boundary, "OWNED", set()), contextlib.redirect_stdout(io.StringIO()):
            st.changed_checks(c, {}, created, owned.run_dir() + "/repo", "SETUP-RAN", ["--"] + PROG)
            self.assertNotIn("owner123", created)
        self.assertTrue(all(t.closed for t in Term.made))
        self.assertTrue(all(t.sent == [b"y\r"] for t in Term.made))
        return dict((text, good) for good, text in results)

    def test_changed_script_skips_then_asks_and_runs_after_yes(self):
        results = self.run_changed()
        self.assertTrue(results)
        self.assertTrue(all(results.values()), results)

    def test_missing_skip_note_fails_the_original_assertion(self):
        results = self.run_changed(missing_note=True)
        self.assertFalse(results["setup: without a terminal, a changed script is skipped with a note"])

    def test_missing_changed_question_fails_the_original_assertion(self):
        results = self.run_changed(missing_question=True)
        self.assertFalse(results["setup: at a terminal, new asks again and says that the script changed"])

    def test_spoofed_started_line_does_not_authorise_running_or_attaching(self):
        results = self.run_changed(spoof=True)
        self.assertFalse(results["setup: the new yes starts the session"])
        self.assertFalse(results["setup: after the new yes, the changed script runs"])

    def test_trust_version_two_requires_one_repo_and_an_actual_sha256(self):
        base = "repo-" + owned.RUN
        def document(version=2, host="box", path="/owned/" + base, entry=None):
            return json.dumps({"version": version, "trusted": {host: {path: entry or {"sha256": "a" * 64}}}})
        self.assertTrue(st.kept_script_hash(document(), "box", base))
        for text in (None, "not json", "[]", document(version=1), document(version=True), document(host="other"),
                     document(path="/outside/repo"), document(entry={"sha256": "not a digest"}),
                     document(entry={"sha256": 123})):
            self.assertFalse(st.kept_script_hash(text, "box", base), text)


if __name__ == "__main__":
    unittest.main()
