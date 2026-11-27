# Process wall lifetime

The namespace launcher's exit is not proof that its namespace init has ended.
The driver keeps the init's own pidfd until the kernel reports `POLLIN` on that
descriptor. It retries interrupted and empty polls. Other readiness and poll
errors keep ownership uncertain and make the wait fail. Such a wait cannot
publish clean kept state or authorise destruction of the runtime files.

A later kill uses that same descriptor and can finish physical cleanup. It
keeps the earlier failure in the wait result. It never looks up a saved PID.

Destroy removes only runtime objects captured by the driver under its pinned
session directory: its specification, root mount point and namespace log. It
keeps the work, state, manifest and other session files. A substituted runtime
object is not deleted. The host's session deletion owns removal of session data.

Prepared specifications contain one JSON document and optional JSON whitespace.
The decoder reads the pinned regular file's actual size, rejects trailing data,
and checks its size and identity again. Saving preserves both write and close
failures, and removes only the temporary file created by that save.
