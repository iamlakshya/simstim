---
status: proposed
date: 2026-11-28
---

# 0058 Build the gVisor wall on pinned upstream runsc without patches

## Context

The gVisor wall (level `gvisor`, W2, isolation spec 21.4) puts gVisor's user-space kernel (the Sentry) between a session and the host kernel for hosts without KVM. Earlier drafts of the plan considered carrying a custom `runsc` patch, building gVisor from source with Bazel, and reusing bubblewrap's Ubuntu AppArmor profile (`bwrap-userns-restrict`) to launch `runsc`. Under [decision 0053](0053-rebuild-with-open-source-parts.md), we prefer unmodified, pinned open-source release binaries whenever they meet the requirement.

Step 1 verification against upstream gVisor `release-20261123.1` established three concrete facts:

1. Upstream releases ship as `gvisor.tar.bz2` containing `runsc` alongside required sidecars under `gvisor-bin/` (`gvisor_sentry`, `gvisor-sentry-prewarmer`, `gvisor-proc-umounter` and `runsc-fd-parking`). `runsc` checks sidecar release matching and refuses to boot without `gvisor-bin/`.
2. Single-entry OCI user-namespace mappings (`hostID: <login uid> -> containerID: 1000, size: 1`) work without `--rootless`, without `newuidmap`, and without an outer `unshare` or `bwrap` wrapper. Host files owned by the login user appear owned by uid 1000 (`agent`) inside the Sentry, while `runsc` runs with the exact spec 21.4 command line: `--root <SessionDir>/runsc --network=none --directfs=false --ignore-cgroups --host-uds=none run --bundle <SessionDir>/bundle --pass-fd 3:3 <id>`.
3. `runsc` spawns its gofer and Sentry in new user namespaces with `AmbientCaps` (`CAP_SYS_ADMIN`, `CAP_SYS_CHROOT`, `CAP_SETPCAP` and `CAP_SYS_PTRACE`). Ubuntu's `bwrap-userns-restrict` profile transitions children into `unpriv_bwrap`, which enforces `audit deny capability,`, so launching `runsc` under `bwrap` on Ubuntu 24.04 or later fails with `EPERM`.

## Options

1. Maintain a patched gVisor fork and Bazel build pipeline.
2. Adopt unmodified upstream `runsc` releases (`release-20261123.1`), pinned by SHA-256 for both the `gvisor.tar.bz2` archive and every extracted binary, using single-entry OCI user-namespace mode, OCI seccomp defence in depth, and a dedicated AppArmor 4.0 `userns,` profile (`/etc/apparmor.d/simstim-runsc`) on Ubuntu hosts where `kernel.apparmor_restrict_unprivileged_userns = 1`.

## Decision

Option 2. `internal/wall/gvisor` pins upstream `google/gvisor` `release-20261123.1` (`81f68cb7d4661a9313d891a1758a765abe8eb27e`) in `guest/supply.lock` and verifies SHA-256 digests for `amd64` and `arm64` archives and binaries (`runsc` plus the four required `gvisor-bin/` sidecars). The driver writes an OCI 1.0.2 bundle with no hooks, read-only rootfs and system mounts, filtered `/etc`, fresh mode-0700 tmpfs `/home/agent` with allowlisted `StateDirs`, writable `/work/<name>`, zero capabilities, `noNewPrivileges: true`, single-entry UID/GID mapping (`host -> 1000`), and an OCI seccomp profile blocking `unshare`, `mount`, `umount2`, `bpf`, `io_uring_*`, `clone3`, `AF_VSOCK` and `NETLINK_AUDIT`.

The supervisor launches `runsc` directly with `--network=none --directfs=false --ignore-cgroups --host-uds=none run --bundle <SessionDir>/bundle --pass-fd 3:3 <id>`, handing `wall-init` the `internal/link` socketpair on fd 3 so all outbound HTTP and HTTPS traffic routes through the host `egress` process with zero listening ports. On Debian 12 and 13 no root step is required. On Ubuntu 24.04 or later with `kernel.apparmor_restrict_unprivileged_userns = 1`, the probe prints one `sudo` command to install and load `/etc/apparmor.d/simstim-runsc` scoped to the pinned static `runsc` binary.

## Consequences

- Good, because we carry zero gVisor patches, zero Bazel builds, and zero custody daemons, while getting upstream security fixes by bumping one pin in `guest/supply.lock` and `internal/wall/gvisor/pins.go`.
- Good, because single-entry OCI user-namespace mapping needs neither `--rootless` nor `newuidmap` (`uidmap` is absent on minimal cloud images), and preserves clean host file ownership on `/work` and `StateDirs`.
- Good, because the gVisor wall reuses the same supervisor, `wall-init`, `egress` proxy and agent mount layout as the process wall.
- Bad, because Ubuntu 24.04+ hosts with AppArmor user-namespace restriction need a dedicated one-time `sudo` profile for the pinned `runsc` path in addition to `bwrap-userns-restrict`, and the upstream runtime occupies roughly 100 MiB extracted (`runsc` plus `gvisor-bin/`).
