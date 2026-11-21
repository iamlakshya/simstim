---
status: proposed
date: 2026-11-21
---

# 0049 Nitro slot donations stay until the parent reboots

## Context

The Nitro plan proposes undoing `nitro-slots` by writing the old count to
`/sys/class/misc/nitro_enclaves/pcie_slots`. The upstream
[driver contract](https://github.com/firecracker-microvm/firecracker/blob/firecracker-v1.18/docs/nitro-sandboxes/getting-started.md#slot-budget)
permits increases only. Each extra group of eight slots donates 128 MiB to the
Nitro System. That memory stays donated until the parent reboots. A smaller
write fails, so the proposed undo cannot restore the old setting.

## Options

1. Keep the proposed write and leave undo failing until the owner intervenes.
2. Reboot the parent from the undo script.
3. Record the donation and report it as retained until a later boot.

## Decision

Choose option 3. The fixed root step records the original count, the applied
count and the current boot ID. Its review states the memory donation and its
lifetime. Undo on that boot reports the retained donation and keeps the
journal. It never writes a smaller count or reboots the machine. After an
independent reboot, undo can remove that journal without changing the new
boot's slot budget.

The ACL and udev steps have their own journals. Removing their entries does
not imply that the slot donation was reversed. Slot-account undo removes only
its added rules and preserves later users' entries in the shared udev file.

## Consequences

The review and undo describe what the kernel can do. Undo can finish with a
retained machine-wide change. Reclaiming donated memory requires a reboot,
which the host owner must authorise separately. No script can treat a reboot
or a fresh slot count as permission to start another sandbox; admission still
reads the current native counters under the ordinary placement lock.
