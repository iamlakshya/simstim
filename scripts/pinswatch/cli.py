"""The command line of the pins watch: python3 scripts/pins-watch.py [--open-issues]."""
import argparse
import os
from pathlib import Path
import sys

from pinswatch.checks import check
from pinswatch.issues import open_issues
from pinswatch.transport import GitHub, Problem, clean

ROOT = Path(__file__).resolve().parents[2]


def say(message, hint):
    """Print a problem and its hint. Both are cleaned, so no outside text prints raw."""
    message, hint = clean(message, 2000), clean(hint, 2000)
    print(f"\n{message}\nTry: {hint}", file=sys.stderr)
    if os.environ.get("GITHUB_ACTIONS") == "true":
        text = f"{message} Try: {hint}".replace("%", "%25").replace("\r", "%0D").replace("\n", "%0A")
        print(f"::error title=pins-watch::{text}")


class Parser(argparse.ArgumentParser):
    def error(self, message):
        self.print_usage(sys.stderr)
        print(f"pins-watch: {clean(message, 300)}.\nTry: python3 scripts/pins-watch.py --help", file=sys.stderr)
        sys.exit(2)


def main(argv=None):
    parser = Parser(prog="pins-watch.py", description="Check the pins that no package ecosystem covers (spec 19.5).")
    parser.add_argument("--root", type=Path, default=ROOT, help="the checkout to read (default: this one)")
    parser.add_argument("--open-issues", action="store_true",
                        help="then open an issue for each problem that has none (needs GITHUB_REPOSITORY and GITHUB_TOKEN)")
    args = parser.parse_args(argv)
    github = GitHub(os.environ.get("GITHUB_TOKEN") or None)
    report = check(args.root, github)
    order = {"error": 0, "finding": 1, "unread": 2}
    problems = [Problem(message, hint) for _, message, hint in sorted(report.problems, key=lambda p: order[p[0]])]
    failed = bool(problems)
    if args.open_issues:
        issue_problems = open_issues(github, report.issues)
        problems += issue_problems
        # The check's own job fails on findings and unread advisories. This
        # one fails only when it could not do its own work.
        failed = bool(issue_problems) or any(kind == "error" for kind, _, _ in report.problems)
    for problem in problems:
        say(problem.message, problem.hint)
    if failed:
        say(f"pins-watch: {len(problems)} {'problem' if len(problems) == 1 else 'problems'}.",
            "Fix each problem above, then run python3 scripts/pins-watch.py again")
        return 1
    print("pins-watch: ok" if not problems else "pins-watch: issues are up to date")
    return 0
