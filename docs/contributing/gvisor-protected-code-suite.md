# Protected userspace-kernel code

The original runtime opener owns one private suite of its signed, sealed
executable objects. It derives a read-only open description through each
original held descriptor and checks the same native inode, immutable bytes,
seals, mode and signed item. A plain descriptor duplicate keeps its access
mode. The derivation opens only this process's original held descriptor.

The host command factory checks that same suite against its accepted runtime
and every executable projection. The original session and physical probe
owners retain the suite and recheck it before starting their native command.
The runtime closes its derived handles through its existing failure or
positive physical cleanup path.

The controller projection returns owned duplicates of all five distinct code
objects: runsc, the proc umounter, Sentry, FD parking and the prewarmer. All
five must come from the same supported patched build. Partial inventories and
older labels are refused.

This suite supplies public code custody. Actual native creation, memory
privacy, filesystem and aggregate barriers remain independently required.
Read-only access to a readable executable does not preserve non-dumpability
across exec. The fixed raw entry and original native ownership remain in place.
The mapped helper writes the sealed controller state and executes the held
fixed runsc. The outside C owner checks its current native witnesses,
filesystem result and aggregate. The original host operation gates the late
guest transfer. [The production route](gvisor-controller-production.md) records
these calls. Native and hardware acceptance remain separate.

Test sources use metadata fixtures and native sealed file objects. They cover
read-only description conversion, same-inode retention, copied or missing
suites, replaced signed roles, changed runtime items, closed owners and
incomplete companion sets. They execute no runtime code.
