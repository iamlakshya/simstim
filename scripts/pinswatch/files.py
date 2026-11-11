"""The files that the pins watch reads: the pins, and the verdicts (spec 19.5)."""
import re

from pinswatch.transport import Problem, clean

try:
    import tomllib
except ImportError:  # Python before 3.11
    tomllib = None

ZON = "vt/build.zig.zon"
LOCK = "guest/supply.lock"
REVIEWED = "scripts/pins-watch-reviewed.toml"
HEX40 = re.compile(r"[0-9a-f]{40}")
NAME = re.compile(r"[a-z0-9][a-z0-9._-]{0,63}")
REPO = re.compile(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+")
TAG_FORM = re.compile(r"[A-Za-z0-9._/-]*\{version\}[A-Za-z0-9._/-]*")
REF = re.compile(r"[!-~]{1,200}")  # printable ASCII with no space, as a tag name is
# How the packages of Ghostty's advisories map to its release tags (spec 19.5, the tag rule).
GHOSTTY_TAGS = {"ghostty": "v{version}", "libghostty-vt": "vt-{version}"}
NOT_UTF8 = "Save the file as UTF-8"


class Pin:
    def __init__(self, name, commit, repo, watch, source, ref=None, tags=None):
        self.name, self.commit, self.repo, self.watch, self.source = name, commit, repo, watch, source
        self.ref, self.tags, self.short = ref, tags or {}, commit[:8]

    def move(self, fix):
        where = "(vt/README.md)" if self.source == ZON else f"in {LOCK}"
        return f"Move the pin past {fix} {where}"

    def tag_form(self, package):
        """The form of the release tags of an advisory's package, such as "v{version}", or None."""
        return self.tags.get(package.lower(), self.tags.get("*"))


def read_toml(root, name):
    if tomllib is None:
        raise Problem("pins-watch needs Python 3.11 or later, for tomllib.", "Install Python 3.11 or later")
    try:
        with open(root / name, "rb") as source:
            return tomllib.load(source)
    except OSError as error:
        raise Problem(f"{name}: cannot read it: {clean(error.strerror or error)}.",
                      "Restore the file from git, with its header (spec 19.5)") from None
    except UnicodeDecodeError:
        raise Problem(f"{name}: cannot read it: it is not UTF-8 text.", NOT_UTF8) from None
    except tomllib.TOMLDecodeError as error:
        raise Problem(f"{name}: not valid TOML: {clean(error)}.", "Fix the line that the message names") from None


def zig_code(text):
    """Zig source without its // comments. A // inside a string stays."""
    out, i, in_string = [], 0, False
    while i < len(text):
        if in_string:
            step = 2 if text[i] == "\\" else 1
            in_string = text[i] not in '"\n'
            out.append(text[i:i + step])
            i += step
        elif text.startswith("//", i) or text.startswith("\\\\", i):
            end = text.find("\n", i)
            end = len(text) if end < 0 else end
            if text[i] == "\\":  # a line of a multi-line string, which keeps its text
                out.append(text[i:end])
            i = end
        else:
            in_string = text[i] == '"'
            out.append(text[i])
            i += 1
    return "".join(out)


def ghostty_pin(root):
    """The Ghostty commit, from the URL of the active .ghostty dependency."""
    problem = Problem(f"Could not find the Ghostty commit in {ZON}.",
                      "Keep the dependency's URL in the form …/archive/<commit>.tar.gz")
    try:
        code = zig_code((root / ZON).read_text(encoding="utf-8"))
    except UnicodeDecodeError:
        raise Problem(f"{ZON}: cannot read it: it is not UTF-8 text.", NOT_UTF8 + ", as Zig needs") from None
    except OSError:
        raise problem from None
    start = code.find(".dependencies")
    block = re.search(r"\.ghostty\s*=\s*\.\{(.*?)\}", code[max(start, 0):], re.DOTALL)
    url = block and re.search(r'\.url\s*=\s*"([^"]*)"', block.group(1))
    commit = url and re.search(r"/archive/([0-9a-f]{40})\.tar\.gz$", url.group(1))
    if not commit:
        raise problem
    return Pin("ghostty", commit.group(1), "ghostty-org/ghostty", "github", ZON, tags=GHOSTTY_TAGS)


def lock_pin(entry, index):
    name = entry.get("name")
    if not isinstance(name, str) or not name:
        raise Problem(f"{LOCK}: pin {index} has no name.", 'Add a short name, such as name = "cloud-hypervisor"')
    if not NAME.fullmatch(name):
        raise Problem(f'{LOCK}: the pin name "{clean(name, 64)}" is not a short name.',
                      'Use lower-case letters, digits, dots and dashes, such as name = "cloud-hypervisor"')
    label = f'{LOCK}: the pin "{name}"'
    for key, hint in (("ref", 'Add the tag of its commit, such as ref = "v54.0"'),
                      ("commit", "Add the 40-character commit of its ref")):
        if not isinstance(entry.get(key), str) or not entry[key]:
            raise Problem(f"{label} has no {key}.", hint)
    if not REF.fullmatch(entry["ref"]):
        raise Problem(f"{label} has a ref with a space, or a character that is not printable ASCII.",
                      "Set ref to the tag name, as git shows it")
    if not HEX40.fullmatch(entry["commit"]):
        raise Problem(f"{label} has a commit that is not 40 lower-case hex characters.",
                      "Add the 40-character commit of its ref")
    watch = entry.get("watch", "github")
    if watch not in ("github", "osv"):
        raise Problem(f"{label} has watch = {clean(repr(watch), 40)}.", 'Use watch = "github" or watch = "osv"')
    repo = entry.get("repo")
    if watch == "github" and (not isinstance(repo, str) or not REPO.fullmatch(repo)):
        raise Problem(f"{label} has no repo.", 'Add its GitHub repository, such as repo = "owner/name", or set watch = "osv"')
    tag = entry.get("tag", "v{version}")
    if not isinstance(tag, str) or not TAG_FORM.fullmatch(tag):
        raise Problem(f"{label} has a tag form that is not a tag name with {{version}} in it.",
                      'Use a form such as tag = "v{version}", or leave tag out')
    return Pin(name, entry["commit"], repo if watch == "github" else None, watch, LOCK,
               ref=entry["ref"], tags={"*": tag})


def lock_pins(root):
    """The pins of guest/supply.lock, and a Problem for each bad one."""
    data = read_toml(root, LOCK)
    if data.get("version") != 1:
        raise Problem(f"{LOCK}: version {clean(repr(data.get('version')), 40)} is not known.",
                      "Set version = 1, or update scripts/pins-watch.py")
    entries = data.get("pin", [])
    if not isinstance(entries, list) or not all(isinstance(entry, dict) for entry in entries):
        raise Problem(f"{LOCK}: pin is not a list of [[pin]] tables.", "Write each pin as a [[pin]] table (spec 19.5)")
    pins, problems, taken = [], [], {"ghostty"}
    for index, entry in enumerate(entries, 1):
        try:
            pin = lock_pin(entry, index)
            if pin.name in taken:
                raise Problem(f'{LOCK}: two pins are named "{pin.name}".', "Give each pin its own name")
            taken.add(pin.name)
            pins.append(pin)
        except Problem as problem:
            problems.append(problem)
    return pins, problems


def load_verdicts(root):
    """The verdicts, keyed by (advisory, pin, commit), and a Problem for each bad one."""
    if not (root / REVIEWED).exists():
        return {}, []
    entries = read_toml(root, REVIEWED).get("verdict", [])
    if not isinstance(entries, list) or not all(isinstance(entry, dict) for entry in entries):
        return {}, [Problem(f"{REVIEWED}: verdict is not a list of [[verdict]] tables.",
                            "Write each verdict as a [[verdict]] table (spec 19.5)")]
    verdicts, problems = {}, []
    for index, entry in enumerate(entries, 1):
        label = f"{REVIEWED}: verdict {index}"
        missing = [key for key in ("advisory", "pin", "commit", "verdict", "why")
                   if not isinstance(entry.get(key), str) or not entry[key].strip()]
        if missing:
            problems.append(Problem(f"{label} has no {missing[0]}.", "Give each verdict an advisory, a pin, a commit, a verdict and why"))
        elif not HEX40.fullmatch(entry["commit"]):
            problems.append(Problem(f"{label} has a commit that is not 40 lower-case hex characters.",
                                    f"Name the pin's whole commit, as {ZON} or {LOCK} has it"))
        elif entry["verdict"] not in ("fixed", "not-affected"):
            problems.append(Problem(f'{label} says verdict = "{clean(entry["verdict"], 40)}".', 'Use "fixed" or "not-affected"'))
        else:
            verdicts[(entry["advisory"].upper(), entry["pin"], entry["commit"])] = (entry["verdict"], clean(entry["why"]))
    return verdicts, problems
