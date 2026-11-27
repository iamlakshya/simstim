#!/usr/bin/env python3
"""List source paths without relying on an enclosing checkout's Git metadata."""
import argparse
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import tempfile


class ListingError(Exception):
    """A source selection could not be checked safely."""


def git(command, root, env):
    try:
        result = subprocess.run(
            ["git", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null",
             "-c", "core.attributesFile=/dev/null", "-c", "core.untrackedCache=false",
             *command], cwd=root, env=env, capture_output=True, timeout=30,
            check=False)
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise ListingError("Git could not list the source files") from exc
    if result.returncode:
        raise ListingError("Git could not list the source files")
    return result.stdout


def source_paths(root, data):
    """Validate the complete selection before emitting any paths."""
    paths = []
    for raw in sorted(set(data.split(b"\0")) - {b""}):
        name = os.fsdecode(raw)
        path = Path(name)
        if path.is_absolute() or ".." in path.parts or "\n" in name or "\r" in name:
            raise ListingError("Git returned an unsafe source path")
        candidate = root / path
        try:
            target = candidate.resolve(strict=True)
        except FileNotFoundError:
            # A tracked file may have been deleted in the working tree.
            if candidate.is_symlink():
                raise ListingError("a source link is broken: " + name) from None
            continue
        except (OSError, RuntimeError) as exc:
            raise ListingError("a source path cannot be read: " + name) from exc
        if not target.is_relative_to(root):
            raise ListingError("a source link leaves the source tree: " + name)
        if not stat.S_ISREG(target.stat().st_mode):
            raise ListingError("a source path is not a regular file: " + name)
        paths.append(raw)
    if not paths:
        raise ListingError("the check selected no source files")
    return b"\0".join(paths) + b"\0"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tracked", action="store_true")
    parser.add_argument("--require-git", action="store_true")
    parser.add_argument("patterns", nargs="*")
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
    env.update(GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL="/dev/null",
               GIT_OPTIONAL_LOCKS="0", GIT_TERMINAL_PROMPT="0")
    try:
        if shutil.which("git", path=env.get("PATH")) is None:
            raise ListingError("source checks need Git")
        metadata = root / ".git"
        if metadata.exists() or metadata.is_symlink():
            top = git(["rev-parse", "--show-toplevel"], root, env)
            if Path(os.fsdecode(top).strip()).resolve() != root:
                raise ListingError("the source folder does not own its Git metadata")
            flags = ["ls-files", "--cached", "--full-name", "-z"]
            if not args.tracked:
                flags += ["--others", "--exclude-standard"]
            data = git([*flags, "--", *args.patterns], root, env)
        else:
            if args.require_git:
                raise ListingError("this check needs the checkout's Git history")
            # An empty temporary index makes every archive file a candidate.
            # Git applies the archive's own .gitignore files, including their
            # negated rules. No metadata is written into the source folder.
            with tempfile.TemporaryDirectory(prefix="source-files-") as temp:
                git(["init", "--bare", "--quiet", "--template=", temp], root, env)
                info = Path(temp) / "info"
                info.mkdir(exist_ok=True)
                (info / "exclude").write_text("/.claude/\n")
                data = git(["--git-dir=" + temp, "--work-tree=" + str(root),
                            "ls-files", "--others", "--exclude-standard",
                            "--full-name", "-z", "--", *args.patterns], root, env)
        sys.stdout.buffer.write(source_paths(root, data))
    except (ListingError, OSError) as exc:
        print("Error: " + str(exc) + ".", file=sys.stderr)
        print("Try: use a complete source tree with Git installed, and keep source links inside it.",
              file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
