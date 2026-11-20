#!/usr/bin/env bash
# Tests install.sh end to end against a local snapshot release: it must
# install a release whose signature and checksum match, and refuse one whose
# checksums, signature or archive were changed. Needs goreleaser and Zig 0.17.
set -euo pipefail
cd "$(dirname "$0")/.."

APP_NAME=$(sed -n 's/^const Name = "\(.*\)"/\1/p' internal/brand/brand.go)
MODULE=$(sed -n 's/^module //p' go.mod)
export APP_NAME MODULE

work=$(mktemp -d)
server=""
cleanup() {
    [ -n "$server" ] && kill "$server" 2>/dev/null || true
    rm -rf "$work"
}
trap cleanup EXIT

echo "test-install: building a snapshot release"
goreleaser release --snapshot --clean --skip=publish,sign,sbom >"$work/build.log" 2>&1 ||
    { cat "$work/build.log" >&2; exit 1; }
version=$(sed -n 's/.*"version":"\([^"]*\)".*/\1/p' dist/metadata.json)

# Two throwaway keys stand in for the release keys, as while a rotation lists
# the old key and the new one.
ssh-keygen -q -t ed25519 -N '' -C test -f "$work/key"
ssh-keygen -q -t ed25519 -N '' -C test2 -f "$work/key2"
signers="release namespaces=\"file\" $(cut -d' ' -f1,2 "$work/key.pub")
release namespaces=\"file\" $(cut -d' ' -f1,2 "$work/key2.pub")"
mkdir "$work/www"
cp dist/*.tar.gz dist/checksums.txt "$work/www/"
ssh-keygen -q -Y sign -n file -f "$work/key" "$work/www/checksums.txt"

port=$((20000 + RANDOM % 20000))
(cd "$work/www" && exec python3 -m http.server "$port" --bind 127.0.0.1 >/dev/null 2>&1) &
server=$!
for _ in $(seq 50); do curl -fs "http://127.0.0.1:$port/checksums.txt" >/dev/null && break; sleep 0.1; done

run() { # run <install dir> [extra env...]
    local dir=$1
    shift
    env "$@" INSTALL_DIR="$dir" VERSION="v$version" BASE_URL="http://127.0.0.1:$port" SIGNERS="$signers" sh install.sh
}

# 1. A good release installs, and the binary runs.
run "$work/ok" >"$work/ok.log" 2>&1 || { cat "$work/ok.log" >&2; exit 1; }
"$work/ok/$APP_NAME" version >/dev/null
grep -q "signature and checksum verified" "$work/ok.log"
echo "test-install: ok, a signed release installs and runs"

# 1b. A release signed by the other listed key installs too.
cp "$work/www/checksums.txt.sig" "$work/sig.first"
cp "$work/www/checksums.txt" "$work/c2" && ssh-keygen -q -Y sign -n file -f "$work/key2" "$work/c2"
cp "$work/c2.sig" "$work/www/checksums.txt.sig"
run "$work/ok2" >"$work/ok2.log" 2>&1 || { cat "$work/ok2.log" >&2; exit 1; }
"$work/ok2/$APP_NAME" version >/dev/null
echo "test-install: ok, a release signed by the second listed key installs"
cp "$work/sig.first" "$work/www/checksums.txt.sig"

expect_fail() { # expect_fail <what> <message>
    if run "$work/bad" >"$work/bad.log" 2>&1; then
        echo "test-install: FAIL, installed despite $1" >&2
        exit 1
    fi
    grep -q "$2" "$work/bad.log" || { echo "test-install: FAIL, wrong error for $1:" >&2; cat "$work/bad.log" >&2; exit 1; }
    [ ! -e "$work/bad/$APP_NAME" ] || { echo "test-install: FAIL, left a binary after $1" >&2; exit 1; }
    echo "test-install: ok, refused $1"
}

# 2. Changed checksums break the signature.
cp "$work/www/checksums.txt" "$work/checksums.good"
echo "0000000000000000000000000000000000000000000000000000000000000000  extra.tar.gz" >>"$work/www/checksums.txt"
expect_fail "changed checksums" "does not match the release key"
cp "$work/checksums.good" "$work/www/checksums.txt"

# 3. A signature by a third key, which is not listed, is refused.
cp "$work/www/checksums.txt.sig" "$work/sig.good"
ssh-keygen -q -t ed25519 -N '' -C other -f "$work/other"
cp "$work/www/checksums.txt" "$work/c" && ssh-keygen -q -Y sign -n file -f "$work/other" "$work/c"
cp "$work/c.sig" "$work/www/checksums.txt.sig"
expect_fail "a signature by another key" "does not match the release key"
cp "$work/sig.good" "$work/www/checksums.txt.sig"

# 4. A changed archive breaks the checksum.
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m); case "$arch" in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; esac
archive="$work/www/${APP_NAME}_${version}_${os}_${arch}.tar.gz"
printf 'x' >>"$archive"
expect_fail "a changed archive" "checksum mismatch"
echo "test-install: all checks passed"
