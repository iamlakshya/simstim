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

base=${1:-origin/main}
rounds=${BENCH_ROUNDS:-6}
again=${BENCH_AGAIN_ROUNDS:-12}
max=${BENCH_MAX:-10}
pkgs=(./internal/proto ./internal/ring ./internal/term ./internal/keys ./internal/host ./internal/client)
benchstat="golang.org/x/perf/cmd/benchstat@v0.0.0-20260929162123-406019bb8b68"

work=$(mktemp -d)
cleanup() {
    git worktree remove --force "$work/base" >/dev/null 2>&1 || true
    rm -rf "$work"
}
trap cleanup EXIT

git worktree add --detach "$work/base" "$base" >/dev/null 2>&1 || { echo "bench: no commit $base" >&2; exit 1; }
build() { # build <tree> <out dir>
    mkdir -p "$2"
    for p in "${pkgs[@]}"; do
        name=$(basename "$p")
        (cd "$1" && go test -c -o "$2/$name.test" "$p" >/dev/null 2>&1) || true
    done
}
echo "bench: building $base and the working tree"
build "$work/base" "$work/bin-base"
build "$PWD" "$work/bin-head"

run() { # run <bin dir> <out file> <package dir> <benchmarks>
    for t in "$1"/*.test; do
        dir=${pkgs[0]}
        for p in "${pkgs[@]}"; do [ "$(basename "$p").test" = "$(basename "$t")" ] && dir=$p; done
        (cd "$3/$dir" && "$t" -test.run '^$' -test.bench "$4" -test.benchmem -test.count 1 -test.benchtime 300ms) >>"$2"
    done
}

measure() { # measure <rounds> <benchmarks> <out prefix>
    for i in $(seq "$1"); do
        printf '\rbench: round %d of %d' "$i" "$1"
        if [ $((i % 2)) -eq 1 ]; then
            run "$work/bin-base" "$3-base.txt" "$work/base" "$2"
            run "$work/bin-head" "$3-head.txt" "$PWD" "$2"
        else
            run "$work/bin-head" "$3-head.txt" "$PWD" "$2"
            run "$work/bin-base" "$3-base.txt" "$work/base" "$2"
        fi
    done
    echo
}

compare() { # compare <out prefix>: prints benchstat's tables, and writes the slow benchmarks' rows to <out prefix>-slow.txt
    GOTOOLCHAIN=auto go run "$benchstat" -table '' -row .name base="$1-base.txt" head="$1-head.txt" 2>/dev/null >"$1-stat.txt" ||
        GOTOOLCHAIN=auto go run "$benchstat" "$1-base.txt" "$1-head.txt" >"$1-stat.txt"
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

measure "$rounds" . "$work/all"
compare "$work/all"
if [ ! -s "$work/all-slow.txt" ]; then
    echo "bench: no benchmark is more than $max% slower"
    exit 0
fi
echo "bench: more than $max% slower in $rounds rounds:"
sed 's/^/  /' "$work/all-slow.txt"

# Run only the slow ones again. The old benchstat format adds -<GOMAXPROCS>
# to each name, so awk removes that suffix.
names=$(awk '{ sub(/-[0-9]+$/, "", $1); print $1 }' "$work/all-slow.txt" | paste -sd '|' -)
echo "bench: running them again, alone, for $again rounds"
measure "$again" "^Benchmark($names)\$" "$work/again"
if ! grep -q '^Benchmark' "$work/again-head.txt"; then
    echo "bench: could not run $names again" >&2
    exit 1
fi
compare "$work/again"
if [ ! -s "$work/again-slow.txt" ]; then
    echo "bench: the slowdown did not hold in $again more rounds"
    echo "bench: no benchmark is more than $max% slower"
    exit 0
fi
echo "bench: slower than the base by more than $max% in both runs:"
sed 's/^/  /' "$work/again-slow.txt"
exit 1
