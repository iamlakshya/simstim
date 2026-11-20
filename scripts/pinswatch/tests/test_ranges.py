"""How a range reads: the grammar of spec 19.5, intervals, printable ASCII and version lists."""
from pinswatch import ranges
from pinswatch.tests.support import GHOSTTY, OLD, VT, Case, Checkout, by_id, vmm
from pinswatch.transport import Unknown

OUTSIDE = "its text is outside the grammar of spec 19.5"
NOT_ASCII = "it holds a character that is not printable ASCII"


class RangeTests(Case):
    def test_the_recorded_ranges(self):
        cases = {"GHSA-w9q4-5jvv-mcv4": [(None, ["e257bb45e6d4d61c14eacdc717be89c6110f99ff"])],
                 "GHSA-jqr7-69mm-v35h": [("d7bb4b863", ["0af5c9e55"])],
                 "GHSA-qjfx-r533-6vx3": [("5b2d3b7df184b8395519baa235ee9539e2fb1a9b", ["65ff54c67df3d55e3ebf4dd31c7afa93c4cfb922"])]}
        for ghsa, want in cases.items():
            entry = [v for v in by_id(ghsa)["vulnerabilities"] if v["package"]["name"] == "libghostty-vt"][0]
            self.assertEqual(ranges.intervals(entry["vulnerable_version_range"], entry["patched_versions"]), want)

    def test_the_grammar(self):
        self.assertEqual(ranges.intervals("from commit ABCDEF1 up to but not including commit 1234abc", ""),
                         [("abcdef1", ["1234abc"])])
        self.assertEqual(ranges.intervals("since abcdef1, <1234abc", ""), [("abcdef1", ["1234abc"])])
        self.assertEqual(ranges.intervals("main from abcdef1 up to  but not including 1234abc", "main at 1234abc"),
                         [("abcdef1", ["1234abc"])])
        self.assertFalse(ranges.names_commits("<= 1.3.1", "1.4.0"))
        self.assertFalse(ranges.names_commits("a sha256 of " + "ab" * 32, ""))

    def test_anything_outside_the_grammar_is_unread(self):
        for text in ("< bbbbbbb | >= ccccccc", "!>= aaaaaaa", "not < bbbbbbb", "(>= aaaaaaa)", "> abcdef1", "=> abcdef1",
                     "after abcdef1", "<= abcdef1", "introduced in abcdef1", "abcdef1..1234abc", ">= aaaaaaa && < bbbbbbb",
                     ">= aaaaaaa and < bbbbbbb", ">= aaaaaaa, < bbbbbbb,", ">= aaaaaaa,, < bbbbbbb", "main main < bbbbbbb",
                     "< bbbbbbb main", "< commit", "< 1.4.0 or < bbbbbbb", "< bbbbbbb # a note"):
            with self.subTest(text=text), self.assertRaisesRegex(Unknown, OUTSIDE):
                ranges.intervals(text, "")
        for patched in ("main 1234abc", "1234abc,", ", 1234abc", "1234abc 1.4.0", "at 1234abc"):
            with self.subTest(patched=patched), self.assertRaisesRegex(Unknown, OUTSIDE):
                ranges.intervals("< 1234abc", patched)

    def test_each_interval_stays_whole(self):
        self.assertEqual(ranges.intervals(">= aaaaaaa, < bbbbbbb, or >= ccccccc, < ddddddd", ""),
                         [("aaaaaaa", ["bbbbbbb"]), ("ccccccc", ["ddddddd"])])
        self.assertEqual(ranges.intervals(">= aaaaaaa || < ccccccc", "ccccccc"), [("aaaaaaa", []), (None, ["ccccccc"])])
        for text, patched, reason in ((">= aaaaaaa, >= bbbbbbb", "", "two bounds of one kind"),
                                      ("< aaaaaaa or", "", "names no commit"),
                                      (">= aaaaaaa, < bbbbbbb or >= ccccccc", "eeeeeee", "do not match the intervals")):
            with self.subTest(text=text), self.assertRaisesRegex(Unknown, reason):
                ranges.intervals(text, patched)

    def test_a_long_range_is_read_whole(self):
        intro, fix = "5b2d3b7df184b8395519baa235ee9539e2fb1a9b", "65ff54c67df3d55e3ebf4dd31c7afa93c4cfb922"
        text = f">= {intro}," + " " * (216 - len(f">= {intro},< {fix}")) + f"< {fix}"
        self.assertEqual(len(text), 216)  # longer than the 200 characters that a message shows
        self.assertEqual(ranges.intervals(text, ""), [(intro, [fix])])
        checkout = Checkout(self, ghostty=OLD)
        checkout.net.advisories[GHOSTTY] = [by_id("GHSA-qjfx-r533-6vx3", vulnerable_version_range=text)]
        self.assertEqual(checkout.findings() | checkout.findings("unread"), set())

    def test_only_printable_ascii_is_read(self):
        for text in ("main from d7bb4b863 up​to but not including 0af5c9e55", "͏< 0af5c9e55", "<️ 0af5c9e55",
                     "< 0af5c9e55͏", "main from d7bb4b863 up to but not including 0af5c9e55", "< 0af5c9e55",
                     "<\t0af5c9e55"):
            with self.subTest(text=text), self.assertRaisesRegex(Unknown, NOT_ASCII):
                ranges.intervals(text, "")
        with self.assertRaisesRegex(Unknown, NOT_ASCII):
            ranges.intervals("< 0af5c9e55", "0af5c9e55️")
        for vulnerable, patched in (("<= 1.3.1", "1.4.0​"), ("͏>= 1.0.0, <= 1.3.1", "1.4.0"), (">=️ 1.0.0", "1.4.0")):
            with self.subTest(vulnerable=vulnerable), self.assertRaisesRegex(Unknown, NOT_ASCII):
                ranges.version_range(vulnerable, patched)

    def test_a_patched_list_has_no_empty_element(self):
        self.assertEqual(ranges.version_range(">= 1.0.0, <= 1.3.1", "1.3.2, 1.4.0"), ("1.0.0", ["1.3.2", "1.4.0"]))
        self.assertEqual(ranges.version_range("<= 1.3.1", ""), (None, []))
        for patched in (",1.4.0", "1.4.0,", "1.4.0,,1.3.0"):
            with self.subTest(patched=patched), self.assertRaisesRegex(Unknown, "its patched versions are not plain versions"):
                ranges.version_range("< 1.4.0", patched)


class RangeCheckTests(Case):
    """The same rules, through a whole run: none of these forms may pass."""

    def test_a_pin_in_a_later_interval_is_affected(self):
        checkout = vmm(self, ">= aaaaaaa, < bbbbbbb, or >= ccccccc, < ddddddd",
                       {"aaaaaaa": "ahead", "bbbbbbb": "ahead", "ccccccc": "ahead", "ddddddd": "behind"})
        self.assertEqual(checkout.findings(), {"GHSA-2222-3333-4444"})
        code, out = checkout.run()
        self.assertEqual(code, 1)
        self.assertIn("vmm (11111111) is in GHSA-2222-3333-4444: it lacks the fix ddddddd.", out)

    def test_an_unknown_operator_never_clears_a_pin(self):
        # Read part by part, each of these ranges would clear the pin.
        for text, statuses in (("< bbbbbbb | >= ccccccc", {"bbbbbbb": "ahead", "ccccccc": "ahead"}),
                               ("!>= aaaaaaa", {"aaaaaaa": "behind"}),
                               (">= aaaaaaa (" + "comment " * 30 + ") or >= bbbbbbb, < ccccccc",
                                {"aaaaaaa": "behind", "bbbbbbb": "behind", "ccccccc": "behind"})):
            with self.subTest(text=text):
                checkout = vmm(self, text, statuses)
                self.assertEqual(checkout.findings("unread"), {"GHSA-2222-3333-4444"})
                code, out = checkout.run()
                self.assertEqual(code, 1)
                self.assertIn(f"{OUTSIDE}, from", out)

    def test_reworded_ranges_never_pass(self):
        texts = {"main from commit d7bb4b863 up to but not including 0af5c9e55": "finding",
                 "introduced in d7bb4b863, fixed in 0af5c9e55": "unread",
                 "d7bb4b863..0af5c9e55": "unread",
                 "starting with d7bb4b863, before 0af5c9e55": "unread",
                 "≥ d7bb4b863, < 0af5c9e55": "unread",
                 "> d7bb4b863, < 0af5c9e55": "unread",
                 "after d7bb4b863, < 0af5c9e55": "unread"}
        for text, kind in texts.items():
            with self.subTest(text=text):
                checkout = Checkout(self, ghostty=OLD)
                checkout.net.advisories[GHOSTTY] = [by_id("GHSA-jqr7-69mm-v35h", vulnerable_version_range=text)]
                self.assertEqual(checkout.findings(kind), {"GHSA-jqr7-69mm-v35h"})
                self.assertEqual(checkout.run()[0], 1)

    def test_an_invisible_character_never_clears_a_pin(self):
        # Without the character, each of these ranges is a fix that the vt-1.0.0 pin holds.
        for text in ("main from d7bb4b863 up​to but not including 0af5c9e55",
                     "main from d7bb4b863 ͏up to but not including 0af5c9e55",
                     "main from d7bb4b863 up to but not including️ 0af5c9e55"):
            with self.subTest(text=text):
                checkout = Checkout(self, ghostty=VT)
                checkout.net.advisories[GHOSTTY] = [by_id("GHSA-jqr7-69mm-v35h", vulnerable_version_range=text, patched_versions="")]
                self.assertEqual(checkout.findings("unread"), {"GHSA-jqr7-69mm-v35h"})
                code, out = checkout.run()
                self.assertEqual(code, 1)
                self.assertIn(f"{NOT_ASCII}.", out)
        checkout = vmm(self, "͏< bbbbbbb", {"bbbbbbb": "ahead"})
        self.assertEqual(checkout.run()[0], 1)

    def test_a_patched_list_with_an_empty_element_is_unread(self):
        for patched in (",1.4.0", "1.4.0,", "1.4.0,,1.3.0"):
            with self.subTest(patched=patched):
                checkout = Checkout(self, ghostty=VT)
                checkout.net.advisories[GHOSTTY] = [by_id("GHSA-v8cw-fg4j-jcc4", vulnerable_version_range="< 1.4.0",
                                                          patched_versions=patched)]
                self.assertEqual(checkout.findings("unread"), {"GHSA-v8cw-fg4j-jcc4"})
                self.assertEqual(checkout.run()[0], 1)
