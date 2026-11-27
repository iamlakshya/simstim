# The Nitro sandbox backend

The `nitro` driver uses the same owned VM engine as `firecracker`. The original
client Need names `nitro` explicitly. A Firecracker artefact alone never selects
Nitro. The signed runtime needs Firecracker 1.18.0 or later, the `guest-efi`
kernel and the normal tools image. The other VM drivers keep the `guest` kernel.

Nitro uses `/dev/nitro_enclaves`, direct mode and floating vCPUs. The launcher
retains the actual character device and checks its sysfs device number and
supported flags before the jail captures that descriptor. It exposes four block
devices and vsock, with no network device, balloon, metadata service or hotplug.
The Firecracker KVM driver keeps MMIO and never enables PCI.

The driver retains the existing runtime leases, native creation handle and
owned vsock peer. Startup cancellation kills and joins that creation. A failed
native wait retains its owners until physical exit is proved. A saved PID,
configuration file, probe result or admission observation cannot replace them.

Admission uses the ordinary placement lock and protected manifest reservations.
It counts declared unit memory, the five-times-parent vCPU budget, pending
starts and the native PCIe slot counters. Reserved memory pool counters use
bytes. A zero reserved pool uses contiguous allocation at launch, bounded by
the ordinary host memory reservation. Kernel allocation can still fail.

Upstream's [slot contract](https://github.com/firecracker-microvm/firecracker/blob/firecracker-v1.18/docs/nitro-sandboxes/getting-started.md#slot-budget)
only permits increases. Each group of eight extra slots donates 128 MiB until
the next reboot. This supersedes the plan's proposed undo by writing the old
count: the kernel rejects that write. The root journal records both counts.
Undo reports the retained donation until a later boot, without rebooting.
[Decision 0049](decisions/0049-nitro-slot-budget-undo.md) records this choice.

The pinned [enclave configuration](https://github.com/firecracker-microvm/firecracker/blob/v1.18.0/src/vmm/src/vmm_config/enclave.rs)
and [ioctl definitions](https://github.com/firecracker-microvm/firecracker/blob/v1.18.0/src/vmm/src/nitro_enclave/ne_ioctl.rs)
define the direct-mode options and required flags. The signed EFI kernel is a
separate artefact: x86_64 needs an EFI-stub bzImage, while arm64 uses its raw
EFI-stub Image. An ELF vmlinux cannot boot Nitro.

Unit and protocol fixtures can check these contracts without EC2. They do not
prove the hardware boundary, start latency, fidelity or donated-memory cgroup
accounting. Acceptance needs an owner-provided x86_64 and Graviton EC2 host.
The implementation never creates or terminates an EC2 instance.
