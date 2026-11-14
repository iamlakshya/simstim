"""Offline retry tests using Go's real benchmark matcher, not Python's regexes."""
from collections import Counter
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parent
PACKAGES = tuple("./internal/" + name for name in ("proto", "ring", "term", "keys", "host", "client"))
LABELS = ("C1", "C1Cut", "C1Invalid", "UTF8Cut", "ASCII")
NESTED = {f"BenchmarkC1TranslateFrames/{label}/{size}"
          for label in LABELS for size in (4096, 16384, 65536)}
FIXTURE = r'''package fixture
import ("testing"; "strconv")
var sink int
func BenchmarkC1TranslateFrames(b *testing.B) {
    for _, name := range []string{"C1", "C1Cut", "C1Invalid", "UTF8Cut", "ASCII"} {
        for _, size := range []int{4096, 16384, 65536} {
            b.Run(name+"/"+strconv.Itoa(size), func(b *testing.B) {
                for b.Loop() { sink++ }
            })
        }
    }
}
func BenchmarkAttachOutputFrames(b *testing.B) { for b.Loop() { sink++ } }
func BenchmarkLiteral(b *testing.B) {
    b.Run("[dot].+", func(b *testing.B) { for b.Loop() { sink++ } })
}
'''


def isolated_env(root, **extra):
    env = {key: value for key, value in os.environ.items() if not key.startswith("XDG_")}
    for key in ("HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_STATE_HOME",
                "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR",
                "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"):
        path = root / key.lower()
        path.mkdir(mode=0o700, parents=True, exist_ok=True)
        env[key] = str(path)
    env.update(GOWORK="off", GOTOOLCHAIN="local", **extra)
    temporary = root / "tmp"
    temporary.mkdir(exist_ok=True)
    env["TMPDIR"] = str(temporary)
    return env


def names(output):
    return {re.sub(r"-[0-9]+$", "", row.split()[0])
            for row in output.splitlines() if row.startswith("Benchmark")}


class ConfirmationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory()
        cls.addClassCleanup(cls.temp.cleanup)
        cls.root = Path(cls.temp.name)
        (cls.root / "go.mod").write_text("module example.invalid/benchfixture\n\ngo 1.26\n")
        (cls.root / "bench_test.go").write_text(FIXTURE)
        cls.binary = cls.root / "fixture.test"
        cls.env = isolated_env(cls.root / "state")
        subprocess.run(["go", "test", "-c", "-o", str(cls.binary), "."],
                       cwd=cls.root, env=cls.env, check=True, capture_output=True, timeout=120)

    def pattern(self, rows):
        return subprocess.check_output(
            ["python3", "-I", str(ROOT / "bench-pattern.py")],
            input="\n".join(rows), text=True, env=self.env).strip()

    def match(self, pattern):
        result = subprocess.run([str(self.binary), "-test.run", "^$", "-test.bench", pattern,
                                 "-test.benchtime", "1x"], env=self.env, check=True,
                                capture_output=True, text=True, timeout=20)
        return names(result.stdout)

    def test_all_fifteen_nested_names(self):
        rows = [name.removeprefix("Benchmark") + " 1ns" for name in sorted(NESTED)]
        self.assertEqual(self.match(self.pattern(rows)), NESTED)

    def test_nested_scalar_and_legacy_cpu_suffix(self):
        rows = ["C1TranslateFrames/C1/65536-8 1ns", "AttachOutputFrames-8 1ns"]
        self.assertEqual(self.match(self.pattern(rows)),
                         {"BenchmarkC1TranslateFrames/C1/65536", "BenchmarkAttachOutputFrames"})
        self.assertEqual(self.match("^Benchmark(C1TranslateFrames/C1/65536)$"), set())

    def test_literal_regexp_characters(self):
        self.assertEqual(self.match(self.pattern(["Literal/[dot].+ 1ns"])),
                         {"BenchmarkLiteral/[dot].+"})

    def script_run(self, collision=None, default_output=False, **settings):
        with tempfile.TemporaryDirectory(dir=self.root) as folder:
            root = Path(folder)
            (root / "scripts").mkdir()
            for package in PACKAGES:
                (root / package).mkdir(parents=True)
            (root / "bin").mkdir()
            for name in ("bench.sh", "bench-pattern.py"):
                shutil.copyfile(ROOT / name, root / "scripts" / name)
            evidence = root / "evidence"
            protected = root / "protected"
            marker = "existing evidence must stay"
            if collision:
                protected.mkdir()
                (protected / "marker").write_text(marker)
                if collision == "directory":
                    evidence.mkdir()
                    (evidence / "marker").write_text(marker)
                elif collision == "file":
                    evidence.write_text(marker)
                elif collision == "link":
                    evidence.symlink_to(protected, target_is_directory=True)
                else:
                    evidence.symlink_to(root / "absent", target_is_directory=True)
            fixtures = {
                "git": r'''if [ "$2" = add ]; then
    printf '%s\n' "$4" >>"$BENCH_WORKTREE_LOG"
    for package in proto ring term keys host client; do
        mkdir -p "$4/internal/$package"
    done
fi
''',
                "go": r'''case "$1" in
env)
    if [ "$2" = GOSUMDB ]; then
        if [ "${BENCH_STATS_FAIL_PHASE:-}" = sumdb ]; then echo off; else echo sum.golang.org; fi
    else
        printf '{}\n'
    fi
    ;;
mod)
    [ "$2" = download ] && [ "$3" = -json ] && [ "$4" = 'golang.org/x/perf@v0.0.0-20260929162123-406019bb8b68' ] || exit 1
    printf 'download\n' >>"$BENCH_TOOL_LOG"
    if [ "${BENCH_STATS_FAIL_PHASE:-}" = download ]; then echo 'fixture module preparation failed' >&2; exit 42; fi
    printf '{"Path":"golang.org/x/perf","Version":"v0.0.0-20260929162123-406019bb8b68","Sum":"h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","GoModSum":"h1:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB="}\n'
    ;;
install)
    [ "$2" = 'golang.org/x/perf/cmd/benchstat@v0.0.0-20260929162123-406019bb8b68' ] || exit 1
    printf 'install\n' >>"$BENCH_TOOL_LOG"
    if [ "${BENCH_STATS_FAIL_PHASE:-}" = install ]; then echo 'fixture statistics build failed' >&2; exit 42; fi
    cp "$BENCH_STATS_FIXTURE" "$GOBIN/benchstat"
    chmod +x "$GOBIN/benchstat"
    ;;
version)
    if [ "${2:-}" = -m ]; then
        case "$3" in
            */tools/benchstat)
                printf 'buildinfo\n' >>"$BENCH_TOOL_LOG"
                tool_version='v0.0.0-20260929162123-406019bb8b68'
                tool_sum='h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA='
                if [ "${BENCH_STATS_FAIL_PHASE:-}" = version ]; then tool_version='v0.0.0-wrong-version'; fi
                if [ "${BENCH_STATS_FAIL_PHASE:-}" = checksum ]; then tool_sum='h1:wrong-checksum'; fi
                printf '%s: go1.26.11\n\tpath\tgolang.org/x/perf/cmd/benchstat\n\tmod\tgolang.org/x/perf\t%s\t%s\n' "$3" "$tool_version" "$tool_sum"
                ;;
            *) printf '%s: go1.26.11\n' "$3" ;;
        esac
    else
        printf 'go version go1.26.11 fixture/fixture\n'
    fi
    ;;
test)
    case "$4" in
        */bin-base/*) side=base ;;
        */bin-head/*) side=head ;;
        *) echo 'fixture output path has no side' >&2; exit 1 ;;
    esac
    printf '%s %s\n' "$side" "$5" >>"$BENCH_BUILD_LOG"
    if [ "${BENCH_FAIL_SIDE:-}" = "$side" ] && [ "${BENCH_FAIL_PACKAGE:-}" = "$5" ]; then
        # A failed compiler may leave output, so its status is authoritative.
        cp "$BENCH_WRAPPER" "$4"
        chmod +x "$4"
        printf 'fixture compiler stdout: %s %s\n' "$side" "$5"
        printf 'fixture compiler stderr: %s %s\n' "$side" "$5" >&2
        exit 42
    fi
    if [ "${BENCH_BAD_BINARY_SIDE:-}" = "$side" ] && [ "${BENCH_BAD_BINARY_PACKAGE:-}" = "$5" ] && [ "${BENCH_BAD_BINARY_MODE:-}" = missing ]; then
        exit 0
    fi
    cp "$BENCH_WRAPPER" "$4"
    chmod +x "$4"
    if [ "${BENCH_BAD_BINARY_SIDE:-}" = "$side" ] && [ "${BENCH_BAD_BINARY_PACKAGE:-}" = "$5" ] && [ "${BENCH_BAD_BINARY_MODE:-}" = permission ]; then
        chmod 0644 "$4"
    fi
    if [ "${BENCH_DROP_BASE_AT_END:-}" = 1 ] && [ "$side" = head ] && [ "$5" = ./internal/client ]; then
        rm -f "${4%/bin-head/*}/bin-base/ring.test"
    fi
    ;;
*) exit 1;;
esac
''',
                "stats": r'''if [ "${1:-}" = -h ]; then
    printf 'help\n' >>"$BENCH_TOOL_LOG"
    if [ "${BENCH_STATS_FAIL_PHASE:-}" = help ]; then echo 'fixture statistics execution failed' >&2; exit 42; fi
    echo 'fixture statistics help'
    exit 0
fi
printf 'compare\n' >>"$BENCH_STATS_LOG"
again=0
for arg in "$@"; do case "$arg" in *again-base.txt) again=1;; esac; done
printf 'sec/op\n'
if [ "$again" = 0 ]; then
    printf 'C1TranslateFrames/C1/65536 1ns 2ns +25.00%% (p=0.000 n=2)\n'
else
    printf 'C1TranslateFrames/C1/65536 1ns 1ns +0.00%% (p=1.000 n=2)\n'
fi
''',
                "wrapper": r'''out=$(mktemp)
trap 'rm -f "$out"' EXIT
"$BENCH_FIXTURE" "$@" -test.benchtime 1x >"$out"
printf '%s\n' "$0" >>"$BENCH_MATRIX_LOG"
printf '=== invocation ===\n' >>"$BENCH_LOG"
cat "$out" >>"$BENCH_LOG"
cat "$out"
''',
            }
            for name, body in fixtures.items():
                path = root / "bin" / name
                path.write_text("#!/usr/bin/env bash\nset -eu\n" + body)
                path.chmod(0o755)
            log = root / "runs.log"
            matrix = root / "matrix.log"
            builds = root / "builds.log"
            stats = root / "stats.log"
            tools = root / "tools.log"
            worktrees = root / "worktrees.log"
            env = isolated_env(root / "state", PATH=str(root / "bin") + os.pathsep + os.environ["PATH"],
                               BENCH_WRAPPER=str(root / "bin/wrapper"), BENCH_FIXTURE=str(self.binary),
                               BENCH_LOG=str(log), BENCH_MATRIX_LOG=str(matrix), BENCH_BUILD_LOG=str(builds),
                               BENCH_STATS_LOG=str(stats), BENCH_TOOL_LOG=str(tools),
                               BENCH_STATS_FIXTURE=str(root / "bin/stats"), BENCH_WORKTREE_LOG=str(worktrees),
                               BENCH_OUTPUT_DIR=str(evidence), BENCH_ROUNDS="1", BENCH_AGAIN_ROUNDS="2",
                               BENCH_MAX="15", **settings)
            if default_output:
                env.pop("BENCH_OUTPUT_DIR")
            result = subprocess.run(["bash", "scripts/bench.sh", "fixture"], cwd=root, env=env,
                                    capture_output=True, text=True, timeout=30)
            if default_output:
                folders = list((root / "build/bench").glob("run.*"))
                self.assertEqual(len(folders), 1)
                evidence = folders[0]
            files = {str(path.relative_to(evidence)): path.read_bytes()
                     for path in evidence.rglob("*") if path.is_file()} if evidence.is_dir() else {}
            executable = [str(path.relative_to(evidence)) for path in evidence.rglob("*.test")
                          if path.is_file() and os.access(path, os.X_OK)] if evidence.is_dir() else []
            if collision == "file":
                existing = evidence.read_text()
            elif collision == "directory":
                existing = (evidence / "marker").read_text()
            elif collision:
                existing = (protected / "marker").read_text()
            else:
                existing = None
            return {
                "result": result,
                "runs": log.read_text().split("=== invocation ===\n")[1:] if log.exists() else [],
                "matrix": matrix.read_text().splitlines() if matrix.exists() else [],
                "builds": builds.read_text().splitlines() if builds.exists() else [],
                "stats": stats.read_text().splitlines() if stats.exists() else [],
                "tools": tools.read_text().splitlines() if tools.exists() else [],
                "worktrees": worktrees.read_text().splitlines() if worktrees.exists() else [],
                "temporary_root": str(root / "state/tmp"),
                "worktrees_removed": all(not Path(path).exists() for path in worktrees.read_text().splitlines()) if worktrees.exists() else True,
                "evidence_files": files,
                "evidence_executables": executable,
                "evidence_is_dir": evidence.is_dir(),
                "evidence_is_link": evidence.is_symlink(),
                "existing_marker": existing,
            }

    @unittest.skipUnless(shutil.which("bash"), "the benchmark script needs Bash")
    def test_script_confirmation_reaches_real_nested_benchmark(self):
        run = self.script_run()
        result = run["result"]
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("the slowdown did not hold in 2 more rounds", result.stdout)
        expected_builds = [f"{side} {package}" for side in ("base", "head") for package in PACKAGES]
        self.assertEqual(run["builds"], expected_builds)
        # Six packages, both sides, one initial and two confirmation rounds.
        self.assertEqual(len(run["runs"]), 6 * len(PACKAGES))
        sampled = Counter((Path(path).parent.name, Path(path).stem) for path in run["matrix"])
        expected = Counter({("bin-" + side, Path(package).name): 3
                            for side in ("base", "head") for package in PACKAGES})
        self.assertEqual(sampled, expected)
        self.assertEqual(len(run["stats"]), 2)
        self.assertEqual(run["tools"], ["download", "install", "buildinfo", "help"])
        self.assert_durable_status(run)
        self.assertEqual(set(run["evidence_executables"]),
                         {"bin-" + side + "/" + Path(package).name + ".test"
                          for side in ("base", "head") for package in PACKAGES})
        for prefix in ("all", "again"):
            for suffix in ("base.txt", "head.txt", "stat.txt", "slow.txt"):
                self.assertIn(prefix + "-" + suffix, run["evidence_files"])
        verified = json.loads(run["evidence_files"]["tools/verified.json"])
        self.assertEqual(verified["version"], "v0.0.0-20260929162123-406019bb8b68")
        self.assertEqual(verified["sum"], "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
        self.assertEqual(len(verified["binary_sha256"]), 64)
        for output in run["runs"][:2 * len(PACKAGES)]:
            self.assertTrue(NESTED <= names(output))
        for output in run["runs"][2 * len(PACKAGES):]:
            self.assertEqual(names(output), {"BenchmarkC1TranslateFrames/C1/65536"})

    def assert_durable_status(self, run):
        self.assertTrue(run["evidence_is_dir"])
        self.assertEqual(run["evidence_files"]["exit-code.txt"].strip(),
                         str(run["result"].returncode).encode())

    def assert_no_samples(self, run):
        self.assertNotEqual(run["result"].returncode, 0)
        self.assertEqual(run["runs"], [])
        self.assertEqual(run["matrix"], [])
        self.assertEqual(run["stats"], [])

    @unittest.skipUnless(shutil.which("bash"), "the benchmark script needs Bash")
    def test_base_compile_failure_stops_before_sampling(self):
        run = self.script_run(BENCH_FAIL_SIDE="base", BENCH_FAIL_PACKAGE="./internal/ring")
        self.assert_no_samples(run)
        self.assertEqual(run["builds"], ["base ./internal/proto", "base ./internal/ring"])
        self.assertIn("fixture compiler stdout: base ./internal/ring", run["result"].stdout)
        self.assertIn("fixture compiler stderr: base ./internal/ring", run["result"].stderr)
        self.assertIn("could not build base benchmark package ./internal/ring", run["result"].stderr)
        self.assertIn("Try:", run["result"].stderr)
        self.assert_durable_status(run)
        self.assertIn(b"fixture compiler stdout: base ./internal/ring", run["evidence_files"]["build-base-ring.stdout"])
        self.assertIn(b"fixture compiler stderr: base ./internal/ring", run["evidence_files"]["build-base-ring.stderr"])

    @unittest.skipUnless(shutil.which("bash"), "the benchmark script needs Bash")
    def test_head_compile_failure_stops_before_sampling(self):
        run = self.script_run(BENCH_FAIL_SIDE="head", BENCH_FAIL_PACKAGE="./internal/ring")
        self.assert_no_samples(run)
        expected = [f"base {package}" for package in PACKAGES]
        expected += ["head ./internal/proto", "head ./internal/ring"]
        self.assertEqual(run["builds"], expected)
        self.assertIn("fixture compiler stdout: head ./internal/ring", run["result"].stdout)
        self.assertIn("fixture compiler stderr: head ./internal/ring", run["result"].stderr)
        self.assertIn("could not build head benchmark package ./internal/ring", run["result"].stderr)
        self.assert_durable_status(run)
        self.assertIn(b"fixture compiler stdout: head ./internal/ring", run["evidence_files"]["build-head-ring.stdout"])
        self.assertIn(b"fixture compiler stderr: head ./internal/ring", run["evidence_files"]["build-head-ring.stderr"])

    @unittest.skipUnless(shutil.which("bash"), "the benchmark script needs Bash")
    def test_missing_or_nonexecutable_binary_stops_before_sampling(self):
        for side in ("base", "head"):
            for mode in ("missing", "permission"):
                with self.subTest(side=side, mode=mode):
                    run = self.script_run(BENCH_BAD_BINARY_SIDE=side,
                                          BENCH_BAD_BINARY_PACKAGE="./internal/ring",
                                          BENCH_BAD_BINARY_MODE=mode)
                    self.assert_no_samples(run)
                    self.assertIn(f"{side} benchmark binary for ./internal/ring is missing or cannot be executed",
                                  run["result"].stderr)
                    self.assertIn("Try:", run["result"].stderr)

    @unittest.skipUnless(shutil.which("bash"), "the benchmark script needs Bash")
    def test_both_complete_binary_sets_are_checked_before_sampling(self):
        run = self.script_run(BENCH_DROP_BASE_AT_END="1")
        self.assert_no_samples(run)
        self.assertEqual(run["builds"], [f"{side} {package}" for side in ("base", "head")
                                        for package in PACKAGES])
        self.assertIn("base benchmark binary for ./internal/ring is missing or cannot be executed",
                      run["result"].stderr)


    @unittest.skipUnless(shutil.which("bash"), "the benchmark script needs Bash")
    def test_statistics_preparation_failure_precedes_package_builds_and_samples(self):
        for phase in ("sumdb", "download", "install", "version", "checksum", "help"):
            with self.subTest(phase=phase):
                run = self.script_run(BENCH_STATS_FAIL_PHASE=phase)
                self.assert_no_samples(run)
                self.assertEqual(run["builds"], [])
                self.assertEqual(run["worktrees"], [])
                self.assert_durable_status(run)
                self.assertIn("Try:", run["result"].stderr)

    @unittest.skipUnless(shutil.which("bash"), "the benchmark script needs Bash")
    def test_existing_evidence_paths_are_never_changed(self):
        for collision in ("directory", "file", "link", "dangling"):
            with self.subTest(collision=collision):
                run = self.script_run(collision=collision)
                self.assert_no_samples(run)
                self.assertEqual(run["builds"], [])
                self.assertEqual(run["tools"], [])
                self.assertEqual(run["existing_marker"], "existing evidence must stay")
                self.assertEqual(run["evidence_is_link"], collision in ("link", "dangling"))
                self.assertIn("evidence path already exists", run["result"].stderr)

    @unittest.skipUnless(shutil.which("bash"), "the benchmark script needs Bash")
    def test_default_evidence_is_not_the_disposable_worktree(self):
        run = self.script_run(default_output=True)
        result = run["result"]
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assert_durable_status(run)
        self.assertEqual(len(run["worktrees"]), 1)
        self.assertTrue(Path(run["worktrees"][0]).is_relative_to(run["temporary_root"]))
        self.assertTrue(run["worktrees_removed"])
        self.assertIn("all-base.txt", run["evidence_files"])
        self.assertIn("again-head.txt", run["evidence_files"])
        self.assertEqual(len(run["evidence_executables"]), 12)


if __name__ == "__main__":
    unittest.main()
