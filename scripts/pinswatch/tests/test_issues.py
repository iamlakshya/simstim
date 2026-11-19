"""The issues of the daily run."""
import re

from pinswatch import issues, transport
from pinswatch.tests.support import ADVISORIES, GHOSTTY, HASHED, OLD, OSV, REPO, V120, VT, Case, Checkout, by_id, verdicts


def issue(title, login=issues.BOT, labels=("dependencies", "needs triage")):
    return {"title": title, "user": {"login": login}, "labels": [{"name": name} for name in labels]}


class IssueTests(Case):
    def run_issues(self, checkout, **env):
        environ = {"GITHUB_REPOSITORY": REPO, "GITHUB_RUN_ID": "42"}
        environ.update(env)
        return checkout.run("--open-issues", **environ)

    def test_one_issue_for_each_new_finding(self):
        checkout = Checkout(self, ghostty=OLD)
        checkout.net.advisories[GHOSTTY] = [a for a in ADVISORIES if a["ghsa_id"] in HASHED]
        checkout.net.issues = [issue("pins-watch: ghostty is in GHSA-w9q4-5jvv-mcv4"),
                               issue("pins-watch: ghostty is in GHSA-jqr7-69mm-v35h", login="someone"),
                               issue("pins-watch: ghostty is in GHSA-jqr7-69mm-v35h", labels=["question"]),
                               issue("pins-watch: ghostty is in GHSA-jqr7-69mm-v35h (and more)")]
        code, out = self.run_issues(checkout)
        self.assertEqual(code, 0, out)
        self.assertEqual([created["title"] for created in checkout.net.created], ["pins-watch: ghostty is in GHSA-jqr7-69mm-v35h"])
        listed = [url for method, url, _ in checkout.net.requests if method == "GET" and "/issues?" in url]
        self.assertIn("creator=github-actions%5Bbot%5D", listed[0])
        self.assertIn("labels=dependencies", listed[0])
        body = checkout.net.created[0]
        self.assertEqual(body["labels"], ["dependencies", "needs triage"])
        for text in ("https://github.com/ghostty-org/ghostty/security/advisories/GHSA-jqr7-69mm-v35h", "`medium, CVSS 5.5`",
                     "`main from d7bb4b863 up to but not including 0af5c9e55`", f"`{OLD}`",
                     "Move the pin past 0af5c9e5 (vt/README.md)", f"https://github.com/{REPO}/actions/runs/42"):
            self.assertIn(text, body["body"])
        self.assertIn("An open issue already has the title pins-watch: ghostty is in GHSA-w9q4-5jvv-mcv4", out)

    def test_an_unread_advisory_gets_its_own_issue(self):
        checkout = Checkout(self, ghostty=VT)
        checkout.net.advisories[GHOSTTY] = [by_id("GHSA-hp9c-qp4q-cpw3", vulnerable_version_range="1.0.0 to 1.3.1")]
        self.assertEqual(self.run_issues(checkout)[0], 0)
        self.assertEqual([created["title"] for created in checkout.net.created],
                         ["pins-watch: ghostty cannot be checked against GHSA-hp9c-qp4q-cpw3"])
        self.assertIn("Check it by hand, then record a verdict for commit 0fa3d577", checkout.net.created[0]["body"])

    def test_outside_text_in_an_issue_is_code(self):
        # A range from an advisory can mention a person, or hold Markdown. In the body it stays inside code.
        checkout = Checkout(self, ghostty=VT)
        checkout.net.advisories[GHOSTTY] = [by_id("GHSA-hp9c-qp4q-cpw3",
                                                  vulnerable_version_range="@someone **now** [x](https://example.com) `tick` <b>",
                                                  patched_versions="@team")]
        self.assertEqual(self.run_issues(checkout)[0], 0)
        body = checkout.net.created[0]["body"]
        self.assertIn("`@someone **now** [x](https://example.com) 'tick' <b>`", body)
        outside_code = re.sub(r"`[^`\n]*`", "", body)
        for raw in ("@", "**", "](", "<b>"):
            self.assertNotIn(raw, outside_code)

    def test_no_issue_for_a_verdict(self):
        checkout = Checkout(self, ghostty=OLD, reviewed=verdicts(["GHSA-jqr7-69mm-v35h"], OLD, verdict="fixed"))
        checkout.net.advisories[GHOSTTY] = [a for a in ADVISORIES if a["ghsa_id"] in HASHED]
        self.assertEqual(self.run_issues(checkout)[0], 0)
        self.assertEqual([created["title"] for created in checkout.net.created], ["pins-watch: ghostty is in GHSA-w9q4-5jvv-mcv4"])

    def test_an_osv_severity_must_be_a_vector(self):
        lock = f'\n[[pin]]\nname = "old-terminal"\nref = "v1.2.0"\ncommit = "{V120}"\nwatch = "osv"\n'
        for score, want in (("CVSS:3.1/AV:N/AC:L/PR:N/UI:R/S:U/C:L/I:L/A:L", "`CVSS:3.1/AV:N/AC:L/PR:N/UI:R/S:U/C:L/I:L/A:L`"),
                            ("@someone [x](https://example.com)", "`unknown`")):
            with self.subTest(score=score):
                checkout = Checkout(self, ghostty=VT, lock=lock)
                checkout.net.osv[V120] = {"vulns": [dict(OSV[V120]["vulns"][0], severity=[{"type": "CVSS_V3", "score": score}])]}
                self.assertEqual(self.run_issues(checkout)[0], 0)
                self.assertIn(f"- Severity: {want}", checkout.net.created[0]["body"])
                self.assertNotIn("@someone", checkout.net.created[0]["body"])

    def test_it_fails_when_it_cannot_do_its_work(self):
        checkout = Checkout(self, ghostty=OLD)
        checkout.net.fail[f"POST {transport.GITHUB_API}/repos/{REPO}/issues"] = 403
        code, out = self.run_issues(checkout)
        self.assertEqual(code, 1)
        self.assertIn('Could not open the issue "pins-watch: ghostty is in GHSA-w9q4-5jvv-mcv4" in example/project: HTTP 403.', out)
        code, out = self.run_issues(Checkout(self, ghostty=OLD), GITHUB_REPOSITORY="")
        self.assertEqual(code, 1)
        self.assertIn("Could not open issues: GITHUB_REPOSITORY or GITHUB_TOKEN is not set.", out)
