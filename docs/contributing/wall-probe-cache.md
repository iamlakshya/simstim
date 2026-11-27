# Wall probe cache

The public capability request always collects fresh facts. On Linux it
publishes them through the existing `walls.json` role and its native metadata
producer. There is one cache. A registered driver's construction result stays
separate from a successful physical probe.

The metadata producer retains its actual placement operation. The canonical
writer records the prepared inode before replacing the old file, syncs it and
updates the exact producer reference under the placement lock. Interrupted
publication is replayed conservatively. A prepared file is cancelled or an
already published inode is reconciled; replay cannot publish prepared data.

A session start reads cached facts only with its existing native owner. The
state root, file, exact producer reference and absence of an uncertain write
must match. The boot ID and mapped helper byte identity must also match. A
same-user file with mode 0600 is insufficient on its own.

A physical gVisor result also names the digest of the exact original selected
runtime manifest. A cache miss cannot choose a manifest from inventory. The
real New request can run its own closed diagnostic preflight using its retained
delivery receipt. Neither cached facts nor a physical result issues a session
operation, original boot loan or execution grant.

Drain, root-generation and committed shutdown checks remain in the native
publisher. Unknown metadata or a file without a producer reference is retained
for inspection. A cache failure never turns into a success bit.
