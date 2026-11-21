# HTTP/2 scheduling and native ownership

The intercepted connection keeps its original TLS stream, request handler and
native writer. The public server still limits active streams to 128 and queued
control frames to 10,000. No private PING or extra connection reserves a slot.

The scheduler visits open streams in admission order. After choosing one ready
frame it starts at the following stream. It skips a stream whose front DATA
frame has no flow credit. Only the original public `Consume` method splits a
DATA request and spends its stream and connection credit. Its final remainder
keeps the original completion notification. Connection controls take priority.

The public frame request has no control-frame type accessor. On the pinned
library, an empty public Random scheduler can distinguish a copy of a zero-size
stream frame: closing the temporary stream drops ordinary frames but retains
a control reset. It holds at most one copy and never consumes DATA credit,
writes native bytes or notifies the original writer. The real queue receives
the original request once. Native tests cover trailers, empty DATA, active
resets and repeated reuse of this classifier.

This behaviour requires exact Go 1.27.4, the unreplaced `golang.org/x/net`
version 0.62.0 and its documented `http2legacy` build tag. The tag selects the
original server, which calls the supplied scheduler. The default Go 1.27
wrapper ignores that hook. The build checks its actual compiled tag and its
original toolchain and module metadata before reading requests or forwarding
credentials. Unsupported builds do not fall back to another HTTP protocol.

The official compiler fixes missing dependency metadata in ordinary non-main
test binaries. All tests still use the standard Go producer. Source selection
alone does not prove native behaviour: ownership, scheduler, cancellation,
resource, coverage, fuzz and performance gates all remain binding. A later
upgrade needs fresh qualification. See the [build requirements](testing.md).

As in the Go 1.26 standard server, RFC 7540 stream priorities do not change
round-robin order. The pinned public API does not deliver RFC 9218 updates to a
custom scheduler. No newer priority behaviour is claimed.

Native TLS tests gate an original selected write, admit 128 streams, hold one
stream's DATA window closed and require a real reset to overtake ready work.
Each continuously ready queue gets one selection before another round begins.
Every permitted body must still arrive unchanged and complete once. Separate
tests retain the full control budget, cancellation fences, original deadlines,
header dictionary and connection-credit refunds.

The refund fixture checks the actual native DATA length on every cancelled
selection. Six 16,384-byte frames exceed the original 65,535-byte connection
window. Its byte writer produces those frames directly; a string writer's
4 KiB buffering cannot stand in for that measured credit budget.

An upload write can end exactly at its frame header. The observer retains the
same frame, padding and body offset until the next successful payload write.
Empty writes change none of that state. Native prefix tests exercise both a
single write and fragmented writes around the header and padding boundaries.

Dictionary checks keep the original header count, order and values after
authenticating the one appended private binding with the same connection key
and stream. Its HTTP/2 wire name is lowercase; the Go header-map key uses
canonical mixed case. A private field must not hide a missing original field.

`BenchmarkPhysicalH2SchedulerNative128` measures batches of 128 complete
responses on the same owned TLS connection. It compares the original public
server default, the previous Random wrapper and the deterministic scheduler.
It reports throughput, allocations, exact mean and maximum request latency,
and the 99th percentile of a bounded tail sample. Successful benchmark traffic
does not establish cancellation correctness or waive resource requirements.
