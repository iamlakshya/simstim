"""Tests 1 to 8 of spec 19.5."""
from http.client import IncompleteRead
import urllib.error

from pinswatch import files, transport
from pinswatch.tests.support import (GHOSTTY, OLD, OLD_FINDINGS, OSV, V120, VMM, VT, Case, Checkout, by_id,
                                     verdicts, vmm)


class SpecTests(Case):
    def test_spec_1_the_old_pin(self):
        checkout = Checkout(self, ghostty=OLD)
        self.assertEqual(checkout.findings(), OLD_FINDINGS)
        self.assertEqual(checkout.findings("unread"), set())
        code, out = checkout.run()
        self.assertEqual(code, 1)
        self.assertIn("ghostty (ad97d331) is in GHSA-w9q4-5jvv-mcv4: it lacks the fix e257bb45.\n"
                      "Try: Move the pin past e257bb45 (vt/README.md), or record a verdict in scripts/pins-watch-reviewed.toml", out)
        self.assertIn("ghostty (ad97d331) is in GHSA-jqr7-69mm-v35h: it lacks the fix 0af5c9e5.", out)
        self.assertIn("ghostty (ad97d331) is in GHSA-2r63-vmw6-3m3c: it lacks the release v1.4.0.\n"
                      "Try: Move the pin past the release v1.4.0 (vt/README.md), or record a verdict in", out)
        for clear in ("GHSA-qjfx-r533-6vx3", "GHSA-3r5j-c6rj-xq2v", "GHSA-4jxv-xgrp-5m3r", "GHSA-q9fg-cpmh-c78x",
                      "GHSA-98wc-794w-gjx3", "GHSA-hfg5-8q2c-crhc", "GHSA-5hcq-3j4q-4v6p"):
            self.assertNotIn(clear, out)
        self.assert_try_lines(checkout.err)

    def test_spec_2_the_vt_1_0_0_commit(self):
        checkout = Checkout(self, ghostty=VT)
        self.assertEqual(checkout.findings(), set())
        self.assertEqual(checkout.findings("unread"), set())
        code, out = checkout.run()
        self.assertEqual(code, 0, out)
        self.assertIn("pins-watch: ok", out)

    def test_spec_3_an_advisory_that_cannot_be_read(self):
        checkout = Checkout(self, ghostty=VT)
        checkout.net.advisories[GHOSTTY] = [by_id("GHSA-hp9c-qp4q-cpw3", vulnerable_version_range="1.0.0 to 1.3.1")]
        code, out = checkout.run()
        self.assertEqual(code, 1)
        self.assertIn('Cannot read the range of GHSA-hp9c-qp4q-cpw3 for ghostty: "1.0.0 to 1.3.1": it is not a plain version range.\n'
                      "Try: Check it by hand, then record a verdict for commit 0fa3d577 in scripts/pins-watch-reviewed.toml", out)
        self.assertFalse([r for r in checkout.net.requests if "/compare/" in r[1]])

    def test_spec_4_a_verdict_holds_for_its_commit_only(self):
        for commit, code in ((VT, 0), (OLD, 1)):
            with self.subTest(commit=commit):
                checkout = Checkout(self, ghostty=VT, reviewed=verdicts(["GHSA-hp9c-qp4q-cpw3"], commit))
                checkout.net.advisories[GHOSTTY] = [by_id("GHSA-hp9c-qp4q-cpw3", vulnerable_version_range="1.0.0 to 1.3.1")]
                self.assertEqual(checkout.run()[0], code, checkout.out + checkout.err)
                if code == 0:
                    self.assertIn("ghostty (0fa3d577): GHSA-hp9c-qp4q-cpw3 has the verdict not-affected", checkout.out)
                else:
                    self.assertIn("Cannot read the range of GHSA-hp9c-qp4q-cpw3", checkout.err)

    def test_spec_5_an_osv_entry_is_a_finding(self):
        lock = f'\n[[pin]]\nname = "old-terminal"\nref = "v1.2.0"\ncommit = "{V120}"\nwatch = "osv"\n'
        checkout = Checkout(self, ghostty=VT, lock=lock)
        code, out = checkout.run()
        self.assertEqual(code, 1)
        self.assertIn("old-terminal (3e38e284) is in CVE-2026-26982: OSV lists its commit as affected.\n"
                      "Try: Move the pin past the fix that CVE-2026-26982 names in guest/supply.lock", out)
        self.assertIn("old-terminal (3e38e284), from guest/supply.lock: 1 entries in OSV.", out)
        # A verdict may name the entry's alias, the repository's own advisory.
        checkout.write(files.REVIEWED, verdicts(["GHSA-4jxv-xgrp-5m3r"], V120, pin="old-terminal", verdict="fixed"))
        self.assertEqual(checkout.run()[0], 0, checkout.out + checkout.err)
        self.assertTrue(OSV[V120]["vulns"])

    def test_spec_6_an_api_failure_fails_the_job(self):
        api = f"GET {transport.GITHUB_API}/repos/{GHOSTTY}"
        cases = [(f"{api}/security-advisories", 403, "Could not read the advisories of ghostty-org/ghostty: HTTP 403."),
                 (f"{api}/security-advisories", urllib.error.URLError("no route to host"),
                  "Could not read the advisories of ghostty-org/ghostty: no route to host."),
                 (f"{api}/security-advisories", IncompleteRead(b"[{"), "Could not read the advisories of ghostty-org/ghostty: IncompleteRead"),
                 (f"{api}/compare/", 403, "Could not compare the commits of GHSA-"),
                 (f"{api}/compare/", 500, ": HTTP 500."),
                 (f"{api}/git/ref/tags/", 502, ": HTTP 502."),
                 (f"{api}/commits/", 503, "Could not read the commit 0fa3d577 of ghostty in ghostty-org/ghostty: HTTP 503."),
                 (f"POST {transport.OSV_QUERY}", 503, "Could not query OSV for ghostty (0fa3d577): HTTP 503.")]
        for prefix, failure, message in cases:
            with self.subTest(prefix=prefix, failure=failure):
                checkout = Checkout(self, ghostty=VT)
                checkout.net.fail[prefix] = failure
                code, out = checkout.run()
                self.assertEqual(code, 1)
                self.assertIn(message, out)
                self.assertIn("Try: Run the job again later.", out)
                self.assertNotIn("Traceback", out)
                self.assert_try_lines(checkout.err)

    def test_spec_7_the_lock_file(self):
        checkout = Checkout(self, ghostty=VT, lock='\n[[pin]]\nname = "chv"\nrepo = "cloud-hypervisor/cloud-hypervisor"\nref = "v54.0"\n')
        code, out = checkout.run()
        self.assertEqual(code, 1)
        self.assertIn('guest/supply.lock: the pin "chv" has no commit.\nTry: Add the 40-character commit of its ref', out)
        lock = (f'\n[[pin]]\nname = "kernel"\nref = "6.18.1"\ncommit = "{VT}"\nwatch = "osv"\n'
                'url = "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-6.18.1.tar.xz"\nsha256 = "00"\n')
        checkout = Checkout(self, ghostty=VT, lock=lock)
        self.assertEqual(checkout.run()[0], 0, checkout.out + checkout.err)

    def test_spec_8_a_last_affected_commit_is_unread(self):
        checkout = vmm(self, f"<= {VMM[:12]}", {})
        code, out = checkout.run()
        self.assertEqual(code, 1)
        self.assertIn(f'Cannot read the range of GHSA-2222-3333-4444 for vmm: "<= {VMM[:12]}": '
                      f'its text is outside the grammar of spec 19.5, from "<= {VMM[:12]}".', out)
