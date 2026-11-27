#!/usr/bin/env bash
# Two real native builds from separate checkouts and empty isolated state.
set -euo pipefail
umask 077
cd "$(dirname "$0")/.."
if [ "$#" -ne 2 ] || { [ "$1" != amd64 ] && [ "$1" != arm64 ]; } || [ -e "$2" ]; then
    echo 'gvisor repro: native architecture and a new absolute output root are required. Try: choose amd64 or arm64 and retain previous preparations.' >&2
    exit 1
fi
case "$2" in /*) ;; *)
    echo 'gvisor repro: the output root must be absolute. Try: choose a new native job directory.' >&2
    exit 1 ;; esac
native_arch=$(uname -m)
if [ "$(uname -s)" != Linux ] || { [ "$1/$native_arch" != amd64/x86_64 ] && [ "$1/$native_arch" != arm64/aarch64 ]; }; then
    echo 'gvisor repro: this is not the requested native Linux architecture. Try: use the matching native runner.' >&2
    exit 1
fi
repro_arch=$1
repro_root=$2
mkdir -p "$repro_root/bootstrap"
export HOME="$repro_root/bootstrap/home" USERPROFILE="$repro_root/bootstrap/home"
export XDG_CONFIG_HOME="$repro_root/bootstrap/xdg/config" XDG_CACHE_HOME="$repro_root/bootstrap/xdg/cache"
export XDG_DATA_HOME="$repro_root/bootstrap/xdg/data" XDG_STATE_HOME="$repro_root/bootstrap/xdg/state"
export XDG_RUNTIME_DIR="$repro_root/bootstrap/xdg/run" XDG_CONFIG_DIRS="$repro_root/bootstrap/xdg/config-dirs"
export XDG_DATA_DIRS="$repro_root/bootstrap/xdg/data-dirs"
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null
mkdir -p "$HOME" "$XDG_CONFIG_HOME" "$XDG_CACHE_HOME" "$XDG_DATA_HOME" "$XDG_STATE_HOME" \
    "$XDG_RUNTIME_DIR" "$XDG_CONFIG_DIRS" "$XDG_DATA_DIRS"
shasum -a 256 third_party/gvisor/build-tools.json third_party/gvisor/hostguard.patch \
    third_party/gvisor/hostguard-inputs.json internal/wall/gvisor/native_build.go \
    scripts/prepare-gvisor-build.py scripts/build-gvisor-runtime.py >"$repro_root/source-pins-before.txt"
python3 scripts/prepare-gvisor-build.py --output "$repro_root/tools" --cache "$repro_root/input-downloads"
export GVISOR_BAZEL="$repro_root/tools/bazel" GVISOR_TOOL_INPUTS="$repro_root/tools/tools.json"
export GVISOR_DISTDIR="$repro_root/tools/distdir"
upstream_commit=$(python3 -c 'import json; print(json.load(open("third_party/gvisor/build-tools.json"))["upstream_commit"])')
for pass in 1 2; do
    state="$repro_root/run-$pass"
    mkdir -p "$state"
    export HOME="$state/home" USERPROFILE="$state/home"
    export XDG_CONFIG_HOME="$state/xdg/config" XDG_CACHE_HOME="$state/xdg/cache"
    export XDG_DATA_HOME="$state/xdg/data" XDG_STATE_HOME="$state/xdg/state"
    export XDG_RUNTIME_DIR="$state/xdg/run" XDG_CONFIG_DIRS="$state/xdg/config-dirs"
    export XDG_DATA_DIRS="$state/xdg/data-dirs"
    mkdir -p "$HOME" "$XDG_CONFIG_HOME" "$XDG_CACHE_HOME" "$XDG_DATA_HOME" "$XDG_STATE_HOME" \
        "$XDG_RUNTIME_DIR" "$XDG_CONFIG_DIRS" "$XDG_DATA_DIRS"
    git -c core.hooksPath=/dev/null init "$state/source"
    git -C "$state/source" -c core.hooksPath=/dev/null remote add origin https://github.com/google/gvisor.git
    git -C "$state/source" -c core.hooksPath=/dev/null fetch --depth=1 origin "$upstream_commit"
    git -C "$state/source" -c core.hooksPath=/dev/null checkout --detach FETCH_HEAD
    python3 scripts/build-gvisor-runtime.py "$state/source" "$state/output" >"$state/build.log" 2>&1 ||
        { cat "$state/build.log" >&2; exit 1; }
done
shasum -a 256 third_party/gvisor/build-tools.json third_party/gvisor/hostguard.patch \
    third_party/gvisor/hostguard-inputs.json internal/wall/gvisor/native_build.go \
    scripts/prepare-gvisor-build.py scripts/build-gvisor-runtime.py >"$repro_root/source-pins-after.txt"
diff "$repro_root/source-pins-before.txt" "$repro_root/source-pins-after.txt"
diff -r "$repro_root/run-1/output" "$repro_root/run-2/output"
printf '%s\n' "gvisor repro: both actual $repro_arch closures and their observed provenance match; retained at $repro_root"
