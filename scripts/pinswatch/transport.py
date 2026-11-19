"""HTTP for the pins watch: GitHub's REST API and OSV.

GITHUB_TOKEN, if set, goes to api.github.com only. A redirect never carries it,
and the watch follows no redirect to another host.
"""
from http.client import HTTPException
import json
import re
import unicodedata
import urllib.error
import urllib.parse
import urllib.request

GITHUB_API = "https://api.github.com"
OSV_QUERY = "https://api.osv.dev/v1/query"
RETRY = "Run the job again later. Locally, set GITHUB_TOKEN."
MAX_PAGES = 50


class Problem(Exception):
    """A message for the user, with what to try next."""

    def __init__(self, message, hint):
        super().__init__(message)
        self.message, self.hint = message, hint


class ApiError(Exception):
    """An API call that failed: its text says how, such as "HTTP 403"."""


class Unknown(Exception):
    """A part of a range that the watch cannot read: its text says why."""


def clean(text, limit=200):
    """Make outside text safe to print and to quote in an issue: one line, no hidden characters.

    The watch cleans text only to print it, never to read it.
    """
    text = "".join(" " if unicodedata.category(c) in ("Cc", "Cf", "Zl", "Zp") else c for c in str(text))
    text = " ".join(text.split())
    return text if len(text) <= limit else text[:limit - 1] + "…"


class SameHost(urllib.request.HTTPRedirectHandler):
    """Follow a redirect only to the host of the request, so that nothing goes anywhere else."""

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        old, new = urllib.parse.urlsplit(req.full_url), urllib.parse.urlsplit(newurl)
        if (new.scheme, new.netloc) != (old.scheme, old.netloc):
            fp.close()
            raise urllib.error.URLError(f"a redirect to another host, {clean(new.netloc, 60)}")
        return super().redirect_request(req, fp, code, msg, headers, newurl)


OPENER = urllib.request.build_opener(SameHost)


def http(method, url, body=None, headers=None):
    """Send one request, and return its status, headers and body. The tests replace it, or OPENER."""
    headers = dict(headers or {})
    token = headers.pop("Authorization", None)
    request = urllib.request.Request(url, data=body, method=method, headers=headers)
    if token:
        request.add_unredirected_header("Authorization", token)  # a redirect never carries it
    try:
        with OPENER.open(request, timeout=60) as answer:
            return answer.status, dict(answer.headers), answer.read()
    except urllib.error.HTTPError as error:
        return error.code, dict(error.headers or {}), error.read()


def send(method, url, body, headers):
    try:
        return http(method, url, body, headers)
    except (OSError, HTTPException) as error:  # no network, a timeout, a cut answer or a refused redirect
        raise ApiError(clean(getattr(error, "reason", None) or str(error) or type(error).__name__)) from None


def parse_json(body):
    try:
        return json.loads(body)
    except ValueError:
        raise ApiError("the answer is not JSON") from None


def header(headers, name):
    return next((value for key, value in headers.items() if key.lower() == name.lower()), "")


def obj(value):
    """The value if it is a JSON object, or an empty one: answers may lack a field."""
    return value if isinstance(value, dict) else {}


def items(value):
    """The value if it is a JSON list, or an empty one."""
    return value if isinstance(value, list) else []


class GitHub:
    """GitHub's REST API. The token, if any, is for the rate limit."""

    def __init__(self, token):
        self.token = token

    def request(self, method, path, payload=None):
        url = path if path.startswith("https://") else GITHUB_API + path
        if not url.startswith(GITHUB_API + "/"):
            raise ApiError("a link that leaves api.github.com")  # the token goes nowhere else
        headers = {"Accept": "application/vnd.github+json", "User-Agent": "pins-watch",
                   "X-GitHub-Api-Version": "2022-11-28"}
        if self.token:
            headers["Authorization"] = "Bearer " + self.token
        body = None
        if payload is not None:
            body = json.dumps(payload).encode()
            headers["Content-Type"] = "application/json"
        return send(method, url, body, headers)

    def get_all(self, path):
        """Every page of a list, following the Link header."""
        found, url = [], path
        for _ in range(MAX_PAGES):
            status, headers, body = self.request("GET", url)
            if status != 200:
                raise ApiError(f"HTTP {status}")
            page = parse_json(body)
            if not isinstance(page, list):
                raise ApiError("the answer is not a list")
            found.extend(page)
            url = next((m.group(1) for m in re.finditer(r'<([^>]+)>\s*;\s*rel="next"', header(headers, "Link"))), None)
            if not url:
                return found
        raise ApiError(f"more than {MAX_PAGES} pages")


def osv_query(commit):
    """Every OSV entry that lists the commit as affected."""
    vulns, token = [], None
    for _ in range(MAX_PAGES):
        payload = {"commit": commit, **({"page_token": token} if token else {})}
        status, _, body = send("POST", OSV_QUERY, json.dumps(payload).encode(),
                               {"Content-Type": "application/json", "User-Agent": "pins-watch"})
        if status != 200:
            raise ApiError(f"HTTP {status}")
        answer = parse_json(body)
        if not isinstance(answer, dict) or not isinstance(answer.get("vulns", []), list):
            raise ApiError("the answer has no list of vulnerabilities")
        vulns.extend(answer.get("vulns", []))
        token = answer.get("next_page_token")
        if not token:
            return vulns
    raise ApiError(f"more than {MAX_PAGES} pages")
