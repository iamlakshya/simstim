#!/usr/bin/env python3
"""Build the exact patched companion closure in an existing clean checkout."""

import argparse
import datetime
import hashlib
import json
import os
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


def build_tool_inputs(repository, source, arch):
    """Bind the actual prepared upstream tools without changing project Go."""
    lock_path = repository / "third_party/gvisor/build-tools.json"
    lock = json.loads(lock_path.read_text())
    prepared = Path(os.environ.get("GVISOR_TOOL_INPUTS", "")).resolve(strict=True)
    metadata = json.loads(prepared.read_text())
    host = "linux/" + arch
    pin = lock["artifacts"][host]
    if metadata["host_platform"] != host or metadata["upstream_commit"] != UPSTREAM or metadata["lock_sha256"] != hashlib.sha256(lock_path.read_bytes()).hexdigest():
        raise ValueError("the prepared upstream tool source or host differs")
    bazel = Path(os.environ.get("GVISOR_BAZEL", "")).resolve(strict=True)
    if hashlib.sha256(bazel.read_bytes()).hexdigest() != pin["bazel"]["sha256"]:
        raise ValueError("the actual Bazel executable differs from its checksum pin")
    if bazel.stat().st_size != pin["bazel"]["size"]:
        raise ValueError("the actual Bazel size differs from its pin")
    if run(source, str(bazel), "--version", capture=True).strip() != "bazel " + lock["bazel_version"]:
        raise ValueError("the actual Bazel version differs from the source-selected version")
    # The root version file is a Git symlink, not a literal version blob.
    if (source / "images/default/bazelversion").read_text().strip() != lock["bazel_version"]:
        raise ValueError("the upstream Bazel version target changed")
    go_mod = (source / "go.mod").read_text()
    if not re.search(r"(?m)^go\s+" + re.escape(lock["go_sdk_version"]) + r"\s*$", go_mod) or re.search(r"(?m)^toolchain\s+", go_mod):
        raise ValueError("the upstream Go from_file selection changed")
    module = (source / "MODULE.bazel").read_text()
    sdk_patch = Path(lock["sdk_patch"])
    sdk_label = "//" + sdk_patch.parent.as_posix() + ":" + sdk_patch.name
    if ('version = "' + lock["rules_go"] + '"') not in module or "go_sdk.from_file" not in module or sdk_label not in module:
        raise ValueError("the actual rules_go SDK/patch declaration changed")
    for name, expected in lock["source_files"].items():
        if hashlib.sha256((source / name).read_bytes()).hexdigest() != expected:
            raise ValueError("the actual upstream build input differs: " + name)
    distdir = Path(os.environ.get("GVISOR_DISTDIR", "")).resolve(strict=True)
    archive = distdir / pin["go_sdk"]["url"].rsplit("/", 1)[1]
    if archive.stat().st_size != pin["go_sdk"]["size"] or hashlib.sha256(archive.read_bytes()).hexdigest() != pin["go_sdk"]["sha256"]:
        raise ValueError("the source-selected upstream Go archive changed")
    inputs = {"version": 1, "upstream_commit": UPSTREAM,
              "tool_scope": "upstream-gvisor-only",
              "bazel": {"version": lock["bazel_version"], "sha256": pin["bazel"]["sha256"]},
              "go_sdk": {"version": lock["go_sdk_version"], "sha256": pin["go_sdk"]["sha256"],
                         "selection": "rules_go.from_file", "patch_sha256": hashlib.sha256((source / lock["sdk_patch"]).read_bytes()).hexdigest()},
              "rules_go": lock["rules_go"], "gazelle": lock["gazelle"],
              "source_files": {name: hashlib.sha256((source / name).read_bytes()).hexdigest()
                               for name in (*lock["source_files"], "images/default/bazelversion")}}
    return str(bazel), str(distdir), inputs


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
        # Git records only the owner's execute bit; the rest of the mode
        # comes from the checkout's umask, so compare just that bit.
        if not target.is_file() or (target.stat().st_mode & 0o100) != (entry["mode"] & 0o100):
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
    bazel, distdir, build_inputs = build_tool_inputs(repository, source, arch)
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
        # Upstream builds its release runsc with -c opt. The default fastbuild
        # mode makes rules_go strip the symbols that check_go_entry reads.
        native_config = "x86_64" if arch == "amd64" else "aarch64"
        run(source, bazel, "build", "--config=" + native_config, "--compilation_mode=opt", "--distdir=" + distdir, "--lockfile_mode=update", "--stamp",
            "--workspace_status_command=" + str(status), *TARGETS.values())
    items = []
    for name, target in TARGETS.items():
        files = run(source, bazel, "cquery", "--config=" + native_config, "--compilation_mode=opt", "--distdir=" + distdir, "--lockfile_mode=update", "--output=files", target, capture=True).splitlines()
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
    generated_lock = source / "MODULE.bazel.lock"
    if not generated_lock.is_file():
        raise ValueError("the actual Bazel build did not retain its resolved module lock")
    build_inputs["resolved_module_lock_sha256"] = hashlib.sha256(generated_lock.read_bytes()).hexdigest()
    build_inputs["runtime_patch_sha256"] = digest
    build_inputs["runtime_source_map_sha256"] = hashlib.sha256((patch.parent / "hostguard-inputs.json").read_bytes()).hexdigest()
    build_inputs["items"] = items
    source_epoch = int(run(source, "git", "show", "-s", "--format=%ct", UPSTREAM, capture=True).strip())
    source_time = datetime.datetime.fromtimestamp(source_epoch, datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    build_inputs["source_epoch"] = source_epoch
    input_bytes = (json.dumps(build_inputs, indent=2) + "\n").encode()
    (output / "build-inputs.json").write_bytes(input_bytes)
    shutil.copyfile(generated_lock, output / "MODULE.bazel.lock")
    # This names observed inputs and outputs. It is not a claim that the
    # runner image, apt or the complete transitive toolchain was pre-pinned.
    sbom = {"spdxVersion": "SPDX-2.3", "dataLicense": "CC0-1.0", "SPDXID": "SPDXRef-DOCUMENT",
            "name": "gvisor-native-" + version + "-" + arch,
            "documentNamespace": "https://gvisor.dev/build/" + version + "/" + arch + "/" + hashlib.sha256(input_bytes).hexdigest(),
            "creationInfo": {"creators": ["Tool: build-gvisor-runtime.py"], "created": source_time},
            "documentDescribes": ["SPDXRef-" + item["name"].replace("_", "-") for item in items],
            "packages": [{"name": item["name"], "SPDXID": "SPDXRef-" + item["name"].replace("_", "-"),
                          "versionInfo": version, "downloadLocation": "NOASSERTION", "filesAnalyzed": False,
                          "licenseConcluded": "NOASSERTION", "licenseDeclared": "NOASSERTION", "copyrightText": "NOASSERTION",
                          "checksums": [{"algorithm": "SHA256", "checksumValue": item["sha256"]}]} for item in items],
            "annotations": [{"annotationType": "OTHER", "annotator": "Tool: build-gvisor-runtime.py",
                             "annotationDate": source_time, "comment": "Reproducible source epoch; observed build inputs follow.\n" + input_bytes.decode()}]}
    sbom_bytes = (json.dumps(sbom, indent=2) + "\n").encode()
    (output / "build.spdx.json").write_bytes(sbom_bytes)
    record = {"upstream_commit": UPSTREAM, "patch_sha256": digest,
              "version": version, "arch": arch, "items": items,
              "build_inputs_sha256": hashlib.sha256(input_bytes).hexdigest(),
              "sbom_sha256": hashlib.sha256(sbom_bytes).hexdigest()}
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
        print("Try: retain this build and use a clean checkout of the pinned source, with Bazel, readelf and gVisor's build packages (build-essential, crossbuild-essential-amd64 and crossbuild-essential-arm64) installed.", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
