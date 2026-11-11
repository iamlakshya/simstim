"""The pins watch: it checks the pins that no package ecosystem covers (spec 19.5).

Run it with python3 scripts/pins-watch.py, and its tests with
python3 scripts/test-pins-watch.py. The modules:

- transport: HTTP, GitHub's API and OSV, with the rules that keep the token on api.github.com
- files: the pins of vt/build.zig.zon and guest/supply.lock, and the verdicts
- ranges: how an advisory's range reads, as intervals of commits or as release versions
- checks: the check of each pin, and the report
- issues: the issues of the daily run
- cli: the command line
"""
