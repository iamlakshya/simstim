# gVisor native owner

`host.NewGvisorDriver` takes no caller options. It joins the same original
accepted boot loan, protected primary operation, selected signed runtime and
native host storage domain. A saved bundle or expected-spec context supplies
data. It cannot issue the original receipt. The factory checks the protected
plan before preparing storage and again at the actual native start. One loan
can start one userspace kernel.

The process mount plan is captured once. System entries, explicit grants and
kept state carry the identities of their actual open descriptors. The bundle
creates placeholders from those same descriptors. The jail's `Bind.File`
captures the source capability and uses `Path` only as its fixed target. A
duplicate target must have the same object and write right. Conflicts fail.
An anonymous source must be an immutable, unlinked tmpfs memfd with the base
byte seals. A regular-file bind cannot acquire read or write rights from an
`O_PATH` or write-only descriptor.

The gVisor purpose needs user namespaces. Its outer user namespace maps only
the login user and group to 0. Its private PID 1 builds the root, closes raw
setup descriptors, drops all capabilities and verifies that it is not dumpable.
The trusted exec stage starts in a child user and mount namespace. Its only
mapping is outer 0 to inner 0. It keeps the same PID namespace, so outer PID 1's
death also ends descendants that call `setsid` or create another PID namespace.

Bootstrap capabilities belong only to that child user namespace. They grant
no capability over the ancestor PID 1 or its mounts. The existing generic jail
profiles keep their original refusals. Only the versioned gVisor purpose can
select its separate bootstrap profile. Legacy stages refuse that purpose.

The virtual guest runs as UID and GID 1000. Its home and runtime tmpfs belong to
that virtual identity. The [pinned upstream patch](../../third_party/gvisor/README.md)
supplies the matching Gofer ownership translation and early process-memory
protection. All companions must be built together and included in the original
signed release manifest. The one canonical runtime byte validator checks their
sealed copies; ordinary linked-file validation cannot accept those copies.

The native owner retains runtime descriptors and storage while physical exit
is uncertain. Type checks do not establish kernel behaviour. Native tests must
still verify read-only mount locks, aliases, nondumpable ancestors, file
fidelity and recursive PID namespace closure. The Landlock availability gates
remain unchanged until that evidence is accepted.
