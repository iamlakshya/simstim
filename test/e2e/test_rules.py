"""Offline tests of the e2e run's rules as code: what it may run on a test
host (the allow list), what it skips and the Go tests that cover that,
every external call's timeout, default-alert safety on owner hosts, and
run.py's frame: python3 test/e2e/test_rules.py.
"""
import ast, contextlib, inspect, io, os, re, unittest

import sys
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fakes import HERE, MODULES, source  # noqa: E402
import allowed  # noqa: E402
import boundary  # noqa: E402
import owned as ow  # noqa: E402
import run_parts as rp  # noqa: E402


def read_file(path):
    with open(path) as f:
        return f.read()


class Alerts(unittest.TestCase):
    """The alerts check reads `host alerts <host> --json`, and nothing more."""
    def report(self, **kw):
        info = {"v": 1, "alerts": {"path": "~/.config/client/alerts.toml", "exists": False}, "log": []}
        for k, v in kw.items():
            if k in ("path", "exists"):
                info["alerts"][k] = v
            elif v is None:
                info.pop(k)
            else:
                info[k] = v
        return info

    def test_the_report(self):
        self.assertTrue(rp.alerts_report(self.report()))
        self.assertTrue(rp.alerts_report(self.report(path="/srv/xdg/client/alerts.toml", exists=True, log=None)))
        for name, info in {
            "no report": None,
            "a list": [],
            "no version": self.report(v=None),
            "a version that is true": self.report(v=True),
            "no alerts": self.report(alerts=None),
            "exists as a string": self.report(exists="false"),
            "a log that is no list": self.report(log={}),
            "a path that a shell reads": self.report(path="~/x;rm -rf ~/y/alerts.toml"),
            "a path with a command": self.report(path="~/$(id)/alerts.toml"),
            "a path that goes up": self.report(path="~/../alerts.toml"),
            "a relative path": self.report(path="x/alerts.toml"),
            "another file": self.report(path="~/a/other.toml"),
        }.items():
            with self.subTest(name):
                self.assertFalse(rp.alerts_report(info))


class Structure(unittest.TestCase):
    def test_no_client_kill_or_rm_on_a_session(self):
        for name in MODULES:
            for node in ast.walk(ast.parse(source(name))):
                if isinstance(node, (ast.List, ast.Tuple)):
                    words = [e.value for e in node.elts if isinstance(e, ast.Constant)]
                    client = any((isinstance(e, ast.Name) and e.id == "cli") or (isinstance(e, ast.Attribute) and e.attr == "cli") for e in node.elts)
                    self.assertFalse(client and ({"kill", "rm"} & set(words)), "%s:%d runs the client's %s" % (name, node.lineno, words))

    def test_no_change_to_the_hosts_alerts(self):
        for name in MODULES:
            for node in ast.walk(ast.parse(source(name))):
                if isinstance(node, (ast.List, ast.Tuple)):
                    words = {e.value for e in node.elts if isinstance(e, ast.Constant)}
                    self.assertFalse("alerts" in words and words - {"host", "alerts", "--json"},
                                     "%s:%d runs host alerts %s" % (name, node.lineno, sorted(words, key=str)))
            self.assertNotIn('"host", "alerts"] +', source(name))

    def test_no_fixed_path_on_the_host(self):
        fixed = re.compile(r"(~|\$HOME)/\.?e2e-(?!lock\b|run-%s)")
        for name in allowed.OWNER_MODULES + allowed.DISPOSABLE_MODULES:
            tree = ast.parse(source(name))
            docs = {id(n.value) for n in ast.walk(tree) if isinstance(n, ast.Expr)}
            for node in ast.walk(tree):
                if isinstance(node, ast.Constant) and isinstance(node.value, str) and id(node) not in docs:
                    self.assertIsNone(fixed.search(node.value), "%s:%d names a fixed path: %r" % (name, node.lineno, node.value[:80]))

    def test_run_py_cleans_up_in_a_finally(self):
        tree = ast.parse(source("run.py"))
        tries = [n for n in tree.body if isinstance(n, ast.Try)]
        self.assertEqual(len(tries), 1)
        finish = [n for n in ast.walk(ast.Module(body=tries[0].finalbody, type_ignores=[]))
                  if isinstance(n, ast.Call) and ast.unparse(n.func) == "run_parts.finish"]
        self.assertTrue(finish, "the finally calls run_parts.finish")
        loops = [n for n in tries[0].finalbody if isinstance(n, ast.For)]
        self.assertTrue(loops and "'finished' in state" in ast.unparse(loops[0]), "the finally retries finish if interrupted before completion")
        fails = [n for n in tries[0].finalbody if isinstance(n, ast.If) and ast.unparse(n.test) == "not clean"]
        self.assertTrue(fails and "sys.exit(" in ast.unparse(fails[0]), "a run whose clean-up failed fails")
        body = ast.unparse(ast.Module(body=tries[0].body, type_ignores=[]))
        for check in ["setup_checks()", "start(", "run_parts.pull_check", "run_parts.json_check", "run_parts.alerts_report"]:
            self.assertIn(check, body)
        self.assertIn("run_parts.helper_path(C, envx)", ast.unparse(tries[0].body[0]))
        self.assertIn("owned.take_lock(C)", ast.unparse(tries[0].body[1]))
        refusal = tries[0].body[2]
        self.assertTrue(isinstance(refusal, ast.If) and ast.unparse(refusal.test) == "not locked" and "sys.exit(" in ast.unparse(refusal),
                        "a held lock stops the run")
        self.assertIn("owned.make_run_dir(C)", ast.unparse(tries[0].body[3]))
        head = ast.unparse(ast.Module(body=tree.body[:tree.body.index(tries[0])], type_ignores=[]))
        self.assertIn("run_parts.exit_on_signals()", head)
        self.assertNotIn("take_lock", head)
        after = ast.unparse(ast.Module(body=tree.body[tree.body.index(tries[0]) + 1:], type_ignores=[]))
        self.assertEqual(after.strip(), "")
        self.assertNotIn("Started [^/]+/", source("run.py"))


class AllowList(unittest.TestCase):
    """allowed.check() verifies both tables, unused entries, and code shapes."""
    def tables(self):
        return allowed.tables()

    def test_allowed_check_reports_nothing(self):
        self.assertEqual(allowed.check(), [])

    def test_every_command_is_allowed(self):
        found = allowed.scan()
        owner, only = self.tables()
        for kind, keys in found.items():
            for key, uses in keys.items():
                on_owner = [w for w, place in uses if place == "owner"]
                self.assertFalse(on_owner and key not in owner[kind],
                                 "%s %r at %s runs on every host, and is not in allowed.py's owner table" % (kind, key, ", ".join(on_owner)))
                self.assertTrue(key in owner[kind] or key in only[kind],
                                "%s %r at %s is not in allowed.py" % (kind, key, ", ".join(w for w, _ in uses)))
            for key in owner[kind]:
                self.assertTrue(any(place == "owner" for _, place in keys.get(key, [])),
                                "allowed.py's owner entry %s %r runs on no host outside a disposable one" % (kind, owner[kind][key] if kind == "host" else key))
            for key in only[kind]:
                self.assertIn(key, keys, "allowed.py's DISPOSABLE_ONLY %s %r is used by no e2e module" % (kind, only[kind][key] if kind == "host" else key))

    def test_no_entry_in_both_tables(self):
        owner, only = self.tables()
        for kind in owner:
            both = set(owner[kind]) & set(only[kind])
            self.assertFalse(both, "%s entries in both tables: %r" % (kind, sorted(map(str, both))))

    def test_each_entry_has_its_kind(self):
        kinds = (allowed.START, allowed.READ, allowed.ATTACH, allowed.DELETE, allowed.OWN_FOLDER, "the run's own lock")
        for table in (allowed.CLIENT, allowed.HOST, allowed.START_FLAGS, allowed.SENDS):
            for key, reason in table.items():
                self.assertTrue(reason.startswith(kinds), "%r: %r names none of the five kinds" % (key, reason))
        for table in allowed.DISPOSABLE_ONLY.values():
            for key, reason in table.items():
                self.assertTrue(reason.startswith(allowed.ONLY), "%r: %r says not why it runs only on a disposable host" % (key, reason))

    def test_the_scan_sees_what_is_not_allowed(self):
        tree = ast.parse("""
subprocess.run([cli, "kill", HOST + "/" + sid])
host_rc("rm -rf ~/x")
start_new(c, envx, created, name, "--worktree")
t.send(b"echo hi\\r")
""")
        found = {"client": set(), "host": set(), "flags": set(), "sends": set()}
        for node in ast.walk(tree):
            words = allowed.client_words(node)
            if words is not None:
                found["client"].add(words)
        found["host"] = {ast.unparse(n.args[0]) for n in allowed.calls(tree, {"host_rc"})}
        found["flags"] = {n.args[-1].value for n in allowed.calls(tree, {"start_new"})}
        found["sends"] = {n.args[0].value for n in allowed.calls(tree, {"send"})}
        self.assertNotIn(("kill",), allowed.CLIENT)
        self.assertEqual(found["client"], {("kill",)})
        self.assertNotIn(allowed.host_key(found["host"].pop()), {allowed.host_key(k) for k in allowed.HOST})
        self.assertNotIn(found["flags"].pop(), allowed.START_FLAGS)
        self.assertNotIn(found["sends"].pop(), allowed.SENDS)


def parents(tree):
    stack = [(tree, ())]
    while stack:
        node, up = stack.pop()
        yield node, up
        for child in ast.iter_child_nodes(node):
            stack.append((child, up + (node,)))


class Disposable(unittest.TestCase):
    """The checks that change a host's shared state or raise default alerts
    run only on a disposable host, every external call has a timeout, and the
    run's own environment turns no debug on."""
    def test_only_on_a_disposable_host(self):
        for module, guard, names in [("run.py", "DISPOSABLE", {"disposable", "guided_setup", "disposable_checks", "dc"}),
                                     ("run_parts.py", "state.get('disposable')", {"disposable"}),
                                     ("agent_setup.py", "c.DISPOSABLE", {"agent_login", "disposable_setup"})]:
            seen = 0
            for node, up in parents(ast.parse(source(module))):
                used = (isinstance(node, ast.Name) and node.id in names) or \
                       (isinstance(node, ast.Import) and {a.name for a in node.names} & names) or \
                       (isinstance(node, ast.ImportFrom) and ({node.module} | {a.name for a in node.names}) & names)
                if used:
                    seen += 1
                    self.assertTrue(any(isinstance(u, ast.If) and ast.unparse(u.test) == guard
                                        and node in ast.walk(ast.Module(body=u.body, type_ignores=[]))
                                        for u in up), "%s:%d reaches %s outside `if %s`" % (module, node.lineno, names, guard))
            self.assertTrue(seen, module)
        self.assertIn('DISPOSABLE = run_parts.disposable(T, os.environ.get("E2E_DISPOSABLE", ""), os.environ.get("E2E_DISPOSABLE_MARKER", ""))',
                      source("run.py"))

    def test_no_debug_in_the_runs_environment(self):
        body = ast.parse(source("run.py")).body
        text = [ast.unparse(n) for n in body]
        at = [i for i, t in enumerate(text) if t == "run_parts.clean_environ(NAME, os.environ)"]
        self.assertTrue(at, "run.py takes the debug variable out of its environment")
        self.assertLess(at[0], [i for i, t in enumerate(text) if t.startswith("R = boundary.Root(")][0])
        self.assertLess(at[0], [i for i, t in enumerate(text) if t.startswith("envx = R.env(")][0])

    def test_the_helper_check_comes_before_any_client_call(self):
        tree = ast.parse(source("run.py"))
        body = [ast.unparse(n) for n in [n for n in tree.body if isinstance(n, ast.Try)][0].body]
        check = [i for i, t in enumerate(body) if "run_parts.helper_ready(C, envx, DISPOSABLE)" in t][0]
        gate = [i for i, t in enumerate(body) if "run_parts.start_gate(C, envx, state)" in t][0]
        client = [i for i, t in enumerate(body) if "owned.sessions_on(C, envx)" in t][0]
        self.assertLess(check, gate)
        self.assertLess(check, client)
        self.assertIn("state['client_ok'] = True", body[check + 2])

    def test_every_external_call_has_a_timeout(self):
        # 1. boundary.run requires timeout= as a keyword-only parameter with no default.
        sig = inspect.signature(boundary.run)
        self.assertEqual(sig.parameters["timeout"].kind, inspect.Parameter.KEYWORD_ONLY)
        self.assertIs(sig.parameters["timeout"].default, inspect.Parameter.empty)
        # 2. Every subprocess.run in boundary.py has timeout= and stdin=, and no other non-test module calls subprocess.*.
        runs = list(allowed.calls(ast.parse(source("boundary.py")), {"run"}))
        sub_runs = [c for c in runs if ast.unparse(c.func) in ("subprocess.run", "_subprocess.run")]
        self.assertGreaterEqual(len(sub_runs), 5)
        for c in sub_runs:
            kws = {k.arg for k in c.keywords if k.arg}
            self.assertIn("timeout", kws, "boundary.py:%d missing timeout=" % c.lineno)
            self.assertIn("stdin", kws, "boundary.py:%d missing stdin=" % c.lineno)
        for mod in allowed.OWNER_MODULES + allowed.DISPOSABLE_MODULES + allowed.SUPPORT:
            if mod in ("fakes.py", "login_fakes.py"):
                continue
            for node in ast.walk(ast.parse(source(mod))):
                if isinstance(node, ast.Call):
                    self.assertFalse(ast.unparse(node.func).startswith(("subprocess.", "_subprocess.")),
                                     "%s:%d calls subprocess directly" % (mod, node.lineno))
        # 3. Every run_cmd, run_or_timeout and json_of call path has an explicit timeout.
        for mod in allowed.OWNER_MODULES + allowed.DISPOSABLE_MODULES:
            for c in allowed.calls(ast.parse(source(mod)), {"run_cmd"}):
                has_kw = any(k.arg == "timeout" for k in c.keywords)
                has_pos = len(c.args) >= 3
                self.assertTrue(has_kw or has_pos, "%s:%d calls run_cmd without timeout" % (mod, c.lineno))
        self.assertIn("timeout=120", source("run_parts.py"))
        self.assertIn("timeout=120", source("owned.py"))

    def test_owner_checks_raise_no_default_alerts(self):
        root = os.path.dirname(os.path.dirname(HERE))
        ev_go = read_file(os.path.join(root, "internal", "alerts", "event.go"))
        consts = dict(re.findall(r'^\s*(\w+)\s*=\s*"([a-z_]+)"', ev_go, re.M))
        m = re.search(r"var DefaultEvents = \[\]string\{([^}]+)\}", ev_go)
        self.assertIsNotNone(m)
        defaults = {consts[name.strip()] for name in m.group(1).split(",") if name.strip()}
        self.assertEqual(defaults, {"needs_input", "turn_done", "failed", "oom", "memory"})
        for ev in defaults:
            self.assertIn(ev, rp.alerts_report.__doc__)
        self.assertNotIn("--memory-max", allowed.START_FLAGS)
        self.assertIn("--memory-max", allowed.DISPOSABLE_ONLY["flags"])
        # Every session script started on owner hosts stays alive until helper rm mutes alerts before SIGHUP.
        for mod in allowed.OWNER_MODULES:
            for node, up in allowed.walk(ast.parse(source(mod))):
                if allowed.guarded(node, up) or not isinstance(node, ast.Call):
                    continue
                if ast.unparse(node.func) in ("owned.program", "program") and len(node.args) >= 2 and isinstance(node.args[1], ast.Constant):
                    script = node.args[1].value
                    self.assertTrue("sleep" in script, "%s:%d owner session script exits without sleeping: %r" % (mod, node.lineno, script))
                    self.assertNotIn("exit ", script, "%s:%d owner session script exits explicitly: %r" % (mod, node.lineno, script))

    def test_every_start_names_its_command(self):
        for module in allowed.OWNER_MODULES:
            for call in allowed.calls(ast.parse(source(module)), {"start_new", "start_asked", "start_on_terminal"}):
                text = ast.unparse(call)
                self.assertTrue("'--'" in text or "*prog" in text, "%s:%d: %s" % (module, call.lineno, text))
        self.assertIn("prog = ['--'] + owned.program(", ast.unparse(ast.parse(source("agent_setup.py"))))


class Skips(unittest.TestCase):
    """Each check that the run skips names the Go tests that cover it: each
    test is there, testing.md lists it, and an e2e module prints the SKIP."""
    def test_each_go_test_is_there(self):
        root = os.path.dirname(os.path.dirname(HERE))
        for check, (what, tests) in rp.SKIPPED.items():
            for t in tests:
                pkg, name = t.rsplit(".", 1)
                folder = os.path.join(root, pkg)
                text = "".join(read_file(os.path.join(folder, f)) for f in os.listdir(folder) if f.endswith("_test.go"))
                self.assertIn("func %s(t *testing.T)" % name, text, "%s: %s" % (check, t))

    def test_testing_md_lists_each(self):
        doc = read_file(os.path.join(os.path.dirname(os.path.dirname(HERE)), "docs", "contributing", "testing.md"))
        for check, (what, tests) in rp.SKIPPED.items():
            for t in tests:
                self.assertTrue("`%s`" % t.rsplit(".", 1)[1] in doc, "testing.md does not name %s, of %s" % (t, check))

    def test_each_skip_is_printed(self):
        text = source("run.py") + source("agent_setup.py")
        for check in rp.SKIPPED:
            self.assertTrue('skip("%s")' % check in text, "no e2e module prints the SKIP of %s" % check)
        with contextlib.redirect_stdout(io.StringIO()) as out:
            rp.skip("typing")
        self.assertIn("SKIP typing: it types commands and text into a session", out.getvalue())
        self.assertIn("Covered here by internal/host.TestAttachInputAndResize", out.getvalue())

    def test_the_usage_check_has_the_runs_name(self):
        src = source("run.py")
        self.assertIn('[cli, "new", HOST, "--name", owned.e2e_name("e2e-json-usage"), "--json", "--"]', src)
        self.assertIn('+ owned.program("/bin/sh", "exec sleep 600")', src)

    def test_resource_warning_gate_and_context_managed_opens(self):
        root = os.path.dirname(os.path.dirname(HERE))
        want = "python3 -W error::ResourceWarning -m unittest discover -s test/e2e -p 'test_*.py'"
        self.assertIn(want, read_file(os.path.join(root, "justfile")))
        self.assertIn(want, read_file(os.path.join(root, ".github", "workflows", "ci.yml")))
        for fname in sorted(f for f in os.listdir(HERE) if f.endswith(".py")):
            tree = ast.parse(source(fname))
            with_opens = {id(item.context_expr) for n in ast.walk(tree) if isinstance(n, ast.With) for item in n.items}
            for node in ast.walk(tree):
                if isinstance(node, ast.Call) and isinstance(node.func, ast.Name) and node.func.id == "open":
                    self.assertIn(id(node), with_opens, "%s:%d calls open() outside a `with` block" % (fname, node.lineno))

    def test_testing_md_step2_and_exception_labels(self):
        root = os.path.dirname(os.path.dirname(HERE))
        doc = read_file(os.path.join(root, "docs", "contributing", "testing.md"))
        step2 = next(line for line in doc.splitlines() if line.startswith("2. Make a host"))
        self.assertIn("`<client> host test <name>`", step2)
        self.assertNotIn("host test <name>`.", step2.replace("`<client> host test <name>`", ""))
        code_text = source("allowed.py") + source("run_parts.py")
        for letter in ("E", "F", "H"):
            self.assertIn("- %s. " % letter, doc)
            self.assertIn("exception %s" % letter, code_text)
        self.assertNotIn("- G. ", doc)
        self.assertIn("_NO_INSTALL=1", doc)
        exc_h = next(line for line in doc.splitlines() if line.startswith("- H. "))
        self.assertIn("login shell", exc_h)
        self.assertNotIn("git status", exc_h)


if __name__ == "__main__":
    unittest.main()
