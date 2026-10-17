#!/usr/bin/env bash
# Everything that must hold before the repository goes public
# (docs/contributing/going-public.md): the name check, no secrets and no
# private details in the tree or the history, licence information on every
# file (REUSE), the docs build and their links, the workflows' security, and
# the OpenSSF Scorecard checks this repository controls.
#
#   scripts/public-check.sh          (just public-check)
set -euo pipefail
cd "$(dirname "$0")/.."

fail=0
step() { printf '\n== %s\n' "$*"; }
need() { command -v "$1" >/dev/null 2>&1 || { echo "public-check: $1 not found. Try: scripts/setup.sh" >&2; exit 1; }; }
need gitleaks; need scorecard; need uvx; need python3

step "Local checkout validation (files and history)"

step "The name lives in internal/brand"
scripts/check-name.sh || fail=1

step "No private details in the tree"
scripts/check-private.sh || fail=1

step "No private details in the history"
scripts/check-private.sh --history || fail=1

step "No secrets in the history (gitleaks)"
gitleaks git . --redact --no-banner --exit-code 1 || fail=1

step "Licence information on every file (REUSE)"
uvx --from 'reuse[charset-normalizer]==6.2.0' reuse lint --lines >/dev/null && echo "reuse: ok" || {
    uvx --from 'reuse[charset-normalizer]==6.2.0' reuse lint --lines | head -20
    fail=1
}

step "Workflows (zizmor)"
uvx zizmor@1.16.0 --offline --min-severity medium .github || fail=1

step "Docs: build, lint, spelling, prose and links"
scripts/docs-check.sh || fail=1

if [ "$fail" -eq 0 ]; then echo "Local checkout: ok"; else echo "Local checkout: FAILED"; fi

step "Hosted OpenSSF Scorecard (GitHub state, not the local checkout)"
token=${GITHUB_AUTH_TOKEN:-${GITHUB_TOKEN:-$(gh auth token 2>/dev/null || true)}}
if [ -z "$token" ]; then
    echo "skipped: needs a GitHub token (gh auth login, or GITHUB_TOKEN)"
    fail=1
else
    module=$(sed -n 's/^module //p' go.mod)
    out=$(mktemp)
    GITHUB_AUTH_TOKEN=$token scorecard --repo="$module" --format=json >"$out" || {
        status=$?
        echo "Hosted Scorecard: command failed (exit $status). Try: check the token and Scorecard diagnostics above." >&2
        fail=1
    }
    python3 scripts/check-scorecard.py "$out" || fail=1
    rm -f "$out"
fi

echo
if [ "$fail" -eq 0 ]; then echo "public-check: ok"; else echo "public-check: FAILED"; fi
exit "$fail"
