# Verified runtime descriptor lifetime

The native microvm consumer passes the original checked store descriptor and selected runtime item to the supply-owned verified runtime constructor. The original protected Need still chooses the item. Kernel measurement and content mismatches remain digest-mismatch errors. Unexpected measurement errors do not silently select a fallback.

A matching measured kernel retains that same immutable inode. A VMM keeps the existing execute-only sealed projection after measurement and still requires the native NoDump prerequisite. Direct execution of a readable measured VMM remains an unresolved boundary.

Each verified runtime owns its descriptor. File returns a borrowed handle. The boot resource group retains both owners through the existing agent/resource lease, which both native runtime routes transfer to the same actual engine. Existing physical-exit cleanup releases that combined lease. A failed preparation that created no child releases it directly. Boot file cleanup never independently closes borrowed runtime handles.

This source join needs native measurement, rename/unlink, fallback and physical cancellation tests. It is not hardware or NoDump acceptance evidence.
