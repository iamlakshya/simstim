#!/usr/bin/env bash
# Builds the docs site into build/site/public (decision 0022): first the
# pages and data from docs/ (go run ./cmd/docgen -site site/src), with
# llms.txt and a Markdown copy of each page for this build's address, then
# Astro with Starlight (site/). The name comes from internal/brand through
# the generated data, and the repository's address from go.mod.
#
#   scripts/docs-site.sh [version] [base]
#
# version is dev or a release tag. base is where this build is served: a
# path such as / or /v1.2/, or a whole address such as
# https://docs.example.org/dev/. A build under /vX.Y/ or /dev/ finds the
# version list (versions.json) one level up.
set -euo pipefail
cd "$(dirname "$0")/.."

version=${1:-dev}
base=${2:-/}
module=$(sed -n 's/^module //p' go.mod)

need() { command -v "$1" >/dev/null 2>&1 || { echo "docs-site: $1 not found. Try: scripts/setup.sh (it installs Node's pinned version and pnpm)" >&2; exit 1; }; }
need node
need pnpm

versions=0
case "$base" in
http://* | https://*)
    versions=1 # a build for GitHub Pages, which has a version list
    url=$(printf '%s' "$base" | sed -E 's#^(https?://[^/]+).*#\1#')
    path=$(printf '%s' "$base" | sed -E 's#^https?://[^/]+##')
    ;;
*)
    url=https://example.invalid
    path=$base
    ;;
esac
path="/${path#/}"
path="${path%/}/"
root=$(printf '%s' "$path" | sed -E 's#(v[0-9]+\.[0-9]+|dev)/$##')

go run ./cmd/docgen -site site/src -site-url "$url$path"
# The lockfile pins every package with its hash; install only what it says.
if [ ! -d site/node_modules ] || [ site/pnpm-lock.yaml -nt site/node_modules/.pnpm/lock.yaml ]; then
    pnpm --dir site install --frozen-lockfile
fi
ASTRO_TELEMETRY_DISABLED=1 SITE_URL="$url" SITE_BASE="$path" SITE_ROOT="$root" DOCS_VERSION="$version" SITE_VERSIONS="$versions" \
    REPO_URL="https://$module" pnpm --dir site exec astro build --silent
echo "docs-site: built build/site/public ($version at $url$path)"
