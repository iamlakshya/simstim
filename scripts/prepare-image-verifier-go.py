#!/usr/bin/env python3
"""Prepare only the isolated verifier compiler from official pinned archives.

This source is used by the protected release job. Normal dependency/integrity
preparation precedes offline builds. It never changes main Go, PATH or go.mod.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import platform
import re
import subprocess
import tarfile
import tempfile
import tomllib

VERSION = "go1.27.4"
HOSTS = {("Linux", "x86_64"): ("linux", "amd64"),
         ("Linux", "aarch64"): ("linux", "arm64"),
         ("Darwin", "x86_64"): ("darwin", "amd64"),
         ("Darwin", "arm64"): ("darwin", "arm64")}


def digest(path: Path) -> str:
    value = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1 << 20), b""):
            value.update(block)
    return value.hexdigest()


def lock_archive(path: Path, host: tuple[str, str]) -> dict:
    document = tomllib.loads(path.read_text())
    if set(document) != {"version", "archive"} or document["version"] != VERSION:
        raise ValueError("unsupported isolated compiler lock")
    rows = document["archive"]
    if not isinstance(rows, list) or len(rows) != 4:
        raise ValueError("the compiler lock must bind the four supported hosts")
    selected = None
    seen = set()
    for row in rows:
        if set(row) != {"os", "arch", "file", "size", "sha256"}:
            raise ValueError("unknown compiler archive field")
        pair = (row["os"], row["arch"])
        if pair not in set(HOSTS.values()) or pair in seen:
            raise ValueError("unsupported or repeated compiler host")
        seen.add(pair)
        expected = f"{VERSION}.{pair[0]}-{pair[1]}.tar.gz"
        if (row["file"] != expected or not isinstance(row["size"], int)
                or not 0 < row["size"] <= 512 << 20
                or not re.fullmatch(r"[0-9a-f]{64}", row["sha256"])):
            raise ValueError("invalid official compiler archive identity")
        if pair == host:
            selected = row
    if selected is None:
        raise ValueError("this build host has no official compiler pin")
    return selected


def verify_archive(path: Path, row: dict) -> None:
    if path.is_symlink() or not path.is_file():
        raise ValueError("compiler archive is not an ordinary file")
    if path.stat().st_size != row["size"] or digest(path) != row["sha256"]:
        raise ValueError("official compiler archive size or SHA-256 mismatch")


def archive_members(archive: tarfile.TarFile) -> list[tarfile.TarInfo]:
    members = archive.getmembers()
    if len(members) > 1_000_000 or sum(item.size for item in members) > 2 << 30:
        raise ValueError("compiler archive exceeds its extraction bound")
    names = set()
    links = set()
    for item in members:
        name = PurePosixPath(item.name)
        if (not item.name or name.is_absolute() or ".." in name.parts
                or str(name) != item.name.rstrip("/")
                or not name.parts or name.parts[0] != "go"
                or "\\" in item.name or "\x00" in item.name
                or str(name) in names):
            raise ValueError("compiler archive has an unsafe or repeated path")
        names.add(str(name))
        if not (item.isdir() or item.isfile() or item.issym() or item.islnk()):
            raise ValueError("compiler archive contains a special file")
        if item.issym() or item.islnk():
            target = PurePosixPath(item.linkname)
            if target.is_absolute() or ".." in target.parts or "\\" in item.linkname:
                raise ValueError("compiler archive has an escaping link")
            if item.islnk() and (not target.parts or target.parts[0] != "go"):
                raise ValueError("compiler archive has a hard link outside its root")
            links.add(name)
        if item.uid < 0 or item.gid < 0 or item.mode & ~0o777:
            raise ValueError("compiler archive has unsupported ownership/mode")
    for item in members:
        if any(parent in links for parent in PurePosixPath(item.name).parents):
            raise ValueError("compiler archive traverses a link parent")
    return members


def isolated_environment(home: Path) -> dict[str, str]:
    locations = {"HOME": home, "USERPROFILE": home,
                 "XDG_CONFIG_HOME": home / "config", "XDG_CACHE_HOME": home / "cache",
                 "XDG_DATA_HOME": home / "data", "XDG_STATE_HOME": home / "state",
                 "XDG_RUNTIME_DIR": home / "run", "XDG_CONFIG_DIRS": home / "config-dirs",
                 "XDG_DATA_DIRS": home / "data-dirs"}
    for path in set(locations.values()):
        path.mkdir(parents=True, exist_ok=True, mode=0o700)
    environment = os.environ.copy()
    environment.update({key: str(path) for key, path in locations.items()})
    return environment


def append_environment(path: Path, values: dict[str, str]) -> None:
    for key, value in values.items():
        if not re.fullmatch(r"IMAGEVERIFY_[A-Z0-9_]+", key) or "\n" in value or "\r" in value:
            raise ValueError("unsafe compiler environment output")
    with path.open("a", encoding="utf-8") as output:
        for key, value in values.items():
            output.write(f"{key}={value}\n")


def prepare(args: argparse.Namespace) -> dict:
    host = HOSTS.get((platform.system(), platform.machine()))
    if host is None:
        raise ValueError("unsupported compiler preparation host")
    row = lock_archive(args.lock, host)
    output = args.output.absolute()
    if output.exists() or output.is_symlink():
        raise ValueError("compiler output exists; preserve it and choose a fresh task directory")
    output.mkdir(parents=True, mode=0o700)
    cache = args.cache.absolute()
    cache.mkdir(parents=True, exist_ok=True, mode=0o700)
    if cache.is_symlink():
        raise ValueError("compiler archive cache is a link")
    archive_path = cache / row["file"]
    if not archive_path.exists():
        with tempfile.NamedTemporaryFile(dir=cache, prefix=".go-download-", delete=False) as part:
            part_path = Path(part.name)
        try:
            subprocess.run(["curl", "--fail", "--silent", "--show-error", "--location",
                            "--proto", "=https", "--proto-redir", "=https",
                            "--max-filesize", str(row["size"]), "--output", str(part_path),
                            "https://go.dev/dl/" + row["file"]], check=True)
            verify_archive(part_path, row)
            # Never replace an archive that another preparation published.
            os.link(part_path, archive_path)
        finally:
            part_path.unlink(missing_ok=True)
    verify_archive(archive_path, row)
    with tarfile.open(archive_path, "r:gz") as archive:
        archive.extractall(output, members=archive_members(archive), numeric_owner=False)
    compiler = output / "go/bin/go"
    if compiler.is_symlink() or not compiler.is_file() or not os.access(compiler, os.X_OK):
        raise ValueError("official archive did not produce its ordinary Go executable")
    environment = isolated_environment(output / "state/home")
    environment.update({"GOTOOLCHAIN": "local", "GOMODCACHE": str(output / "state/mod"),
                        "GOCACHE": str(output / "state/build")})
    version = subprocess.run([str(compiler), "version"], env=environment,
                             check=True, text=True, capture_output=True).stdout.split()
    if len(version) < 3 or version[2] != VERSION:
        raise ValueError("official compiler executable has a different version")
    module = args.module.absolute()
    main_module = args.main_module.absolute()
    protected = [module / "go.mod", module / "go.sum", main_module / "go.mod", main_module / "go.sum"]
    before = {str(path): digest(path) for path in protected}
    if args.prepare_module:
        preparation = environment | {"GOPROXY": "https://proxy.golang.org", "GOSUMDB": "sum.golang.org"}
        subprocess.run([str(compiler), "mod", "download"], cwd=module, env=preparation, check=True)
        subprocess.run([str(compiler), "mod", "verify"], cwd=module, env=preparation, check=True)
        if before != {str(path): digest(path) for path in protected}:
            raise ValueError("isolated module metadata changed during integrity preparation")
    values = {"IMAGEVERIFY_GO": str(compiler), "IMAGEVERIFY_GO_SHA256": digest(compiler),
              "IMAGEVERIFY_GOMODCACHE": environment["GOMODCACHE"],
              "IMAGEVERIFY_GOCACHE": environment["GOCACHE"],
              "IMAGEVERIFY_HOME": environment["HOME"]}
    if args.env_file:
        append_environment(args.env_file, values)
    receipt = {"version": VERSION, "host": list(host), "archive": row,
               "compiler_sha256": values["IMAGEVERIFY_GO_SHA256"],
               "module_metadata_sha256": before, "module_integrity_prepared": args.prepare_module,
               "main_toolchain_changed": False, "environment": values}
    (output / "PREPARED.json").write_text(json.dumps(receipt, indent=2) + "\n")
    return receipt


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--lock", type=Path, default=Path("tools/imageverify/toolchain.lock"))
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--cache", type=Path, default=Path(".tools/dl"))
    parser.add_argument("--module", type=Path, default=Path("tools/imageverify"))
    parser.add_argument("--main-module", type=Path, default=Path("."))
    parser.add_argument("--prepare-module", action="store_true")
    parser.add_argument("--env-file", type=Path)
    args = parser.parse_args()
    try:
        receipt = prepare(args)
    except (OSError, ValueError, subprocess.CalledProcessError, tarfile.TarError) as error:
        parser.exit(1, f"image-verifier compiler: {error}\nTry: preserve partial artifacts and rerun official preparation in a fresh task directory.\n")
    print(json.dumps({"version": receipt["version"], "host": receipt["host"],
                      "module_integrity_prepared": receipt["module_integrity_prepared"],
                      "main_toolchain_changed": False}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
