"""Source oracles for PSL preparation. These never need network or Go."""

import copy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import generate


def fixture():
    """A metadata trie with private rules and an exception subtree."""
    labels = ["ck", "com", "www", "inside", "amazonaws", "compute", "s3"]
    text = "".join(labels).encode("ascii")
    # kind, wildcard, low child, high child. The exception makes its
    # syntactic descendant inactive, just as the library's early break does.
    descriptors = [
        (2, True, 2, 3), (0, False, 4, 5), (1, False, 3, 4),
        (0, False, 0, 0), (2, False, 5, 7), (2, True, 0, 0),
        (0, False, 0, 0),
    ]
    children, nodes, offset = bytearray(), bytearray(), 0
    for index, (label, descriptor) in enumerate(zip(labels, descriptors)):
        kind, wildcard, lo, hi = descriptor
        children.extend((lo | hi << 14 | kind << 28 | int(wildcard) << 30).to_bytes(4, "big"))
        icann = index < 4
        nodes.extend((len(label) | offset << 6 | int(icann) << 22 | index << 23).to_bytes(5, "big"))
        offset += len(label)
    rules = ["*.ck", "!www.ck", "inside.www.ck", "com", "*.compute.amazonaws.com", "s3.amazonaws.com"]
    table = 'const version = "fixture"\nconst numTLD = 2\n'
    table += "\n".join(f"const {name} = {value}" for name, value in generate.LAYOUT.items())
    tests = "const numICANNRules = 4\nvar rules = [...]string{\n"
    tests += "".join(json.dumps(rule) + ",\n" for rule in rules) + "}\n"
    files = {
        "publicsuffix/table.go": table.encode("ascii"),
        "publicsuffix/table_test.go": tests.encode("ascii"),
        "publicsuffix/data/nodes": bytes(nodes),
        "publicsuffix/data/children": bytes(children),
        "publicsuffix/data/text": text,
    }
    pin = {
        "module_version": "v0.0.0", "data_version": "fixture",
        "expected": {"nodes": 7, "rules": 6, "icann_rules": 4, "private_rules": 2,
                     "ancestor_count": 2,
                     "ancestor_sha256": hashlib.sha256(b"amazonaws.com\ncom\n").hexdigest()},
    }
    return files, pin


class PSLSourceOracles(unittest.TestCase):
    def test_private_and_wildcard_ancestors_exclude_exception_subtrees(self):
        files, pin = fixture()
        anchors = generate.trie_rules(files, pin)
        self.assertIn("s3.amazonaws.com", anchors)
        self.assertIn("compute.amazonaws.com", anchors)
        self.assertNotIn("www.ck", anchors)
        self.assertNotIn("inside.www.ck", anchors)
        self.assertEqual(generate.proper_ancestors(anchors), ["amazonaws.com", "com"])

    def test_only_proper_ancestors_are_negative(self):
        self.assertEqual(generate.proper_ancestors(["s3.amazonaws.com"]), ["amazonaws.com", "com"])
        self.assertEqual(generate.proper_ancestors(["ck"]), [])

    def test_rule_metadata_loss_or_unreachable_nodes_is_rejected(self):
        files, pin = fixture()
        changed = dict(files)
        changed["publicsuffix/table_test.go"] = changed["publicsuffix/table_test.go"].replace(b'"s3.amazonaws.com",\n', b"")
        with self.assertRaises(ValueError):
            generate.trie_rules(changed, pin)
        changed = dict(files)
        changed["publicsuffix/data/nodes"] += changed["publicsuffix/data/nodes"][-5:]
        pin["expected"]["nodes"] += 1
        with self.assertRaises(ValueError):
            generate.trie_rules(changed, pin)

    def test_changed_snapshot_or_layout_is_rejected(self):
        files, pin = fixture()
        for old, new in [(b'"fixture"', b'"other"'), (b"nodesBits = 40", b"nodesBits = 32")]:
            changed = dict(files)
            changed["publicsuffix/table.go"] = changed["publicsuffix/table.go"].replace(old, new)
            with self.assertRaises(ValueError):
                generate.trie_rules(changed, pin)

    def test_selected_version_replacement_and_sums_are_required(self):
        pin = json.loads((generate.HERE / "pins.json").read_text())
        module = {"Path": pin["module"], "Version": pin["module_version"],
                  "Sum": pin["module_sum"], "GoModSum": pin["module_go_mod_sum"], "Dir": "."}
        self.assertEqual(generate.validate_selected_module(module, pin), Path("."))
        for key, value in [("Version", "v0.60.0"), ("Replace", {}), ("Sum", "changed"),
                           ("GoModSum", "changed"), ("Dir", ""), ("Path", "example.invalid/fork")]:
            changed = dict(module)
            changed[key] = value
            with self.assertRaises(ValueError):
                generate.validate_selected_module(changed, pin)

    def test_selected_lookup_keeps_cached_bootstrap_and_blocks_download_paths(self):
        pin = json.loads((generate.HERE / "pins.json").read_text())
        module = {"Path": pin["module"], "Version": pin["module_version"],
                  "Sum": pin["module_sum"], "GoModSum": pin["module_go_mod_sum"], "Dir": "."}
        result = mock.Mock(returncode=0, stdout=json.dumps(module))
        with mock.patch.dict(generate.os.environ, {"GOPRIVATE": "*", "GONOPROXY": "*"}):
            with mock.patch.object(generate.subprocess, "run", return_value=result) as run:
                self.assertEqual(generate.selected_module(pin), Path("."))
        env = run.call_args.kwargs["env"]
        for name, value in {"GOPROXY": "off", "GOSUMDB": "off", "GONOPROXY": "none",
                            "GOPRIVATE": "none", "GOVCS": "*:off", "GOTOOLCHAIN": "auto"}.items():
            self.assertEqual(env[name], value)

    def test_declared_module_pin_cannot_drift(self):
        pin = json.loads((generate.HERE / "pins.json").read_text())
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "go.mod").write_text('module example.invalid/test\nrequire golang.org/x/net ' + pin["module_version"] + "\n")
            sums = (f'{pin["module"]} {pin["module_version"]} {pin["module_sum"]}\n'
                    f'{pin["module"]} {pin["module_version"]}/go.mod {pin["module_go_mod_sum"]}\n')
            (root / "go.sum").write_text(sums)
            generate.check_root_pin(root, pin)
            (root / "go.mod").write_text('module example.invalid/test\nrequire golang.org/x/net v0.60.0\n')
            with self.assertRaises(ValueError):
                generate.check_root_pin(root, pin)

    def test_cached_source_byte_drift_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "table").write_bytes(b"original")
            pin = {"source_sha256": {"table": generate.digest(b"original")}}
            self.assertEqual(generate.source_files(root, pin), {"table": b"original"})
            (root / "table").write_bytes(b"modified")
            with self.assertRaises(ValueError):
                generate.source_files(root, pin)

    def test_rendered_data_has_complete_provenance_and_no_partial_upgrade(self):
        files, pin = fixture()
        rendered = generate.render_data(files, pin)
        self.assertIn(b'const pslIndexCount = 2', rendered)
        self.assertIn(b'"amazonaws.com\\ncom\\n"', rendered)
        self.assertIn(b'SPDX-License-Identifier: MPL-2.0', rendered)
        changed = copy.deepcopy(pin)
        changed["expected"]["ancestor_sha256"] = "stale"
        with self.assertRaises(ValueError):
            generate.render_data(files, changed)
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "data.go"
            target.write_bytes(b"previous complete source")
            with mock.patch.object(generate.os, "replace", side_effect=OSError("interrupted")):
                with self.assertRaises(OSError):
                    generate.atomic_write(target, rendered)
            self.assertEqual(target.read_bytes(), b"previous complete source")
            self.assertEqual(list(Path(directory).iterdir()), [target])
            generate.atomic_write(target, rendered)
            self.assertEqual(target.read_bytes(), rendered)


if __name__ == "__main__":
    unittest.main()
