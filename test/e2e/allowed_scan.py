"""The AST scanner of allowed.py: checks every non-test e2e module against
allowed.py's owner-host and DISPOSABLE_ONLY tables, and refuses code shapes
that could bypass boundary.py or hide a command, flag, key or default-alert
signal from the static scan.
"""
import ast, os, re

import allowed


def host_key(src):
    """The code of src, a command's source, as check() compares it."""
    return ast.dump(ast.parse(src, mode="eval").body)


def is_cli(e):
    return (isinstance(e, ast.Name) and e.id == "cli") or (isinstance(e, ast.Attribute) and e.attr == "cli")


def client_words(node):
    """The words and flags of an argv list whose first word is the client,
    or None. The client is the name cli or an attribute cli."""
    if not isinstance(node, ast.List) or not node.elts or not is_cli(node.elts[0]):
        return None
    return tuple(e.value for e in node.elts if isinstance(e, ast.Constant) and isinstance(e.value, str))


def calls(tree, names):
    """Each call in tree of a function or method named one of names."""
    for node in ast.walk(tree):
        if isinstance(node, ast.Call):
            f = node.func
            name = f.id if isinstance(f, ast.Name) else f.attr if isinstance(f, ast.Attribute) else None
            if name in names:
                yield node


def guarded(node, up):
    """Whether node is in the body of an `if` on a disposable host: one of
    allowed.GUARDS, around it in up."""
    for u in up:
        if isinstance(u, ast.If) and ast.unparse(u.test) in allowed.GUARDS and any(node is n for b in u.body for n in ast.walk(b)):
            return True
    return False


def walk(tree):
    """Each node of tree, with the chain of nodes around it."""
    stack = [(tree, ())]
    while stack:
        node, up = stack.pop()
        yield node, up
        for child in ast.iter_child_nodes(node):
            stack.append((child, up + (node,)))


def parsed(here, name):
    with open(os.path.join(here, name)) as f:
        return ast.parse(f.read())


def scan(here=None):
    """What the e2e modules run: {kind: {key: [(where, place)]}}. place is
    "owner" for what a module of OWNER_MODULES runs outside an `if` on a
    disposable host, and "disposable" for the rest."""
    here = here or allowed.HERE
    found = {"client": {}, "host": {}, "flags": {}, "sends": {}}

    def add(kind, key, where, place):
        found[kind].setdefault(key, []).append((where, place))

    for name in allowed.OWNER_MODULES + allowed.DISPOSABLE_MODULES:
        for node, up in walk(parsed(here, name)):
            place = "disposable" if name in allowed.DISPOSABLE_MODULES or guarded(node, up) else "owner"
            where = "%s:%d" % (name, getattr(node, "lineno", 0))
            words = client_words(node)
            if words is not None:
                add("client", words, where, place)
            if not isinstance(node, ast.Call):
                continue
            f = node.func
            fname = f.id if isinstance(f, ast.Name) else f.attr if isinstance(f, ast.Attribute) else None
            if fname in ("host_rc", "host_ssh", "host_out"):
                arg = node.args[0] if node.args else ast.Constant("<no command>")
                add("host", ast.dump(arg), "%s %s" % (where, ast.unparse(arg)), place)
            elif fname in ("start", "start_new", "start_on_terminal", "start_asked"):
                for sub in ast.walk(node):
                    if isinstance(sub, ast.Constant) and isinstance(sub.value, str) and sub.value.startswith("--"):
                        add("flags", sub.value, where, place)
            elif fname == "send":
                arg = node.args[0] if node.args else None
                add("sends", arg.value if isinstance(arg, ast.Constant) else "<%s>" % ast.unparse(arg), where, place)
    return found


def tables():
    owner = {"client": allowed.CLIENT, "host": {host_key(k): k for k in allowed.HOST},
             "flags": allowed.START_FLAGS, "sends": allowed.SENDS}
    d = allowed.DISPOSABLE_ONLY
    only = {"client": d["client"], "host": {host_key(k): k for k in d["host"]},
            "flags": d["flags"], "sends": d["sends"]}
    return owner, only


def table_problems(here):
    """Each use that the tables do not hold, and each entry that no module uses."""
    found, (owner, only), out = scan(here), tables(), []
    for kind, keys in found.items():
        for key, uses in keys.items():
            on_owner = [w for w, place in uses if place == "owner"]
            if on_owner and key not in owner[kind]:
                out.append("%s %r at %s runs on every host, and is not in allowed.py's owner table" % (kind, key, ", ".join(on_owner)))
            elif key not in owner[kind] and key not in only[kind]:
                out.append("%s %r at %s is not in allowed.py" % (kind, key, ", ".join(w for w, _ in uses)))
        for key in owner[kind]:
            if not any(place == "owner" for _, place in keys.get(key, [])):
                out.append("allowed.py's owner entry %s %r runs on no host outside a disposable one" % (kind, owner[kind][key] if kind == "host" else key))
        for key in only[kind]:
            if key not in keys:
                out.append("allowed.py's DISPOSABLE_ONLY %s %r is used by no e2e module" % (kind, only[kind][key] if kind == "host" else key))
        for key in set(owner[kind]) & set(only[kind]):
            out.append("%s %r is in both tables" % (kind, key))
    return out


# What only the boundary may call or reach: a process, a terminal's
# descriptor, the command line of the host.
BANNED_MODULES = {"subprocess", "pty", "pexpect", "harness", "importlib", "posix", "nt",
                  "_posixsubprocess", "ctypes", "multiprocessing", "asyncio"}
DYNAMIC = {"__import__", "eval", "exec"}
OS_BANNED = re.compile(r"(system|popen|fork\w*|write|kill\w*|exec\w*|spawn\w*|posix_spawn\w*)$")
BOUNDARY_BANNED = BANNED_MODULES | {"os", "signal", "shutil"}
PROCESS = re.compile(r"(subprocess\.\w+|os\.(system|popen|fork\w*|write|exec\w*|spawn\w*|posix_spawn\w*|kill\w*)|pty\.\w+|importlib\.\w+|__import__)$")
ALIASED = {"cli", "host_argv", "host_rc", "host_ssh", "host_out", "ssh", "rc", "out", "run_cmd", "term", "Term", "argv", "on_host", "inner", "drop_link", "getattr"}
# Terminal bytes or commands that raise default alert events (needs_input):
# OSC 777;notify, OSC 9;<text> (excluding 9;4 progress and 9;9 cwd), OSC 99,
# or `hook notification`. Owner-host checks emit none (item V).
ALERT_SIGNAL = re.compile(r"(\]777;notify|\]99;|\]9;(?![49];)|hook notification)")


def function_of(up):
    return next((u for u in reversed(up) if isinstance(u, ast.FunctionDef)), None)


def qualified(node, aliases):
    """A known module reference, including a private alias, or None."""
    if isinstance(node, ast.Name):
        return aliases.get(node.id)
    if isinstance(node, ast.Attribute):
        base = qualified(node.value, aliases)
        return base + "." + node.attr if base else None
    return None


def module_aliases(tree):
    roots = BOUNDARY_BANNED | {"sys", "_os"}
    aliases = {n: n for n in roots}
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            aliases.update({a.asname or a.name: a.name for a in node.names if a.name.split(".")[0] in roots})
        elif isinstance(node, ast.ImportFrom) and (node.module or "").split(".")[0] in roots:
            aliases.update({a.asname or a.name: node.module + "." + a.name for a in node.names})
    changes = True
    while changes:
        changes = False
        for node in ast.walk(tree):
            for target, value in assigned(node):
                ref = qualified(value, aliases) if value is not None else None
                if isinstance(target, ast.Name) and ref and target.id not in aliases:
                    aliases[target.id], changes = ref, True
    return aliases


def unsafe_module_attribute(node, aliases):
    ref = qualified(node, aliases)
    if not ref or not isinstance(node, ast.Attribute):
        return False
    modules = BANNED_MODULES | {"os", "_os", "sys", "_sys"}
    base = ref.rsplit(".", 1)[0].rsplit(".", 1)[-1]
    return node.attr in modules or base in modules and bool(OS_BANNED.fullmatch(node.attr))


def git_shape(node, up):
    """Only the exact laptop pull commands may be written in owner modules."""
    expr, parents = node, list(up)
    while parents and isinstance(parents[-1], ast.BinOp):
        expr = parents.pop()
    if not (len(node.elts) == 1 and isinstance(expr, ast.BinOp) and isinstance(expr.op, ast.Add) and isinstance(expr.right, ast.List)
            and isinstance(expr.left, ast.BinOp) and isinstance(expr.left.op, ast.Add)
            and expr.left.left is node and ast.unparse(expr.left.right) == "allowed.GIT_FLAGS"):
        return False
    tail = expr.right.elts
    words = tuple(n.value if isinstance(n, ast.Constant) and isinstance(n.value, str) else None for n in tail)
    init = words[:-1] in allowed.GIT_INIT and isinstance(tail[-1], ast.Name) and tail[-1].id == "local"
    log = words in allowed.GIT_LOG
    call = parents[-1] if parents else None
    if not (isinstance(call, ast.Call) and call.args and call.args[0] is expr
            and ast.unparse(call.func) in ("run_parts.run_or_timeout", "run_or_timeout", "owned.run_cmd", "run_cmd", "boundary.run")):
        return False
    cwd = next((k.value for k in call.keywords if k.arg == "cwd"), None)
    if ast.unparse(call.func).endswith("run_or_timeout") and len(call.args) >= 3:
        cwd = call.args[2]
    if init:
        return cwd is None or isinstance(cwd, ast.Constant) and cwd.value is None or isinstance(cwd, ast.Name) and cwd.id == "local"
    return log and isinstance(cwd, ast.Name) and cwd.id == "local"


def raw_process_ref(node, aliases=None):
    """Whether a reference names a raw process or interpreter operation."""
    name = node.id if isinstance(node, ast.Name) else node.attr if isinstance(node, ast.Attribute) else None
    return bool(PROCESS.match(ast.unparse(node))) or name in DYNAMIC or unsafe_module_attribute(node, aliases or {})


def registry_problem(node, up, sys_names):
    """A modules lookup must name a known module without exposing the registry."""
    if not (isinstance(node, ast.Attribute) and node.attr == "modules" and
            isinstance(node.value, ast.Name) and node.value.id in sys_names):
        return False
    parent = up[-1] if up else None
    key = None
    if isinstance(parent, ast.Subscript) and parent.value is node:
        key = parent.slice
    elif isinstance(parent, ast.Attribute) and parent.attr == "get" and len(up) > 1:
        call = up[-2]
        if isinstance(call, ast.Call) and call.func is parent and call.args:
            key = call.args[0]
    if not isinstance(key, ast.Constant) or not isinstance(key.value, str):
        return True
    return key.value.split(".")[0] in BOUNDARY_BANNED | {"builtins", "sys"}


def boundary_problems(here):
    """Checks boundary.py itself: self.argv runs only inside _run and marker,
    and every subprocess.run in boundary.py has a timeout and explicit stdin."""
    out = []
    if not os.path.exists(os.path.join(here, "boundary.py")):
        return out
    for node, up in walk(parsed(here, "boundary.py")):
        at = "boundary.py:%d" % getattr(node, "lineno", 0)
        if not isinstance(node, ast.Call):
            continue
        fn = function_of(up)
        fname = fn.name if fn else "<module>"
        call = ast.unparse(node.func)
        if call == "self.argv" and fname not in ("_run", "marker"):
            out.append("%s calls self.argv in %s: only _run and marker may build a host argv" % (at, fname))
        if call in ("subprocess.run", "_subprocess.run"):
            kws = {k.arg for k in node.keywords if k.arg}
            if "timeout" not in kws:
                out.append("%s calls subprocess.run without timeout=" % at)
            if "stdin" not in kws:
                out.append("%s calls subprocess.run without stdin=" % at)
    return out


def shape_problems(here):
    """The code shapes that would hide a call from scan(): a module that is
    in no list, a process outside the boundary, a built client list, the
    client or a host call under another name, a disposable module imported
    outside its guard, a second DISPOSABLE, or an alert signal on an owner host."""
    out = boundary_problems(here)
    disposable_names = {m[:-3] for m in allowed.DISPOSABLE_MODULES}
    names = sorted(n for n in os.listdir(here) if n.endswith(".py") and not n.startswith("test_"))
    for name in names:
        if name not in allowed.OWNER_MODULES + allowed.DISPOSABLE_MODULES + allowed.BOUNDARY + allowed.SUPPORT:
            out.append("%s is an e2e module in no list of allowed.py" % name)
    defined = 0
    for name in names:
        if name in allowed.SUPPORT:
            continue
        tree = parsed(here, name)
        aliases = module_aliases(tree)
        boundary_names = {"boundary"} | {
            a.asname for n in ast.walk(tree) if isinstance(n, ast.Import) for a in n.names if a.name == "boundary" and a.asname
        }
        sys_names = {"sys"} | {
            a.asname for n in ast.walk(tree) if isinstance(n, ast.Import) for a in n.names if a.name == "sys" and a.asname
        }
        for node, up in walk(tree):
            at = "%s:%d" % (name, getattr(node, "lineno", 0))
            if name not in allowed.BOUNDARY:
                if isinstance(node, ast.Call) and raw_process_ref(node.func, aliases):
                    out.append("%s calls %s: only boundary.py starts a process" % (at, ast.unparse(node.func)))
                call_name = node.func.id if isinstance(node, ast.Call) and isinstance(node.func, ast.Name) else \
                    node.func.attr if isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute) else None
                if call_name == "getattr" and node.args and isinstance(node.args[0], ast.Name) \
                        and node.args[0].id in ({"os"} | boundary_names | sys_names):
                    out.append("%s calls getattr on %s" % (at, node.args[0].id))
                if isinstance(node, (ast.Name, ast.Attribute)) and isinstance(node.ctx, ast.Load) and raw_process_ref(node, aliases):
                    ref_name = node.id if isinstance(node, ast.Name) else node.attr
                    if ref_name in DYNAMIC:
                        out.append("%s reaches %s: only boundary.py may load interpreter operations" % (at, ast.unparse(node)))
                if registry_problem(node, up, sys_names):
                    out.append("%s reaches sys.modules without a known safe module" % at)
                if unsafe_module_attribute(node, aliases):
                    out.append("%s reaches a raw process module through %s" % (at, ast.unparse(node)))
                if isinstance(node, ast.keyword) and node.arg == "shell":
                    out.append("%s passes shell=" % at)
                if isinstance(node, ast.Import):
                    banned = sorted({a.name.split(".")[0] for a in node.names} & BANNED_MODULES)
                    if banned:
                        out.append("%s imports %s: only boundary.py starts a process" % (at, banned))
                    for a in node.names:
                        if a.name == "os" and a.asname:
                            out.append("%s aliases os as %s: only boundary.py starts a process" % (at, a.asname))
                if isinstance(node, ast.ImportFrom):
                    top = (node.module or "").split(".")[0]
                    if top in BANNED_MODULES:
                        out.append("%s imports %s from %s: only boundary.py starts a process" % (at, sorted(a.name for a in node.names), node.module))
                    if top == "builtins":
                        dynamic = sorted(a.name for a in node.names if a.name in DYNAMIC | {"getattr", "*"})
                        if dynamic:
                            out.append("%s imports interpreter access %s from builtins" % (at, dynamic))
                    if top == "sys" and any(a.name in ("modules", "*") for a in node.names):
                        out.append("%s imports the module registry from sys" % at)
                    if top == "os":
                        bad_os = sorted(a.name for a in node.names if a.name == "*" or OS_BANNED.match(a.name))
                        if bad_os:
                            out.append("%s imports %s from os: only boundary.py starts a process" % (at, bad_os))
                    if top == "boundary":
                        bad_b = sorted(a.name for a in node.names if a.name == "*" or a.name.startswith("_") or a.name in BOUNDARY_BANNED)
                        if bad_b:
                            out.append("%s imports internal %s from boundary" % (at, bad_b))
                if isinstance(node, ast.Attribute) and isinstance(node.value, ast.Name) and node.value.id in boundary_names \
                        and (node.attr.startswith("_") or node.attr in BOUNDARY_BANNED):
                    out.append("%s reaches boundary.%s: only boundary.py starts a process" % (at, node.attr))
                if isinstance(node, ast.Attribute) and node.attr == "fd":
                    out.append("%s reaches a terminal's descriptor" % at)
                if isinstance(node, ast.Call) and isinstance(node.func, (ast.Name, ast.Attribute)) and \
                        (node.func.id if isinstance(node.func, ast.Name) else node.func.attr) in ("argv", "on_host", "inner"):
                    out.append("%s builds the host's command line itself" % at)
            if name in allowed.BOUNDARY:
                continue
            # Owner-host modules outside `if DISPOSABLE` never emit default-alert signals (item V).
            if name in allowed.OWNER_MODULES and not guarded(node, up) and isinstance(node, ast.Constant) and isinstance(node.value, (str, bytes)):
                parent = up[-1] if up else None
                in_regex = isinstance(parent, ast.Call) and ast.unparse(parent.func) == "re.compile"
                in_doc = isinstance(parent, ast.Expr) and len(up) >= 2 and isinstance(up[-2], (ast.Module, ast.FunctionDef, ast.ClassDef))
                text = node.value.decode("latin1") if isinstance(node.value, bytes) else node.value
                if not (in_regex or in_doc) and ALERT_SIGNAL.search(text):
                    out.append("%s has default-alert signal %r outside `if DISPOSABLE`" % (at, text[:80]))
            if name in allowed.OWNER_MODULES and isinstance(node, ast.List) and node.elts and isinstance(node.elts[0], ast.Constant) \
                    and node.elts[0].value == "git" and not git_shape(node, up):
                out.append("%s hides or changes a laptop git command" % at)
            # The client: only as the first word of a literal list, as cli=, or shown in a message (owned.shown).
            if is_cli(node) and isinstance(node.ctx, ast.Load):
                parent = up[-1] if up else None
                first = isinstance(parent, ast.List) and parent.elts and parent.elts[0] is node
                kw = isinstance(parent, ast.keyword) and parent.arg == "cli"
                shown = isinstance(parent, ast.Call) and ast.unparse(parent.func) in ("owned.shown", "shown")
                if not (first or kw or shown):
                    out.append("%s uses the client other than as an argv's first word" % at)
            if client_words(node) is not None:
                parent = up[-1] if up else None
                if any(isinstance(e, ast.Starred) for e in node.elts):
                    out.append("%s builds a client list with *" % at)
                if isinstance(parent, ast.BinOp):
                    other = parent.right if parent.left is node else parent.left
                    fn = function_of(up)
                    fn_src = ast.unparse(fn) if fn is not None else ""
                    checked = "args = owned.command(args)" in fn_src or "args = command(args)" in fn_src
                    ok = isinstance(parent.op, ast.Add) and parent.left is node and (
                        (checked and ast.unparse(other) in ("args", "list(args)")) or ast.unparse(other).startswith(("owned.program(", "program(")))
                    if not ok:
                        out.append("%s builds a client list with %s" % (at, type(parent.op).__name__))
            for target, value in assigned(node):
                if value is not None and not (isinstance(target, ast.keyword) or (isinstance(node, ast.keyword) and target.id == node.arg)):
                    vname = value.id if isinstance(value, ast.Name) else value.attr if isinstance(value, ast.Attribute) else None
                    if vname in ALIASED or raw_process_ref(value, aliases) or (isinstance(value, ast.Name) and value.id in ({"os"} | boundary_names)):
                        out.append("%s gives a host call, a terminal or the client another name" % at)
                if isinstance(target, ast.Name) and target.id == "prog" and value is not None:
                    vsrc = ast.unparse(value)
                    if not (vsrc.startswith("['--'] + owned.program(") or vsrc.startswith("['--'] + program(")):
                        out.append("%s sets prog without owned.program: %s" % (at, vsrc))
                if name in allowed.OWNER_MODULES and not guarded(node, up) and isinstance(target, ast.Name) \
                        and target.id in ("repo", "exit_file") and value is not None:
                    vsrc = ast.unparse(value)
                    if not vsrc.startswith(("owned.run_dir(", "run_dir(")):
                        out.append("%s sets %s outside run_dir(): %s" % (at, target.id, vsrc))
                tname = target.id if isinstance(target, ast.Name) else target.attr if isinstance(target, ast.Attribute) else \
                    target.slice.value if isinstance(target, ast.Subscript) and isinstance(target.slice, ast.Constant) else None
                if str(tname).lower() == "disposable":
                    if name == "run.py" and isinstance(target, ast.Name) and isinstance(up[-1], ast.Module):
                        defined += 1
                    elif not (isinstance(value, ast.Name) and value.id == "DISPOSABLE"):
                        out.append("%s sets %s to %s" % (at, tname, ast.unparse(value) if value is not None else "?"))
            if isinstance(node, ast.Call) and any(isinstance(a, ast.Starred) for a in node.args) and \
                    ast.unparse(node.func).split(".")[-1] in ("start", "start_new", "start_on_terminal", "start_asked"):
                for a in node.args:
                    if isinstance(a, ast.Starred) and not (ast.unparse(a.value) in ("prog", "TYPING_SHELL") or ast.unparse(a.value).startswith(("owned.program(", "program("))):
                        out.append("%s gives a start *%s" % (at, ast.unparse(a.value)))
            imported = set()
            if isinstance(node, ast.Import):
                imported = {a.name for a in node.names}
            elif isinstance(node, ast.ImportFrom):
                imported = {node.module} | {a.name for a in node.names}
            if imported & disposable_names and name in allowed.OWNER_MODULES and not guarded(node, up):
                out.append("%s imports %s outside `if DISPOSABLE`" % (at, sorted(imported & disposable_names)))
    if defined != 1:
        out.append("run.py defines DISPOSABLE %d times" % defined)
    return out


def assigned(node):
    """Each (target, value) that node sets: an assignment, annotated
    assignment, walrus expression, keyword argument, or dict entry."""
    if isinstance(node, ast.Assign):
        for t in node.targets:
            if isinstance(t, ast.Tuple) and isinstance(node.value, ast.Tuple) and len(t.elts) == len(node.value.elts):
                yield from zip(t.elts, node.value.elts)
            elif isinstance(t, ast.Tuple):
                yield from ((e, None) for e in t.elts)
            else:
                yield t, node.value
    elif isinstance(node, (ast.AnnAssign, ast.NamedExpr)):
        yield node.target, node.value
    elif isinstance(node, ast.keyword) and node.arg:
        yield ast.Name(node.arg), node.value
    elif isinstance(node, ast.Dict):
        for k, v in zip(node.keys, node.values):
            if isinstance(k, ast.Constant) and isinstance(k.value, str):
                yield ast.Name(k.value), v


def check(here=None):
    """Every problem of the e2e modules in here: what the tables do not
    hold, and the shapes that would hide a call."""
    here = here or allowed.HERE
    return table_problems(here) + shape_problems(here)
