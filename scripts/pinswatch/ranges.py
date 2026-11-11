"""How an advisory's range reads (spec 19.5): as intervals of commits, or as release versions.

One rule, an allow-list: a field must be printable ASCII, and it must split
completely into the tokens of the grammar in spec 19.5, with plain spaces
between them. Any other character or token makes the entry unread. The watch
never picks out the parts that it knows and skips the rest.
"""
import re

from pinswatch.transport import Unknown, clean, obj

HASH = re.compile(r"(?<![0-9A-Za-z])[0-9A-Fa-f]{7,40}(?![0-9A-Za-z])")
PRINTABLE = re.compile(r"[ -~]*")
END = r"(?![0-9A-Za-z])"  # a word ends where no letter or digit follows
# The tokens of the hash rule's grammar (spec 19.5). Words match in any case.
TOKEN = re.compile("|".join([
    rf"(?P<hash>[0-9A-Fa-f]{{7,40}}){END}",
    rf"(?P<introduced>>=|(?:from|since){END})",
    rf"(?P<fix><(?!=)|up +to +but +not +including{END})",
    rf"(?P<commit>commit{END})",
    rf"(?P<branch>main{END})",
    rf"(?P<at>at{END})",
    rf"(?P<separator>\|\||or{END})",
    r"(?P<comma>,)",
]), re.IGNORECASE)
VERSION = re.compile(r"\d+(?:\.\d+){1,3}")
BOUND = re.compile(r"(>=|<=|<|=)? *(\d+(?:\.\d+){1,3})")
NOT_ASCII = "it holds a character that is not printable ASCII"


def text(value):
    """A field of an answer as the watch reads it: the original string, or nothing."""
    return value if isinstance(value, str) else ""


def printable(value):
    """Whether a field holds printable ASCII only: no control, invisible or look-alike character."""
    return PRINTABLE.fullmatch(value) is not None


def names_commits(vulnerable, patched):
    """Whether an entry names commits, so the hash rule reads it. Otherwise the tag rule does."""
    return bool(HASH.search(vulnerable) or HASH.search(patched))


def outside(near):
    return Unknown(f'its text is outside the grammar of spec 19.5, from "{clean(near, 24)}"')


def tokens(value):
    """The tokens of a field, or Unknown when a character or a word is not in the grammar."""
    if not printable(value):
        raise Unknown(NOT_ASCII)
    found, pos = [], 0
    while True:
        while pos < len(value) and value[pos] == " ":
            pos += 1
        if pos == len(value):
            return found
        match = TOKEN.match(value, pos)
        if not match:
            raise outside(value[pos:])
        found.append((match.lastgroup, match.group()))
        pos = match.end()


def interval(group):
    """One interval: [main] bound *([","] bound), each bound a marker, an optional "commit" and a hash."""
    introduced, fixes, i = [], [], 0
    if group and group[0][0] == "branch":
        i = 1
    if i == len(group):
        raise Unknown("an interval of its range names no commit")
    while i < len(group):
        if group[i][0] == "comma" and (introduced or fixes):
            i += 1
        kind = group[i][0] if i < len(group) else None
        if kind not in ("introduced", "fix"):
            raise outside(group[i][1] if i < len(group) else "the end")
        i += 1
        if i < len(group) and group[i][0] == "commit":
            i += 1
        if i == len(group) or group[i][0] != "hash":
            raise outside(group[i][1] if i < len(group) else "the end")
        (introduced if kind == "introduced" else fixes).append(group[i][1].lower())
        i += 1
    if len(introduced) > 1 or len(fixes) > 1:
        raise Unknown("an interval of its range has two bounds of one kind")
    return (introduced[0] if introduced else None), fixes


def patched_commits(patched):
    """The commits of patched_versions: an empty field, or [main at] hash *("," [main at] hash)."""
    found, toks, i = [], tokens(patched), 0
    while i < len(toks):
        if toks[i][0] == "branch":
            if i + 1 == len(toks) or toks[i + 1][0] != "at":
                raise outside(toks[i][1])
            i += 2
        if i == len(toks) or toks[i][0] != "hash":
            raise outside(toks[i][1] if i < len(toks) else "the end")
        found.append(toks[i][1].lower())
        i += 1
        if i < len(toks):
            if toks[i][0] != "comma" or i + 1 == len(toks):
                raise outside(toks[i][1])
            i += 1
    return found


def intervals(vulnerable, patched):
    """The intervals of an entry that names commits (spec 19.5, the hash rule).

    Each interval is (introduced, fixes): the commit that brought the bug, or
    None, and the commits that fix it. A pin is affected when it lies in any
    interval. Raises Unknown for text outside the grammar.
    """
    groups = [[]]
    for token in tokens(vulnerable):
        if token[0] == "separator":
            groups.append([])
        else:
            groups[-1].append(token)
    found = []
    for index, group in enumerate(groups):
        if group and group[-1][0] == "comma" and index < len(groups) - 1:
            group = group[:-1]  # a comma before "or"
        found.append(interval(group))
    patched_fixes = patched_commits(patched)
    if len(found) == 1:
        found[0][1].extend(commit for commit in patched_fixes if commit not in found[0][1])
    elif any(commit not in [fix for _, fixes in found for fix in fixes] for commit in patched_fixes):
        raise Unknown("its patched commits do not match the intervals of its range")
    return found


def version_range(vulnerable, patched):
    """The lowest affected version (or None) and the patched versions of an entry with no hash.

    Raises Unknown when either field is outside the grammar (spec 19.5, the tag rule).
    """
    if not printable(vulnerable) or not printable(patched):
        raise Unknown(NOT_ASCII)
    lower = None
    for part in vulnerable.split(","):
        bound = BOUND.fullmatch(part.strip(" "))
        if not bound:
            raise Unknown("it is not a plain version range")
        if bound.group(1) in (None, "=", ">="):
            if lower is not None:
                raise Unknown("it has two lower bounds")
            lower = bound.group(2)
    versions = [part.strip(" ") for part in patched.split(",")] if patched.strip(" ") else []
    if not all(VERSION.fullmatch(version) for version in versions):
        raise Unknown("its patched versions are not plain versions")
    return lower, versions


def severity_of(advisory):
    """A repository advisory's severity, with its CVSS score where it has one."""
    word = advisory.get("severity")
    word = word if word in ("critical", "high", "medium", "low") else "unknown"
    scores = obj(advisory.get("cvss_severities"))
    for score in (obj(scores.get("cvss_v4")).get("score"), obj(scores.get("cvss_v3")).get("score"),
                  obj(advisory.get("cvss")).get("score")):
        if type(score) in (int, float) and 0 < score <= 10:
            return f"{word}, CVSS {score}"
    return word
