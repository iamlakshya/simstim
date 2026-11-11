"""The hash rule and the pin's own commit and ref, through a whole check."""
from pinswatch.tests.support import OLD, OLD_FINDINGS, VMM, Case, Checkout, verdicts, vmm


class WatchTests(Case):
    def test_a_pin_before_the_bug_is_not_affected(self):
        checkout = vmm(self, ">= aaaaaaa, < bbbbbbb", {"aaaaaaa": "behind", "bbbbbbb": "behind"})
        self.assertEqual(checkout.run()[0], 0, checkout.out + checkout.err)

    def test_a_pin_after_the_bug_and_before_the_fix_is_affected(self):
        checkout = vmm(self, ">= aaaaaaa, < bbbbbbb", {"aaaaaaa": "ahead", "bbbbbbb": "diverged"})
        checkout.net.osv[VMM] = {"vulns": [{"id": "CVE-2026-0001", "aliases": ["GHSA-2222-3333-4444"]}]}
        self.assertEqual(checkout.findings(), {"GHSA-2222-3333-4444"})
        code, out = checkout.run()
        self.assertEqual(code, 1)
        self.assertIn("vmm (11111111) is in GHSA-2222-3333-4444: it lacks the fix bbbbbbb.\n"
                      "Try: Move the pin past bbbbbbb in guest/supply.lock", out)
        self.assertNotIn("CVE-2026-0001", out)

    def test_a_range_with_no_fix(self):
        checkout = vmm(self, ">= aaaaaaa or >= bbbbbbb", {"aaaaaaa": "behind", "bbbbbbb": "identical"})
        self.assertEqual(checkout.run()[0], 1)
        self.assertIn("vmm (11111111) is in GHSA-2222-3333-4444: it holds bbbbbbb, and the advisory names no fix.",
                      checkout.out + checkout.err)

    def test_an_unknown_commit_is_unread(self):
        checkout = vmm(self, "< bbbbbbb", {"bbbbbbb": 404})
        code, out = checkout.run()
        self.assertEqual(code, 1)
        self.assertIn('Cannot read the range of GHSA-2222-3333-4444 for vmm: "< bbbbbbb": the compare of bbbbbbb with the pin answered HTTP 404.', out)
        checkout = vmm(self, "< bbbbbbb", {"bbbbbbb": 404}, reviewed=verdicts(["GHSA-2222-3333-4444"], VMM, pin="vmm"))
        self.assertEqual(checkout.run()[0], 0, checkout.out + checkout.err)

    def test_every_fix_is_tested_before_one_is_unknown(self):
        checkout = vmm(self, "< bbbbbbb", {"bbbbbbb": 404, "ccccccc": "ahead"}, patched="ccccccc")
        self.assertEqual(checkout.run()[0], 0, checkout.out + checkout.err)

    def test_the_pin_commit_and_its_ref(self):
        checkout = vmm(self, "< bbbbbbb", {"bbbbbbb": "ahead"})
        checkout.net.commits[VMM] = 422
        code, out = checkout.run()
        self.assertEqual(code, 1)
        self.assertIn("vmm: the commit 11111111 is not in example/vmm.\nTry: Check the commit of the pin in guest/supply.lock", out)
        self.assertNotIn("Cannot read", out)
        checkout = vmm(self, "< bbbbbbb", {"bbbbbbb": "ahead"})
        checkout.net.refs["example/vmm"]["v1.0"]["object"]["sha"] = "2" * 40
        self.assertEqual(checkout.run()[0], 1)
        self.assertIn('guest/supply.lock: the ref "v1.0" of the pin "vmm" is the commit 22222222, not 11111111.', checkout.err)
        checkout = vmm(self, "< bbbbbbb", {"bbbbbbb": "ahead"})
        del checkout.net.refs["example/vmm"]["v1.0"]
        self.assertEqual(checkout.run()[0], 1)
        self.assertIn('the ref "v1.0" of the pin "vmm" does not name a commit: the tag v1.0 is not in example/vmm.', checkout.err)

    def test_a_ref_to_a_blob_or_a_tree_is_refused(self):
        for kind in ("blob", "tree"):
            with self.subTest(kind=kind):
                checkout = vmm(self, "< bbbbbbb", {"bbbbbbb": "ahead"})
                checkout.net.refs["example/vmm"]["v1.0"]["object"]["type"] = kind
                code, _ = checkout.run()
                self.assertEqual(code, 1)
                self.assertIn(f'guest/supply.lock: the ref "v1.0" of the pin "vmm" does not name a commit: '
                              f"the tag v1.0 names a {kind}, not a commit.\nTry: Set ref to the tag of the pin's commit",
                              checkout.err)
                self.assert_try_lines(checkout.err)

    def test_a_strange_answer_fails_and_is_not_kept(self):
        checkout = vmm(self, "< bbbbbbb", {"bbbbbbb": {"status": "sideways"}})
        second = {"ghsa_id": "GHSA-5555-6666-7777", "cvss_severities": [], "vulnerabilities": "< bbbbbbb"}
        third = {"ghsa_id": "GHSA-5555-6666-8888", "cvss_severities": [], "vulnerabilities": [{"vulnerable_version_range": "< bbbbbbb"}]}
        checkout.net.advisories["example/vmm"] += [second, third]
        code, out = checkout.run()
        self.assertEqual(code, 1)
        self.assertEqual(out.count("an unknown compare status"), 2)
        self.assertIn('Cannot read the range of GHSA-5555-6666-7777 for vmm: "no range": it has no entry.', out)
        self.assertNotIn("Traceback", out)

    def test_every_page_is_read(self):
        checkout = Checkout(self, ghostty=OLD)
        checkout.net.page_size = 5
        self.assertEqual(checkout.findings(), OLD_FINDINGS)
        self.assertIn("18 advisories in ghostty-org/ghostty", checkout.out)
