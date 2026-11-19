"""Where requests go: the token stays on api.github.com, and outside text cannot start a workflow command."""
import contextlib
import email.message
import io
import os
from unittest import mock
import urllib.request
import urllib.response

from pinswatch import cli, transport
from pinswatch.tests.support import GHOSTTY, OLD, VMM, VT, Case, Checkout


class Fake(urllib.request.HTTPSHandler):
    """A connection that answers from a table, so the real http() path runs with no network."""

    def __init__(self, answers):
        super().__init__()
        self.answers, self.seen = answers, []

    def https_open(self, req):
        self.seen.append((req.full_url, dict(req.header_items())))
        status, headers, body = self.answers[req.full_url]
        message = email.message.Message()
        for key, value in headers.items():
            message[key] = value
        reply = urllib.response.addinfourl(io.BytesIO(body), message, req.full_url, status)
        reply.msg = "recorded"
        return reply


class HttpTests(Case):
    def send(self, target):
        fake = Fake({"https://api.github.com/a": (302, {"Location": target}, b""),
                     "https://api.github.com/b": (200, {}, b"[]"), "https://example.com/steal": (200, {}, b"[]")})
        with mock.patch.object(transport, "OPENER", urllib.request.build_opener(transport.SameHost, fake)):
            try:
                return transport.GitHub("offline-token").get_all("/a"), fake.seen
            except transport.ApiError as error:
                return str(error), fake.seen

    def test_a_redirect_to_another_host_is_refused(self):
        result, seen = self.send("https://example.com/steal")
        self.assertEqual(result, "a redirect to another host, example.com")
        self.assertEqual([url for url, _ in seen], ["https://api.github.com/a"])
        self.assertEqual(seen[0][1].get("Authorization"), "Bearer offline-token")

    def test_a_redirect_on_the_same_host_drops_the_token(self):
        result, seen = self.send("https://api.github.com/b")
        self.assertEqual(result, [])
        self.assertEqual([url for url, _ in seen], ["https://api.github.com/a", "https://api.github.com/b"])
        self.assertNotIn("Authorization", seen[1][1])

    def test_the_token_goes_to_github_only(self):
        checkout = Checkout(self, ghostty=OLD)
        checkout.run()
        for _, url, headers in checkout.net.requests:
            if url.startswith(transport.GITHUB_API + "/"):
                self.assertEqual(headers.get("Authorization"), "Bearer offline-token")
            else:
                self.assertNotIn("Authorization", headers, url)
        # A next page on another host is refused, so the token never goes there.
        checkout = Checkout(self, ghostty=OLD)
        fake = checkout.net
        fake.page_size = 5

        def elsewhere(method, url, body=None, headers=None):
            status, answer_headers, answer = fake(method, url, body, headers)
            if "Link" in answer_headers:
                answer_headers["Link"] = '<https://example.com/steal>; rel="next"'
            return status, answer_headers, answer
        checkout.net = elsewhere
        code, out = checkout.run()
        self.assertEqual(code, 1)
        self.assertIn("Could not read the advisories of ghostty-org/ghostty: a link that leaves api.github.com.", out)
        self.assertFalse([url for _, url, _ in fake.requests if "example.com" in url])

    def test_outside_text_cannot_start_a_workflow_command(self):
        checkout = Checkout(self, ghostty=VT)
        checkout.net.advisories[GHOSTTY] = [{
            "ghsa_id": "GHSA-2222-3333-4444",
            "vulnerabilities": [{"vulnerable_version_range": "1.0\n::add-mask::x\r\n::warning::y ‮gnp.exe"}]}]
        code, _ = checkout.run(GITHUB_ACTIONS="true")
        self.assertEqual(code, 1)
        for line in (checkout.out + checkout.err).splitlines():
            self.assertFalse(line.startswith("::") and not line.startswith("::error title=pins-watch::"), line)
            self.assertNotIn("‮", line)
        self.assertIn('"1.0 ::add-mask::x ::warning::y gnp.exe": it holds a character that is not printable ASCII.', checkout.err)

    def assert_safe(self, output):
        for line in output.splitlines():
            self.assertFalse(line.startswith("::") and not line.startswith("::error title=pins-watch::"), line)
        for character in ("\x1b", "\r", "\x07", "‮"):
            self.assertNotIn(character, output)

    def test_a_bad_ref_prints_nothing_raw(self):
        for ref in ("bad\\n::warning::injected", "bad\\u001b[2J\\u0007"):  # TOML escapes: a line break, then terminal controls
            with self.subTest(ref=ref):
                lock = f'\n[[pin]]\nname = "vmm"\nrepo = "example/vmm"\nref = "{ref}"\ncommit = "{VMM}"\n'
                checkout = Checkout(self, ghostty=VT, lock=lock)
                checkout.net.commits[VMM] = {"sha": VMM}
                code, out = checkout.run(GITHUB_ACTIONS="true")
                self.assertEqual(code, 1)
                self.assert_safe(out)
                self.assertIn('guest/supply.lock: the pin "vmm" has a ref with a space, or a character that is not printable ASCII.\n'
                              "Try: Set ref to the tag name, as git shows it", checkout.err)

    def test_say_cleans_what_it_prints(self):
        out, err = io.StringIO(), io.StringIO()
        with mock.patch.dict(os.environ, {"GITHUB_ACTIONS": "true"}), contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            cli.say('the tag bad\n::warning::injected\x1b[2J is not in example/vmm.', "Check it\r\n::error::fake")
        self.assert_safe(out.getvalue() + err.getvalue())
        self.assertIn("\nthe tag bad ::warning::injected [2J is not in example/vmm.\nTry: Check it ::error::fake\n", err.getvalue())
