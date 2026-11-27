#!/usr/bin/env bash
# Fails when a hand-written Go source file, or a Python file of the
# end-to-end harness (its tests too), is over 500 lines, the quality bar in
# docs/contributing/testing.md. Go tests are not counted. Split a long file
# by topic: one file for each part of a feature.
set -euo pipefail
cd "$(dirname "$0")/.."
max=500
files=$(mktemp)
trap 'rm -f "$files"' EXIT
bash scripts/source-files.sh -- '*.go' 'test/e2e/*.py' >"$files"
fail=0
checked=0
while IFS= read -r -d '' f; do
    case "$f" in *_test.go) continue ;; esac
    checked=$((checked + 1))
    n=$(wc -l <"$f" | tr -d ' ')
    if [ "$n" -gt "$max" ]; then
        echo "$n $f"
        fail=1
    fi
done <"$files"
if [ "$checked" -eq 0 ]; then
    echo "Error: the size check selected no source files." >&2
    echo "Try: use a complete source tree, then run the check again." >&2
    exit 2
fi
if [ "$fail" -ne 0 ]; then
    echo
    echo "Error: these files are over $max lines."
    echo "Try: move a group of functions into a file of their own, by topic."
    exit 1
fi
echo "check-size: ok (no Go source file or e2e Python file is over $max lines)"
