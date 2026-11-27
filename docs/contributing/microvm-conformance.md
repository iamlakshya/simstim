# Measure the microvm wall

This suite needs a real macOS client and prepared Linux KVM hosts. Run it on
bare metal and with nested virtualisation for each shipped architecture:
amd64 and arm64. Keep each platform's native assurance separate. A build,
a fake guest or a skipped native check does not provide a hardware result.

No results for this suite have been recorded yet. Cached start, wake, stop,
memory, density, escape, fidelity and key latency remain unproved on both
hosts. The default for `sandbox.idle_stop` remains off. Do not change it to two
hours until the required measurements exist and wake meets its target.

Use the existing E2E runner on a disposable host whose marker matches
`E2E_DISPOSABLE_MARKER`. It owns an isolated client home, all seven XDG paths,
the host lock and fresh session IDs. It deletes only its own IDs. It writes
host actions and raw results to a new evidence directory outside that client
root. A failure keeps the report incomplete. The final cleanup is part of the
result.

Obtain the actual same-source VM-equipped client from the normal qualified
H10, supply and release producers. A helper-only build does not provide the
current guest tools or runtime pins. No such qualified candidate handoff has
been recorded for this suite yet. The runner never injects a VMM, kernel or
tools image into host storage by SSH, or constructs a boot owner from a
manifest. Root owns the real candidate and cache preparation handoff.

Build `test/sandboxprobe` statically for the Linux host's architecture. Build
the test binaries of `internal/link` and `internal/supervisor` for that same
architecture too. Use the normal pinned Go compiler and current helper build.
Then run:

```sh
E2E_DISPOSABLE=1 E2E_DISPOSABLE_MARKER=<the-host-marker> \
scripts/time-to-agent.sh --host <saved-host> --client <client-binary> \
  --probe <linux-sandboxprobe> --link-bench <linux-link-test-binary> \
  --supervisor-bench <linux-supervisor-test-binary> --wall microvm \
  --substrate bare-metal --revision <exact-built-commit> \
  --runs 20 --stop-first --shell --output <new-evidence-directory>
```

Repeat with `--substrate nested` on the other host, then repeat the pair for
the other architecture. Missing hardware remains unproved. This entry point creates
no provider, applies no root step and changes no host floor. The image must
already be cached. The explicit `none` timing controls need a disposable host
that permits `--downgrade`.

The suite checks live output bytes, key encodings, resize, a 400-record
reconnect, stop, cold wake, explicit resume and an empty new ring. The static
guest probe tests host-file and socket isolation, KVM, vsock, `io_uring`,
`clone3`, user namespaces, terminal injection, netlink and direct TCP/UDP.
The i386 compatibility syscall runs separately on amd64. Its applicability
is recorded separately on other architectures.

The disk check uses a 128 MiB session disk and at most 512 MiB of writes,
through a 1 MiB buffer. It checks actual ENOSPC and the host's allocated blocks.
It never fills the host disk. Component throughput runs in owned sessions
capped at 512 MiB. There is no unbounded memory stress loop.

The density check starts exactly two idle Claude Code sessions, each with
1 GiB guest RAM. It waits 60 seconds and checks both. It never tries another
session after the target does not fit. The process-wall target is three.
Insufficient capacity is pending hardware evidence, rather than a smaller
target.

The native memory sample records `memory.current`, both page-table counters,
each process's RSS, the VMM's resident guest mapping and sealed runtime sizes.
The guest reports its used memory less the agent process group's RSS as a
claim. Permission errors, a changed invocation, a missing mapping or an
unresolved partition make the result incomplete. The suite does not weaken
process protections or acquire a root permission to obtain a reading.

For throughput, each socket-pair case passes 1 GiB, three times on one core.
The supervisor benchmark uses its actual output relay. Preserve these raw
logs alongside the existing full race, coverage and benchmark gates.

For key latency, each side records 1,000 acknowledged fixture windows. The
supervisor observes complete output frame arrivals without reading their
payload. All split ACK frames update the same bounded host-clock window.
The last frame of the acknowledged window is a conservative upper bound on
its PTY write. The baseline is the `none` holder's exact bridge-to-PTY time.
The checker never subtracts a guest clock from a host clock, or subtracts the
baseline's echo round trip. Incomplete, unrelated or dropped windows are
inconclusive and block the release gate.

Linux and macOS debug events now read the common kernel `CLOCK_MONOTONIC`
at each event. A process-start anchor could include unmeasured preemption.
The new clock cost belongs in these measurements. Historical timing figures
do not qualify this implementation.

`debug bundle` reads the current holder trace through its actual open writer
descriptor. An opaque epoch prevents crossing turnover or another writer.
One read sends at most 1 MiB. The supervisor's host-clock ring holds at most
2,048 output windows. These records are diagnostic data; they grant no native
operation, file path, process or start permission.

The manual conformance workflow reuses existing prepared Linux and macOS
runners. `CONFORMANCE_CANDIDATE_ROOT` selects the actual qualified client;
its build information must match the selected source. It provisions no
machine. It preserves native suite
results separately for each architecture, with the race detector, count three, default vet, pinned
Go, isolated homes and offline prepared module inputs. The native log checker
rejects a failed, incomplete or skipped required run. Run it only after the
shared fresh 8 GiB and global worker limits permit it.

The release workflow requires a completed conformance workflow for its exact
source before publishing the VM images. Set `MICROVM_CONFORMANCE_RUN` to that
actual run. A target miss other than key latency needs a real opened issue;
`MICROVM_CONFORMANCE_ISSUES` maps `architecture:substrate:metric` to that issue's URL. A
failed key gate blocks publication. The release checker recomputes key bounds
from original raw artifacts. Its summary changes no default and is not a
native permission or a substitute for the other required CI checks.
