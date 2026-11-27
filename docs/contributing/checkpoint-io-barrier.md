# Drain disk writers before a current copy

A task freeze alone does not drain kernel asynchronous disk work. The
current-copy producer must retain the original prepared disk and every native
writer, hold the guest filesystem freeze, freeze the actual writer groups,
then sync that same disk before cloning it. A saved entry, a guest reply or
an observed group name cannot supply those owners.

The pinned Cloud Hypervisor 54.1 source has three raw disk engines. Its
factory tries io_uring, then legacy Linux AIO, then synchronous I/O. Denying
io_uring alone can therefore select legacy AIO. `direct=on` controls caching;
it does not choose the synchronous engine.

The fixed writable raw disk command sets `_disable_io_uring=on` and
`_disable_aio=on`. These are internal options in this exact pin. Upstream
labels them for testing and omits them from the public API; they are not a
stable option contract across versions. The pinned CLI parser accepts both,
and the device manager
passes them to the raw disk factory. With both disabled, the factory selects
`RawBackend::Sync`. That engine finishes each vectored file operation before
reporting its completion and uses `fsync` for a flush.

The evidence is in the exact source pinned by `guest/supply.lock`, commit
`74aeae19cb1ee2c44d33796b0e4e95e1a68e94dd`:

- `vmm/src/config.rs`: the `DiskConfig` parser and both disable options.
- `vmm/src/device_manager.rs`: construction of `DiskOpenOptions`.
- `vmm/src/vm_config.rs`: the internal option declarations; their
  omission from API output means API readback cannot prove this choice.
- `block/src/factory.rs`: `open_raw` and its engine selection.
- `block/src/formats/raw/engine_sync.rs`: synchronous operations and flush.

The fixed Firecracker 1.18.0 configuration selects `io_engine = "Sync"` for
the session drive. Both paths still need their original code, configuration,
namespace, process and disk proofs. Configuration bytes are restrictions on
an already genuine creator; they cannot construct a native source.

The current-copy contract refuses an older or unknown original writer mode.
It checks the actual original native command, rather than API output. A pin
upgrade must prove this parser and engine contract again.
It must also refuse a group that was already frozen, an escaped writer, or a
monitor inside a group that will freeze. Its independent native monitor must
survive controller loss, thaw the same groups on expiry or cancellation and
join before custody releases. Native writer and failure witnesses remain
required; source inspection and argument tests do not prove that lifecycle.
