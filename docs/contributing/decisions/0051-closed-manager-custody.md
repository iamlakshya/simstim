---
status: proposed
date: 2026-11-21
---

# 0051 Retain native custody in the user's manager between stopped runs

## Context

A stopped record cannot exclude a writer left by an earlier run. Neither can
a missing socket, an absent unit or a newly acquired VM lease. A managed
session needs a native owner while its runtime and supervisor are absent.
The existing live, changed-boot and same-request retained-stop sources keep
their separate checks.

## Options

1. Infer a cold source from stopped metadata and absence observations.
2. Keep a supervisor or a separate custodian process for each stopped session.
3. Retain the original kernel file descriptions in the user's existing
   service manager, with a distinct closed source for the next run.

## Decision

Propose option 3 for managed sessions. Do not add a persistent custodian
process. Unmanaged None continuation remains a separate, narrower decision.

The actual outside operation creates an owned runtime service. Creation sets
`NotifyAccess=main`, a five-descriptor store and descriptor preservation.
One active processless target holds passive `After=` references. It has no
dependency that starts a session. A per-user native file lock serialises
configuration reloads and descriptor admission. An actual temporary host
preparation keeps its original transient lifetime instead.

The original main first cancels and joins every actual runtime, borrow, copy,
freeze, file, egress, reader and alert owner. It then deposits five real kernel
objects: the session directory, original locked holder description, original
cgroup directory, original main pidfd and a peer-closed Unix sequence-packet
socket. That socket contains one claim packet. Each deposit uses `FDPOLL=0`.
A separate barrier and an exact native store comparison prove acceptance.
The original main stays alive through those checks. Its later native exit and
the original cgroup's emptiness prove processless custody. A passive bootstrap
may reuse that same native cgroup only when its actual new invocation, pidfd
and code identity are bound, it is the only process, and no child cgroup
exists. This exception is unavailable after possible factory release.

Reactivation rewrites the owned runtime command for the current trusted helper
and calls `StartUnit` on the same loaded service. Its watcher binds the real
new job and invocation. The bootstrap validates every inherited kernel object
and keeps the existing holder description. An existing flock observation
does not require its exited locker PID to equal the current sender. It must
never acquire, unlock or reopen that description. Live `op.lock` loans retain
their current-sender PID requirement.

The actual current operation reserves the complete bootstrap cap before
native dispatch. Inherited-kernel validation transfers the same protected
charge into resume preparation. Cancellation or an unknown native outcome
retains that charge until actual cleanup joins. A descriptor name, nonce,
copied row or accounting leaf grants no execution permission. A previous cap
correlates protected metadata. The selected target stays fully charged. A
retained original cgroup must be empty and its actual memory and swap use must
fit before a lower execution limit is written.

Managed None uses the same native kernel substrate through a distinct source.
It keeps ordinary RSS accounting and no declared VM quantum. Its original
pending owner holds placement serialisation from before StartUnit through
native birth, public identity and the first actual RSS registration.

Only the final admitted request may consume the one packet. It must still
pass current image, policy, floor, secret and resource checks. The new main
removes the manager's aliases and keeps the same holder description before
receiving private values or creating a runtime. Claim acknowledgement keeps
the main passive. Only a separate same-line release, after every outside proof
alias has native retirement, permits factories. Possible release is recorded
before its first physical send. A lost acknowledgement after claim cannot
renew that claim. Cancellation after possible release needs the original
backend cleanup acknowledgement and complete native closure.

An ordinary same-request stop selects its distinct typed main path before
acknowledgement. It suppresses manager escrow. After native exit and original
cgroup closure, its real captured configuration handles permit exact cleanup.
Its earlier absence and holder-acquisition checks remain unchanged. None stop
publication consumes its own N5 completion and never casts into either class.

## Consequences

- The stopped session needs no runtime or supervisor process.
- Native kernel custody survives requests without turning metadata into a
  source. The original holder description remains continuous across wakes.
- Owned runtime files permit helper upgrades and current resource settings.
- Missing pins, partial stores, consumed claims and unknown cleanup fail
  closed. They require their original retained owners or explicit repair.
- Descriptor admission must use real manager limits and current usage. An
  arbitrary retained-session count is not an adequate resource bound.
- Descriptor preservation has a documented systemd floor of 254. The
  inspected implementations are 255 and 257. Actual lifecycle, fault and race
  witnesses on both remain required before acceptance.
- All backend owners must participate, including privileged slot owners
  outside the main cgroup. Bootstrap cap changes, helper replacement,
  cancellation, cleanup, repeated wake and CLI fidelity need actual witnesses.
  An initial microvm slice does not complete the feature.
