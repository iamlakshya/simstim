#!/usr/bin/env python3
"""Local synthetic preparation checks. No tool execution or network access."""

import hashlib
import importlib.util
import json
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("gvisor_prepare", Path(__file__).with_name("prepare-gvisor-build.py"))
PREPARE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(PREPARE)


class PreparationTests(unittest.TestCase):
    def fixture(self, root):
        cache = root / "downloads"
        cache.mkdir()
        artifacts = {}
        for name, data in (("bazel", b"inert-bazel-fixture"), ("go_sdk", b"inert-sdk-fixture")):
            (cache / name).write_bytes(data)
            artifacts[name] = {"url": "https://example.invalid/" + name,
                               "sha256": hashlib.sha256(data).hexdigest(), "size": len(data)}
        lock = root / "tools.json"
        lock.write_text(json.dumps({"version": 1, "upstream_commit": "a" * 40,
                                   "bazel_version": "8.8.1", "go_sdk_version": "1.27.3",
                                   "artifacts": {"linux/amd64": artifacts}}))
        return SimpleNamespace(lock=lock, output=root / "prepared", cache=cache, env_file=root / "environment")

    def test_preparation_uses_only_checked_bytes_and_scoped_variables(self):
        with tempfile.TemporaryDirectory() as temporary:
            args = self.fixture(Path(temporary))
            with patch.object(PREPARE.platform, "machine", return_value="x86_64"), patch.object(PREPARE.sys, "platform", "linux"), patch.object(PREPARE.urllib.request, "build_opener", side_effect=AssertionError("no network")):
                PREPARE.prepare(args)
                with self.assertRaisesRegex(ValueError, "already exists"):
                    PREPARE.prepare(args)
            self.assertEqual((args.output / "bazel").read_bytes(), b"inert-bazel-fixture")
            self.assertEqual((args.output / "distdir/go_sdk").read_bytes(), b"inert-sdk-fixture")
            lines = args.env_file.read_text().splitlines()
            self.assertEqual({line.split("=", 1)[0] for line in lines}, {"GVISOR_BAZEL", "GVISOR_TOOL_INPUTS", "GVISOR_DISTDIR"})
            self.assertFalse((args.output / "go").exists())

    def test_changed_cache_input_is_kept_and_refused(self):
        with tempfile.TemporaryDirectory() as temporary:
            args = self.fixture(Path(temporary))
            (args.cache / "bazel").write_bytes(b"another input")
            with patch.object(PREPARE.platform, "machine", return_value="x86_64"), patch.object(PREPARE.sys, "platform", "linux"), patch.object(PREPARE.urllib.request, "build_opener", side_effect=AssertionError("no network")):
                with self.assertRaisesRegex(ValueError, "existing tool input differs"):
                    PREPARE.prepare(args)
            self.assertEqual((args.cache / "bazel").read_bytes(), b"another input")
            self.assertFalse(args.output.exists())

    def test_download_requires_https_and_exact_size(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            with self.assertRaisesRegex(ValueError, "HTTPS"):
                PREPARE.download(root, {"url": "http://example.invalid/tool"})
            data = root / "tool"
            data.write_bytes(b"tool")
            pin = {"sha256": hashlib.sha256(b"tool").hexdigest(), "size": 3}
            self.assertFalse(PREPARE.checked(data, pin))
            pin["size"] = 4
            self.assertTrue(PREPARE.checked(data, pin))
            alias = root / "alias"
            alias.symlink_to(data)
            self.assertFalse(PREPARE.checked(alias, pin))


if __name__ == "__main__":
    unittest.main()
