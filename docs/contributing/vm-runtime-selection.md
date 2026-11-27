# Select VM boot metadata from a complete runtime collection

Runtime delivery keeps the client's complete architecture-specific runtime
request. A release can carry both Cloud Hypervisor and Firecracker. That full
collection stays in the runtime store with its original header and receipt.
The actual signed release bytes remain the authority on the client.

A VM Need names one driver. `vmneed.SelectRuntimeForDriver` derives its exact
VMM, normal guest kernel and tools metadata from that original collection.
It preserves every selected item unchanged, including its architecture,
digest, size, URL, verity metadata, version and advisory information. Missing
or duplicate roles fail. The normal kernel cannot borrow an EFI kernel role.

The returned value is comparison and plan data. It is not a newly signed
manifest, a replacement store record or a source of execution permission.
The client still delivers the complete original request. The native boot
owner still requires the original accepted operation, protected Need, held
runtime descriptors, image and agents producer, admission and lifetime.

Driver probes use the same derived role set when examining a full store record.
A complete verified byte set can report construction prerequisites. It cannot
report a successful boot, native ownership or compatibility without the
existing controlled boot evidence. Synthetic store fixtures test this boundary;
they are not consumable VM boot inputs.

The fixed H10 test boot and P3.8 measurement path have their own producer and
cleanup requirements. This metadata handoff does not advertise or activate a
private handler, issue another native capability, write a cache over SSH or
turn a hand-written JSON plan into original-client authority.
