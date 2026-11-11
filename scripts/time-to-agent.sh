#!/usr/bin/env bash
# Measures the time from a clean machine to an agent running on a host you
# can already reach with ssh (goal item D12): install the client with
# install.sh, connect the host, and start an agent. It uses a local snapshot
# release (signed with a throwaway key) and a throwaway home for the client's
# config, cache and state, so nothing of yours is used or changed locally.
#
#   scripts/time-to-agent.sh <ssh target> [ssh config] [agent]
#
# On the host it installs the snapshot's helper next to any others, starts
# one session and kills it again. Needs goreleaser and Zig 0.17.
set -euo pipefail
cd "$(dirname "$0")/.."

target=${1:?usage: time-to-agent.sh <ssh target> [ssh config] [agent]}
sshcfg=${2:-}
agent=${3:-claude}
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

echo "time-to-agent: building a snapshot release (not timed)"
goreleaser release --snapshot --clean --skip=publish,sign,sbom >"$work/build.log" 2>&1 ||
    { cat "$work/build.log" >&2; exit 1; }
version=$(sed -n 's/.*"version":"\([^"]*\)".*/\1/p' dist/metadata.json)
ssh-keygen -q -t ed25519 -N '' -C test -f "$work/key"
signers="release namespaces=\"file\" $(cut -d' ' -f1,2 "$work/key.pub")"
mkdir "$work/www"
cp dist/*.tar.gz dist/checksums.txt "$work/www/"
ssh-keygen -q -Y sign -n file -f "$work/key" "$work/www/checksums.txt"
port=$((20000 + RANDOM % 20000))
(cd "$work/www" && exec python3 -m http.server "$port" --bind 127.0.0.1 >/dev/null 2>&1) &
server=$!
for _ in $(seq 50); do curl -fs "http://127.0.0.1:$port/checksums.txt" >/dev/null && break; sleep 0.1; done

# A clean client: its own config, cache, state and install folder.
export XDG_CONFIG_HOME="$work/config" XDG_CACHE_HOME="$work/cache" XDG_STATE_HOME="$work/state"
bin="$work/bin/$APP_NAME"
now() { python3 -c 'import time; print(time.monotonic())'; }
since() { python3 -c "print(f'{$(now) - $1:.1f}')"; }

t0=$(now)
t=$t0
INSTALL_DIR="$work/bin" VERSION="v$version" BASE_URL="http://127.0.0.1:$port" SIGNERS="$signers" sh install.sh >"$work/install.log" 2>&1 ||
    { cat "$work/install.log" >&2; exit 1; }
install_s=$(since "$t")

t=$(now)
add=("$bin" host add tta "$target" --yes)
[ -n "$sshcfg" ] && add+=(--ssh-config "$sshcfg")
"${add[@]}" >"$work/add.log" 2>&1 || { cat "$work/add.log" >&2; exit 1; }
add_s=$(since "$t")

t=$(now)
"$bin" new tta --agent "$agent" --name time-to-agent --detach >"$work/new.log" 2>&1 || { cat "$work/new.log" >&2; exit 1; }
state=""
for _ in $(seq 300); do
    state=$("$bin" ls --json 2>/dev/null | python3 -c '
import json, sys
for h in json.load(sys.stdin):
    for s in h.get("sessions") or []:
        if s["name"] == "time-to-agent" and s.get("alive"):
            print(s.get("state", ""))
' || true)
    case "$state" in working | needs_input | idle) break ;; esac
    sleep 0.1
done
agent_s=$(since "$t")
total_s=$(since "$t0")
"$bin" kill tta/time-to-agent >/dev/null 2>&1 || true

printf 'time-to-agent: %s %s, %s\n' "$APP_NAME" "$version" "$(uname -sm)"
printf '  install (install.sh, signature and checksum)  %6ss\n' "$install_s"
printf '  host add (probe, helper install, save)        %6ss\n' "$add_s"
printf '  new --agent %-8s (until the agent is %s)   %6ss\n' "$agent" "${state:-not seen}" "$agent_s"
printf '  total machine time                            %6ss\n' "$total_s"
printf 'The helper %s stays on the host, next to any others.\n' "$version"
