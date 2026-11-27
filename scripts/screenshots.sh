#!/usr/bin/env bash
# Makes the README's screenshots and recorded demos from the demo: sample
# hosts and sessions, nothing connects anywhere.
# Usage: scripts/screenshots.sh path/to/client
#
# Stills come from the shot list below. Demos come from the tapes in
# docs/demos/*.tape: each is a ttyshot script that records an animated SVG and
# an asciicast into docs/images, with motion on.
#
# ttyshot runs the client in a virtual terminal and draws each screen as an SVG
# terminal window, cell by cell. SVG keeps the text sharp at any size.
set -euo pipefail
cd "$(dirname "$0")/.."
go_build_toolchain=$(sh scripts/go-build-tags.sh toolchain)
export GOTOOLCHAIN="$go_build_toolchain"
go_build_tags=$(sh scripts/go-build-tags.sh)
export GO_BUILD_TAGS="$go_build_tags"

cli=$(cd "$(dirname "${1:?usage: scripts/screenshots.sh path/to/client}")" && pwd)/$(basename "$1")
name=$(sed -n 's/^const Name = "\(.*\)"/\1/p' internal/brand/brand.go)
out=docs/images

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
go build -tags="$go_build_tags" -o "$tmp/ttyshot" ./test/ttyshot
# A fresh config with motion off: the spinner stands still, and the user's own
# settings never show in an image.
mkdir -p "$tmp/config/$name" "$out"
printf '[ui]\nmotion = "off"\n' >"$tmp/config/$name/config.toml"

down='\x1b[B'
# shot <image> <key>...: open the demo, select the host "homelab", press the
# keys, and save the screen.
shot() {
    local image=$1
    shift
    {
        echo 'wait "homelab" 10s'
        printf 'send "%s"\nsleep 300ms\nsend "%s"\nsleep 1s\n' "$down" "$down"
        for k in "$@"; do printf 'send "%s"\nsleep 1s\n' "$k"; done
        echo "save $out/$image.svg"
    } | XDG_CONFIG_HOME="$tmp/config" TERM=xterm-ghostty COLORTERM=truecolor \
        "$tmp/ttyshot" -cols 110 -rows 30 -- "$cli" demo >/dev/null
    echo "$out/$image.svg"
}

shot hosts
shot sessions '\r'
shot new-session '\r' 'n' 'claude ~/src/a'
shot search '/' 'te'
shot commands '\r' '\x0f'
shot attached '\r' '\r'
shot add-host 'a'

# The recorded demos, with motion on, as people see it.
mkdir -p "$tmp/motion/$name"
printf '[ui]\nmotion = "on"\n' >"$tmp/motion/$name/config.toml"
for tape in docs/demos/*.tape; do
    XDG_CONFIG_HOME="$tmp/motion" TERM=xterm-ghostty COLORTERM=truecolor \
        "$tmp/ttyshot" -cols 110 -rows 30 -script "$tape" -- "$cli" demo >/dev/null
    echo "$tape"
done
