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

Runtime selections belong to the accepted connection, rather than a completed
transfer job. A successful delivery closes its actual transfer resources before
issuing the held selection. Cancelling that finished job does not cancel the
selection. Construction still stops when its request or connection ends.

A fresh status request closes its inventory resources before retaining a
selection. An ended original selection remains unusable. Only a genuine new
status or delivery request can retire that same owner, then check the original
files again. Failed cleanup keeps the original object and blocks replacement.
Closing the connection cancels its producer lifetime and joins its handlers
before retiring the held files and native publication owners.

Boot-file cleanup keeps its actual first descriptor-close diagnostic after the
original files have retired and their custody has been removed. Preparation
outcome reporting reads that diagnostic separately from physical cleanup.
An unknown close still retains the original owner and cannot acknowledge cleanup.

The purpose-specific read-only experiment keeps the original measured 0555
VMM inode. It requires the actually admitted two-ID and two-GID namespace.
The same original namespace-init thread becomes the VMM directly. It keeps
a real/effective GID mismatch through its final credential drop, empties all
capabilities and sets NNP and NoDump before the sole final exec. No helper
exec intervenes in that thread. The kernel must retain NoDump and collapse
its effective, saved and filesystem GIDs at exec. Both supported architectures
need separate native proof of those outcomes.

The existing trusted control helper is created from that thread after the
capability drop. Its own first exec collapses its identity. Its captured
native entry applies a restrictive filesystem layer and denies future exec
on every thread. It opens the subordinate-owned disk and API/vsock endpoints;
it never creates a VMM. The original outside creator independently observes
the actual init executable and namespaces using its existing namespace-owner
authority. Native disk and peer handles retain that same creation scope.
A marker, plan, PID, EOF or copied role field supplies no authority.

The original outer parent retains the actual init pidfd and original creator
endpoint. Parent loss triggers a native kill and positive wait. Final artifact
release is separate from physical runtime closure. The original cgroup and
creation handles gate resource cleanup; dumpability and PDEATHSIG assumptions
do not replace that proof. All helpers retain the same per-artifact fence.
The normal verified installer provisions that existing fence before cache
metadata may become read-only. Legacy read-only metadata without a fence
requires actual preparation; no alternate registry is created.

The one-ID read-only direct path remains open. A sealed copy is not completion
of that direct requirement. This unpublished source experiment does not
establish native helper, privacy, parent-death or VM acceptance.

The original read-only VMM path keeps the current nine-file initial layout.
Its last slot carries the actual directory anchor retained before the existing
subordinate ownership change. The control helper opens the disk under its real
subordinate identity and returns the disk and parent on the original native
channel. The host retains those received descriptors for snapshot borrowing;
it never reopens that subordinate disk. Current control replies recheck the
same named disk before and after a borrow. The ordinary final disk remains at
slot 8, with the final private mode descriptors at slots 10 and 11.

This source path still needs native execution evidence. The generic checkpoint
store requires login-owned source roots and does not yet consume this
subordinate-owned disk loan. The login-UID path below uses a genuine second GID map. A single UID/GID
map still lacks this route.

Generated Firecracker boot data retains its actual partial config owner on
every preparation failure. The existing boot-file custody adopts that owner
before handling the error, keeps it through the native borrower lifetime, and
reports cleanup I/O diagnostics separately. Failed temporary-file removal or
an unknown descriptor or root close keeps the original objects for cleanup.

Physical phase cleanup also preserves the actual first close diagnostic from
its original code, disk, parent and boundary-pipe owners. The same runtime lease
retains a bounded report after successful cleanup drops those owners. This
report cannot establish descriptor retirement or acknowledge unknown cleanup.

The original read-only control server sends every state or power-button reply
through its fixed native disk reply. The same current disk and parent travel
with the original sender witness. A plain reply cannot satisfy this consumer.

## Login UID with two groups

The original read-only VMM source can retain the login UID when the host has
a genuine distinct subordinate group and `newgidmap`. Its original outside
mapper writes the single login-UID map and the real two-GID map. The native
source checks each actual map separately on the original init and control
helper. A plan's UID mode does not issue this mapping.

The same original init retains UID0 and uses real/saved GID0 with effective
and filesystem GID1 before its sole final exec. Its caps are empty, NNP is
set and dumpability is zero. The actual helper exec and final program exec
collapse to the login group. Native peer and disk checks use those actual
execution IDs. The second group serves only the existing credential mismatch.

The original thread, code, pidfd, namespace, SCM witness, sidecar OFD and
physical actor and cgroup joins remain required. No capability is kept across
a helper exec. A host without a genuinely admitted second GID remains
unsupported by this read-only route. Native execution is a separate check.

The original read-only init restricts every Go runtime thread. It sets NNP
and drops every bounding capability before clearing ambient, inheritable,
permitted and effective sets on all threads. The static helper build uses
`CGO_ENABLED=0`. An unsupported all-thread syscall refuses this route. It
never falls back to clearing the startup thread alone. Later threads inherit
the final zero-capability state. The original native creator and physical
actor and cgroup joins remain required after any parent-death signal clears.

## Subordinate checkpoint writer

The original read-only VM control actor performs checkpoint writes under its
own private VM root. The versioned actor layout is `vm/checkpoints`. The
original operation, engine, full Need and same native control actor remain
required before and after each borrow. Every reply carries the original
current disk and parent witness. The host never opens an actor-owned disk
through an O_PATH anchor or weakens the generic cache ownership rules.

The actor performs real clone probes, space checks, paged listing, creation,
retention and cleanup of its own last creation. It never falls back from
FICLONE to a full copy. Existing sibling `session/checkpoints` storage is
preserved and explicitly requires migration before this new route can run.
It is not silently reinterpreted.

Independent selected snapshot custody and the distinct stopped storage actor
remain required for rewind. This running writer refuses those purposes.
Stopping must still kill and physically join the original VM, init and
control actor. A later stopped actor must come from retained original native
custody and the same actual stopped-session issuer. Neither saved journal
data nor a directory descriptor may recreate it.
