"""Offline checks of the laptop git boundary and its static call shapes."""
import os, shutil, subprocess, sys, tempfile, types, unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import allowed  # noqa: E402
import allowed_scan  # noqa: E402
import boundary  # noqa: E402


HERE = os.path.dirname(os.path.abspath(__file__))


class LaptopGit(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="e2e-laptop-git-")
        self.addCleanup(self.tmp.cleanup)
        self.root = os.path.join(self.tmp.name, "root")
        self.outside = os.path.join(self.tmp.name, "outside")
        self.repo = os.path.join(self.root, "repo")
        for path in (self.root, self.outside, self.repo, os.path.join(self.repo, ".git")):
            os.makedirs(path, exist_ok=True)
        self.c = types.SimpleNamespace(
            cli="client", NAME="client", HOST="box", DISPOSABLE=False,
            root=types.SimpleNamespace(path=self.root, home=os.path.join(self.root, "home")))
        self.env = {"HOME": self.c.root.home, "CLIENT_NO_INSTALL": "1", "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
                    "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull}
        self.c.root.dirs = {key: os.path.join(self.root, folder) for key, folder in
                            (("XDG_CONFIG_HOME", "config"), ("XDG_STATE_HOME", "state"),
                             ("XDG_DATA_HOME", "data"), ("XDG_CACHE_HOME", "cache"), ("XDG_RUNTIME_DIR", "runtime"))}
        for path in self.c.root.dirs.values():
            os.makedirs(path)
        self.env.update(self.c.root.dirs)
        old = boundary.CTX
        boundary.CTX = self.c
        self.addCleanup(setattr, boundary, "CTX", old)

    def argv(self, tail):
        return ["git"] + allowed.GIT_FLAGS + tail

    def refused_before_spawn(self, tail, cwd=None, env=None):
        with mock.patch.object(boundary._subprocess, "run") as run:
            with self.assertRaises(boundary.Refused):
                boundary.run(self.argv(tail), timeout=3, env=env or self.env, cwd=cwd)
            run.assert_not_called()

    def test_current_safe_shapes_and_init_default_cwd(self):
        target = os.path.join(self.root, "new")
        for tail in (["init", "--template=", "-q", target],
                     ["init", "--template=", "-q", "-b", "mine", target]):
            with self.subTest(tail=tail), mock.patch.object(boundary._subprocess, "run") as run:
                boundary.run(self.argv(tail), timeout=3, env=self.env)
                self.assertEqual(run.call_args.kwargs["cwd"], os.path.realpath(self.root))
                self.assertEqual(run.call_args.args[0], self.argv(tail))
        for tail in (["log", "--format=%s", "e2e-topic"], ["log", "--format=%s", "-1", "e2e-topic"]):
            with self.subTest(tail=tail), mock.patch.object(boundary._subprocess, "run") as run:
                boundary.run(self.argv(tail), timeout=3, env=self.env, cwd=self.repo)
                self.assertEqual(run.call_args.kwargs["cwd"], os.path.realpath(self.repo))

    def test_later_configuration_and_remote_shapes_are_refused(self):
        bad = (
            ["-c", "core.fsmonitor=command", "status"],
            ["-c", "core.sshCommand=command", "log", "--format=%s", "e2e-topic"],
            ["--config-env=core.hooksPath=HOOKS", "log", "--format=%s", "e2e-topic"],
            ["--exec-path=/outside", "log", "--format=%s", "e2e-topic"],
            ["push", "remote", "HEAD"], ["fetch", "remote"], ["status"],
            ["log", "--format=%s", "--all"], ["log", "--format=%s", "--output=/outside"],
            ["log", "--format=%s", "e2e-topic", "--", "file"],
            ["init", "--template=", "-q", "-b", "other", self.repo],
            ["init", "--template=/outside", "-q", self.repo],
            ["init", "--template=", "-q", self.repo, "--separate-git-dir=/outside"],
        )
        for tail in bad:
            with self.subTest(tail=tail):
                self.refused_before_spawn(tail, cwd=self.repo)

    def test_git_flags_prefix_cannot_be_changed(self):
        argv = self.argv(["log", "--format=%s", "e2e-topic"])
        argv[2] = "core.hooksPath=/outside"
        self.assertIsNotNone(allowed.laptop_refusal(argv, self.c, cwd=self.repo))

    def test_paths_and_working_directories_stay_inside_the_root(self):
        bad_paths = (self.outside, self.root, os.path.join(self.root, "..", "outside"), self.root + "-neighbor/repo")
        for path in bad_paths:
            with self.subTest(path=path):
                self.refused_before_spawn(["init", "--template=", "-q", path])
        self.refused_before_spawn(["log", "--format=%s", "e2e-topic"], cwd=self.outside)
        self.refused_before_spawn(["log", "--format=%s", "e2e-topic"], cwd=os.path.join(self.root, "missing"))
        self.assertIsNotNone(allowed.laptop_refusal(["client", "version", "--json"], self.c, cwd=self.outside))
        with mock.patch.object(boundary._subprocess, "run") as run:
            with self.assertRaises(boundary.Refused):
                boundary.run(["client", "version", "--json"], timeout=3, env=self.env, cwd=self.outside)
            run.assert_not_called()

    def test_symlink_escapes_and_git_storage_redirection_are_refused(self):
        link = os.path.join(self.root, "link")
        os.symlink(self.outside, link)
        self.refused_before_spawn(["init", "--template=", "-q", os.path.join(link, "new")])
        self.refused_before_spawn(["log", "--format=%s", "e2e-topic"], cwd=link)
        shutil.rmtree(os.path.join(self.repo, ".git"))
        external_git = os.path.join(self.outside, "git")
        os.makedirs(external_git)
        os.symlink(external_git, os.path.join(self.repo, ".git"))
        self.refused_before_spawn(["log", "--format=%s", "e2e-topic"], cwd=self.repo)
        self.refused_before_spawn(["init", "--template=", "-q", self.repo])
        os.unlink(os.path.join(self.repo, ".git"))
        with open(os.path.join(self.repo, ".git"), "w") as f:
            f.write("gitdir: " + external_git + "\n")
        self.refused_before_spawn(["log", "--format=%s", "e2e-topic"], cwd=self.repo)

    def test_internal_symlink_is_resolved_before_spawn(self):
        link = os.path.join(self.root, "inside")
        os.symlink(self.repo, link)
        with mock.patch.object(boundary._subprocess, "run") as run:
            boundary.run(self.argv(["log", "--format=%s", "e2e-topic"]), timeout=3, env=self.env, cwd=link)
            self.assertEqual(run.call_args.kwargs["cwd"], os.path.realpath(self.repo))

    def test_git_environment_redirects_are_refused(self):
        for key in ("GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_EXEC_PATH",
                    "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CONFIG_COUNT"):
            with self.subTest(key=key):
                self.refused_before_spawn(["log", "--format=%s", "e2e-topic"], cwd=self.repo,
                                          env=dict(self.env, **{key: self.outside}))
        for change in ({"GIT_CONFIG_GLOBAL": self.outside}, {"GIT_CONFIG_NOSYSTEM": "0"}):
            self.refused_before_spawn(["log", "--format=%s", "e2e-topic"], cwd=self.repo, env=dict(self.env, **change))

    def test_git_is_not_started_as_a_terminal(self):
        with mock.patch.object(boundary._harness, "Term") as term:
            with self.assertRaises(boundary.Refused):
                boundary.Term(self.argv(["init", "--template=", "-q", self.repo]), env=self.env)
            term.assert_not_called()

    def test_actual_safe_init_and_log_work(self):
        if shutil.which("git") is None:
            self.skipTest("git is not installed")
        shutil.rmtree(self.repo)
        p = boundary.run(self.argv(["init", "--template=", "-q", "-b", "mine", self.repo]), timeout=10, env=self.env)
        self.assertEqual(p.returncode, 0, p.stderr)
        for tail in (["commit", "--allow-empty", "-qm", "from the session"], ["branch", "e2e-topic"]):
            p = subprocess.run(self.argv(tail), cwd=self.repo, env=self.env, stdin=subprocess.DEVNULL,
                               capture_output=True, timeout=10)
            self.assertEqual(p.returncode, 0, p.stderr)
        for tail in (["log", "--format=%s", "e2e-topic"], ["log", "--format=%s", "-1", "e2e-topic"]):
            p = boundary.run(self.argv(tail), timeout=10, env=self.env, cwd=self.repo)
            self.assertEqual(p.returncode, 0, p.stderr)
            self.assertEqual(p.stdout, b"from the session\n")


class StaticGit(unittest.TestCase):
    def copy_suite(self):
        folder = tempfile.mkdtemp(prefix="e2e-git-scan-")
        self.addCleanup(shutil.rmtree, folder, True)
        for name in os.listdir(HERE):
            if name.endswith(".py") and not name.startswith("test_"):
                shutil.copy(os.path.join(HERE, name), os.path.join(folder, name))
        return folder

    def problems(self, code):
        folder = self.copy_suite()
        with open(os.path.join(folder, "run_parts.py"), "a") as f:
            f.write("\n" + code + "\n")
        return allowed_scan.shape_problems(folder)

    def test_current_calls_and_safe_paths_or_data_methods_are_allowed(self):
        self.assertEqual(allowed_scan.shape_problems(self.copy_suite()), [])
        for code in ('value = os.path.join("a", "b")', 'paths = os.path\nvalue = paths.dirname("a")',
                     'writer = record.write\nwriter("data")', 'record.system("data")'):
            with self.subTest(code=code):
                self.assertEqual(self.problems(code), [])

    def test_nested_process_module_references_and_aliases_are_refused(self):
        for code in ('os.path.os.system("command")', 'module = os.path.os',
                     'paths = os.path\nmodule = paths.os\nmodule.system("command")',
                     'paths = os.path\nrun = paths.os.system\nrun("command")',
                     'shutil.os.system("command")'):
            with self.subTest(code=code):
                self.assertTrue(any("process" in p for p in self.problems(code)))

    def test_static_owner_git_cannot_override_or_hide_the_safe_shape(self):
        bad = (
            'run_or_timeout(["git"] + allowed.GIT_FLAGS + ["-c", "core.fsmonitor=command", "status"], envx, local)',
            'run_or_timeout(["git"] + allowed.GIT_FLAGS + ["push", "remote", "HEAD"], envx, local)',
            'run_or_timeout(["git", "--exec-path=/outside"] + allowed.GIT_FLAGS + ["log", "--format=%s", "e2e-topic"], envx, local)',
            'run_or_timeout(["git"] + allowed.GIT_FLAGS + ["log", "--format=%s", "e2e-topic", "--all"], envx, local)',
            'run_or_timeout(["git"] + allowed.GIT_FLAGS + ["init", "--template=", "-q", "/outside"], envx, None)',
            'args = ["git"] + allowed.GIT_FLAGS + ["log", "--format=%s", "e2e-topic"]',
            'run_or_timeout(["git"] + extra + ["log", "--format=%s", "e2e-topic"], envx, local)',
            'run_or_timeout(["git"] + allowed.GIT_FLAGS + ["log", "--format=%s", "e2e-topic"], envx, "/outside")',
        )
        for code in bad:
            with self.subTest(code=code):
                self.assertTrue(any("laptop git" in p for p in self.problems(code)))

    def test_static_exact_init_and_log_variants_stay_allowed(self):
        for tail, cwd in (('["init", "--template=", "-q", local]', "None"),
                          ('["init", "--template=", "-q", "-b", "mine", local]', "None"),
                          ('["log", "--format=%s", "e2e-topic"]', "local"),
                          ('["log", "--format=%s", "-1", "e2e-topic"]', "local")):
            with self.subTest(tail=tail):
                self.assertEqual(self.problems('run_or_timeout(["git"] + allowed.GIT_FLAGS + ' + tail + ', envx, ' + cwd + ')'), [])


if __name__ == "__main__":
    unittest.main()
