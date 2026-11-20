"""The tag rule of spec 19.5: entries that name release versions, read through release tags."""
from pinswatch.tests.support import GHOSTTY, OLD, VMM, VT, Case, Checkout, by_id, tag_commit


def library_advisory():
    """An advisory of libghostty-vt that names versions only, fixed in its first release."""
    return {"ghsa_id": "GHSA-2222-3333-5555", "severity": "medium", "vulnerabilities": [
        {"package": {"ecosystem": "other", "name": "libghostty-vt"},
         "vulnerable_version_range": "< 1.0.0", "patched_versions": "1.0.0"}]}


class TagRuleTests(Case):
    def test_libghostty_vt_maps_to_its_own_tags(self):
        checkout = Checkout(self, ghostty=OLD)
        checkout.net.advisories[GHOSTTY] = [library_advisory()]
        code, out = checkout.run()
        self.assertEqual(code, 1)
        self.assertIn("ghostty (ad97d331) is in GHSA-2222-3333-5555: it lacks the release vt-1.0.0.\n"
                      "Try: Move the pin past the release vt-1.0.0 (vt/README.md)", out)
        checkout = Checkout(self, ghostty=VT)
        checkout.net.advisories[GHOSTTY] = [library_advisory()]
        self.assertEqual(checkout.run()[0], 0, checkout.out + checkout.err)

    def test_a_lower_bound_alone_never_clears_a_pin(self):
        # The pin is older than 1.3.0, the first affected release, and 1.4.0 does not reach it: unread, not clear.
        checkout = Checkout(self, ghostty=OLD)
        checkout.net.advisories[GHOSTTY] = [by_id("GHSA-hp9c-qp4q-cpw3")]
        checkout.net.compare[f"{tag_commit('v1.3.0')}...{OLD}"] = {"status": "behind"}
        self.assertEqual(checkout.findings("unread"), {"GHSA-hp9c-qp4q-cpw3"})
        self.assertIn("the pin is older than v1.3.0, and no patched release reaches it", checkout.run()[1])

    def test_a_missing_tag_or_package_is_unread(self):
        for fields, reason in (({"patched_versions": "1.9.9"}, "the tag v1.9.9 is not in ghostty-org/ghostty"),
                               ({"package": {"name": "ghostty-web"}}, "the package ghostty-web has no tag form"),
                               ({"patched_versions": "1.4.0 or later"}, "its patched versions are not plain versions"),
                               ({"vulnerable_version_range": ">= 1.0.0, >= 1.1.0"}, "it has two lower bounds")):
            with self.subTest(fields=fields):
                checkout = Checkout(self, ghostty=VT)
                checkout.net.advisories[GHOSTTY] = [by_id("GHSA-v8cw-fg4j-jcc4", **fields)]
                code, out = checkout.run()
                self.assertEqual(code, 1)
                self.assertIn(reason, out)

    def test_a_tag_of_a_blob_or_a_tree_is_unread(self):
        for kind in ("blob", "tree"):
            with self.subTest(kind=kind):
                checkout = Checkout(self, ghostty=VT)
                checkout.net.advisories[GHOSTTY] = [by_id("GHSA-v8cw-fg4j-jcc4")]
                # The SHA is a commit that the pin holds, so a resolver that takes any object would clear the pin.
                checkout.net.refs[GHOSTTY]["v1.4.0"] = {"object": {"sha": tag_commit("v1.4.0"), "type": kind}}
                self.assertEqual(checkout.findings("unread"), {"GHSA-v8cw-fg4j-jcc4"})
                code, _ = checkout.run()
                self.assertEqual(code, 1)
                self.assertIn(f"the tag v1.4.0 names a {kind}, not a commit.\n"
                              "Try: Check it by hand, then record a verdict for commit 0fa3d577", checkout.err)

    def test_a_supply_pin_uses_its_tag_form(self):
        lock = f'\n[[pin]]\nname = "vmm"\nrepo = "example/vmm"\nref = "v1.0"\ncommit = "{VMM}"\ntag = "release-{{version}}"\n'
        checkout = Checkout(self, ghostty=VT, lock=lock)
        checkout.net.advisories["example/vmm"] = [{"ghsa_id": "GHSA-2222-3333-4444", "vulnerabilities": [
            {"package": {"name": "vmm"}, "vulnerable_version_range": "< 2.0", "patched_versions": "2.0"}]}]
        checkout.net.commits[VMM] = {"sha": VMM}
        checkout.net.refs["example/vmm"] = {"v1.0": {"object": {"sha": VMM, "type": "commit"}},
                                            "release-2.0": {"object": {"sha": "3" * 40, "type": "commit"}}}
        checkout.net.compare[f"{'3' * 40}...{VMM}"] = {"status": "behind"}
        code, out = checkout.run()
        self.assertEqual(code, 1)
        self.assertIn("vmm (11111111) is in GHSA-2222-3333-4444: it lacks the release release-2.0.\n"
                      "Try: Move the pin past the release release-2.0 in guest/supply.lock", out)
