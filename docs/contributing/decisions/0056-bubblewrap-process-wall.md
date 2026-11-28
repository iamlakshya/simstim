---
status: proposed
date: 2026-11-28
---

# 0056 Build the process wall with bubblewrap, go-seccomp-bpf and go-landlock

## Context

The process wall (level `process`, W1) isolates an agent session on the host's Linux kernel with new namespaces, a private root file system, zero capabilities, seccomp and Landlock. Earlier sections of the isolation specification sketched a custom static C or Go namespace launcher (`cmd/userns`) to do the namespace setup, mounts, pivot and double user-namespace drop. Under [decision 0053](0053-rebuild-with-open-source-parts.md), we prefer maintained open-source tools over custom kernel plumbing whenever a well-known tool meets the requirement.

## Options

1. Build and maintain a custom static namespace launcher (`cmd/userns`) and a custom AppArmor wrapper.
2. Build the process wall directly on `bubblewrap` (`bwrap`), `github.com/elastic/go-seccomp-bpf` and `github.com/landlock-lsm/go-landlock`, passing the seccomp filter and synthetic `/etc` files over inherited file descriptors and applying Landlock inside `wall-init` before it reads from the link.

## Decision

Option 2. `bubblewrap` is widely packaged, audited and already ships Ubuntu's `bwrap-userns-restrict` AppArmor profile. The supervisor launches `bwrap` with unshared user, mount, PID, IPC, UTS, cgroup and network namespaces, uid and gid 1000, `--cap-drop ALL`, `--die-with-parent`, `--new-session`, `--disable-userns` (when supported), a filtered read-only `/etc` from `internal/wall/process/etc.toml` plus synthetic `/etc/passwd`, `/etc/group`, `/etc/hosts` and `/etc/resolv.conf` via `--ro-bind-data`, a fresh mode-0700 tmpfs `/home/agent` with only allowed `StateDirs` bound in, read-only system and agent trees under `/opt/agents/`, and `--seccomp` fed from `go-seccomp-bpf` with a prepended host-ABI guard (`AUDIT_ARCH` check killing foreign ABIs such as i386 `int 0x80` and 32-bit Arm).

As its first act before opening the link socket, `wall-init` applies `go-landlock` in a single ruleset covering filesystem access (including `WithRefer` on ABI 2+ and `WithIoctlDev` on `/dev/ptmx`, `/dev/tty` and `/dev/pts` on ABI 5+) and IPC scoping (on ABI 6+), leaving TCP networking unhandled by Landlock so the guest's loopback HTTP proxy (`127.0.0.1:3128`) can bind inside its isolated network namespace and forward flows over `internal/link` to the host's `egress` process.

## Consequences

- Good, because we maintain no custom `pivot_root`, mount-namespace or AppArmor launcher code, and Ubuntu 24.04 users rely on the distribution's own `bwrap-userns-restrict` profile.
- Good, because combining filesystem and IPC-scope rules in one Landlock ruleset preserves cross-directory renames inside the workspace on Linux 6.12 while still enforcing ABI 6 IPC scoping.
- Good, because every security boundary (namespaces, seccomp, Landlock, filtered `/etc`, isolated loopback proxy) is verified in `test/e2e/escape.py` and package tests.
- Bad, because hosts must have the `bubblewrap` package installed for `--wall process`, and a guest-side port-53 DNS stub cannot bind without a capability, so direct DNS stays disabled and tools use the explicit loopback proxy.
