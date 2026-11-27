#!/usr/bin/env bash
# Only the existing protected signed-tag workflow calls this publication step.
set -euo pipefail
umask 077
cd "$(dirname "$0")/../.."
go_build_toolchain=$(sh scripts/go-build-tags.sh toolchain)
export GOTOOLCHAIN="$go_build_toolchain"
go_build_tags=$(sh scripts/go-build-tags.sh)
export GO_BUILD_TAGS="$go_build_tags"
if [ "$#" -ne 2 ] || { [ "$1" != amd64 ] && [ "$1" != arm64 ]; } ||
    [ "${GITHUB_ACTIONS:-}" != true ] || [ "${GITHUB_REF_TYPE:-}" != tag ] ||
    [ "${GITHUB_REF_NAME:-}" != "$2" ]; then
    echo 'default-image: publication belongs to the protected signed-tag job. Try: prepare artifacts locally and use the normal release gate.' >&2
    exit 1
fi
image_arch=$1
release_tag=$2
case "$release_tag" in v[0-9]* ) ;; *) echo 'default-image: invalid release tag. Try: use the normal signed release workflow.' >&2; exit 1 ;; esac
case "$release_tag" in *[!a-zA-Z0-9._+-]* ) echo 'default-image: unsupported release tag spelling. Try: use a version tag.' >&2; exit 1 ;; esac
out_dir=${DEFAULT_IMAGE_OUT_DIR:-build/default-image}
out_dir=$(cd "$out_dir" && pwd -P)
work=$(mktemp -d "$out_dir/.publish-$image_arch.XXXXXXXX")
if [ -z "${DOCKER_HOST:-}" ]; then DOCKER_HOST=$(docker context inspect --format '{{.Endpoints.docker.Host}}'); fi
case "$DOCKER_HOST" in unix://*) export DOCKER_HOST ;; *)
    echo 'default-image: publication needs the approved local Docker endpoint. Try: use the protected release runner.' >&2
    exit 1 ;; esac
unset DOCKER_CONTEXT
go build -tags="$go_build_tags" -trimpath -o "$work/supply" ./cmd/supply
pin() { "$work/supply" pin get "$1"; }
repository=$("$work/supply" default-image repository)
"$work/supply" default-image check --dir "$out_dir/$image_arch" >"$work/checked.json"
python3 - "$work" <<'PY'
import base64,json,os,pathlib,sys
work=pathlib.Path(sys.argv[1]);checked=json.loads((work/'checked.json').read_text())
(work/'expected').write_text(checked['digest']+'\n')
token=os.environ['GITHUB_TOKEN'];actor=os.environ['GITHUB_ACTOR']
(work/'auth.json').write_text(json.dumps({'auths':{'ghcr.io':{'auth':base64.b64encode((actor+':'+token).encode()).decode()}}}))
PY
trap 'rm -f "$work/auth.json"' EXIT
base_image=$(pin builder.image)
# Debian's signed, hash-bound snapshot pins this build-only copy tool too.
# It is never installed in the guest image. No registry credential is an argv.
docker run --rm --platform "linux/$image_arch" \
    --mount "type=bind,src=$out_dir/$image_arch,dst=/image,readonly" \
    --mount "type=bind,src=$work,dst=/publication" \
    --env "BUILD_SNAPSHOT=$(pin builder.snapshot)" --env "BUILD_SUITE=$(pin builder.suite)" \
    --env "BUILD_INRELEASE=$(pin builder.inrelease_sha256)" \
    --env "IMAGE_ARCH=$image_arch" --env "IMAGE_REPOSITORY=$repository" --env "IMAGE_TAG=$release_tag-$image_arch" \
    "$base_image" /bin/bash -euo pipefail -c '
rm -f /etc/apt/sources.list /etc/apt/sources.list.d/debian.sources
printf "deb [check-valid-until=no] http://snapshot.debian.org/archive/debian/%s/ %s main\n" "$BUILD_SNAPSHOT" "$BUILD_SUITE" >/etc/apt/sources.list
apt-get -o Acquire::Check-Valid-Until=false -o Acquire::Languages=none update
mapfile -t metadata < <(find /var/lib/apt/lists -maxdepth 1 -type f -name "*_InRelease")
[ "${#metadata[@]}" -eq 1 ]
printf "%s  %s\n" "$BUILD_INRELEASE" "${metadata[0]}" | sha256sum --check --status
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends skopeo ca-certificates
skopeo copy --preserve-digests --digestfile /publication/result \
    --src-no-creds --dest-authfile /publication/auth.json \
    "oci:/image:$IMAGE_ARCH" "docker://$IMAGE_REPOSITORY:$IMAGE_TAG"
[ "$(cat /publication/result)" = "$(cat /publication/expected)" ]
skopeo inspect --raw --authfile /publication/auth.json \
    "docker://$IMAGE_REPOSITORY@$(cat /publication/expected)" >/publication/manifest
readback=$(sha256sum /publication/manifest)
[ "${readback%% *}" = "$(sed "s/^sha256://" /publication/expected)" ]
'
"$work/supply" default-image published --dir "$out_dir/$image_arch"
