#!/usr/bin/env python3
"""Pure archive/metadata rejection fixtures: no download, Go or helper runs."""
import argparse
import hashlib
import importlib.util
import io
from pathlib import Path
import tarfile
import tempfile
import unittest

MODULE = Path(__file__).with_name("prepare-image-verifier-go.py")
SPEC = importlib.util.spec_from_file_location("verifier_compiler_prep", MODULE)
PREP = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(PREP)


class CompilerPreparationContract(unittest.TestCase):
    def test_actual_official_linux_pin_is_selected(self):
        lock = Path(__file__).resolve().parents[1] / "tools/imageverify/toolchain.lock"
        row = PREP.lock_archive(lock, ("linux", "amd64"))
        self.assertEqual(row["file"], "go1.27.4.linux-amd64.tar.gz")
        self.assertEqual(row["size"], 70680823)
        self.assertEqual(row["sha256"], "7226736c2339719cda28fbf3a54d71aa63bcefe376e98cef00034268dab24840")

    def test_unknown_or_wrong_host_lock_never_selects_another_compiler(self):
        lock = Path(__file__).resolve().parents[1] / "tools/imageverify/toolchain.lock"
        with tempfile.TemporaryDirectory() as temporary:
            target = Path(temporary) / "toolchain.lock"
            for change in (lambda value: value.replace('version = "go1.27.4"', 'version = "go1.28.0"'),
                           lambda value: value + '\nfuture = true\n',
                           lambda value: value.replace('os = "linux"', 'os = "windows"', 1)):
                target.write_text(change(lock.read_text()))
                with self.assertRaises(ValueError):
                    PREP.lock_archive(target, ("linux", "amd64"))

    def test_size_and_digest_are_checked_before_archive_use(self):
        with tempfile.TemporaryDirectory() as temporary:
            target = Path(temporary) / "archive"
            target.write_bytes(b"controlled archive fixture")
            row = {"size": target.stat().st_size, "sha256": hashlib.sha256(target.read_bytes()).hexdigest()}
            PREP.verify_archive(target, row)
            target.write_bytes(b"changed archive fixture")
            with self.assertRaises(ValueError):
                PREP.verify_archive(target, row)

    def archive(self, names):
        data = io.BytesIO()
        with tarfile.open(fileobj=data, mode="w") as output:
            for name, kind, link in names:
                item = tarfile.TarInfo(name)
                item.mode = 0o755
                item.type = kind
                item.linkname = link
                item.size = 0
                output.addfile(item)
        data.seek(0)
        return tarfile.open(fileobj=data, mode="r")

    def test_only_contained_ordinary_archive_roles_extract(self):
        with self.archive([("go", tarfile.DIRTYPE, ""), ("go/bin/go", tarfile.REGTYPE, "")]) as archive:
            self.assertEqual(len(PREP.archive_members(archive)), 2)
        for members in ([('/outside', tarfile.REGTYPE, '')],
                        [('go/../outside', tarfile.REGTYPE, '')],
                        [('go/bin/./go', tarfile.REGTYPE, '')],
                        [('go/bin/go', tarfile.REGTYPE, ''), ('go/bin/go', tarfile.REGTYPE, '')],
                        [('go/link', tarfile.SYMTYPE, '../../outside')],
                        [('go/link', tarfile.LNKTYPE, 'outside')],
                        [('go/link', tarfile.SYMTYPE, 'inside'), ('go/link/child', tarfile.REGTYPE, '')],
                        [('go/device', tarfile.CHRTYPE, '')]):
            with self.archive(members) as archive:
                with self.assertRaises(ValueError):
                    PREP.archive_members(archive)

    def test_existing_output_fails_before_any_download_or_execution(self):
        lock = Path(__file__).resolve().parents[1] / "tools/imageverify/toolchain.lock"
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / "existing"
            output.mkdir()
            marker = output / "foreign"
            marker.write_text("preserve")
            args = argparse.Namespace(lock=lock, output=output, cache=Path(temporary) / "cache")
            with self.assertRaises(ValueError):
                PREP.prepare(args)
            self.assertEqual(marker.read_text(), "preserve")
            self.assertFalse(args.cache.exists())


if __name__ == "__main__":
    unittest.main()
