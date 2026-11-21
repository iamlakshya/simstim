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

# The repeatable wall path uses the existing marker-authenticated E2E runner.
# It assumes the client, static guest fixture and cached image are prepared.
# It never creates a host, changes a root prerequisite or grows density until
# a start fails. The legacy clean-install measurement remains below.
if [ "${1:-}" = "--host" ]; then
    shift
    E2E_HOST=${1:?usage: time-to-agent.sh --host <saved-host> --client <binary> --probe <linux-fixture> --wall <wall> --substrate <bare-metal|nested> --output <new-directory> --revision <sha>}
    shift
    E2E_WALL=microvm
    E2E_RUNS=20
    measured_client=""
    while [ "$#" -gt 0 ]; do
        case "$1" in
            --client) measured_client=${2:?missing client}; shift 2 ;;
            --probe) E2E_PROBE=${2:?missing Linux probe}; shift 2 ;;
            --link-bench) E2E_LINK_BENCH=${2:?missing Linux link benchmark}; shift 2 ;;
            --supervisor-bench) E2E_SUPERVISOR_BENCH=${2:?missing Linux supervisor benchmark}; shift 2 ;;
            --wall) E2E_WALL=${2:?missing wall}; shift 2 ;;
            --substrate) E2E_SUBSTRATE=${2:?missing substrate}; shift 2 ;;
            --output) E2E_EVIDENCE_DIR=${2:?missing evidence directory}; shift 2 ;;
            --revision) E2E_REVISION=${2:?missing built revision}; shift 2 ;;
            --runs) E2E_RUNS=${2:?missing run count}; shift 2 ;;
            --stop-first) shift ;; # Every measured iteration exercises stop/wake.
            --shell) export E2E_MEASURE_SHELL=1; shift ;;
            *) echo "time-to-agent: unknown wall measurement argument: $1. Try: use the documented --host measurement form." >&2; exit 2 ;;
        esac
    done
    : "${measured_client:?set --client}" "${E2E_PROBE:?set --probe}" "${E2E_LINK_BENCH:?set --link-bench}" "${E2E_SUPERVISOR_BENCH:?set --supervisor-bench}" "${E2E_SUBSTRATE:?set --substrate}" "${E2E_EVIDENCE_DIR:?set --output}" "${E2E_REVISION:?set --revision}"
    [ "$E2E_RUNS" = 20 ] || { echo 'time-to-agent: the release gate requires 20 runs. Try: omit --runs or use --runs 20.' >&2; exit 2; }
    export E2E_HOST E2E_WALL E2E_RUNS E2E_PROBE E2E_LINK_BENCH E2E_SUPERVISOR_BENCH E2E_SUBSTRATE E2E_EVIDENCE_DIR E2E_REVISION
    export E2E_ONLY=microvm-measure
    exec python3 test/e2e/run.py "$measured_client"
fi

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
