# Native file facts

On Linux, `os.FileInfo.Sys` returns `*syscall.Stat_t`. The `Stat_t` type from
`golang.org/x/sys/unix` is a different Go type. Use the former when reading
an `os` result. Use `unix.Fstat` when observing the same retained descriptor.

The native manager checks keep their original owner, file type, mode and inode
requirements. The operation-lock observer keeps its original current-sender
PID requirement. Correcting the stat representation changes none of these
predicates and does not create a manager, operation or execution source.

Regression tests use actual Unix sockets, executable file stats and locked
file descriptions. A reopened file with the same inode must still fail the
operation-lock observation. Refusal must neither acquire nor release a lock.
