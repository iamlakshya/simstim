#!/usr/bin/env bash
# Download pinned VMM inputs and mirror their raw runtime files. Never run a
# VMM or its jailer. The manifest writer repeats these checks before embedding.
set -euo pipefail
cd "$(dirname "$0")/.."

if [ "$#" -ne 1 ]; then
    echo "mirror-vmm: expected the release version." >&2
    echo "Try: scripts/mirror-vmm.sh <release>" >&2
    exit 1
fi

vmm_dir="${VMM_OUT_DIR:-build/vmm}"
mkdir -p "$vmm_dir/inputs"
pin() { go run ./cmd/supply pin get "$1"; }

for vmm_name in $(pin vmm.names); do
    for vmm_target in amd64 arm64 source; do
        vmm_key="vmm.${vmm_name}.${vmm_target}"
        vmm_input="$vmm_dir/inputs/$(pin "$vmm_key.input")"
        if [ ! -f "$vmm_input" ]; then
            vmm_url=$(pin "$vmm_key.url")
            vmm_size=$(pin "$vmm_key.size")
            if ! curl --fail --silent --show-error --location --retry 3 \
                --proto '=https' --proto-redir '=https' --max-filesize "$vmm_size" \
                --output "$vmm_input.part" "$vmm_url"; then
                echo "mirror-vmm: cannot download the pinned $vmm_name $vmm_target input." >&2
                echo "Try: check network access and run scripts/mirror-vmm.sh again." >&2
                exit 1
            fi
            mv "$vmm_input.part" "$vmm_input"
        fi
        go run ./cmd/supply pin verify "vmm:${vmm_name}:${vmm_target}" "$vmm_input"
    done
done

go run ./cmd/supply mirror-vmm -release "$1" -dir "$vmm_dir"
