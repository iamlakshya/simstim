"""Validate a hosted Scorecard JSON report: python3 scripts/check-scorecard.py FILE."""
import json
import sys


# The remaining checks depend on releases, time or settings owned by the owner.
WANT = {"Binary-Artifacts": 10, "Dangerous-Workflow": 10, "Dependency-Update-Tool": 10,
        "Fuzzing": 10, "License": 10, "Pinned-Dependencies": 9, "SAST": 10,
        "Security-Policy": 10, "Token-Permissions": 10, "Vulnerabilities": 10}


def check(path):
    with open(path) as source:
        report = json.load(source)
    if not isinstance(report, dict) or not isinstance(report.get("checks"), list):
        raise ValueError("expected an object with a checks list")
    overall = report.get("score")
    if type(overall) not in (int, float) or not 0 <= overall <= 10:
        raise ValueError("overall score must be a finite number from 0 to 10")
    seen = set()
    bad = False
    for entry in report["checks"]:
        if not isinstance(entry, dict) or not isinstance(entry.get("name"), str) or not entry["name"]:
            raise ValueError("each check must have a name")
        name, score, reason = entry["name"], entry.get("score"), entry.get("reason")
        if type(score) is not int or not -1 <= score <= 10:
            raise ValueError(f"{name}: score must be an integer from -1 to 10")
        if not isinstance(reason, str):
            raise ValueError(f"{name}: reason must be text")
        need = WANT.get(name)
        if need is not None and name in seen:
            raise ValueError(f"duplicate expected check: {name}")
        seen.add(name)
        mark = "  " if need is None else ("ok" if score >= need else "NO")
        bad |= mark == "NO"
        print(f"{mark} {score:>3}  {name}  ({reason[:70]})")
    missing = WANT.keys() - seen
    if missing:
        raise ValueError("missing expected checks: " + ", ".join(sorted(missing)))
    print(f"Hosted overall: {overall} (GitHub state, not the local checkout)")
    if bad:
        raise ValueError("expected checks below their thresholds")


if __name__ == "__main__":
    try:
        if len(sys.argv) != 2:
            raise ValueError("expected one JSON report file")
        check(sys.argv[1])
    except (OSError, ValueError) as error:
        print(f"Hosted Scorecard: {error}. Try: rerun Scorecard and inspect the hosted report.", file=sys.stderr)
        sys.exit(1)
