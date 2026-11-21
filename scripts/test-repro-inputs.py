#!/usr/bin/env python3
"""Offline driver guard/argument tests; these are not image or build proofs."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

SOURCE = Path(__file__).resolve().parent.parent


class DriverInputs(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / "repo"
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.env = {"PATH": str(self.bin) + os.pathsep + os.environ["PATH"]}
        temporary = self.root / "tmp"
        temporary.mkdir()
        self.env["TMPDIR"] = str(temporary)
        for name in ("HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_CACHE_HOME",
                     "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR",
                     "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"):
            path = self.root / name
            path.mkdir()
            self.env[name] = str(path)

    def copy(self, name):
        target = self.repo / name
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(SOURCE / name, target)
        return target

    def fake_go(self):
        path = self.bin / "go"
        # This process only records argv; it never builds or checks an image.
        path.write_text("#!/usr/bin/env python3\nimport json,sys\nfrom pathlib import Path\n"
                        + "Path(" + repr(str(self.root / "argv.json")) + ").write_text(json.dumps(sys.argv[1:]))\n")
        path.chmod(0o700)

    def test_missing_real_inputs_do_not_invoke_a_compiler(self):
        self.fake_go()
        driver = self.copy("scripts/repro.sh")
        result = subprocess.run(["bash", str(driver)], env=self.env, capture_output=True, timeout=5)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(b"native default image pairs", result.stderr)
        self.assertFalse((self.root / "argv.json").exists())

    def test_manifest_path_and_version_are_single_literal_arguments(self):
        self.fake_go()
        driver = self.copy("scripts/write-release-manifest.sh")
        marker = self.root / "unexpected"
        literal = str(self.root / ("image path $(touch " + str(marker) + ")"))
        version = "1.2.3$(touch " + str(marker) + ")"
        self.env["DEFAULT_IMAGE_MANIFEST_INPUTS"] = literal
        result = subprocess.run(["bash", str(driver), version], env=self.env, capture_output=True, timeout=5)
        self.assertEqual(result.returncode, 0, result.stderr)
        args = json.loads((self.root / "argv.json").read_text())
        self.assertEqual(args[args.index("-images") + 1], literal)
        self.assertEqual(args[args.index("-release") + 1], version)
        self.assertIn("-require-images", args)
        self.assertIn("-require-launch", args)
        self.assertIn("-require-image-verifier", args)
        self.assertFalse(marker.exists())

    def test_install_refuses_a_relative_prepared_snapshot_before_build_or_server(self):
        driver = self.copy("scripts/test-install.sh")
        self.copy("internal/brand/brand.go")
        self.copy("go.mod")
        self.env["INSTALL_SNAPSHOT_DIR"] = "relative-snapshot"
        result = subprocess.run(["bash", str(driver)], env=self.env, capture_output=True, timeout=5)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(b"prepared snapshot path must be absolute", result.stderr)


if __name__ == "__main__":
    unittest.main()
