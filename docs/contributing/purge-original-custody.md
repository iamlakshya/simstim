# Original removal process resources

The original removal keeps its real code factory, child creation, successful `Cmd.Wait` and native process handle. Its completion receipt binds those same objects. It uses their first native close facts after cleanup.

The factory keeps both writable and read-only code descriptors during the handover. It records each transient directory scan in the same resource pool. An incomplete directory identity can close its owned descriptors, but cannot authorise removal of named data.

The raw server attaches its resource pool before the first control read. Request cancellation interrupts I/O with a deadline. The real reader and writer finish before their connection closes. The listener also interrupts and joins its original `Accept` before its first native close.

The domain’s bounded lock waiter leaves the supplied descriptor with its owner. That owner records the first close which releases the flock. A retry reads the same facts. An earlier close outside that owner remains uncertain.

Native retirement and diagnostic errors have separate checks. Known retirement permits the next physical cleanup step while preserving the diagnostic for the caller. Unknown retirement retains the actual owner. Missing paths, a second close and an already closed descriptor do not prove completion.

This resource plumbing creates no session source, stopped proof or new cleanup permission. Walled source production, read-only readers and final stopped storage cleanup have their own native contracts.

The first deadline interruption has its own retained diagnostic. Reading that diagnostic waits for the real I/O operation and never replaces it with an empty action. A retry does not substitute a later already-closed error. The listener and framing gates must still join before the first native close. An interruption error is not retirement evidence.
