"""The issues of the daily run (spec 19.5): one for each finding and each unread advisory."""
import os
import urllib.parse

from pinswatch import files
from pinswatch.transport import RETRY, ApiError, Problem, obj, parse_json

LABELS = ["dependencies", "needs triage"]
BOT = "github-actions[bot]"


def issue_title(issue):
    if issue["kind"] == "unread":
        return f"pins-watch: {issue['pin']} cannot be checked against {issue['advisory']}"
    return f"pins-watch: {issue['pin']} is in {issue['advisory']}"


def issue_body(issue, run):
    """The body: outside text goes in as code, so it cannot mention anyone or add a link."""
    ranges = ", ".join("`" + text.replace("`", "'") + "`" for text in issue["ranges"])
    if issue["kind"] == "unread":
        first = f"The daily pins watch cannot check the pin {issue['pin']} against {issue['advisory']}, so a person must."
    else:
        first = f"The daily pins watch found that the pin {issue['pin']} is in {issue['advisory']}."
    lines = [first, "",
             f"- Advisory: {issue['link']}",
             f"- Severity: `{issue['severity']}`",
             f"- Range: {ranges}, from {issue['by']}",
             f"- Pin: `{issue['commit']}`, in `{issue['source']}`",
             f"- Next step: {issue['next']}.", "",
             "Question 42 of the isolation spec sets how fast a release follows. "
             "`docs/contributing/releasing.md` says what to do."]
    return "\n".join(lines + ([f"\nFound by {run}."] if run else []))


def open_issues(github, issues):
    """Open an issue for each finding and unread advisory that has no open issue yet, and return the problems."""
    repo = os.environ.get("GITHUB_REPOSITORY", "")
    if not files.REPO.fullmatch(repo) or not github.token:
        return [Problem("Could not open issues: GITHUB_REPOSITORY or GITHUB_TOKEN is not set.",
                        "Run --open-issues in the workflow, or set both, with GITHUB_REPOSITORY=owner/name")]
    if not issues:
        print("No finding, so no issue to open.")
        return []
    # Only the workflow's own issues count: no one else can open one with this label.
    query = urllib.parse.urlencode({"state": "open", "creator": BOT, "labels": LABELS[0], "per_page": 100})
    try:
        titles = {str(issue.get("title", "")) for issue in github.get_all(f"/repos/{repo}/issues?{query}")
                  if isinstance(issue, dict) and "pull_request" not in issue}
    except ApiError as error:
        return [Problem(f"Could not list the open issues of {repo}: {error}.", RETRY)]
    server, run_id = os.environ.get("GITHUB_SERVER_URL", "https://github.com"), os.environ.get("GITHUB_RUN_ID", "")
    run = f"{server}/{repo}/actions/runs/{run_id}" if run_id.isdigit() else ""
    problems = []
    for issue in issues:
        title = issue_title(issue)
        if title in titles:
            print(f"An open issue already has the title {title}")
            continue
        try:
            status, _, body = github.request("POST", f"/repos/{repo}/issues",
                                             {"title": title, "body": issue_body(issue, run), "labels": LABELS})
            if status != 201:
                raise ApiError(f"HTTP {status}")
            number = obj(parse_json(body)).get("number")
            if type(number) is not int:
                raise ApiError("an answer with no issue number")
        except ApiError as error:
            problems.append(Problem(f'Could not open the issue "{title}" in {repo}: {error}.',
                                    "Check that the job has issues: write, and that the repository has issues turned on"))
            continue
        print(f"Opened issue #{number}: {title}")
        titles.add(title)
    return problems
