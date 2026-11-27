#!/bin/sh
# Installs the client from the latest GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/<owner>/<repo>/main/install.sh | sh
#
# It checks the signature of the release's checksums with ssh-keygen (part of
# OpenSSH, which the tool needs anyway), then the archive's SHA-256.
#
# Options (environment variables):
#   INSTALL_DIR   where to put the binary (default: ~/.local/bin)
#   VERSION       a release tag, for example v0.1.0 (default: the latest)
#   GITHUB_TOKEN  needed only while the repository is private
#   BASE_URL      a mirror of the release files, used instead of GitHub
#   SIGNERS       allowed-signers lines to trust instead of the release keys (tests)
set -eu

MODULE="github.com/iamlakshya/simstim"
REPO="${MODULE#github.com/}"
NAME="${REPO##*/}"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
# The release keys, one allowed-signers line each, the same as
# .github/release-signers. A signature by any of them passes. During a key
# rotation the new key is listed beside the old one, until a release that the
# new key signed ships (docs/contributing/releasing.md).
RELEASE_SIGNERS='release namespaces="file" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGyv3BRbYeIpUvqwia5q4CuqngP+aUzouWZ8x73U2LOy'
SIGNERS="${SIGNERS:-$RELEASE_SIGNERS}"

say() { printf '%s\n' "$*" >&2; }
die() { say "Error: $*"; exit 1; }

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in darwin|linux) ;; *) die "this installer supports macOS and Linux, not $os" ;; esac
arch=$(uname -m)
case "$arch" in x86_64|amd64) arch=amd64 ;; arm64|aarch64) arch=arm64 ;; *) die "unsupported architecture $arch" ;; esac

command -v ssh-keygen >/dev/null 2>&1 || die "ssh-keygen not found: install OpenSSH first, which the tool needs too"

auth=""
[ -n "${GITHUB_TOKEN:-}" ] && auth="Authorization: Bearer $GITHUB_TOKEN"
get() { curl -fsSL ${auth:+-H "$auth"} "$@"; }

if [ -z "${VERSION:-}" ] && [ -n "${BASE_URL:-}" ]; then
  die "set VERSION too when BASE_URL is set"
fi
if [ -z "${VERSION:-}" ]; then
  VERSION=$(get "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
  [ -n "$VERSION" ] || die "could not find the latest release (is the repository private? set GITHUB_TOKEN)"
fi
ver="${VERSION#v}"
archive="${NAME}_${ver}_${os}_${arch}.tar.gz"
base="${BASE_URL:-https://github.com/$REPO/releases/download/$VERSION}"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
say "Downloading ${NAME} ${VERSION} for ${os}/${arch}…"
get -o "$tmp/$archive" "$base/$archive" || die "download failed: $base/$archive"
get -o "$tmp/checksums.txt" "$base/checksums.txt" || die "could not download checksums.txt"
get -o "$tmp/checksums.txt.sig" "$base/checksums.txt.sig" || die "could not download checksums.txt.sig, the release's signature"

# The signature proves the checksums come from the release job; the checksum
# then proves the archive is the one it built.
printf '%s\n' "$SIGNERS" >"$tmp/signers"
ssh-keygen -Y verify -f "$tmp/signers" -I release -n file -s "$tmp/checksums.txt.sig" <"$tmp/checksums.txt" >/dev/null 2>&1 ||
  die "the signature of checksums.txt does not match the release key, so nothing was installed"

want=$(grep " $archive\$" "$tmp/checksums.txt" | cut -d' ' -f1)
[ -n "$want" ] || die "no checksum for $archive"
if command -v sha256sum >/dev/null 2>&1; then got=$(sha256sum "$tmp/$archive" | cut -d' ' -f1)
else got=$(shasum -a 256 "$tmp/$archive" | cut -d' ' -f1); fi
[ "$want" = "$got" ] || die "checksum mismatch for $archive, so nothing was installed"

tar -xzf "$tmp/$archive" -C "$tmp" "$NAME"
mkdir -p "$INSTALL_DIR"
mv -f "$tmp/$NAME" "$INSTALL_DIR/$NAME"
chmod 755 "$INSTALL_DIR/$NAME"
say "Installed $INSTALL_DIR/$NAME ($VERSION): signature and checksum verified."
case ":$PATH:" in *":$INSTALL_DIR:"*) ;; *) say "Add $INSTALL_DIR to your PATH to run it." ;; esac
