# Failed preparation keeps its owner

A factory can return an actual resource and an error together. The caller must
adopt that same resource before handling the error. A later failure does not
prove that a process, lease, directory handle or capture disappeared.

The VM factory creates its actual sandbox before acquiring its preparation
lease. A failed lock, metadata write or final close returns that same sandbox.
The failed preparation refuses Start. Non-destructive release closes only the
same prepared owners. Destructive cleanup rechecks its actual exclusive lease
before removing owned data. A native runtime still needs its positive original
physical exit proof.

The process factory also retains its metadata owner before writing the
specification. Its prepared release fences Start and preserves durable runtime
files. Failed native copy cleanup keeps the original publisher and staging
objects. A later cleanup call cannot erase an earlier unknown close result.

The userspace-kernel factory returns its actual partial storage, runtime and
command owners. A failed command constructor keeps the same command available
for cleanup. Running or uncertain original creators retain their code,
topology and storage. Real command preparation release reports cached native
code close errors. No unsupported release is counted as success.

Disk observation roots and state captures follow the same rule. Cleanup starts
a permanent Start fence. A failed native release can be retried through the
same owner. A failed removal keeps the original roots and directory identity
for a later attempt. A descriptor Close error remains an error: the operating
system wrapper may already have invalidated that handle, so repeating Close
or reopening a pathname does not prove successful cleanup.

A capture removes only the incoming directory created by its own exclusive
constructor. A substituted name is refused. Committed generations are left
alone. Constructor errors return the actual partial capture to the caller.

The private fixed test boot may acknowledge cleanup only after its actual
runtime, capture and disk owners have completed cleanup and its original
operation and holder loans can close. Error wrappers and logical Wait results
supply no physical or file ownership permission.

The VM factory keeps its actual boot-file result before checking an error. Cleanup joins the actual native borrower, then closes files, runtime owners, image leases and stores in order. A failed step keeps the same objects. Native file retirement uses the actual unfiltered, untraced Linux close thread; a pathname, an error field or an already-closed object supplies no retirement proof.
