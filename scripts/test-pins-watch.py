"""Offline tests of the pins watch: python3 scripts/test-pins-watch.py.

The tests are in scripts/pinswatch/tests/, one file for each group. Each test
runs the watch in a throwaway checkout, with recorded answers in place of the
network (scripts/testdata/pins-watch/). The tests named test_spec_<n> are the
tests of spec 19.5 (docs/contributing/isolation-spec.md).
"""
from pathlib import Path
import sys
import unittest

HERE = Path(__file__).resolve().parent

try:
    import tomllib  # noqa: F401 (the watch reads TOML with it)
except ImportError:
    sys.exit("test-pins-watch: the pins watch needs Python 3.11 or later, for tomllib.\n"
             "Try: Install Python 3.11 or later, and run the tests with it")

if __name__ == "__main__":
    suite = unittest.defaultTestLoader.discover(str(HERE / "pinswatch" / "tests"), top_level_dir=str(HERE))
    sys.exit(0 if unittest.TextTestRunner().run(suite).wasSuccessful() else 1)
