# Jail process ownership

The command captures its child's pidfd at creation with Go's `SysProcAttr.PidFD`.
It never reopens a saved PID. The kernel must report `POLLIN` on that descriptor
before the driver can report physical exit and release it. Interrupted and
empty waits retry. Poll errors and unrelated readiness retain the descriptor.

The existing group cleanup uses a number only while the owned child remains
unreaped. A `waitid` failure revokes that numeric authority before any further
group signal. Later signals use only the retained creation pidfd. A later
positive poll can finish the same owner's cleanup and keeps earlier errors.

`PhysicalExitV1` proves closure of the owned jail process. It does not promise a
clean exit or closure of descendants that escaped its group or namespace. A
wall that relies on PID namespace teardown must establish that namespace in
its own purpose contract. A command that failed before creating a child has
no live owned process to retain. Off Linux, the method reports false.

The logical result, status pipe, log EOF and done channel do not supply physical
proof. Setup, wait, signal, status and close errors remain in the result. An
uncertain command keeps its native owner for later cleanup. Its caller keeps
the runtime's files and leases while physical exit remains uncertain.
