#!/usr/bin/env bash
# Compares the attach pump's benchmarks between a base commit and the working
# tree, on this machine, and fails on a clear slowdown.
#
#   scripts/bench.sh [base]       (default: origin/main)
#
# Both sides are built as test binaries first, then run in turns, so that a
# change in the machine's load hits both alike. The side that runs first
# changes each round. benchstat (pinned) compares them.
#
# A benchmark is slow when it exists on both sides and is more than BENCH_MAX
# percent slower (default 10) with p below 0.05. A slow benchmark runs again,
# alone, for BENCH_AGAIN_ROUNDS more rounds (default 12), and the check fails
# only if it is still slow. One noisy round on a shared runner then does not
# fail the check, but a real slowdown does.
set -euo pipefail
cd "$(dirname "$0")/.."
go_build_toolchain=$(sh scripts/go-build-tags.sh toolchain)
export GOTOOLCHAIN="$go_build_toolchain"
go_build_tags=$(sh scripts/go-build-tags.sh)
export GO_BUILD_TAGS="$go_build_tags"

base=${1:-origin/main}
rounds=${BENCH_ROUNDS:-6}
again=${BENCH_AGAIN_ROUNDS:-12}
max=${BENCH_MAX:-10}
pkgs=(./internal/proto ./internal/ring ./internal/term ./internal/keys ./internal/host ./internal/client)
benchstat_module="golang.org/x/perf"
benchstat_version="v0.0.0-20260929162123-406019bb8b68"
benchstat="$benchstat_module/cmd/benchstat@$benchstat_version"

# Evidence is never a cleanup target. A caller using a disposable test
# home can choose a persistent path outside it with BENCH_OUTPUT_DIR.
if [ -n "${BENCH_OUTPUT_DIR:-}" ]; then
    evidence=$BENCH_OUTPUT_DIR
    if [ -e "$evidence" ] || [ -L "$evidence" ]; then
        echo "bench: the evidence path already exists: $evidence" >&2
        echo "Try: choose a new BENCH_OUTPUT_DIR below an existing writable directory." >&2
        exit 1
    fi
    if ! mkdir -m 0700 -- "$evidence"; then
        echo "bench: the evidence directory could not be made: $evidence" >&2
        echo "Try: choose a new BENCH_OUTPUT_DIR below an existing writable directory." >&2
        exit 1
    fi
else
    if ! mkdir -p build/bench || ! evidence=$(mktemp -d "$PWD/build/bench/run.XXXXXXXXXX"); then
        echo "bench: the default evidence directory could not be made." >&2
        echo "Try: choose a new writable BENCH_OUTPUT_DIR, then run this check again." >&2
        exit 1
    fi
fi
evidence=$(cd "$evidence" && pwd -P)
work=""
cleanup() {
    local status=$?
    trap - EXIT
    printf '%s\n' "$status" >"$evidence/exit-code.txt"
    if [ -n "$work" ]; then
        git worktree remove --force "$work/base" >/dev/null 2>&1 || true
        rm -rf -- "$work"
    fi
    echo "bench: evidence kept in $evidence" >&2
    exit "$status"
}
trap cleanup EXIT

prepare_stats() {
    local sumdb
    echo "bench: preparing $benchstat" >&2
    sumdb=$(go env GOSUMDB)
    if [ "$sumdb" = off ]; then
        echo "bench: Go's checksum database is disabled." >&2
        echo "Try: restore Go's normal GOSUMDB setting, then prepare the pinned statistics tool." >&2
        return 1
    fi
    mkdir -m 0700 "$evidence/tools"
    if ! go mod download -json "$benchstat_module@$benchstat_version" >"$evidence/tools/module.json" 2>"$evidence/tools/module.stderr"; then
        cat "$evidence/tools/module.json"
        cat "$evidence/tools/module.stderr" >&2
        echo "bench: the pinned statistics module could not be prepared." >&2
        echo "Try: prepare its pinned Go dependencies with checksum verification, then run this check again." >&2
        return 1
    fi
    if ! GOBIN="$evidence/tools" go install "$benchstat" >"$evidence/tools/install.stdout" 2>"$evidence/tools/install.stderr"; then
        cat "$evidence/tools/install.stdout"
        cat "$evidence/tools/install.stderr" >&2
        echo "bench: the pinned statistics tool could not be built." >&2
        echo "Try: prepare its pinned Go dependencies with checksum verification, then run this check again." >&2
        return 1
    fi
    stats_bin="$evidence/tools/benchstat"
    if [ ! -f "$stats_bin" ] || [ ! -x "$stats_bin" ]; then
        echo "bench: the pinned statistics tool is missing or cannot be executed." >&2
        echo "Try: fix its Go build, then run this check again." >&2
        return 1
    fi
    if ! go version -m "$stats_bin" >"$evidence/tools/buildinfo.txt" 2>"$evidence/tools/buildinfo.stderr"; then
        cat "$evidence/tools/buildinfo.stderr" >&2
        echo "bench: the statistics tool's build information could not be read." >&2
        echo "Try: rebuild the pinned tool with Go, then run this check again." >&2
        return 1
    fi
    if ! python3 -I - "$evidence/tools/module.json" "$evidence/tools/buildinfo.txt" "$stats_bin" "$benchstat_module" "$benchstat_version" >"$evidence/tools/verified.json" <<'PY'
import hashlib, json, pathlib, sys
module_file, info_file, binary, expected_path, expected_version = sys.argv[1:]
module = json.loads(pathlib.Path(module_file).read_text())
rows = [line.split() for line in pathlib.Path(info_file).read_text().splitlines()]
expected = ["mod", expected_path, expected_version, module.get("Sum", "")]
valid = (not module.get("Error") and module.get("Path") == expected_path
         and module.get("Version") == expected_version
         and module.get("Sum", "").startswith("h1:")
         and module.get("GoModSum", "").startswith("h1:")
         and ["path", expected_path + "/cmd/benchstat"] in rows
         and expected in rows and not any("=>" in row for row in rows))
if not valid:
    raise SystemExit("the installed statistics tool does not match its pinned module and checksum")
with open(binary, "rb") as handle:
    digest = hashlib.file_digest(handle, "sha256").hexdigest()
print(json.dumps({"path": expected_path, "version": expected_version,
                  "sum": module["Sum"], "go_mod_sum": module["GoModSum"],
                  "binary_sha256": digest}))
PY
    then
        echo "bench: the statistics tool's pin or checksum could not be verified." >&2
        echo "Try: prepare the exact pinned Go tool again, without module replacements." >&2
        return 1
    fi
    if ! "$stats_bin" -h >"$evidence/tools/help.stdout" 2>"$evidence/tools/help.stderr"; then
        cat "$evidence/tools/help.stderr" >&2
        echo "bench: the verified statistics tool could not run." >&2
        echo "Try: build the pinned tool for this machine, then run this check again." >&2
        return 1
    fi
}

# All tool preparation and integrity checks finish before a benchmark is
# built or sampled. Comparison later uses this exact executable directly.
stats_bin="$evidence/tools/benchstat"
work=$(mktemp -d "${TMPDIR:-/tmp}/bench-work.XXXXXXXXXX")
mkdir -m 0700 "$work/tool-prepare"
(
    cd "$work/tool-prepare"
    export GOWORK=off
    if ! actual_module=$(go env GOMOD) || [ "$actual_module" != /dev/null ]; then
        echo "bench: the temporary tool directory is inside a Go module or cannot be checked." >&2
        echo "Try: set TMPDIR to a writable directory outside every Go module, then run this check again." >&2
        exit 1
    fi
    printf '%s\n' "$actual_module" >"$evidence/tool-module-context.txt"
    prepare_stats
)
go version >"$evidence/go-version.txt"
go env -json GOOS GOARCH GOHOSTOS GOHOSTARCH CGO_ENABLED GOFLAGS GOSUMDB GOPROXY >"$evidence/go-env.json"
git worktree add --detach "$work/base" "$base" >/dev/null 2>"$evidence/base-worktree.stderr" || {
    cat "$evidence/base-worktree.stderr" >&2
    echo "bench: no commit $base" >&2
    echo "Try: fetch the base commit, then run this check again." >&2
    exit 1
}
git rev-parse "$base" >"$evidence/base-revision.txt"
git rev-parse HEAD >"$evidence/head-revision.txt"
git status --porcelain >"$evidence/head-status.txt"
git diff --binary HEAD >"$evidence/head.patch"

require_binary() { # require_binary <side> <package> <binary>
    if [ ! -f "$3" ] || [ ! -x "$3" ]; then
        echo "bench: $1 benchmark binary for $2 is missing or cannot be executed: $3" >&2
        echo "Try: build every benchmark package successfully, then run this check again." >&2
        return 1
    fi
}

build() { # build <tree> <out dir> <side>
    local tree=$1 dest=$2 side=$3 pkg bench_bin
    mkdir -p "$dest"
    if [ -d "$tree/internal/usernscode" ]; then
        local cohort_log="$evidence/build-$side-userns.log"
        if ! (cd "$tree" && cmd/userns/build.sh) >"$cohort_log" 2>&1; then
            cat "$cohort_log" >&2
            echo "bench: the $side actual namespace cohort could not be prepared." >&2
            echo "Try: prepare Zig and the complete source, then build that tree's real launchers." >&2
            return 1
        fi
    fi
    for pkg in "${pkgs[@]}"; do
        bench_bin="$dest/$(basename "$pkg").test"
        echo "bench: building $side $pkg" >&2
        local log_name="$evidence/build-$side-$(basename "$pkg")"
        local build_status=0
        (cd "$tree" && go test -tags="$go_build_tags" -c -o "$bench_bin" "$pkg") >"$log_name.stdout" 2>"$log_name.stderr" || build_status=$?
        cat "$log_name.stdout"
        cat "$log_name.stderr" >&2
        if [ "$build_status" -ne 0 ]; then
            echo "bench: could not build $side benchmark package $pkg." >&2
            echo "Try: fix the compiler error above, then run this check again." >&2
            return 1
        fi
        require_binary "$side" "$pkg" "$bench_bin"
        go version -m "$bench_bin" >"$log_name.buildinfo"
        python3 -I - "$bench_bin" >"$log_name.sha256" <<'PYHASH'
import hashlib, sys
with open(sys.argv[1], "rb") as handle:
    print(hashlib.file_digest(handle, "sha256").hexdigest())
PYHASH
    done
}

check_binaries() { # check_binaries <bin dir> <side>
    local bin_dir=$1 side=$2 pkg
    for pkg in "${pkgs[@]}"; do
        require_binary "$side" "$pkg" "$bin_dir/$(basename "$pkg").test"
    done
}
echo "bench: building $base and the working tree"
build "$work/base" "$evidence/bin-base" base
build "$PWD" "$evidence/bin-head" head
# A later build must not have removed an earlier side's binary. No sample
# starts until both complete package sets have been checked.
check_binaries "$evidence/bin-base" base
check_binaries "$evidence/bin-head" head

run() { # run <bin dir> <out file> <tree> <benchmarks> <side>
    local bin_dir=$1 output=$2 tree=$3 pattern=$4 side=$5 pkg bench_bin
    for pkg in "${pkgs[@]}"; do
        bench_bin="$bin_dir/$(basename "$pkg").test"
        require_binary "$side" "$pkg" "$bench_bin"
        (cd "$tree/$pkg" && "$bench_bin" -test.run '^$' -test.bench "$pattern" -test.benchmem -test.count 1 -test.benchtime 300ms) >>"$output"
    done
}

measure() { # measure <rounds> <benchmarks> <out prefix>
    for i in $(seq "$1"); do
        printf '\rbench: round %d of %d' "$i" "$1"
        if [ $((i % 2)) -eq 1 ]; then
            run "$evidence/bin-base" "$3-base.txt" "$work/base" "$2" base
            run "$evidence/bin-head" "$3-head.txt" "$PWD" "$2" head
        else
            run "$evidence/bin-head" "$3-head.txt" "$PWD" "$2" head
            run "$evidence/bin-base" "$3-base.txt" "$work/base" "$2" base
        fi
    done
    echo
}

compare() { # compare <out prefix>: prints benchstat's tables, and writes the slow benchmarks' rows to <out prefix>-slow.txt
    # A full name must keep one package and machine configuration. Otherwise
    # a single row would pool unrelated samples and could hide a slowdown.
    if ! python3 -I - "$1-base.txt" "$1-head.txt" >"$1-coverage.json" <<'PYCOVERAGE'
from collections import Counter
import json, pathlib, re, sys
owners = {}
counts = {}
for side, path in zip(("base", "head"), sys.argv[1:]):
    configuration = {}
    samples = Counter()
    for line in pathlib.Path(path).read_text().splitlines():
        metadata = re.fullmatch(r"([a-z][a-z0-9_-]*):[ \t]*(.*)", line)
        if metadata:
            key, value = metadata.groups()
            if value:
                configuration[key] = value
            else:
                configuration.pop(key, None)
        elif line.startswith("Benchmark"):
            if any(not configuration.get(key) for key in ("pkg", "goos", "goarch")):
                raise SystemExit("benchmark samples are missing package or platform metadata")
            full_name = line.split()[0]
            suffix = re.fullmatch(r"(.+)-(\d+)", full_name)
            name, cpus = (suffix[1], int(suffix[2])) if suffix else (full_name, 1)
            identity = (tuple(sorted(configuration.items())), cpus)
            if name in owners and owners[name] != identity:
                raise SystemExit("benchmark name has conflicting package or configuration: " + name)
            owners[name] = identity
            samples[name] += 1
    if not samples:
        raise SystemExit("benchmark input has no samples: " + pathlib.Path(path).name)
    counts[side] = samples
cases = [{"name": name, "configuration": dict(identity[0]), "runtime_cpus": identity[1],
          "samples": {side: sample_counts[name] for side, sample_counts in counts.items()}}
         for name, identity in sorted(owners.items())]
print(json.dumps({"cases": cases}))
PYCOVERAGE
    then
        echo "bench: benchmark sample identities could not be compared safely." >&2
        echo "Try: keep each benchmark name in one package and configuration on both sides, then run this check again." >&2
        return 1
    fi
    "$stats_bin" -table '' -row .fullname base="$1-base.txt" head="$1-head.txt" >"$1-stat.txt"
    cat "$1-stat.txt"
    python3 -I - "$1-stat.txt" "$max" >"$1-slow.txt" <<'PY'
import re, sys
text = open(sys.argv[1]).read()
limit = float(sys.argv[2])
# Only the sec/op table counts: it comes first.
first = text.split("\n\n")[0] if "sec/op" in text.split("\n\n")[0] else text
for line in first.splitlines():
    m = re.search(r"([+-]\d+(?:\.\d+)?)%\s+\(p=(\d\.\d+)", line)
    if m and float(m.group(1)) > limit and float(m.group(2)) < 0.05:
        print(line.strip())
PY
}

measure "$rounds" . "$evidence/all"
compare "$evidence/all"
if [ ! -s "$evidence/all-slow.txt" ]; then
    echo "bench: no benchmark is more than $max% slower"
    exit 0
fi
echo "bench: more than $max% slower in $rounds rounds:"
sed 's/^/  /' "$evidence/all-slow.txt"

# Run only the slow ones again. Go matches each slash-separated name part
# separately, with alternatives outside groups. Escape literal name bytes.
names=$(awk '{ sub(/-[0-9]+$/, "", $1); print $1 }' "$evidence/all-slow.txt" | paste -sd '|' -)
pattern=$(python3 -I scripts/bench-pattern.py <"$evidence/all-slow.txt")
echo "bench: running them again, alone, for $again rounds"
measure "$again" "$pattern" "$evidence/again"
if ! grep -q '^Benchmark' "$evidence/again-head.txt"; then
    echo "bench: could not run $names again" >&2
    exit 1
fi
compare "$evidence/again"
if [ ! -s "$evidence/again-slow.txt" ]; then
    echo "bench: the slowdown did not hold in $again more rounds"
    echo "bench: no benchmark is more than $max% slower"
    exit 0
fi
echo "bench: slower than the base by more than $max% in both runs:"
sed 's/^/  /' "$evidence/again-slow.txt"
exit 1
