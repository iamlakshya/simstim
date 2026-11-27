#!/usr/bin/env sh
# One complete source-owned tag list. GOFLAGS and caller metadata cannot
# replace either the real carried-code cohort or the legacy HTTP/2 server.
set -eu
main_tags=userns_cohort_v3,http2legacy
case "${1:-main}" in
    main) printf '%s\n' "$main_tags" ;;
    bootstrap-policy)
        # This pure policy emitter precedes the actual C launcher cohort.
        # It does not issue a carried-code owner or build a host helper.
        printf '%s\n' http2legacy ;;
    toolchain)
        if ! pinned_go=$(sed -n 's/^toolchain //p' go.mod) || [ "$pinned_go" != go1.27.4 ]; then
            printf 'Error: the source has no qualified exact Go toolchain.\nTry: use the complete checkout with its Go1.27.4 pin.\n' >&2
            exit 1
        fi
        if [ "${GOTOOLCHAIN:-}" = local ]; then
            case "$(GOTOOLCHAIN=local go version)" in
                "go version $pinned_go "*) printf '%s\n' local ;;
                *)
                    printf 'Error: the local compiler differs from the exact source pin.\nTry: select the verified Go1.27.4 SDK before using GOTOOLCHAIN=local.\n' >&2
                    exit 1 ;;
            esac
        else
            printf '%s\n' "$pinned_go"
        fi ;;
    check)
        if [ "$#" -ne 2 ] || [ "$2" != "$main_tags" ]; then
            printf 'Error: the build must retain the complete source tag list.\nTry: use scripts/go-build-tags.sh for this checkout.\n' >&2
            exit 1
        fi ;;
    *)
        printf 'Error: unknown build tag purpose.\nTry: use main, bootstrap-policy, toolchain or check.\n' >&2
        exit 1 ;;
esac
