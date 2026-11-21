# The init-only slot lease

`CommandSlotV1` accepts the concrete opaque source from the actual root allocator's credential-dropped keeper. The source derives the original root-created listener, lease and nondelegated cgroup from its native handoff. A descriptor snapshot, requested UID, saved identifier or callback is not that source.

The closed purpose supports the fixed root-selected VMM and converter profiles. It requires real user and PID namespaces. It has no generic UID, GID or lease setting, and does not use the generic Landlock-only fallback. Existing driver kernel, device and Landlock gates still apply.

The keeper retains a duplicate of the same read-only root-owned 0644 lease open-file description. The versioned init stage receives it after ordinary inherited files and pinned paths. It checks the exact native inode, mode, link count and access flags. Host root ownership is verified by the actual keeper source before entering the slot UID map; inside the namespace it can appear as an unmapped UID.

The lease stays in trusted init. It is not a `Spec.Files` entry or a final VMM data file. Init marks it close-on-exec. The final stage's setup removes every lease field, and its file vector omits the descriptor. A frozen older or unrelated stage rejects the new init version.

`RetainSlotInitV1` can duplicate only the same command's actual creation pidfd, with the same live original source. It does not look up a PID or create a native guest binding from response data. The root guest inspector must still verify the actual guest peer, init ancestry, namespaces, credentials, executable and cgroup, together with the original Need and operation.

Lease retention is not whole-slot closure or reallocation permission. Linux closes a task's files before its exit path drains PID-namespace children. A root allocator that acquires the lock must therefore perform its separate native whole-slot vacancy fence and quarantine uncertainty. Keeper and init crashes, a process in a new session, and delayed namespace teardown all require native tests. No source or compile result proves those behaviours.
