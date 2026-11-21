#!/usr/bin/env python3
"""Prepare checksum-bound upstream gVisor tools without changing project Go."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import sys
import tempfile
import urllib.request


LIMIT = 256 * 1024 * 1024


class HTTPSRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, newurl):
        if not newurl.startswith("https://"):
            raise ValueError("a tool download left HTTPS")
        return super().redirect_request(request, fp, code, message, headers, newurl)


def checked(path, pin):
    if path.is_symlink() or not path.is_file():
        return False
    digest = hashlib.sha256()
    size = 0
    with path.open("rb") as source:
        for data in iter(lambda: source.read(1024 * 1024), b""):
            size += len(data)
            if size > pin.get("size", LIMIT):
                return False
            digest.update(data)
    return digest.hexdigest() == pin["sha256"] and ("size" not in pin or size == pin["size"])


def download(cache, pin):
    if not pin["url"].startswith("https://"):
        raise ValueError("the selected tool has no HTTPS pin")
    path = cache / pin["url"].rsplit("/", 1)[1]
    if path.exists():
        if not checked(path, pin):
            raise ValueError("an existing tool input differs; retain it and choose a fresh cache")
        return path
    descriptor, temporary = tempfile.mkstemp(prefix=".gvisor-download-", dir=cache)
    temporary = Path(temporary)
    published = False
    try:
        opener = urllib.request.build_opener(HTTPSRedirect())
        with os.fdopen(descriptor, "wb") as target, opener.open(pin["url"], timeout=60) as source:
            size = 0
            while data := source.read(1024 * 1024):
                size += len(data)
                if size > pin.get("size", LIMIT):
                    raise ValueError("the tool download exceeds its pinned size or bound")
                target.write(data)
            target.flush()
            os.fsync(target.fileno())
        if not checked(temporary, pin):
            raise ValueError("the official tool bytes do not match their selected checksum")
        os.link(temporary, path)  # Never replace another preparation's input.
        published = True
        return path
    finally:
        if published:
            temporary.unlink(missing_ok=True)


def prepare(args):
    lock_path = args.lock.resolve(strict=True)
    lock = json.loads(lock_path.read_text())
    architecture = {"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(platform.machine())
    host = sys.platform + "/" + str(architecture)
    if lock["version"] != 1 or host not in lock["artifacts"]:
        raise ValueError("the build host has no exact upstream tool pins")
    output = args.output.resolve()
    if output.exists():
        raise ValueError("the output already exists; retain it and choose a new preparation")
    cache = args.cache.resolve()
    cache.mkdir(mode=0o700, parents=True, exist_ok=True)
    pins = lock["artifacts"][host]
    bazel = download(cache, pins["bazel"])
    sdk = download(cache, pins["go_sdk"])
    output.mkdir(mode=0o700, parents=True)
    binary = output / "bazel"
    shutil.copyfile(bazel, binary)
    if not checked(binary, pins["bazel"]):
        raise ValueError("the copied Bazel differs from its original checked bytes")
    binary.chmod(0o555)
    # Bazel's source-selected rules_go from_file download consumes this exact
    # archive through distdir and applies the upstream SDK patch. No extraction,
    # PATH rewrite, GOTOOLCHAIN change or project Go execution occurs here.
    distdir = output / "distdir"
    distdir.mkdir(mode=0o700)
    shutil.copyfile(sdk, distdir / sdk.name)
    if not checked(distdir / sdk.name, pins["go_sdk"]):
        raise ValueError("the copied upstream Go SDK differs")
    record = {"version": 1, "host_platform": host,
              "lock_sha256": hashlib.sha256(lock_path.read_bytes()).hexdigest(),
              "upstream_commit": lock["upstream_commit"],
              "bazel_version": lock["bazel_version"],
              "go_sdk_version": lock["go_sdk_version"], "artifacts": pins}
    (output / "tools.json").write_text(json.dumps(record, indent=2) + "\n")
    if args.env_file:
        values = {"GVISOR_BAZEL": str(binary), "GVISOR_TOOL_INPUTS": str(output / "tools.json"),
                  "GVISOR_DISTDIR": str(distdir)}
        with args.env_file.open("a") as environment:
            for name, value in values.items():
                if "\n" in value or "\r" in value:
                    raise ValueError("a prepared tool path contains a control character")
                environment.write(name + "=" + value + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--lock", type=Path, default=Path("third_party/gvisor/build-tools.json"))
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--cache", type=Path, required=True)
    parser.add_argument("--env-file", type=Path)
    args = parser.parse_args()
    try:
        prepare(args)
    except (OSError, ValueError, KeyError) as error:
        print("gvisor tools: " + str(error), file=sys.stderr)
        print("Try: retain this preparation and use the exact current public source pins in a fresh output directory.", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
