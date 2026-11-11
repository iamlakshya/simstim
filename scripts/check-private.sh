#!/usr/bin/env bash
# Fails when a tracked file holds a private detail: a personal path, a private
# or Tailscale address, a tailnet name or an email address. Examples in the
# docs use the documentation ranges (192.0.2.0/24, 198.51.100.0/24,
# 203.0.113.0/24), example.com (or the reserved example.invalid) and the
# users "me", "you" and "dev". /home/agent is allowed too: it is the home
# folder of the sandbox's own guest user, which the design names (spec 7.1).
#
# A maintainer can add words of their own (machine names, a homelab's name)
# to private/deny.txt, one extended regex per line. A line that starts with
# "!" is a form that is fine after all, such as a public address that holds
# a denied word. That file is git-ignored, so the words never enter the repo.
# CI runs without it.
set -euo pipefail
cd "$(dirname "$0")/.."

patterns=(
    # Home folders of a real user.
    '/Users/[A-Za-z][A-Za-z0-9._-]*'
    '/home/[A-Za-z][A-Za-z0-9._-]*'
    # Private and carrier-grade NAT (Tailscale) IPv4 addresses.
    '(^|[^0-9.])10\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}($|[^0-9.])'
    '(^|[^0-9.])192\.168\.[0-9]{1,3}\.[0-9]{1,3}($|[^0-9.])'
    '(^|[^0-9.])172\.(1[6-9]|2[0-9]|3[01])\.[0-9]{1,3}\.[0-9]{1,3}($|[^0-9.])'
    '(^|[^0-9.])100\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])\.[0-9]{1,3}\.[0-9]{1,3}($|[^0-9.])'
    # Tailnet names.
    '[A-Za-z0-9-]+\.ts\.net'
    # Email addresses.
    '[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}'
)
more_allow=""
if [ -f private/deny.txt ]; then
    while IFS= read -r line; do
        case "$line" in
        '' | '#'*) continue ;;
        '!'*) more_allow+="|${line#!}" ;;
        *) patterns+=("$line") ;;
        esac
    done <private/deny.txt
fi

# Matches that are fine: generic users, documentation names and addresses,
# the bots' addresses, and another project's public contact addresses that an
# old version of a research note quoted.
allow='/Users/(me|you|runner|shared)([/" ]|$)|/home/(me|you|u|dev|user|runner|linuxbrew|admin|ubuntu|debian|vscode|node|agent)([/" .:;,)\<&`]|$)|example-tailnet\.ts\.net|@([A-Za-z0-9-]+\.)*(example|example\.(com|org|net|invalid)|openssh\.com|users\.noreply\.github\.com)([^A-Za-z0-9.-]|$)|git@github\.com|support@github\.com|noreply@|user@UID|@v[0-9]|@sha256|@latest|[a-z]+@herdr\.dev'
allow+="$more_allow"

# --history checks every version of every file in every commit, plus the
# commit messages: run it before the repository goes public.
if [ "${1:-}" = "--history" ]; then
    fail=0
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    git rev-list --all --objects | while read -r sha path; do
        [ -n "$path" ] || continue
        case "$path" in go.sum | vt/build.zig.zon | scripts/check-private.sh) continue ;; esac
        [ "$(git cat-file -t "$sha")" = blob ] || continue
        git cat-file -p "$sha" | grep -I -q . 2>/dev/null || continue
        printf '%s %s\n' "$sha" "$path"
    done | sort -u -k1,1 >"$tmp/blobs"
    git log --all --format='%B' >"$tmp/messages"
    while read -r sha path; do
        text=$(git cat-file -p "$sha" | sed -E "s#${allow}##g")
        for p in "${patterns[@]}"; do
            # No -q: grep must read all its input, or the writer dies of
            # SIGPIPE and, under pipefail, the match goes unreported.
            if printf '%s\n' "$text" | grep -E -- "$p" >/dev/null; then
                echo "history: $path (blob ${sha:0:7}): $p"
                fail=1
            fi
        done
    done <"$tmp/blobs"
    # No grep -q in these pipes: it stops reading at the first match, the
    # writer dies of SIGPIPE, and under pipefail the match goes unreported.
    for p in "${patterns[@]}"; do
        if sed -E "s#${allow}##g" "$tmp/messages" | grep -E -- "$p" >/dev/null; then
            echo "history: a commit message: $p"
            fail=1
        fi
    done
    if [ "$fail" -ne 0 ]; then
        echo
        echo "Error: the history holds a private detail."
        echo "Try: docs/contributing/going-public.md says how to plan a history rewrite; it needs the owner's yes."
        exit 1
    fi
    echo "check-private: ok, history clean ($(wc -l <"$tmp/blobs" | tr -d ' ') blobs and $(git rev-list --all | wc -l | tr -d ' ') commit messages)"
    exit 0
fi

fail=0
while IFS= read -r file; do
    [ -f "$file" ] || continue
    grep -I -q . "$file" 2>/dev/null || continue # binary or empty
    # Remove the allowed forms first, so a line with one of them and a real
    # hit still fails.
    text=$(sed -E "s#${allow}##g" "$file")
    for p in "${patterns[@]}"; do
        hits=$(printf '%s\n' "$text" | grep -n -E -- "$p" || true)
        if [ -n "$hits" ]; then
            echo "$file: $p"
            echo "$hits" | cut -c1-160 | sed 's/^/  /'
            fail=1
        fi
    done
done < <(git ls-files --cached | grep -v -E '^(go\.sum|vt/build\.zig\.zon|scripts/check-private\.sh)$')

if [ "$fail" -ne 0 ]; then
    echo
    echo "Error: a tracked file holds a private detail."
    echo "Try: use a generic name, example.com, or a documentation address such as 203.0.113.7."
    exit 1
fi
echo "check-private: ok (no personal paths, private addresses or emails)"
