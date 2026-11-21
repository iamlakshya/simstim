# Preparation close outcomes

The fixed preparation test keeps two separate outcomes. Its actual runtime
must finish destruction before the native cancellation reply can acknowledge
cleanup. An original first-close I/O error remains a test failure even when
the same descriptor has independently retired and cleanup can finish safely.

The concrete runtime retains those first-close errors. Its supervisor reads
them while retrying the same destruction owner. Unknown cleanup keeps that
owner in place. Once destruction succeeds, the diagnostic remains separate
from the cleanup error. It cannot keep a physically retired runtime parked
forever, and it cannot establish cleanup by itself.

Only the original private fixed-test cancellation reply carries this report.
Its versioned result has a bounded diagnostic and no execution or custody
fields. The outside parent checks the same reader cursor and native sender,
strict JSON fields, version and size. It still joins the original process,
whole unit and launch owner before removing the test's directory. Only then
can it return the retained I/O error as a preparation failure. A retry or
second close cannot change that original failure into success.

Runtime downloads follow the same distinction. A real Sync or writer Close
failure must prevent publication, even when the descriptor's independent
retirement permits safe cleanup. Closing a file twice supplies no evidence.
