#!/usr/bin/env bash
# Runs only in the fresh task-owned pinned Debian preparation container.
set -euo pipefail
if [ "$(uname -s)" != Linux ] || [ ! -f /.dockerenv ] ||
    [ ! -r /run/default-image-input/authority ] ||
    [ "$(cat /run/default-image-input/authority)" != "${DEFAULT_BUILD_AUTHORITY:-}" ]; then
    echo 'default-image: no original isolated build workload. Try: guest/base-image/build.sh <arch>.' >&2
    exit 1
fi
actual_arch=$(dpkg --print-architecture)
if [ "$actual_arch" != "$DEFAULT_IMAGE_ARCH" ]; then
    echo 'default-image: the root architecture differs from its selected build. Try: use a matching approved native runner.' >&2
    exit 1
fi
rm -f /etc/apt/sources.list /etc/apt/sources.list.d/debian.sources
printf 'deb [check-valid-until=no] http://snapshot.debian.org/archive/debian/%s/ %s main\n' \
    "$DEFAULT_IMAGE_SNAPSHOT" "$DEFAULT_IMAGE_SUITE" >/etc/apt/sources.list
printf '#!/bin/sh\nexit 101\n' >/usr/sbin/policy-rc.d
chmod 0755 /usr/sbin/policy-rc.d
apt-get -o Acquire::Check-Valid-Until=false -o Acquire::Languages=none update
mapfile -t metadata < <(find /var/lib/apt/lists -maxdepth 1 -type f -name '*_InRelease')
if [ "${#metadata[@]}" -ne 1 ]; then
    echo 'default-image: expected one signed snapshot. Try: check guest/supply.lock.' >&2
    exit 1
fi
printf '%s  %s\n' "$DEFAULT_IMAGE_INRELEASE_SHA256" "${metadata[0]}" | sha256sum --check --status
mapfile -t packages </run/default-image-input/packages
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "${packages[@]}"
if getent passwd "$DEFAULT_IMAGE_UID" >/dev/null || getent group "$DEFAULT_IMAGE_GID" >/dev/null; then
    echo 'default-image: fixed account is occupied. Try: inspect the pinned Debian base instead of replacing its account.' >&2
    exit 1
fi
groupadd --gid "$DEFAULT_IMAGE_GID" "$DEFAULT_IMAGE_USER"
useradd --uid "$DEFAULT_IMAGE_UID" --gid "$DEFAULT_IMAGE_GID" --no-user-group \
    --home-dir "$DEFAULT_IMAGE_HOME" --create-home --shell "$DEFAULT_IMAGE_SHELL" \
    --no-log-init "$DEFAULT_IMAGE_USER"
chage --lastday 0 "$DEFAULT_IMAGE_USER"
dpkg-query -W -f='${Package}\t${Version}\t${Architecture}\n' "${packages[@]}" | LC_ALL=C sort >/out/packages.tsv
apt-get clean
rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/* /var/log/*
rm -f /usr/sbin/policy-rc.d
for name in /etc/machine-id /var/lib/dbus/machine-id; do
    if [ -f "$name" ]; then : >"$name"; fi
done
# Content timestamps are canonicalised by the native OCI producer too. This
# avoids needless dpkg time variation without touching Docker's bound files.
find / -xdev \( -path /proc -o -path /sys -o -path /dev -o -path /run -o \
    -path /tmp -o -path /out -o -path /etc/hostname -o -path /etc/hosts -o \
    -path /etc/resolv.conf \) -prune -o -exec touch -h -d "@$SOURCE_DATE_EPOCH" {} +
