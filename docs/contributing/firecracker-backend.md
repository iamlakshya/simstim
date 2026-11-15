# Firecracker boot and control

The driver in `internal/wall/firecracker` selects the Firecracker backend of `internal/wall/microvm`. Prepare, open, start, stop, wait and destruction share the existing disk, lease, confinement and physical exit rules. The backend uses its own layout and lock names. It cannot open a Cloud Hypervisor plan as Firecracker.

The helper registers `wall-init` with the real guest parser. Both namespace launches and the tools image use this entry point. Versioned microvm boot and self-test arguments reach that parser unchanged. Unknown modes fail before the guest touches an inherited link or kernel state.

The Linux helper also registers the disk, runner and final execution stages. Both backends keep the existing `chv-disk`, `chv-run` and `chv-exec` names. These private commands reject all arguments. Their existing closed input and inherited descriptors supply the checked plan.

The host resource selector takes the driver from the protected original Need. It selects only an implemented VM backend and keeps the same live manifest publisher. It does not infer a driver from an inventory row or turn that selection into a startup grant.

The Firecracker backend uses the canonical version 1 VM Need. Its JSON keeps the same eight fields. The driver field selects the runtime role and CPU limits. It does not select a different source of authority. The host still needs the original client plan or its protected operation record.

The selected runtime contains one VMM, the guest kernel and the tools image. Firecracker must be at least version 1.17.0. Release builds pin version 1.18.0 or later. The Cloud Hypervisor compatibility functions keep their existing limits and role checks.

The Firecracker configuration has no network device, metadata service, snapshot or balloon. It uses the default MMIO transport. It does not pass `--enable-pci` or disable the VMM's seccomp filter. The tools, image and agents disks are read only. The session disk is writable. Their order keeps the two dm-verity tables bound to `/dev/vda` and `/dev/vdb`.

The guest memory is the requested whole number of MiB. The host unit also needs the canonical reserve. Configuration rejects a different unit cap. The KVM path of Firecracker 1.18.0 permits up to 32 vCPUs on both architectures. Odd counts are valid with SMT disabled. The [machine configuration source](https://github.com/firecracker-microvm/firecracker/blob/v1.18.0/src/vmm/src/vmm_config/machine_config.rs) and [resource tests](https://github.com/firecracker-microvm/firecracker/blob/v1.18.0/src/vmm/src/resources.rs) define these limits.

Control uses one connection that the runner has already checked against its owned VMM. The client cannot select a new socket path, use an HTTP proxy, follow a redirect or reconnect after the stream closes. State replies accept only the three states in the [pinned API schema](https://github.com/firecracker-microvm/firecracker/blob/v1.18.0/src/firecracker/swagger/firecracker.yaml). Shutdown normally uses the guest control stream. The x86 control path can also send the power button action.

The generated `fc.json` is read only and sits outside writable VM data. It names the exact boot descriptors and private endpoints derived from the validated plan. The file is data. It cannot supply a runtime pin or execution permission.

The runner keeps the actual VMM creation pidfd. It checks each Unix endpoint's kernel peer before and after a live handle check. It sends that handle and the original host side guest socket over its existing private control channel. The receiver uses the common bounded ancillary reader from the first byte. Descriptors belong only to the exact ready message for the session and backend.

`NativeBindingV1` has no public constructor. It checks the exact stream returned by this engine's start, the complete original Need and the same native VMM lifetime. It is a fact for the host's protected operation issuer. It cannot replace that operation, its supervisor lease or its grant checks.

A live VMM process does not prove that its guest stream is still open. The binding also refuses a closed or half closed peer, even when unread bytes remain queued. It checks this without consuming guest data.

The boot and control tests check the wire shape, driver selection, memory cap, device order, read only disks, input refusal, cancellation and connection lifetime. They use no VM and open no listening port. The existing Cloud Hypervisor boot fixture checks that extracting the common kernel command line preserves its bytes.
