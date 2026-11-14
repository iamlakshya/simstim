# Firecracker boot and control

The Firecracker backend uses the canonical version 1 VM Need. Its JSON keeps the same eight fields. The driver field selects the runtime role and CPU limits. It does not select a different source of authority. The host still needs the original client plan or its protected operation record.

The selected runtime contains one VMM, the guest kernel and the tools image. Firecracker must be at least version 1.17.0. Release builds pin version 1.18.0 or later. The Cloud Hypervisor compatibility functions keep their existing limits and role checks.

The Firecracker configuration has no network device, metadata service, snapshot or balloon. It uses the default MMIO transport. It does not pass `--enable-pci` or disable the VMM's seccomp filter. The tools, image and agents disks are read only. The session disk is writable. Their order keeps the two dm-verity tables bound to `/dev/vda` and `/dev/vdb`.

The guest memory is the requested whole number of MiB. The host unit also needs the canonical reserve. Configuration rejects a different unit cap. The KVM path of Firecracker 1.18.0 permits up to 32 vCPUs on both architectures. Odd counts are valid with SMT disabled. The [machine configuration source](https://github.com/firecracker-microvm/firecracker/blob/v1.18.0/src/vmm/src/vmm_config/machine_config.rs) and [resource tests](https://github.com/firecracker-microvm/firecracker/blob/v1.18.0/src/vmm/src/resources.rs) define these limits.

Control uses one connection that the runner has already checked against its owned VMM. The client cannot select a new socket path, use an HTTP proxy, follow a redirect or reconnect after the stream closes. State replies accept only the three states in the [pinned API schema](https://github.com/firecracker-microvm/firecracker/blob/v1.18.0/src/firecracker/swagger/firecracker.yaml). Shutdown normally uses the guest control stream. The x86 control path can also send the power button action.

The boot and control tests check the wire shape, driver selection, memory cap, device order, read only disks, input refusal, cancellation and connection lifetime. They use no VM and open no listening port. The existing Cloud Hypervisor boot fixture checks that extracting the common kernel command line preserves its bytes.
