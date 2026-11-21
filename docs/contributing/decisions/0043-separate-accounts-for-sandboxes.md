---
status: proposed
date: 2026-11-19
---

# 0043 Optional root-owned launcher and slot accounts

## Context

A jail narrows what a VMM can reach. When it runs as the login user, a VMM escape can still reach that account. Separate accounts give the VMM a different host identity and keep runtime files outside its write access.

The privileged boundary must be small and explicit. It must not accept a command string, caller-selected identity or arbitrary host path. A lock's closure cannot by itself establish that every process of a PID namespace has ended.

## Decision

Provide an optional signed static launcher at the product's fixed libexec path. One sudoers rule permits no arguments. Its bounded JSON protocol accepts closed operations with checked IDs, digests and limits. Driver arguments come from fixed templates.

Use 16 system accounts, one at a time per slot. Root owns the content-addressed runtime store and session ownership records. A stopped session keeps its login owner. Session locks keep a stable inode so that removing a record cannot create two concurrent locks for the same ID.

The actual root allocator issues a native lease. Its concrete capsule accepts no descriptor, metadata or callback constructor. The jail retains that same read-only lock description in its namespace init, outside final VMM files. The slot purpose requires actual user and PID namespaces and offers no weaker fallback.

After acquiring a slot lock, require a fresh native vacancy check of the previous allocation's same root-created cgroup and current kernel boot identity. A missing or changed object is quarantined. An empty replacement at a saved path is not evidence of closure. The cgroup stays beneath the original session unit, preserving its ancestor limits.

Native readiness captures the actual root sender and exact guest socket, including creation handles, executable identity, namespaces, cgroup, process protections and original boot roles. JSON and numeric identifiers remain correlation data. The existing original operation and complete client Need are still required by the engine.

The release key of decision 0017 signs a per-architecture envelope in the dedicated launcher namespace. Updates require the launcher role, current architecture, a strictly newer semantic version and an exact executable digest. Verification runs in Go. The release job uses its existing signing key and protected release path.

Separate accounts stay off by default. An outside security review is required before this record can be accepted or the feature can become the default.

## Consequences

An escaped VMM has a slot identity, no login home and no write access to the runtime store. Its egress process remains outside that account. Operators gain an explicit privileged programme, sudoers rule and shared account lifecycle to review.

The native account branch requires newer kernel peer-handle support than some ordinary VMM paths. A host with unsupported namespaces, peer handles or native accounting refuses the optional branch. The ordinary preparation path is unchanged.
