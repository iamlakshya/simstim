# Linger creation and undo

Linger keeps the user manager after logout. A successful enable command does not prove that the current linger file belongs to this setup.

The fixed root step writes an intent in its existing journal before creating anything. It creates an empty root-owned inode in that journal. An exclusive hard link publishes that exact inode at the fixed linger name. Existing files, directories and links are kept. A failed hard link has no copy or replacement fallback.

The journal keeps the original inode alive. Undo checks that real anchor and the current fixed name, account, parent and generation. A stored inode number, baseline, root-step ID, command result or status cannot replace the anchor. An already enabled setting is not adopted.

For a fresh matching origin, the same fixed root operation calls `loginctl disable-linger` for the original account. It checks the actual objects immediately before and after the command. It removes a parent directory only if this step created that exact fixed leaf and it is still empty. It never creates or removes other ancestors.

Unknown, legacy, changed or incomplete evidence is reported as `LEFT linger` with a fixed reason. The journal and its evidence stay. A completed root undo attempt does not mean that every change was undone. A failed or changed postcheck has no raw unlink or rename retry.

Root and logind are trusted privileged actors. The check followed by the username-based command is not an atomic conditional unlink. An independent privileged actor can replace state between those actions. That residual needs native assessment. The code makes no stronger claim against host root.

Setup and doctor results from older direct enable paths have no created-origin anchor. They remain unowned. A caller may use this producer only through its original allowed root execution. A status or footprint cannot create that execution source.

The ordinary filesystem fixtures model root IDs and service notifications. They exercise real inodes, hard links, exclusive publication and replacement checks in a private tree. They do not prove root permissions, durability, logind behaviour or native host cleanup. Those checks remain separate.
