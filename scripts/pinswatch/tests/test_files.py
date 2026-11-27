"""The files that the watch reads, and its command line."""
import contextlib
import io
from pathlib import Path

from pinswatch import cli, files
from pinswatch.tests.support import OLD, VT, Case, Checkout

REPOSITORY = Path(__file__).resolve().parents[3]


class FileTests(Case):
    def test_the_active_ghostty_dependency_counts(self):
        url = "https://github.com/ghostty-org/ghostty/archive/{}.tar.gz"
        commented = [f'.dependencies = .{{\n    // .ghostty = .{{ .url = "{url.format(VT)}" }},\n'
                     f'    .ghostty = .{{ .url = "{url.format(OLD)}" }},\n}},\n',
                     f'.dependencies = .{{\n    .ghostty = .{{\n        // .url = "{url.format(VT)}",\n'
                     f'        .url = "{url.format(OLD)}", // the pin\n    }},\n}},\n']
        for text in commented:
            with self.subTest(text=text):
                checkout = Checkout(self)
                checkout.write(files.ZON, ".{\n" + text + "}\n")
                self.assertEqual(files.ghostty_pin(checkout.root).commit, OLD)
        checkout = Checkout(self)
        checkout.write(files.ZON, f'.{{ .dependencies = .{{ // .ghostty = .{{ .url = "{url.format(OLD)}" }},\n }} }}\n')
        code, out = checkout.run()
        self.assertEqual(code, 1)
        self.assertIn("Could not find the Ghostty commit in vt/build.zig.zon.", out)

    def test_no_ghostty_commit(self):
        checkout = Checkout(self)
        checkout.write(files.ZON, '.{ .dependencies = .{ .ghostty = .{ .url = "https://example.com/vt-1.0.0.tar.gz" } } }')
        code, out = checkout.run()
        self.assertEqual(code, 1)
        self.assertIn("Could not find the Ghostty commit in vt/build.zig.zon.\n"
                      "Try: Keep the dependency's URL in the form …/archive/<commit>.tar.gz", out)

    def test_a_file_that_is_not_utf8(self):
        for name, message in ((files.LOCK, "guest/supply.lock: cannot read it: it is not UTF-8 text.\nTry: Save the file as UTF-8"),
                              (files.REVIEWED, "scripts/pins-watch-reviewed.toml: cannot read it: it is not UTF-8 text."),
                              (files.ZON, "vt/build.zig.zon: cannot read it: it is not UTF-8 text.\nTry: Save the file as UTF-8, as Zig needs")):
            with self.subTest(name=name):
                checkout = Checkout(self, ghostty=VT)
                checkout.write(name, b"version = 1\n# \xff\xfe\n")
                code, out = checkout.run()
                self.assertEqual(code, 1)
                self.assertIn(message, out)
                self.assertNotIn("Traceback", out)
                self.assert_try_lines(checkout.err)

    def test_bad_files(self):
        cases = [(files.LOCK, "version = 2\n", "guest/supply.lock: version 2 is not known."),
                 (files.LOCK, "version = [\n", "guest/supply.lock: not valid TOML"),
                 (files.LOCK, 'version = 1\n[[pin]]\nname = "Big Name"\n', 'the pin name "Big Name" is not a short name.'),
                 (files.LOCK, f'version = 1\n[[pin]]\nname = "a"\nref = "1"\ncommit = "{VT[:12]}"\nwatch = "osv"\n',
                  'the pin "a" has a commit that is not 40 lower-case hex characters.'),
                 (files.LOCK, f'version = 1\n[[pin]]\nname = "a"\nref = "1"\ncommit = "{VT}"\nwatch = "web"\n',
                  "the pin \"a\" has watch = 'web'."),
                 (files.LOCK, f'version = 1\n[[pin]]\nname = "a"\nref = "1"\ncommit = "{VT}"\n', 'the pin "a" has no repo.'),
                 (files.LOCK, f'version = 1\n[[pin]]\nname = "a"\nref = "v1.0͏"\ncommit = "{VT}"\nwatch = "osv"\n',
                  'the pin "a" has a ref with a space, or a character that is not printable ASCII.'),
                 (files.LOCK, f'version = 1\n[[pin]]\nname = "a"\nref = "v1.0"\ncommit = "{VT}"\nwatch = "osv"\ntag = "v{{version}}️"\n',
                  'the pin "a" has a tag form that is not a tag name with {version} in it.'),
                 (files.LOCK, f'version = 1\n[[pin]]\nname = "a"\nref = "1"\ncommit = "{VT}"\nwatch = "osv"\ntag = "v1"\n',
                  'the pin "a" has a tag form that is not a tag name with {version} in it.'),
                 (files.LOCK, f'version = 1\n[[pin]]\nname = "ghostty"\nref = "1"\ncommit = "{VT}"\nwatch = "osv"\n',
                  'two pins are named "ghostty".'),
                 (files.REVIEWED, '[[verdict]]\nadvisory = "GHSA-2222-3333-4444"\npin = "ghostty"\n'
                  f'commit = "{VT}"\nverdict = "fine"\nwhy = "x"\n', 'verdict 1 says verdict = "fine".'),
                 (files.REVIEWED, '[[verdict]]\nadvisory = "GHSA-2222-3333-4444"\n', "verdict 1 has no pin.")]
        for name, text, message in cases:
            with self.subTest(text=text):
                checkout = Checkout(self, ghostty=VT)
                checkout.write(name, text)
                code, out = checkout.run()
                self.assertEqual(code, 1)
                self.assertIn(message, out)
                self.assert_try_lines(checkout.err)

    def test_a_bad_argument_says_what_to_try(self):
        err = io.StringIO()
        with contextlib.redirect_stderr(err), self.assertRaises(SystemExit) as stop:
            cli.main(["--unknown"])
        self.assertEqual(stop.exception.code, 2)
        self.assertIn("pins-watch: unrecognized arguments: --unknown.\nTry: python3 scripts/pins-watch.py --help", err.getvalue())

    def test_the_repository_files(self):
        """The live files parse. This holds after P0.1 moves the pin, and with real pins and verdicts."""
        self.assertTrue(files.HEX40.fullmatch(files.ghostty_pin(REPOSITORY).commit))
        self.assertEqual(files.lock_pins(REPOSITORY)[1], [])
        self.assertEqual(files.load_verdicts(REPOSITORY)[1], [])
