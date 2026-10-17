#!/usr/bin/env bash
# Sets up a machine to work on this repo, in one command:
#
#   scripts/setup.sh      (or `just setup` once just is installed)
#
# It installs the pinned tools in scripts/tools.lock into .tools/ (git-ignored),
# checking each download against its SHA-256 (or, for a "go" row, building
# it with go install, which checks the module's sum), and links them into
# .tools/bin.
# The justfile puts .tools/bin first on its PATH. Go itself comes from go.mod:
# any Go 1.21 or newer downloads the pinned toolchain on first use.
#
#   scripts/setup.sh --only node,typos   only these tools, and no build (CI)
set -euo pipefail
cd "$(dirname "$0")/.."

only=""
if [ "${1:-}" = "--only" ]; then
    only=",${2:?usage: setup.sh --only tool,tool},"
fi
wanted() { [ -z "$only" ] || [[ "$only" == *",$1,"* ]]; }

say() { printf '%s\n' "$*" >&2; }
die() { say "Error: $*"; exit 1; }

case "$(uname -s)" in Darwin) os=darwin ;; Linux) os=linux ;; *) die "setup supports macOS and Linux" ;; esac
case "$(uname -m)" in x86_64 | amd64) arch=amd64 ;; arm64 | aarch64) arch=arm64 ;; *) die "unsupported CPU $(uname -m)" ;; esac
platform="${os}-${arch}"

sha256() {
    if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

command -v go >/dev/null 2>&1 || die "Go is not installed. Try: install Go from https://go.dev/dl, then run this again."
command -v git >/dev/null 2>&1 || die "git is not installed."
command -v ssh >/dev/null 2>&1 || die "OpenSSH is not installed; the tool and its tests need it."

tools=.tools
mkdir -p "$tools/bin" "$tools/dl"

while read -r name version plat sum url; do
    case "$name" in '' | '#'*) continue ;; esac
    wanted "$name" || continue
    if [ "$plat" = "go" ]; then
        # Built from source at a pinned version; Go checks the module's sum.
        dir="$tools/$name-$version"
        if [ ! -x "$dir/$name" ]; then
            say "Installing $name $version (go install)…"
            # A tool may need a newer Go than this repo; let go fetch it.
            GOTOOLCHAIN=auto GOBIN="$PWD/$dir" go install "$url" || die "could not go install $url"
        fi
        ln -sf "$PWD/$dir/$name" "$tools/bin/$name"
        continue
    fi
    [ "$plat" = "$platform" ] || continue
    dir="$tools/$name-$version"
    if [ ! -d "$dir" ]; then
        file="$tools/dl/${url##*/}"
        say "Installing $name ${version}…"
        curl -fsSL -o "$file" "$url" || die "could not download $url"
        got=$(sha256 "$file")
        [ "$got" = "$sum" ] || { rm -f "$file"; die "checksum mismatch for $name $version: got $got"; }
        mkdir -p "$dir.tmp"
        tar -xf "$file" -C "$dir.tmp"
        rm -f "$file"
        mv "$dir.tmp" "$dir"
    fi
    bin=$(find "$dir" -type f -name "$name" -perm -u+x | head -1)
    [ -n "$bin" ] || die "no $name binary in $dir"
    ln -sf "$(cd "$(dirname "$bin")" && pwd)/$name" "$tools/bin/$name"
    if [ "$name" = node ]; then
        # npx runs the Markdown lint (scripts/docs-check.sh).
        for extra in npm npx; do
            [ -e "$(dirname "$bin")/$extra" ] && ln -sf "$(cd "$(dirname "$bin")" && pwd)/$extra" "$tools/bin/$extra"
        done
    fi
done <scripts/tools.lock

export PATH="$PWD/$tools/bin:$PATH"
if [ -n "$only" ]; then
    [ -n "${GITHUB_PATH:-}" ] && echo "$PWD/$tools/bin" >>"$GITHUB_PATH"
    say "Installed: ${only//,/ } into $tools/bin"
    exit 0
fi
say ""
say "Tools in $tools/bin:"
for t in just golangci-lint goreleaser gitleaks zig node pnpm typos vale lychee; do
    printf '  %-14s %s\n' "$t" "$("$t" --version 2>/dev/null | head -1 || "$t" version 2>/dev/null | head -1)" >&2
done
say "  go             $(go version | cut -d' ' -f3) (the toolchain go.mod asks for downloads on first use)"
command -v python3 >/dev/null 2>&1 || say "Note: python3 is missing; it runs the readiness regression tests (just test) and end-to-end tests (just e2e)."

say ""
say "Building and testing once, to check the setup…"
just build
log=$(mktemp)
if ! go test -count=1 ./... >"$log" 2>&1; then
    say ""
    grep -E -- '--- FAIL|^FAIL|panic:' "$log" >&2 || tail -40 "$log" >&2
    rm -f "$log"
    die "a test failed; the lines above say which. Run it again with go test -run <name> -v."
fi
rm -f "$log"
say ""
say "Ready. Run \`just\` to see the commands. To use the tools outside just:"
say "  export PATH=\"$PWD/$tools/bin:\$PATH\""
