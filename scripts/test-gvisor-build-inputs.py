#!/usr/bin/env python3
"""Synthetic upstream-input tests. No Bazel, Go, compiler or network runs."""

import hashlib
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("gvisor_builder", Path(__file__).with_name("build-gvisor-runtime.py"))
BUILDER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(BUILDER)


def digest(data):
    return hashlib.sha256(data).hexdigest()


class BuildInputsTests(unittest.TestCase):
    def fixture(self, root):
        repository, source, tools = root / "repository", root / "source", root / "tools"
        (repository / "third_party/gvisor").mkdir(parents=True)
        (tools / "distdir").mkdir(parents=True)
        files = {"MODULE.bazel": b'bazel_dep(name = "rules_go", version = "0.63.0")\ngo_sdk.from_file(go_mod = "//:go.mod", patches = ["//tools:go_types_memoize.patch"])\n',
                 "go.mod": b"module example.invalid/public-fixture\ngo 1.27.3\n", ".bazelrc": b"public fixture\n",
                 "tools/go_types_memoize.patch": b"public inert patch fixture\n",
                 "images/default/bazelversion": b"8.8.1\n"}
        for name, data in files.items():
            target = source / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(data)
        bazel, sdk = b"inert bazel bytes", b"inert SDK bytes"
        (tools / "bazel").write_bytes(bazel)
        (tools / "distdir/go.tar.gz").write_bytes(sdk)
        lock = {"version": 1, "upstream_commit": BUILDER.UPSTREAM, "bazel_version": "8.8.1", "go_sdk_version": "1.27.3",
                "rules_go": "0.63.0", "gazelle": "0.51.3", "sdk_patch": "tools/go_types_memoize.patch",
                "source_files": {name: digest(data) for name, data in files.items() if name != "images/default/bazelversion"},
                "artifacts": {"linux/amd64": {"bazel": {"sha256": digest(bazel), "size": len(bazel)},
                                                "go_sdk": {"sha256": digest(sdk), "size": len(sdk), "url": "https://example.invalid/go.tar.gz"}}}}
        lock_path = repository / "third_party/gvisor/build-tools.json"
        lock_path.write_text(json.dumps(lock))
        metadata = {"host_platform": "linux/amd64", "upstream_commit": BUILDER.UPSTREAM, "lock_sha256": digest(lock_path.read_bytes())}
        (tools / "tools.json").write_text(json.dumps(metadata))
        environment = {"GVISOR_BAZEL": str(tools / "bazel"), "GVISOR_TOOL_INPUTS": str(tools / "tools.json"), "GVISOR_DISTDIR": str(tools / "distdir")}
        return repository, source, environment

    def test_bazel_sdk_label_is_distinct_from_filesystem_patch_path(self):
        with tempfile.TemporaryDirectory() as temporary:
            repository, source, environment = self.fixture(Path(temporary))
            with patch.dict(os.environ, environment), patch.object(BUILDER, "run", return_value="bazel 8.8.1\n") as run:
                _, _, inputs = BUILDER.build_tool_inputs(repository, source, "amd64")
                run.assert_called_once()
            self.assertEqual(inputs["go_sdk"]["version"], "1.27.3")
            self.assertEqual(inputs["go_sdk"]["patch_sha256"], digest((source / "tools/go_types_memoize.patch").read_bytes()))

    def test_original_sdk_patch_bytes_cannot_be_replaced_by_another_input(self):
        with tempfile.TemporaryDirectory() as temporary:
            repository, source, environment = self.fixture(Path(temporary))
            (source / "tools/go_types_memoize.patch").write_bytes(b"another patch")
            with patch.dict(os.environ, environment), patch.object(BUILDER, "run", return_value="bazel 8.8.1\n"):
                with self.assertRaisesRegex(ValueError, "upstream build input differs"):
                    BUILDER.build_tool_inputs(repository, source, "amd64")


if __name__ == "__main__":
    unittest.main()
