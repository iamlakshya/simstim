# Image publication cleanup

The host keeps one publication fence in the original placement operation.
It covers image pulls, uploads, saved roots and installed-agent conversion.
The fence grants no command, file rule or permission to adopt a store.

The producer creates an unnamed private inode in the checked control folder.
It records that inode's native identity in the existing operation before
linking it into the folder. The complete record is synced before linking.
It then opens a read-only descriptor, takes a shared lock and records readiness.
Only a ready original source can lend a helper descriptor. A failed factory
keeps its original handles for cleanup. A crash during creation leaves no
helper and either an absent name or the complete recorded inode.

Every helper inherits a duplicate of the same open file description. The
guardian closes its private handoff slot after capturing its own duplicate.
That duplicate is close-on-exec before a scope-management subprocess starts.
The concrete converter command carries a separate private lease role through
the jail's actual creation and re-execution path. It never projects that role
through an ordinary read-only file rule, which would create another open file
description and lose the original lock's lifetime.

The last jail helper reserves the converter's fixed raw slot without replacing
an occupied descriptor. Its first filter prevents closing or replacing that
slot, setting close-on-exec, and changing locks through any alias. The filter
permits the first converter exec. The dedicated converter entry then refuses
further execs on every Go runtime thread before parsing input. No Go finaliser
owns the raw slot. Process exit drops it.

Cancellation joins the original guardian and its captured native scope before
releasing conversion resources. The guardian's atomic creation descriptor,
native task and executable, and original cgroup establish that scope. A unit
name, invocation identifier, manager response or timeout cannot prove closure.
Saved-root protection starts before the plan's length or private bytes are
read, after the parent's native observation and its single-byte continuation.

Closing a publication first prevents new helper loans and drops the parent's
shared reference. A different open file description must obtain a fresh
exclusive lock before retirement. Busy probes return outside placement and
cache locks. Parent death alone does not remove a fenced operation: garbage
collection repeats the same exclusive probe while surviving helpers retain
their own copies.

While holding that exclusive lock, the owner records a removal intent in the
same protected operation before unlinking the sidecar. It removes the active
operation only after unlink and directory sync. A failed metadata write keeps
the same owner for retry. After a crash, a present sidecar still needs its
recorded native identity and a fresh exclusive lock. An absent sidecar can
finish only a previously recorded removal or unpublished-creation intent;
absence in a live phase is an error. Unknown, replaced and foreign entries
are preserved. Cleanup errors retain the store and publication until retry.

An agents-image factory can return a receipt together with an error. That
receipt is closed for use, but still owns its failed physical cleanup. Its
caller adopts the exact object before handling the error. The bridge keeps
actual objects, including failed ones, and removes each only after its Close
succeeds. A later request retries pending cleanup before starting new work.
The supervisor adopts failed factory receipts before its error branch and
keeps them through physical sandbox and disk cleanup. Neither caller replaces
the receipt with a digest or relies on its parent eventually dying.

Failure before a receipt exists has an owner too. The existing native
placement keeps the exact failed constructor if its cleanup cannot finish.
Its admission gate covers reservation through source readiness. Before a new
reservation, it retries that same object's Close. One unresolved constructor
therefore blocks another and cannot grow an unbounded cleanup set. Waiting
uses the caller's cancellation directly, with no retry goroutine or timer.
The gate holds no helper, RPC or conversion work. A drain in the same process
retries its actual object; another process still needs the recorded native
fence proof. The existing Active map remains the only durable ledger.

Later resource construction has the same custody rule. A runtime connection
adopts its actual part, store and publication before handling an error. Close
stops at the first unfinished resource and keeps its process slot until the
whole original stack closes. A failed image-store open keeps its publication
on the same connection. Another open retries that source first.

Runtime parts reject new writes once cleanup starts. A failed name check,
unlink or directory sync retains the part and its original descriptor. Only
the actual successful unlink records the removed state. Finishing a part
returns its cleanup error too. Fetch completion joins the original handler;
its retained resources still need successful Close before the job or slot is
released. An abort reports that cleanup error. Connection cleanup joins active
handlers, retains failed resource objects and retries them on another Close.

Source fixtures cover shared-description retention, parent death with a
surviving descendant, creation failure, lock-preserving data checks and
retirement after unlink. The full jail, cgroup, cancellation and garbage
collection fixtures require the admitted Linux host. Source and format checks
alone do not establish native acceptance.

The accepted original startup loan also retains its admitted process placement. Before preparation, the supervisor retains its cleanup-only handle. After sandbox, disk and agent cleanup, it retries the same placement's failed constructor before releasing the borrowed operation and holder. Cancellation keeps this handle usable. A later root lookup or a missing returned sandbox cannot acknowledge that cleanup.

Runtime store factories return their actual partial store and publication on error. The retained caller closes the store before retiring its publication. An image store whose partial-open cleanup failed is retried before another open can reuse it.

A runtime part retains its original incoming directory for unlink and sync. Linux publication links the held writer descriptor. The part retains its actual publication directory until durable sync succeeds, including when a later Close must retry that sync. Directory name replacement cannot redirect those cleanup steps.

A runtime part links its checked read-only alias only after its actual writer Sync and Close succeed. A retired descriptor with a failed Close remains an I/O failure. Cleanup keeps that failure and cannot publish it. Runtime commit syncs the artifact directory before recording a reference. Native first-close facts use the actual unfiltered, untraced thread; roots have no errno inference.
