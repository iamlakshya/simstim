#!/usr/bin/env bash
# List public source paths with Git's ignore rules, in a checkout or an archive.
set -euo pipefail
if ! command -v python3 >/dev/null 2>&1; then
    echo "Error: source checks need Python 3." >&2
    echo "Try: install Python 3, then run the check again." >&2
    exit 2
fi
exec python3 "$(dirname "$0")/source-files.py" "$@"
