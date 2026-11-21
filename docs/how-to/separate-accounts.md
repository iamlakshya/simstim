# Run sandbox VMMs under separate accounts

Separate accounts are an optional Linux host feature. Review [the security boundary](../explanation/slot-accounts.md) before enabling it. The normal preparation path leaves it off.

Ask the host's owner to review the exact script:

```sh
simstim host prepare box --separate-accounts --root-step print
```

The two optional steps are `launcher` and `slots`. The first installs the signed static launcher and one sudoers line that allows no arguments. The second creates 16 system accounts and the root-owned store. No package, service, network listener or setuid program is installed.

The staged launcher must match the original release item selected by the accepted connection. The script copies it into a private root-owned temporary file, checks its size and digest, then installs it. Existing root-owned files are recorded for undo. A symbolic link or substituted stream cannot select another executable.

After reviewing the script, choose `--root-step run` through the usual root-step workflow, or give the printed script to the machine's owner. See [review a host's root steps](give-a-host-the-root-step.md).

An installed account mode still needs the normal supported VMM, KVM, namespace, seccomp, Landlock and resource checks. The native guest check also needs Linux 6.5 or newer. The host must keep `fs.suid_dumpable=0`. Account mode refuses a start when its stronger checks are unavailable.

A host has at most 16 occupied slots. When all are busy, list its sessions and stop one. A quarantined slot needs the host doctor: the launcher could not prove that its previous native allocation ended. Removing a status file does not free that slot.

Keep the recorded undo script. Undo removes the caller's sudoers rule before changing accounts. It refuses while a slot is held, preserves pre-existing accounts and files, and retains session disks and store contents for export or explicit purge. Account and launcher changes shared with another opted-in user stay until that user's reference is removed too.

Removing a preparation that never started, or retrying completed root cleanup, succeeds only when the protected session tree and control socket are absent. Unknown root objects stay for inspection. An interrupted start records caller ownership before creating its mutable disk tree.

Preparation starts the advisory slot projection as unknown. Native start, stop, destroy, prune and slot-list operations refresh it under the existing store lock. Undo removes the caller's projection. Facts read only the fixed file, and an old or missing file never means that slots are free.
