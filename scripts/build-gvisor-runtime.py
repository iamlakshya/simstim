#!/usr/bin/env python3
"""Build the exact patched companion closure in an existing clean checkout."""

import argparse
import hashlib
import json
from pathlib import Path
import platform
import re
import shutil
import subprocess
import sys
import tempfile


UPSTREAM = "7a675ac66a1e2357abdaa6cccedabcd3cce9aa5c"
RELEASE = "release-20261102.0"
TARGETS = {
    "runsc": "//runsc:runsc",
    "gvisor_sentry": "//runsc/cmd/sentry:gvisor_sentry",
    "gvisor-sentry-prewarmer": "//runsc/prewarmer:prewarmer_binary",
    "gvisor-proc-umounter": "//runsc/procumounter:procumounter_binary",
    "runsc-fd-parking": "//runsc/fdparking:fdparking_binary",
}


def run(source, *args, capture=False):
    return subprocess.run(args, cwd=source, check=True, text=True,
                          stdout=subprocess.PIPE if capture else None).stdout


def verify_tree(source, inputs, phase):
    """Check exact native source pre/postimages; zero-context matching is exact."""
    if set(inputs) != {"upstream_commit", "files"} or inputs["upstream_commit"] != UPSTREAM:
        raise ValueError("the patch inputs do not name the exact upstream revision")
    for entry in inputs["files"]:
        if set(entry) != {"path", "before_sha256", "after_sha256", "mode"}:
            raise ValueError("the patch inputs have an unknown file record")
        name = Path(entry["path"])
        if not name.parts or name.is_absolute() or ".." in name.parts or name.parts[0] != "runsc":
            raise ValueError("a patch input escapes its closed upstream source section")
        target = source
        for part in name.parts:
            target = target / part
            if target.is_symlink():
                raise ValueError("a patch input is a symbolic link")
        expected = entry[phase + "_sha256"]
        if expected is None:
            if target.exists():
                raise ValueError("a new patch input already exists: " + str(name))
            continue
        if not target.is_file() or target.stat().st_mode & 0o777 != entry["mode"]:
            raise ValueError("a patch input has another native type or mode: " + str(name))
        if hashlib.sha256(target.read_bytes()).hexdigest() != expected:
            raise ValueError("a patch input differs from its exact " + phase + " image: " + str(name))


def check_go_entry(source, binary):
    """Inspect the built ELF; do not execute a runtime to inspect its entry."""
    header = run(source, "readelf", "--file-header", str(binary), capture=True)
    entry = re.search(r"Entry point address:\s+(0x[0-9a-fA-F]+)", header)
    if entry is None:
        raise ValueError("the runtime has no inspectable ELF entry")
    symbols = run(source, "readelf", "--wide", "--symbols", str(binary), capture=True)
    for line in symbols.splitlines():
        fields = line.split()
        if len(fields) >= 8 and fields[-1] in {
            "gvisor.dev/gvisor/runsc/hostguard.entry",
            "gvisor.dev/gvisor/runsc/hostguard.entry.abi0",
        } and int(fields[1], 16) == int(entry.group(1), 16):
            return
    raise ValueError("the Go runtime ELF entry bypasses its pre-init memory guard")


def build(args):
    repository = Path(__file__).resolve().parents[1]
    source = args.source.resolve(strict=True)
    output = args.output.resolve()
    arch = {"x86_64": "amd64", "aarch64": "arm64"}.get(platform.machine())
    if sys.platform != "linux" or arch is None:
        raise ValueError("build the native closure on Linux amd64 or arm64")
    if output.exists():
        raise ValueError("the output directory already exists; retain it and choose a new one")
    if run(source, "git", "rev-parse", "HEAD", capture=True).strip() != UPSTREAM:
        raise ValueError("the checkout does not name the pinned public upstream commit")
    if run(source, "git", "status", "--porcelain=v1", "--untracked-files=all", capture=True):
        raise ValueError("the upstream checkout has other changes")
    patch = repository / "third_party/gvisor/hostguard.patch"
    digest = hashlib.sha256(patch.read_bytes()).hexdigest()
    pin = (repository / "internal/wall/gvisor/native_build.go").read_text()
    if 'NativePatchSHA256V1 = "' + digest + '"' not in pin:
        raise ValueError("the runtime patch differs from its actual source pin")
    version = RELEASE + "-" + digest[:8]
    if 'NativeBuildVersionV1 = "' + version + '"' not in pin:
        raise ValueError("the runtime version is not derived from the actual pinned patch")
    inputs = json.loads((patch.parent / "hostguard-inputs.json").read_text())
    verify_tree(source, inputs, "before")
    run(source, "git", "apply", "--unidiff-zero", "--check", str(patch))
    run(source, "git", "apply", "--unidiff-zero", str(patch))
    verify_tree(source, inputs, "after")
    output.mkdir(mode=0o700, parents=True)
    # A single invocation builds all five companions from the same patched
    # checkout and stamps both Go entries with the same source-derived version.
    with tempfile.TemporaryDirectory(prefix="gvisor-build-status-") as private:
        status = Path(private) / "status"
        status.write_text("#!/bin/sh\nprintf '%s\\n' 'STABLE_VERSION " + version + "'\n")
        status.chmod(0o700)
        run(source, "bazel", "build", "--stamp",
            "--workspace_status_command=" + str(status), *TARGETS.values())
    items = []
    for name, target in TARGETS.items():
        files = run(source, "bazel", "cquery", "--output=files", target, capture=True).splitlines()
        if len(files) != 1:
            raise ValueError("the runtime target does not produce exactly one companion: " + name)
        built = source / files[0]
        if name in {"runsc", "gvisor_sentry"}:
            check_go_entry(source, built)
        target_file = output / name
        shutil.copyfile(built, target_file)
        target_file.chmod(0o555)
        data = target_file.read_bytes()
        items.append({"kind": "gvisor", "name": name, "version": version,
                      "arch": arch, "released": "2026-11-02",
                      "sha256": hashlib.sha256(data).hexdigest(), "size": len(data)})
    record = {"upstream_commit": UPSTREAM, "patch_sha256": digest,
              "version": version, "arch": arch, "items": items}
    (output / "build.json").write_text(json.dumps(record, indent=2) + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path, help="existing clean pinned upstream checkout")
    parser.add_argument("output", type=Path, help="new output directory")
    args = parser.parse_args()
    try:
        build(args)
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        print("Error: " + str(error), file=sys.stderr)
        print("Try: retain this build and use a clean checkout of the pinned source with Bazel and readelf installed.", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
