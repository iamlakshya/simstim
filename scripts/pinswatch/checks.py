"""The check of each pin against its advisories and OSV (spec 19.5), and the report."""
import re
import urllib.parse

from pinswatch import files, ranges
from pinswatch.transport import RETRY, ApiError, Problem, Unknown, clean, items, obj, osv_query, parse_json

GHSA = re.compile(r"GHSA(?:-[23456789cfghjmpqrvwx]{4}){3}")
ADVISORY_ID = re.compile(r"[A-Za-z][A-Za-z0-9._:-]{2,63}")
CVSS_VECTOR = re.compile(r"CVSS:[0-9.]+(?:/[A-Za-z]+:[A-Za-z]+)+")
STATUSES = ("ahead", "identical", "behind", "diverged")


class Report:
    """What the check found: each problem with its kind, and the ones that get issues."""

    def __init__(self, verdicts):
        self.verdicts = verdicts
        self.problems = []  # (kind, message, hint), kind is "finding", "unread" or "error"
        self.issues = []  # a finding or an unread advisory, with what its issue needs
        self.found = set()  # (pin, advisory ID) of every finding, for OSV's duplicates

    def error(self, message, hint):
        self.problems.append(("error", message, hint))

    def silenced(self, pin, ids):
        for advisory in ids:
            verdict = self.verdicts.get((advisory.upper(), pin.name, pin.commit))
            if verdict:
                print(f"{pin.name} ({pin.short}): {advisory} has the verdict {verdict[0]}: {verdict[1]}")
                return True
        return False

    def add(self, kind, pin, ids, message, hint, details):
        """Add a finding or an unread advisory, unless a verdict names it for the pin's commit."""
        if kind == "finding":
            self.found.update((pin.name, advisory.upper()) for advisory in ids)
        if self.silenced(pin, ids):
            return
        self.problems.append((kind, message, hint))
        self.issues.append(dict(details, kind=kind, pin=pin.name, advisory=ids[0], commit=pin.commit,
                                source=pin.source, next=hint))


def osv_events(vuln):
    """The events of an OSV entry's GIT ranges, as text, such as "introduced 0, fixed 703d11c6"."""
    events = []
    for affected in items(vuln.get("affected")):
        for rng in items(obj(affected).get("ranges")):
            if obj(rng).get("type") != "GIT":
                continue
            for event in items(rng.get("events")):
                events += [f"{kind} {clean(value, 40)[:12]}" for kind, value in obj(event).items()
                           if kind in ("introduced", "fixed", "last_affected", "limit")]
    return ", ".join(events) or "no GIT range"


class Checker:
    def __init__(self, github, report):
        self.github, self.report, self.compares, self.tags = github, report, {}, {}

    def reaches(self, pin, commit):
        """Whether the pin holds the commit (spec 19.5, step 5). Unknown when the repository does not know it."""
        key = (pin.repo, commit, pin.commit)
        if key not in self.compares:
            status, _, body = self.github.request("GET", f"/repos/{pin.repo}/compare/{commit}...{pin.commit}?per_page=1")
            if status in (404, 422):
                raise Unknown(f"the compare of {commit[:8]} with the pin answered HTTP {status}")
            if status != 200:
                raise ApiError(f"HTTP {status}")
            status = obj(parse_json(body)).get("status")
            if status not in STATUSES:
                raise ApiError("an unknown compare status")
            self.compares[key] = status
        return self.compares[key] in ("ahead", "identical")

    def tag_commit(self, pin, tag):
        """The commit of a release tag, with an annotated tag peeled. Unknown when there is none."""
        key, name = (pin.repo, tag), clean(tag, 60)
        if not files.REF.fullmatch(tag):
            raise Unknown(f"the tag {name} has a space, or a character that is not printable ASCII")
        if key not in self.tags:
            status, _, body = self.github.request("GET", f"/repos/{pin.repo}/git/ref/tags/{urllib.parse.quote(tag)}")
            if status in (404, 422):
                raise Unknown(f"the tag {name} is not in {pin.repo}")
            for _ in range(5):
                if status != 200:
                    raise ApiError(f"HTTP {status}")
                target = obj(obj(parse_json(body)).get("object"))
                sha = target.get("sha") if isinstance(target.get("sha"), str) else ""
                if not files.HEX40.fullmatch(sha):
                    raise Unknown(f"the tag {name} names no commit")
                if target.get("type") == "commit":
                    break
                if target.get("type") != "tag":
                    raise Unknown(f"the tag {name} names a {clean(target.get('type'), 20)}, not a commit")
                status, _, body = self.github.request("GET", f"/repos/{pin.repo}/git/tags/{sha}")
            else:
                raise Unknown(f"the tag {name} names a chain of tags")
            self.tags[key] = sha
        return self.tags[key]

    def pin_commit(self, pin):
        """Check that the pin's commit is in its repository, and that its ref names it (spec 19.5, step 1)."""
        status, _, body = self.github.request("GET", f"/repos/{pin.repo}/commits/{pin.commit}")
        if status in (404, 422):
            where = (f"the URL of the ghostty dependency in {files.ZON}" if pin.source == files.ZON
                     else f"the commit of the pin in {files.LOCK}")
            raise Problem(f"{pin.name}: the commit {pin.short} is not in {pin.repo}.", f"Check {where}")
        if status != 200 or obj(parse_json(body)).get("sha") != pin.commit:
            raise ApiError(f"HTTP {status}" if status != 200 else "an answer for another commit")
        if pin.ref:
            try:
                commit = self.tag_commit(pin, pin.ref)
            except Unknown as error:
                raise Problem(f'{files.LOCK}: the ref "{clean(pin.ref, 60)}" of the pin "{pin.name}" does not name a commit: {error}.',
                              "Set ref to the tag of the pin's commit") from None
            if commit != pin.commit:
                raise Problem(f'{files.LOCK}: the ref "{clean(pin.ref, 60)}" of the pin "{pin.name}" is the commit {commit[:8]}, not {pin.short}.',
                              "Set commit to the commit of its ref, or fix the ref")

    def github_pin(self, pin):
        """Check the pin against its repository's advisories, and return how many there are."""
        try:
            self.pin_commit(pin)
        except Problem as problem:
            self.report.error(problem.message, problem.hint)
            return 0
        except ApiError as error:
            self.report.error(f"Could not read the commit {pin.short} of {pin.name} in {pin.repo}: {error}.", RETRY)
            return 0
        try:
            advisories = self.github.get_all(f"/repos/{pin.repo}/security-advisories?state=published&per_page=100")
        except ApiError as error:
            self.report.error(f"Could not read the advisories of {pin.repo}: {error}.", RETRY)
            return 0
        for advisory in advisories:
            ghsa = advisory.get("ghsa_id") if isinstance(advisory, dict) else None
            if not isinstance(ghsa, str) or not GHSA.fullmatch(ghsa):
                self.report.error(f"Could not read the advisories of {pin.repo}: an entry has no GHSA ID.", RETRY)
                continue
            try:
                self.advisory(pin, ghsa, advisory)
            except ApiError as error:
                self.report.error(f"Could not compare the commits of {ghsa} with {pin.name} ({pin.short}) in {pin.repo}: {error}.", RETRY)
        return len(advisories)

    def interval(self, pin, introduced, fixes):
        """None when the pin lies outside the interval, else (what it lacks or holds, what to move past).

        It tests every fix before it gives up on one that the repository does not know.
        """
        unknown = None
        for commit in fixes:
            try:
                if self.reaches(pin, commit):
                    return None
            except Unknown as error:
                unknown = unknown or error
        if introduced:
            try:
                if not self.reaches(pin, introduced):
                    return None  # the pin is older than the bug
            except Unknown as error:
                unknown = unknown or error
        if unknown:
            raise unknown
        if not fixes:
            return f"holds {introduced[:8]}, and the advisory names no fix", None
        names = [fix[:8] for fix in fixes]
        return (f"lacks the fix {names[0]}" if len(names) == 1 else f"lacks every fix: {', '.join(names)}"), " or ".join(names)

    def hash_entry(self, pin, vulnerable, patched):
        """The hash rule: the pin is affected when it lies in any interval of the range."""
        unknown = None
        for introduced, fixes in ranges.intervals(vulnerable, patched):
            try:
                affected = self.interval(pin, introduced, fixes)
            except Unknown as error:
                unknown = unknown or error
                continue
            if affected:
                return affected
        if unknown:
            raise unknown
        return None

    def tag_entry(self, pin, package, vulnerable, patched):
        """The tag rule of spec 19.5 for an entry with no hash: as hash_entry, from release tags.

        A pin is clear only when it holds a patched release. The lower bound
        alone never clears it: a pin older than that release is unread.
        """
        lower, versions = ranges.version_range(vulnerable, patched)
        form = pin.tag_form(package)
        if form is None:
            raise Unknown(f"the package {clean(package, 40) or 'with no name'} has no tag form")
        tags, unknown = [form.format(version=version) for version in versions], None
        for tag in tags:
            try:
                if self.reaches(pin, self.tag_commit(pin, tag)):
                    return None
            except Unknown as error:
                unknown = unknown or error
        if unknown:
            raise unknown
        if lower is not None:
            first = form.format(version=lower)
            if not self.reaches(pin, self.tag_commit(pin, first)):
                raise Unknown(f"the pin is older than {first}, and no patched release reaches it")
        if not tags:
            return "is in a range that names no fixed release", None
        if len(tags) == 1:
            return f"lacks the release {tags[0]}", f"the release {tags[0]}"
        return f"lacks every release: {', '.join(tags)}", f"the release {' or '.join(tags)}"

    def advisory(self, pin, ghsa, advisory):
        """Check one advisory: a finding when an entry affects the pin, else unread when one cannot be read."""
        entries, versioned, texts = [], [], []
        for vuln in items(advisory.get("vulnerabilities")):
            vuln = obj(vuln)
            vulnerable, patched = ranges.text(vuln.get("vulnerable_version_range")), ranges.text(vuln.get("patched_versions"))
            texts.append(vulnerable or "no range")
            if ranges.names_commits(vulnerable, patched):
                entries.append((self.hash_entry, (pin, vulnerable, patched), vulnerable))
            else:
                package = ranges.text(obj(vuln.get("package")).get("name"))
                versioned.append((self.tag_entry, (pin, package, vulnerable, patched), vulnerable))
        entries += versioned  # entries with hashes go first: they name the exact fix
        cve = advisory.get("cve_id")
        ids = [ghsa] + ([cve] if isinstance(cve, str) and ADVISORY_ID.fullmatch(cve) else [])
        details = {"link": f"https://github.com/{pin.repo}/security/advisories/{ghsa}",
                   "severity": ranges.severity_of(advisory), "by": "the repository's advisories"}
        unread = f"Check it by hand, then record a verdict for commit {pin.short} in {files.REVIEWED}"
        reasons = []
        for rule, args, vulnerable in entries:
            try:
                affected = rule(*args)
            except Unknown as error:
                reasons.append(str(error))
                continue
            if affected:
                clause, past = affected
                hint = f"{pin.move(past)}, or record a verdict in {files.REVIEWED}" if past else unread
                self.report.add("finding", pin, ids, f"{pin.name} ({pin.short}) is in {ghsa}: it {clause}.",
                                hint, dict(details, ranges=[clean(vulnerable, 300)]))
                return
        if reasons or not entries:
            quoted = " and ".join(f'"{clean(text, 120)}"' for text in texts) or '"no range"'
            self.report.add("unread", pin, ids,
                            f"Cannot read the range of {ghsa} for {pin.name}: {quoted}: {reasons[0] if reasons else 'it has no entry'}.",
                            unread, dict(details, ranges=[clean(text, 300) for text in texts] or ["no range"]))

    def osv_pin(self, pin):
        """Ask OSV about the pin's commit, and return how many entries it lists."""
        try:
            vulns = osv_query(pin.commit)
        except ApiError as error:
            self.report.error(f"Could not query OSV for {pin.name} ({pin.short}): {error}.", "Run the job again later.")
            return 0
        for vuln in vulns:
            vid = vuln.get("id") if isinstance(vuln, dict) else None
            if not isinstance(vid, str) or not ADVISORY_ID.fullmatch(vid):
                self.report.error(f"Could not query OSV for {pin.name} ({pin.short}): an entry has no ID.", "Run the job again later.")
                continue
            aliases = [a for a in items(vuln.get("aliases")) if isinstance(a, str) and ADVISORY_ID.fullmatch(a)]
            if any((pin.name, advisory.upper()) in self.report.found for advisory in [vid] + aliases):
                continue  # the repository's own advisory covers it, with its own answer
            vectors = [s["score"] for s in items(vuln.get("severity"))
                       if isinstance(obj(s).get("score"), str) and CVSS_VECTOR.fullmatch(s["score"])]
            details = {"link": f"https://osv.dev/vulnerability/{vid}", "severity": ", ".join(vectors) or "unknown",
                       "ranges": [osv_events(vuln)], "by": "OSV"}
            self.report.add("finding", pin, [vid] + aliases, f"{pin.name} ({pin.short}) is in {vid}: OSV lists its commit as affected.",
                            f"{pin.move('the fix that ' + vid + ' names')}, or record a verdict in {files.REVIEWED}", details)
        return len(vulns)


def check(root, github):
    """Check every pin, and return the report."""
    report = Report({})
    try:
        report.verdicts, problems = files.load_verdicts(root)
    except Problem as problem:
        problems = [problem]
    pins = []
    try:
        pins.append(files.ghostty_pin(root))
    except Problem as problem:
        problems.append(problem)
    try:
        more, bad = files.lock_pins(root)
        pins += more
        problems += bad
    except Problem as problem:
        problems.append(problem)
    for problem in problems:
        report.error(problem.message, problem.hint)
    checker = Checker(github, report)
    for pin in pins:
        line = f"{pin.name} ({pin.short}), from {pin.source}:"
        if pin.watch == "github":
            line += f" {checker.github_pin(pin)} advisories in {pin.repo},"
        print(f"{line} {checker.osv_pin(pin)} entries in OSV.")
    return report
