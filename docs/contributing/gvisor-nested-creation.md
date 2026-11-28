# Nested runtime creation ownership

The original mapped helper keeps its same post-map Unix control object outside the serialised setup and the inherited descriptor vector. It uses that channel to transfer the nested controller's original atomic creation pidfd, native child and creator process objects, and its held helper code. The transfer happens after the mapped child's greeting and the outer filesystem restriction, before the child receives its ancestry proof or setup vector.

The outside caller checks the current kernel sender against its original mapped helper. It joins the pidfd's native namespace PID ancestry to both process views, the same kernel birth and parent, and the original captured helper code. It retains that actual creation descriptor before acknowledging custody. A later cancellation, byte recheck or send failure cannot discard it.

The command now signals the same nested pidfd and requires a positive exit from it before releasing its captured runtime resources. A pending or malformed creation report stays uncertain. Mapped-helper exit, transport EOF and an advertised PID do not establish nested exit. The mapped helper retains its own original process and pidfd through reaping too.

This is creation custody. The fixed controller route also binds the original
protected code suite and sealed public layout to C's actual execution. Its
outside witness consumer checks current code, command vector, birth,
namespaces, socket, final filesystem result and aggregate. The same original
host operation gates the late guest stream. [The production route](gvisor-controller-production.md)
describes those calls. Native and hardware acceptance remain separate.

Focused test sources cover malformed native pidfd ancestry, substituted process birth and parent chains, missing or copied original owners, and setup data in creation messages. They do not replace the original nested creation, cancellation, parent-death, malformed rights, raw-entry, systrap and hardware checks.
