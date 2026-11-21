# VM process lifetime

The shared implementation is in `internal/wall/microvm`. The Cloud Hypervisor package keeps its existing entry points and delegates to that engine. Its boot, ownership and integration tests live beside the implementation. The explicit `chvintegration` build tag and hardware admission rules are unchanged.

A VM has one process owner. The controller retains the command that created its jail. It does not recover a process from a saved PID.

`Wait` can fail before the kernel proves exit. That failure ends the current wait result. It does not close the runtime lease, agents lease or session disk. It also does not permit deletion. A log pipe's EOF and a runner's exit message do not replace native exit proof.

The controller keeps the same command after an uncertain wait. `Kill` can still signal that owner. Cleanup can poll its retained native handle without starting another blocking wait. If the owner later proves exit, cleanup can join the diagnostic reader and close the leases. The earlier wait and signal errors remain in the result.

`ReleasePreparedV1` closes an unused preparation immediately. A started VM requires physical exit. `Destroy` applies the same rule before it removes VM data. A logical completion channel alone satisfies neither operation.

Startup failures retain any command that might still run. The runner returns that command even when its input cannot be delivered. The controller owns its wait and cleanup. A failed start with no child can release its preparation.

Native exit and clean exit are separate. A clean result also needs a natural zero exit without a signal or lifecycle error. The existing guest preparation and terminal checks still apply before boot compatibility can be recorded. The jail's physical exit method proves its owned process. It does not claim that an unrelated process, or one that escaped the jail, has exited.

The consumer tests model an uncertain native wait, an open diagnostic writer, a refused signal and later proof from the same owner. They check that leases and the session disk survive uncertainty. The jail tests check its actual creation pidfd separately.

## Recording an explicit stop

The host's `stop` entry retains the original primary operation and the genuine
whole-stop owner. That owner must prove exit of the original supervisor,
emptiness of its retained cgroup, and absence of both native units and cgroups.
An acknowledgement, a stopped row or a failed wait cannot replace those facts.

After that proof, the host may record `stopped` even when forced termination
left an error in the supervisor's final record. Primary and member updates use
their original roots, member receipts and manifest inode checks. The previous
life, status and OOM details stay in `stop_diagnostics_v1`. The earlier reason
also remains in the stopped row. Errors returned by the native producer or
transport stay errors.

This publication certifies neither a clean exit nor a kept-state capture. It
does not change the accepted capture nonce or create a later cold-start source.
Idle stop and `max_run` cannot borrow this explicit request's physical proof.

## Restore coordination

One restore flow holds the native `restore.lock` descriptor across its two
workers and their cancellation cleanup. The existing metadata publisher creates
the fixed file, records its original native ref and validates its strict schema.
A second flow gets `busy` with a retry hint. A missing or replaced original file
is preserved and refused. Its contents grant no session or execution authority.

Each worker still takes its own original primary operation, native source and
fresh memory admission. The coordinator never supplies those permissions.

## Native manager custody

New custody-capable services use owned runtime unit files. Their original
outside operation installs the notification and descriptor-store properties
before the main starts. One active processless target holds a passive `After=`
reference to each service. It has no dependency that starts a session. A
per-user native file lock serialises configuration reloads and descriptor
admission. Admission measures the real manager's open-file limit and current
descriptor use.

The original main can deposit five kernel objects only after its runtime
owners join. They are the original session directory, locked holder file
description, cgroup directory, main pidfd and a peer-closed Unix sequence-packet
socket. The socket holds one claim packet. Each deposit disables descriptor
polling. A separate notification barrier must be followed by exact native
store checks. The barrier alone proves neither acceptance nor physical exit.

Reactivation uses `StartUnit` on the same loaded service and checks its new
invocation. The main validates every inherited object before waiting for the
original outside operation. It observes the existing holder flock without
acquiring or unlocking it. A reopened file with the same inode fails that
check. The old pidfd must prove exit and its retained cgroup must be empty.
Preparing the resume does not consume the claim or start a guest. The final
claim removes the manager's aliases while retaining the same holder file
description. Saved records and descriptor names cannot supply this authority.

Descriptor-store preservation requires systemd 254 or newer. The inspected
native implementations are 255 and 257. Their actual lifecycle and fault
witnesses remain separate acceptance requirements. Existing live, changed-boot
and retained-stop sources keep their own native checks.

A selected restart cap is reserved before the loaded service starts. The
preparation keeps the original selection and the authenticated target from
the same request. Its accounting leaf retains the previous cap during the
atomic manifest change and counts the full selected cap until native
takeover. Failed or uncertain publication keeps that charge. Merely capturing
a retained service does not start its bootstrap.

Managed unwalled resume keeps a distinct manager source. It inherits the
original holder description and kernel objects. Its ordinary pending owner
holds placement serialisation through native bootstrap and the first actual
RSS registration. The execution limit stays separate from RSS accounting.

A claimed bootstrap stays passive after its claim acknowledgement. The
caller closes all five proof aliases before sending its final release. A
partial release can permit factories, so cancellation after that point needs
the original cleanup acknowledgement and complete backend closure.
