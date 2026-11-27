#!/usr/bin/env bash
# Fails when the product name is spelled in code outside internal/brand.
# The module path and import paths may contain it; docs may mention it. The
# site's pages, scripts and styles take it from the site's generated data.
set -euo pipefail
cd "$(dirname "$0")/.."

name=$(sed -n 's/^const Name = "\(.*\)"/\1/p' internal/brand/brand.go)
module=$(sed -n 's/^module //p' go.mod)
if [ -z "$name" ]; then
    echo "Error: cannot read the name from internal/brand/brand.go." >&2
    echo "Try: use a complete source tree, then run the check again." >&2
    exit 2
fi

files=$(mktemp)
trap 'rm -f "$files"' EXIT
bash scripts/source-files.sh -- '*.go' 'justfile' '.goreleaser.yaml' '.github/workflows/*.yml' 'scripts/*.sh' 'install.sh' 'test/e2e/*.py' 'vt/*.zig' 'vt/src/*.zig' 'vt/build.zig.zon' 'internal/*/testdata/*/*.py' 'site/astro.config.mjs' 'site/package.json' 'site/src/*' 'scripts/*.py' >"$files"
fail=0
checked=0
while IFS= read -r -d '' file; do
    case "$file" in
        internal/brand/* | scripts/check-name.sh | scripts/rename.sh) continue ;;
    esac
    checked=$((checked + 1))
    # Drop the module path, then look for the name in any case.
    hits=$(sed "s#${module}##g" "./$file" | grep -n -i -- "$name" || true)
    if [ -n "$hits" ]; then
        echo "$file:"
        echo "$hits" | sed 's/^/  /'
        fail=1
    fi
done <"$files"
if [ "$checked" -eq 0 ]; then
    echo "Error: the name check selected no source files." >&2
    echo "Try: use a complete source tree, then run the check again." >&2
    exit 2
fi

if [ "$fail" -ne 0 ]; then
    echo
    echo "Error: the product name is spelled outside internal/brand."
    echo "Try: use brand.Name, brand.HostBinary or brand.Env(...) instead (see docs/contributing/renaming.md)."
    exit 1
fi
echo "check-name: ok (name \"$name\" appears only in internal/brand)"
