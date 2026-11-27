"""Help checks stay before an end-of-options marker and never run an action."""
import importlib.util
import json
import os
import shlex
from pathlib import Path
import sys
import tempfile
import unittest


SPEC = importlib.util.spec_from_file_location("docs_commands", Path(__file__).with_name("test-docs-commands.py"))
CHECKER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CHECKER)


class HelpCommands(unittest.TestCase):
    def test_help_precedes_marker_without_changing_other_words(self):
        with tempfile.TemporaryDirectory() as temp:
            binary = Path(temp) / "client"
            marker = Path(temp) / "action-ran"
            binary.write_text("#!" + sys.executable + "\n"
                              "import json,pathlib,sys\n"
                              "args=sys.argv[1:]\n"
                              "help_at=args.index('--help')\n"
                              "if '--' in args and help_at>args.index('--'):\n"
                              " pathlib.Path(" + repr(str(marker)) + ").write_text('ran')\n"
                              " sys.exit(1)\n"
                              "print(json.dumps(args))\n")
            binary.chmod(0o755)
            old_binary = CHECKER.binary
            self.addCleanup(setattr, CHECKER, "binary", old_binary)
            CHECKER.binary = str(binary)
            for words in [
                ["attach", "--id", "--", "box/-old"],
                ["pull", "--id", "--into", "review/topic", "--", "box/old_ID"],
                ["new", "box", "--", "/bin/sh", "-c", "echo hello"],
                ["version"],
            ]:
                with self.subTest(words=words):
                    command = "client " + shlex.join(words)
                    result = CHECKER.check_help(command, dict(os.environ))
                    self.assertEqual(result.returncode, 0, result.stderr)
                    actual = json.loads(result.stdout)
                    at = words.index("--") if "--" in words else len(words)
                    self.assertEqual(actual, words[:at] + ["--help"] + words[at:])
                    self.assertFalse(marker.exists(), "help-only validation ran the action")


if __name__ == "__main__":
    unittest.main()
