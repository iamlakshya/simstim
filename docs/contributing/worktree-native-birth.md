# Worktree directory generations

The original worktree creator records its directory's native filesystem handle while that new directory is still open. It records the filesystem type, filesystem identity, opaque handle type and bounded handle bytes. The directory remains open until creation finishes. This record is separate from Git's branch, tip and administrative paths.

On Linux, ext4, XFS and Btrfs can supply this handle. The creator obtains it through its actual directory descriptor, with filesystem checks before and after. It does not open a file by handle, request a privilege or derive ownership from a timestamp. An unsupported filesystem leaves the field absent. Worktree use can continue, but that absence grants no cleanup authority.

Before purge takes cleanup custody, it opens the worktree through its checked original parent and compares a fresh native generation with the creation record. A different generation is preserved. Old records without this field are preserved. Unknown versions and future fields grant no mutation authority. Force skips work-loss questions only. It cannot upgrade an old record or a replacement directory.

The same cleanup object holds the original root and parent before stop. It keeps them across rename, partial child removal, directory unlink and parent sync. A retry uses these same descriptors. It never reconstructs its root from the saved capture name. A missing name proves nothing. The retained original directory must show a native link count of zero before its cleanup can close.

The original physical stopped subject and final clean-work inspection remain separate inputs. A native directory generation supplies neither of them. Unknown worktrees appear in the retained-resource report with instructions to save and inspect them before manual removal.

Native tests must distinguish a supported positive result from a filesystem which cannot supply a handle. A skipped positive fixture is not acceptance of native deletion or an inode-reuse defence.
