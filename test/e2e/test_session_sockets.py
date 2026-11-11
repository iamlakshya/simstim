"""Offline tests of the probe in session_sockets.py:
python3 test/e2e/test_session_sockets.py.

A fake terminal echoes each typed line, as a shell does, and then prints
what the host would print. They run no command and reach no host. The
probe's answer comes only from the lines that it prints, never from the
echo of the typed line: an unexpected answer fails, and only the marker
that the shell prints without python3 skips.
"""
import contextlib, io, os, re, sys, unittest

sys.dont_write_bytecode = True
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import session_sockets as ss  # noqa: E402

SESSION = "bbbb2222"


class Ctx:
    NAME = "client"

    def __init__(self):
        self.results = []

    def ok(self, cond, msg):
        self.results.append((bool(cond), msg))
        return cond


class FakeTerm:
    """Echoes each typed line, wrapped at width columns as a terminal shows
    it, then prints the output lines."""
    def __init__(self, output, width=80):
        self.output, self.width, self.buf = output, width, b""

    def send(self, data):
        line = data.rstrip(b"\r")
        self.buf += b"\r\n".join(line[i:i + self.width] for i in range(0, len(line), self.width)) + b"\r\n"
        self.buf += b"".join(o + b"\r\n" for o in self.output)

    def wait_for(self, pattern, timeout=20):
        return re.search(pattern, self.buf) is not None


def probe(*output, width=80):
    c = Ctx()
    t = FakeTerm(list(output), width)
    with contextlib.redirect_stdout(io.StringIO()) as out:
        ss.probe_step(c, t, SESSION)
    return c.results, out.getvalue(), t.buf


class Probe(unittest.TestCase):
    def refusal(self, results):
        """The result of the refusal check, or None when it did not run."""
        found = [ok for ok, msg in results if "refuses a program in the first" in msg]
        return found[0] if found else None

    def test_the_typed_line_holds_no_marker(self):
        line = ss.probe_line(Ctx(), SESSION)
        self.assertEqual(ss.probe_answers(line), [])
        self.assertNotIn(b"RAW-NONE", line)
        self.assertNotIn(b"RAWDONE-42", line)

    def test_forbidden_passes(self):
        results, _, _ = probe(b"RAW-forbidden", b"RAWDONE-42")
        self.assertEqual(results, [(True, "sockets: the probe of the second session's socket ran"),
                                   (True, "sockets: the second session's socket refuses a program in the first (['forbidden'])")])

    def test_an_unexpected_answer_fails(self):
        # The echo of the typed line comes first, at several widths, so that
        # any part of it can start a line.
        for width in (20, 33, 47, 80, 200):
            results, out, _ = probe(b"RAW-unknown_op", b"RAWDONE-42", width=width)
            self.assertIs(self.refusal(results), False, (width, results))
            self.assertNotIn("SKIP", out)

    def test_a_marker_inside_another_line_is_no_answer(self):
        # Only a line that holds the marker alone is the probe's answer.
        results, out, _ = probe(b"$ echo RAW-NONE was the marker", b"RAW-unknown_op", b"RAWDONE-42")
        self.assertIs(self.refusal(results), False)
        self.assertNotIn("SKIP", out)

    def test_no_answer_fails(self):
        results, out, _ = probe(b"RAWDONE-42")
        self.assertIs(self.refusal(results), False)
        self.assertNotIn("SKIP", out)

    def test_no_python3_skips(self):
        results, out, _ = probe(b"RAW-NONE", b"RAWDONE-42")
        self.assertIsNone(self.refusal(results))
        self.assertIn("SKIP sockets: the host has no python3", out)

    def test_a_probe_that_does_not_end_fails(self):
        results, _, _ = probe(b"RAW-forbidden")
        self.assertEqual(results[0][0], False)


if __name__ == "__main__":
    unittest.main()
