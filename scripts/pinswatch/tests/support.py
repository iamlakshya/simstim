"""What the tests of the pins watch share: the recorded answers, a fake network and a checkout.

Each test runs the watch in a throwaway checkout, built from the fixtures in
scripts/testdata/pins-watch/, with recorded answers in place of the network.
The answers came from GitHub's repository advisory, compare, tag and commit
APIs for ghostty-org/ghostty, and from OSV's query API, on 11 November 2026.
They are cut down to the fields that the watch reads. No test reads the
repository's own pin files, except test_the_repository_files, which only
parses them.
"""
import contextlib
import io
import json
import os
from pathlib import Path
import re
import tempfile
import unittest
from unittest import mock
import urllib.parse

from pinswatch import checks, cli, files, transport

DATA = Path(__file__).resolve().parents[2] / "testdata" / "pins-watch"
OLD = "ad97d331459a0762f7aea5f56fe86748e03b860f"  # the Ghostty pin before P0.1
VT = "0fa3d577a5614150ff8e2c152de5fb9d5c6d2dec"  # the tag vt-1.0.0
V120 = "3e38e284ca593b601fb70e877c3155fedf42e2e5"  # the tag v1.2.0, which OSV lists for CVE-2026-26982
GHOSTTY = "ghostty-org/ghostty"
REPO = "example/project"
VMM = "1111111111111111111111111111111111111111"
PLACEHOLDER = "0" * 40

ADVISORIES = json.loads((DATA / "ghostty-advisories.json").read_text())
COMPARE = json.loads((DATA / "ghostty-compare.json").read_text())
REFS = json.loads((DATA / "ghostty-refs.json").read_text())
OSV = json.loads((DATA / "osv.json").read_text())

HASHED = {"GHSA-w9q4-5jvv-mcv4", "GHSA-jqr7-69mm-v35h", "GHSA-qjfx-r533-6vx3", "GHSA-3r5j-c6rj-xq2v"}
# Advisories with no hash that release 1.4.0 fixed, which the old pin predates.
IN_1_4_0 = {"GHSA-2r63-vmw6-3m3c", "GHSA-hp9c-qp4q-cpw3", "GHSA-v8cw-fg4j-jcc4", "GHSA-mfw9-xcr3-7gfp",
            "GHSA-hcr9-7hcr-mjm8", "GHSA-4hwh-wx3w-3fqg", "GHSA-mxqx-236q-wrv7", "GHSA-mhff-mv4q-fp63",
            "GHSA-cw69-wm67-5pv8"}
OLD_FINDINGS = {"GHSA-w9q4-5jvv-mcv4", "GHSA-jqr7-69mm-v35h"} | IN_1_4_0


def by_id(ghsa, **range_fields):
    """A recorded advisory, with the fields of each entry changed if asked."""
    advisory = json.loads(json.dumps(next(a for a in ADVISORIES if a["ghsa_id"] == ghsa)))
    for vuln in advisory["vulnerabilities"]:
        vuln.update(range_fields)
    return advisory


def tag_commit(tag):
    """The recorded commit of a Ghostty tag, with its annotated tag peeled."""
    return REFS["tags"][REFS["refs"][tag]["object"]["sha"]]["object"]["sha"]


def verdicts(ids, commit, pin="ghostty", verdict="not-affected"):
    return "".join(f'[[verdict]]\nadvisory = "{i}"\npin = "{pin}"\ncommit = "{commit}"\n'
                   f'verdict = "{verdict}"\nwhy = "Checked by hand for a test."\n\n' for i in ids)


def answer(value):
    if isinstance(value, int):
        return value, {}, b'{"message": "Not Found"}'
    return 200, {}, json.dumps(value).encode()


class FakeNet:
    """Recorded answers, by request. It keeps every request, for checks."""

    def __init__(self):
        self.advisories = {GHOSTTY: list(ADVISORIES)}
        self.compare = dict(COMPARE)  # "base...head": an answer, or an HTTP status
        self.refs = {GHOSTTY: json.loads(json.dumps(REFS["refs"]))}  # repository: {tag: an answer, or a status}
        self.tags = dict(REFS["tags"])  # the annotated tags, by their SHA
        self.commits = dict(REFS["commits"])
        self.osv = dict(OSV)
        self.fail = {}  # "METHOD URL prefix": an HTTP status, or an exception to raise
        self.page_size = 0
        self.issues, self.created, self.requests = [], [], []

    def __call__(self, method, url, body=None, headers=None):
        self.requests.append((method, url, headers or {}))
        for prefix, failure in self.fail.items():
            if f"{method} {url}".startswith(prefix):
                if isinstance(failure, BaseException):
                    raise failure
                return failure, {}, b'{"message": "a recorded failure"}'
        if method == "POST" and url == transport.OSV_QUERY:
            return 200, {}, json.dumps(self.osv.get(json.loads(body)["commit"], {})).encode()
        path, repo = url.removeprefix(transport.GITHUB_API), r"/repos/([\w.-]+/[\w.-]+)"
        match = re.fullmatch(repo + r"/security-advisories\?state=published&per_page=100(?:&page=(\d+))?", path)
        if method == "GET" and match:
            found, page = self.advisories.get(match.group(1), []), int(match.group(2) or 1)
            size = self.page_size or max(len(found), 1)
            headers = {}
            if page * size < len(found):
                headers["Link"] = f'<{url.split("&page=")[0]}&page={page + 1}>; rel="next", <{url}>; rel="first"'
            return 200, headers, json.dumps(found[(page - 1) * size:page * size]).encode()
        match = re.fullmatch(repo + r"/compare/(\w+\.\.\.\w+)\?per_page=1", path)
        if method == "GET" and match and match.group(2) in self.compare:
            return answer(self.compare[match.group(2)])
        match = re.fullmatch(repo + r"/git/ref/tags/(.+)", path)
        if method == "GET" and match:
            return answer(self.refs.get(match.group(1), {}).get(urllib.parse.unquote(match.group(2)), 404))
        match = re.fullmatch(repo + r"/git/tags/(\w+)", path)
        if method == "GET" and match and match.group(2) in self.tags:
            return answer(self.tags[match.group(2)])
        match = re.fullmatch(repo + r"/commits/(\w+)", path)
        if method == "GET" and match and match.group(2) in self.commits:
            return answer(self.commits[match.group(2)])
        if method == "GET" and path.startswith(f"/repos/{REPO}/issues?"):
            query = urllib.parse.parse_qs(path.split("?", 1)[1])
            return answer([issue for issue in self.issues
                           if issue["user"]["login"] in query.get("creator", [issue["user"]["login"]])
                           and all(label in [x["name"] for x in issue["labels"]] for label in query.get("labels", []))])
        if method == "POST" and path == f"/repos/{REPO}/issues":
            self.created.append(json.loads(body))
            return 201, {}, json.dumps({"number": 100 + len(self.created)}).encode()
        raise AssertionError(f"no recorded answer for {method} {url}")


class Checkout:
    """A throwaway checkout, from the fixtures, with the three files that the watch reads."""

    def __init__(self, test, ghostty=OLD, lock="", reviewed=""):
        temp = tempfile.TemporaryDirectory()
        test.addCleanup(temp.cleanup)
        self.root, self.net = Path(temp.name), FakeNet()
        for folder in ("vt", "guest", "scripts"):
            (self.root / folder).mkdir()
        self.write(files.ZON, (DATA / "build.zig.zon").read_text().replace(PLACEHOLDER, ghostty))
        self.write(files.LOCK, (DATA / "supply.lock").read_text() + lock)
        self.write(files.REVIEWED, (DATA / "reviewed.toml").read_text() + reviewed)

    def write(self, name, text):
        path = self.root / name
        path.write_bytes(text) if isinstance(text, bytes) else path.write_text(text)

    def run(self, *args, **env):
        environ = {"GITHUB_TOKEN": "offline-token", "GITHUB_ACTIONS": "", "GITHUB_REPOSITORY": "",
                   "GITHUB_RUN_ID": "", "GITHUB_SERVER_URL": "https://github.com"}
        environ.update(env)
        out, err = io.StringIO(), io.StringIO()
        with mock.patch.object(transport, "http", self.net), mock.patch.dict(os.environ, environ), \
                contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = cli.main(["--root", str(self.root), *args])
        self.out, self.err = out.getvalue(), err.getvalue()
        return code, self.out + self.err

    def report(self):
        out = io.StringIO()
        with mock.patch.object(transport, "http", self.net), contextlib.redirect_stdout(out):
            report = checks.check(self.root, transport.GitHub("offline-token"))
        self.out = out.getvalue()
        return report

    def findings(self, kind="finding"):
        """The advisories of the report's findings (or unread advisories): the ones that get issues."""
        return {issue["advisory"] for issue in self.report().issues if issue["kind"] == kind}


def vmm(test, advisory_range, statuses, reviewed="", patched=None):
    """A checkout with a supply.lock pin, "vmm", and one advisory of its own repository."""
    lock = f'\n[[pin]]\nname = "vmm"\nrepo = "example/vmm"\nref = "v1.0"\ncommit = "{VMM}"\n'
    checkout = Checkout(test, ghostty=VT, lock=lock, reviewed=reviewed)
    entry = {"vulnerable_version_range": advisory_range, **({"patched_versions": patched} if patched else {})}
    checkout.net.advisories["example/vmm"] = [{"ghsa_id": "GHSA-2222-3333-4444", "severity": "high", "vulnerabilities": [entry]}]
    checkout.net.commits[VMM] = {"sha": VMM}
    checkout.net.refs["example/vmm"] = {"v1.0": {"ref": "refs/tags/v1.0", "object": {"sha": VMM, "type": "commit"}}}
    checkout.net.compare.update({f"{base}...{VMM}": ({"status": s} if isinstance(s, str) else s)
                                 for base, s in statuses.items()})
    return checkout


class Case(unittest.TestCase):
    def assert_try_lines(self, err):
        """Each message on stderr ends with its Try: line."""
        blocks = [block.strip().splitlines() for block in err.split("\n\n") if block.strip()]
        self.assertTrue(blocks)
        for block in blocks:
            self.assertTrue(block[-1].startswith("Try: "), block)
