#!/usr/bin/env bash
# Prints the CHANGELOG section for a version, for the release's notes.
# Usage: scripts/release-notes.sh v1.2.3   (or "Unreleased")
#
# With LINK_BASE set (such as https://github.com/<owner>/<repo>/blob/v1.2.3/),
# relative links become absolute, so they work on the release's page.
set -euo pipefail
cd "$(dirname "$0")/.."

want=${1:?usage: release-notes.sh <version|Unreleased>}
want=${want#v}

notes=$(awk -v want="$want" '
    /^## \[/ {
        v = $0
        sub(/^## \[/, "", v)
        sub(/\].*/, "", v)
        if (found) exit
        found = (v == want)
        next
    }
    found { print }
' CHANGELOG.md)

if [ -z "$(printf '%s' "$notes" | tr -d '[:space:]')" ]; then
    echo "release-notes: CHANGELOG.md has no section for [$want]" >&2
    echo "Try: rename the [Unreleased] section to [$want] - <date> before tagging." >&2
    exit 1
fi
out=$(printf '%s\n' "$notes" | sed -e '/./,$!d')
if [ -n "${LINK_BASE:-}" ]; then
    out=$(printf '%s\n' "$out" | LINK_BASE=$LINK_BASE perl -pe 's#\]\((?!https?://|\#|mailto:)([^)]+)\)#]($ENV{LINK_BASE}$1)#g')
fi
printf '%s\n' "$out"
