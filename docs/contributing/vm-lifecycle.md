# VM process lifetime

The shared implementation is in `internal/wall/microvm`. The Cloud Hypervisor package keeps its existing entry points and delegates to that engine. Its boot, ownership and integration tests live beside the implementation. The explicit `chvintegration` build tag and hardware admission rules are unchanged.

A VM has one process owner. The controller retains the command that created its jail. It does not recover a process from a saved PID.

`Wait` can fail before the kernel proves exit. That failure ends the current wait result. It does not close the runtime lease, agents lease or session disk. It also does not permit deletion. A log pipe's EOF and a runner's exit message do not replace native exit proof.

The controller keeps the same command after an uncertain wait. `Kill` can still signal that owner. Cleanup can poll its retained native handle without starting another blocking wait. If the owner later proves exit, cleanup can join the diagnostic reader and close the leases. The earlier wait and signal errors remain in the result.

`ReleasePreparedV1` closes an unused preparation immediately. A started VM requires physical exit. `Destroy` applies the same rule before it removes VM data. A logical completion channel alone satisfies neither operation.

Startup failures retain any command that might still run. The runner returns that command even when its input cannot be delivered. The controller owns its wait and cleanup. A failed start with no child can release its preparation.

Native exit and clean exit are separate. A clean result also needs a natural zero exit without a signal or lifecycle error. The existing guest preparation and terminal checks still apply before boot compatibility can be recorded. The jail's physical exit method proves its owned process. It does not claim that an unrelated process, or one that escaped the jail, has exited.

The consumer tests model an uncertain native wait, an open diagnostic writer, a refused signal and later proof from the same owner. They check that leases and the session disk survive uncertainty. The jail tests check its actual creation pidfd separately.
