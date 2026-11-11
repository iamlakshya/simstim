"""Check the pins that no package ecosystem covers: python3 scripts/pins-watch.py.

The pins are the Ghostty commit in vt/build.zig.zon and each [[pin]] of
guest/supply.lock. For a pin on GitHub, the watch reads the repository's own
advisories. It reads the commits of each range, or else its release versions,
through their tags, and asks the compare API whether the pin holds them. It
asks OSV about every pin by its commit. A verdict in
scripts/pins-watch-reviewed.toml silences one advisory for one pin commit.
Spec 19.5 of docs/contributing/isolation-spec.md has the rules, and the
package scripts/pinswatch/ has the code.

It fails when a pin is affected, when it cannot read an advisory's range, and
when an API call fails, so it never passes on a partial read. GITHUB_TOKEN is
optional, for GitHub's rate limit. The watch sends it to api.github.com only,
and it follows no redirect to another host.

    python3 scripts/pins-watch.py                 check, and exit 1 on any problem
    python3 scripts/pins-watch.py --open-issues   check, then open an issue for each new problem
"""
import sys

from pinswatch.cli import main

if __name__ == "__main__":
    sys.exit(main())
