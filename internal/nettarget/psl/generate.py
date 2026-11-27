#!/usr/bin/env python3
"""Generate the negative ancestor data from the exact pinned x/net PSL trie.

No network or IDNA implementation is used. --check validates the selected Go
module, cached source hashes and complete generated output. --write prepares
source from explicit local inputs and replaces the complete data file atomically.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile


HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
OUTPUT = HERE.parent / "wildcard_psl_data.go"
LICENCE = ROOT / "LICENSES/MPL-2.0.txt"
SOURCE_LIMIT = 2 << 20
LAYOUT = {
    "nodesBits": 40, "nodesBitsChildren": 10, "nodesBitsICANN": 1,
    "nodesBitsTextOffset": 16, "nodesBitsTextLength": 6,
    "childrenBitsWildcard": 1, "childrenBitsNodeType": 2,
    "childrenBitsHi": 14, "childrenBitsLo": 14,
    "nodeTypeNormal": 0, "nodeTypeException": 1, "nodeTypeParentOnly": 2,
}


def read_bounded(path):
    with path.open("rb") as stream:
        data = stream.read(SOURCE_LIMIT + 1)
    if len(data) > SOURCE_LIMIT:
        raise ValueError("PSL source exceeds the preparation limit")
    return data


def digest(data):
    return hashlib.sha256(data).hexdigest()


def validate_selected_module(module, pin):
    if (module.get("Path") != pin["module"]
            or module.get("Version") != pin["module_version"]
            or module.get("Replace") is not None
            or module.get("Sum") != pin["module_sum"]
            or module.get("GoModSum") != pin["module_go_mod_sum"]
            or not module.get("Dir")):
        raise ValueError("selected x/net module differs from the PSL source pin")
    return Path(module["Dir"])


def selected_module(pin):
    env = os.environ.copy()
    # Keep the documented bootstrap-plus-cached-toolchain flow. Explicitly
    # disable proxies and direct VCS paths, including inherited private rules.
    env.update(GOPROXY="off", GOSUMDB="off", GONOPROXY="none",
               GOPRIVATE="none", GOVCS="*:off", GOTOOLCHAIN="auto")
    result = subprocess.run(
        ["go", "list", "-mod=readonly", "-m", "-json", pin["module"]],
        cwd=ROOT, env=env, capture_output=True, text=True, timeout=30,
        check=False,
    )
    if result.returncode != 0:
        raise ValueError("cannot inspect the selected cached x/net module")
    return validate_selected_module(json.loads(result.stdout), pin)


def check_root_pin(root, pin):
    mod = read_bounded(root / "go.mod").decode("utf-8")
    versions = re.findall(
        r"(?m)^(?:\s*require\s+|\s+)golang\.org/x/net\s+(v[^\s)]+)", mod,
    )
    if versions != [pin["module_version"]]:
        raise ValueError("go.mod does not declare the exact PSL module pin")
    sums = read_bounded(root / "go.sum").decode("utf-8").splitlines()
    expected = [
        f'{pin["module"]} {pin["module_version"]} {pin["module_sum"]}',
        f'{pin["module"]} {pin["module_version"]}/go.mod {pin["module_go_mod_sum"]}',
    ]
    if any(sums.count(line) != 1 for line in expected):
        raise ValueError("go.sum does not retain the exact PSL module sums")


def source_files(module_dir, pin):
    files = {}
    for relative, expected in pin["source_sha256"].items():
        value = read_bounded(module_dir / relative)
        if digest(value) != expected:
            raise ValueError("cached PSL source differs: " + relative)
        files[relative] = value
    return files


def trie_rules(files, pin):
    """Extract rule metadata, never evaluate a domain against PSL rules."""
    table = files["publicsuffix/table.go"].decode("ascii")
    version = re.search(r'^const version = "([^"]+)"$', table, re.M)
    if version is None or version.group(1) != pin["data_version"]:
        raise ValueError("PSL table version does not match the pinned snapshot")
    for name, expected in LAYOUT.items():
        value = re.search(r"\b" + name + r"\s*=\s*(\d+)\b", table)
        if value is None or int(value.group(1)) != expected:
            raise ValueError("unsupported PSL trie layout")
    tld = re.search(r"\bnumTLD\s*=\s*(\d+)\b", table)
    if tld is None:
        raise ValueError("PSL root count is missing")
    nodes = files["publicsuffix/data/nodes"]
    children = files["publicsuffix/data/children"]
    text = files["publicsuffix/data/text"]
    if len(nodes) % 5 or len(children) % 4 or len(nodes) // 5 != pin["expected"]["nodes"]:
        raise ValueError("invalid PSL table widths or node count")
    rules, anchors, seen = [], [], set()

    def walk(index, tail, blocked=False, depth=0):
        if index in seen or index >= len(nodes) // 5 or depth > 127:
            raise ValueError("PSL trie is cyclic or out of bounds")
        seen.add(index)
        node = int.from_bytes(nodes[index * 5:index * 5 + 5], "big")
        length, offset = node & 63, (node >> 6) & 65535
        label = text[offset:offset + length].decode("ascii")
        if len(label) != length or not re.fullmatch(r"[a-z0-9_-]{1,63}", label):
            raise ValueError("invalid canonical PSL trie label")
        name = label + ("." + tail if tail else "")
        child_index = (node >> 23) & 1023
        if node >> 33 or child_index >= len(children) // 4:
            raise ValueError("invalid PSL node descriptor")
        child = int.from_bytes(children[child_index * 4:child_index * 4 + 4], "big")
        lo, hi = child & 16383, (child >> 14) & 16383
        kind, wildcard = (child >> 28) & 3, bool((child >> 30) & 1)
        if child >> 31 or kind > 2 or not 0 <= lo <= hi <= len(nodes) // 5:
            raise ValueError("invalid PSL child descriptor")
        if kind == 0:
            rules.append(name)
            if not blocked:
                anchors.append(name)
        elif kind == 1:
            rules.append("!" + name)
        if wildcard:
            rules.append("*." + name)
            if not blocked and kind != 1:
                anchors.append(name)
        for next_index in range(lo, hi):
            walk(next_index, name, blocked or kind == 1, depth + 1)

    for index in range(int(tld.group(1))):
        walk(index, "")
    if len(seen) != len(nodes) // 5:
        raise ValueError("PSL trie has unreachable nodes")
    generated = files["publicsuffix/table_test.go"].decode("ascii")
    block = generated.split("var rules = [...]string{", 1)[1].split("\n}", 1)[0]
    canonical = re.findall(r'^\s*"([a-z0-9_.*!\-]+)",\s*$', block, re.M)
    icann = re.search(r"const numICANNRules = (\d+)", generated)
    if (len(rules) != pin["expected"]["rules"] or len(set(rules)) != len(rules)
            or len(canonical) != len(rules) or set(canonical) != set(rules)
            or icann is None or int(icann.group(1)) != pin["expected"]["icann_rules"]
            or len(rules) - int(icann.group(1)) != pin["expected"]["private_rules"]):
        raise ValueError("PSL trie does not reproduce the full canonical rule metadata")
    return anchors


def proper_ancestors(anchors):
    parents = set()
    for anchor in anchors:
        labels = anchor.split(".")
        for index in range(1, len(labels)):
            parents.add(".".join(labels[index:]))
    return sorted(parents)


def render_data(files, pin):
    parents = proper_ancestors(trie_rules(files, pin))
    body = "".join(name + "\n" for name in parents)
    if (len(parents) != pin["expected"]["ancestor_count"]
            or digest(body.encode("ascii")) != pin["expected"]["ancestor_sha256"]):
        raise ValueError("derived PSL ancestors differ from the source pin")
    provenance = json.dumps(pin, sort_keys=True, separators=(",", ":"))
    lines = [
        "// Code generated by psl/generate.py. DO NOT EDIT.",
        "// SPDX-FileCopyrightText: Public Suffix List contributors",
        "// SPDX-License-Identifier: MPL-2.0", "//",
        "// This Source Code Form is subject to the terms of the Mozilla Public",
        "// License, v. 2.0. If a copy of the MPL was not distributed with this",
        "// file, You can obtain one at https://mozilla.org/MPL/2.0/.",
        "// The complete licence is in LICENSES/MPL-2.0.txt.", "", "package nettarget", "",
        "const pslXNetVersion = " + json.dumps(pin["module_version"]),
        "const pslDataVersion = " + json.dumps(pin["data_version"]),
        "const pslIndexBodySHA256 = " + json.dumps(pin["expected"]["ancestor_sha256"]),
        "const pslIndexCount = " + str(len(parents)),
        "const pslIndexProvenance = `" + provenance + "`", "",
    ]
    chunks = ["".join(name + "\n" for name in parents[i:i + 8]) for i in range(0, len(parents), 8)]
    for index, chunk in enumerate(chunks):
        prefix = "const pslAncestorData = " if index == 0 else "\t"
        suffix = " +" if index + 1 < len(chunks) else ""
        lines.append(prefix + json.dumps(chunk) + suffix)
    return ("\n".join(lines) + "\n").encode("ascii")


def atomic_write(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as stream:
            temporary = Path(stream.name)
            stream.write(value)
            stream.flush()
            os.fsync(stream.fileno())
        temporary.chmod(0o644)
        os.replace(temporary, path)
        temporary = None
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    action = parser.add_mutually_exclusive_group(required=True)
    action.add_argument("--check", action="store_true")
    action.add_argument("--write", action="store_true")
    parser.add_argument("--module-dir", type=Path)
    parser.add_argument("--raw-list", type=Path)
    parser.add_argument("--raw-licence", type=Path)
    args = parser.parse_args()
    pin = json.loads(read_bounded(HERE / "pins.json"))
    check_root_pin(ROOT, pin)
    if args.check:
        if args.module_dir or args.raw_list or args.raw_licence:
            raise ValueError("--check requires the actually selected module")
        module = selected_module(pin)
    else:
        if not args.module_dir or not args.raw_list or not args.raw_licence:
            raise ValueError("--write requires all three explicit local source inputs")
        module = args.module_dir
        if digest(read_bounded(args.raw_list)) != pin["upstream"]["sha256"]:
            raise ValueError("raw PSL snapshot differs from the source pin")
        if digest(read_bounded(args.raw_licence)) != pin["upstream"]["licence_sha256"]:
            raise ValueError("upstream PSL licence differs from the source pin")
    output = render_data(source_files(module, pin), pin)
    if args.check:
        if (read_bounded(OUTPUT) != output
                or digest(read_bounded(LICENCE)) != pin["upstream"]["licence_sha256"]):
            raise ValueError("generated PSL data or licence is stale")
        print("PSL source pin and deterministic ancestor data agree.")
    else:
        atomic_write(LICENCE, read_bounded(args.raw_licence))
        atomic_write(OUTPUT, output)
        print("Prepared the complete pinned PSL ancestor data source.")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError, IndexError, subprocess.SubprocessError):
        raise SystemExit("PSL source preparation failed.\nTry: use the pinned local sources and regenerate the complete ancestor data.") from None
