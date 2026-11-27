# First native resource retirement

A closed Go object is not proof that its original kernel descriptor retired. A filtered close can report success without releasing it. A second close may report only that the Go object is closed. Cleanup must retain the actual first native result and the same original object.

The generic resource owner records each original object before calling close. File closure uses the supplier's concrete first-close observer. Its cached result distinguishes physical retirement, a diagnostic and unknown state. A diagnostic stays reported even when retirement is known. Unknown state keeps the original owner and charge. It cannot become success through a second close, ErrClosed, a new descriptor number or a replacement object.

Unix connections and listeners require an actual unfiltered close thread and a live original socket before that first call. Their real I/O users must have joined first. The private startup-line consumer checks its actual read and write gates. Cancellation uses deadlines to interrupt those users before the one native close. The original ancillary receiver retains materialised objects before an error path can discard them.

The native user manager installs its private byte transport before D-Bus starts. Every actual read, write, deadline and raw control reference belongs to that transport. Close fences new users, interrupts existing I/O with a deadline, joins it, then records the first native close. A library EOF or cancellation uses the same owner. The original direct peer and namespace checks, setup guard and unit checks remain mandatory. Partial constructors stay cleanup-only and remain reachable on errors.

Root closure also needs the original live Root, an unfiltered locked thread and every real rooted operation already joined by its owner. The resource helper does not accept a caller's readiness flag. The native manager joins its root checks under its actual resource lock before closing. Other concrete producers retain responsibility for their own borrowers.

The purge composition adds existing peer, identity, lease, line and private-endpoint resources to those generic facts. The endpoint still checks and unlinks only its actual original socket. Native retirement checks grant no session, operation, unit, stop, source, publication or commit authority. Each actual producer still supplies and retains that separate authority.

Source and compiler checks do not prove the supported native class. Native tests must cover the original positive first close, premature untracked closure, cancelled live I/O, unsupported close profiles and retries with the same objects. Whole removal remains gated on all native source, reader and cleanup joins.

Manager cleanup returns a concrete split outcome. The diagnostic belongs to the original operation error or report. Only the actual native retirement check decides whether the resource and charge remain held. A failed constructor preserves its setup error and first close diagnostic, but returns a partial manager only when native retirement is unknown. Cancellation remains an operation diagnostic; it cannot undo known first-close facts. Close itself keeps the original diagnostic on every retry. Callers must not use that diagnostic as an unknown lifetime, discard it, or issue a second native close.
