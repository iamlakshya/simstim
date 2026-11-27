#!/usr/bin/env bash
# Fails when a shell script puts a non-ASCII character right after a variable
# name, as in "$name" followed by an ellipsis: in some locales the shell reads
# the character as part of the name and stops with "unbound variable". Write
# "${name}" before the character instead.
#
# It uses perl, which macOS and Linux both have; macOS grep has no -P.
set -euo pipefail
cd "$(dirname "$0")/.."
files=$(mktemp)
trap 'rm -f "$files"' EXIT
bash scripts/source-files.sh --tracked -- '*.sh' >"$files"
hits=$(xargs -0 perl -ne 'print "$ARGV:$.: $_" if /\$[A-Za-z_][A-Za-z0-9_]*[^\x00-\x7F]/; close ARGV if eof' -- <"$files")
if [ -n "$hits" ]; then
    echo "$hits"
    echo
    echo "Error: a variable name runs into a non-ASCII character."
    echo "Try: wrap the name in braces, as in \${name} before the character."
    exit 1
fi
echo "check-scripts: ok"
