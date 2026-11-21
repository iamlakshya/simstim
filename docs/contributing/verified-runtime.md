# Verified runtime descriptors

The release manifest records the VMM and guest kernel's `fsverity` digest.
It uses SHA-256, 4096-byte blocks and no salt. This is the hash of Linux's
256-byte fs-verity descriptor. It differs from both the file's SHA-256 and
the root of its Merkle tree. The release writer derives it from the actual
kernel and mirrored VMM bytes. The ordinary asset checks verify both digests.

After publishing a checked runtime file, the installer closes its writable
descriptor. Under the existing store transaction, it opens the same owned
inode read-only and enables fs-verity. It temporarily adds the owner's write
bit for that call, then restores 0555 for a VMM or 0444 for a kernel, including
on error. Existing checked files get the same treatment at runtime commit.
The tools image and other disks never receive fs-verity.

At start, the runtime owner retains an opaque `supply.VerifiedRuntime` until
physical teardown. Its `File` method lends the descriptor without transferring
ownership. A matching measured kernel uses the original inode. A rename over
the store name cannot redirect it. When fs-verity is absent, a kernel uses a
checked sealed copy. A measurement mismatch, malformed digest or unexpected
ioctl failure stops the start. Only the documented absence errors select the
copy path. Older manifests without `fsverity` retain the sealed-copy path on
files without fs-verity.

A measured VMM can use its original inode on a writable owned store. This
requires the complete native command owner. It holds the original read-only
sidecar lock across every helper that can still execute the VMM. Store readers,
repair and removal use the same per-artifact lock. They retry outside the
store transaction while a start owns it.

Every read-only VMM projection and final file-offset reopen finishes at 0555.
The original command checks the actual helper creations, their executable
inodes, native namespaces and last projection before changing that same VMM
inode to 0111. The final helper checks it again. The original owner restores
0555 only after the final live task actually executes that VMM, or native
process evidence proves that no helper can still execute it. A started command,
a saved PID, a status record, cancellation and an ordinary EOF grant nothing.
The mode lease ends at that boundary. It does not span the VM's lifetime.
An uncertain result retains the original handles and resource owner for retry.

A genuinely read-only artefact mount, or documented fs-verity absence, retains
the execute-only sealed VMM projection. A digest, inode, ownership or native
proof mismatch refuses the start. A route without the complete native mode
owner cannot admit the direct candidate. Direct VMM use on a read-only store,
and direct admission through the separate-account route, remain open parts of
isolation spec 20.5. They must not be reported as completed by this route.

The digest implementation follows the [Linux fs-verity format](https://docs.kernel.org/filesystems/fsverity.html#file-digest-computation).
Unit tests cover the descriptor format, Merkle boundaries, native descriptor
retention and injected ioctl outcomes. `FSVERITY_REFERENCE` can name an admitted
absolute `fsverity` utility for the three-file digest comparison. That test
only computes digests. Actual enable/measure, write refusal, VMM execution,
Landlock and NoDump proofs require their separately admitted Linux fixtures.

The final VMM execution helper rechecks its inherited kernel descriptor. It
accepts the matching measured inode or the immutable sealed fallback and
requires the existing read-only descriptor projection. It opens no store path.
The original store ownership check happens before user-namespace mapping:
the subordinate helper can run as UID 1 while the store inode maps to UID 0.
The final check verifies the immutable bytes, exact mode, size and read-only
file rights without inventing a new ownership or boot-selection grant.

Native cache bindings use their own closed JSON object boundary below the application API. Duplicate or aliased keys, unknown fields, trailing input and cancellation still refuse without changing the original binding. Runtime handling can use the native cache without an application import cycle.
