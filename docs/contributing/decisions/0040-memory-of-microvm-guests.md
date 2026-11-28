---
status: proposed
date: 2026-11-28
---

# 0040 A microvm session's unit cap is its guest RAM plus a host reserve

**Status:** Proposed. Extends [0024](0024-memory-cap.md) for microvm sandboxes (isolation spec 20.10, [question 80](../isolation-spec.md#15-open-questions)).

## Context

In [0024](0024-memory-cap.md), a session's requested size is its systemd unit's `MemoryMax`. That fits unwalled sessions and process walls, where the agent and holder run as host processes inside the unit.

A microvm session runs a Cloud Hypervisor VMM, a supervisor, and an egress process outside the guest, alongside guest RAM allocated by the VMM and KVM page tables (`pagetables` and `sec_pagetables` in cgroup v2). If `MemoryMax` equalled guest RAM, the VMM's overhead and KVM page tables would push the unit to its cap and trigger an out-of-memory kill on the host before the guest could use its own RAM.

## Options

1. **Subtract the host reserve from guest RAM.** Keep `MemoryMax = size` and give the guest less RAM than the user asked for. A user who asks for `4G` would get roughly `3.85 GiB` inside the guest, and small sessions (`256M`) would lose half their RAM to host overhead.
2. **Add a host reserve above guest RAM.** Give the guest the full requested `size` (`--memory size=<size>M`), and set the unit's `MemoryMax` to `size + reserve`, where `reserve = 128 MiB + ceil(size / 256)` rounded up to a whole MiB. Enable Cloud Hypervisor's virtio-balloon with `free_page_reporting=on` so freed guest pages return to the host, and open disks with `direct=on` so disk reads add no host page cache to the unit's charge.

## Decision

Option 2.

- `guest RAM = size` (minimum 256 MiB, passed to Cloud Hypervisor as `--memory size=<MiB>M`).
- `reserve = 128 MiB + size / 256`, rounded up to a whole MiB (128 MiB fixed budget for VMM overhead, supervisor, egress process and margin, plus `size / 256` for VMM and KVM second-level page tables).
- `MemoryMax = size + reserve`, with `MemorySwapMax=0` kept from [0008](0008-memory-max-only.md). For example, a 1 GiB microvm reserves 1,156 MiB and a 4 GiB microvm reserves 4,240 MiB.
- Cloud Hypervisor runs with `--balloon size=0,free_page_reporting=on` so freed guest pages lower `memory.current` on the host, while admission continues to count the full `size + reserve` without overcommit.
- Unwalled sessions and process walls keep the rule of [0024](0024-memory-cap.md) unchanged.

## Consequences

- Good, because the guest always receives the full RAM the user asked for, and host-side VMM, supervisor and page-table memory never causes premature out-of-memory kills of the VMM.
- Good, because `direct=on` keeps host page cache out of the unit's cgroup charge and `free_page_reporting=on` returns idle guest pages to the host.
- Bad, because each microvm reserves 128 MiB plus `size / 256` more of the host's admission budget than a process wall of the same guest size.
