# Fork a sandbox

Fork copies a sandbox's files and conversation into independent sessions:

```sh
simstim fork box/auth-fix
simstim fork box/auth-fix --count 3
```

The first command attaches to its child. Use `--detach` to leave it running.
Passing `--count` starts without attaching. It accepts 1 to 32.
Use `--name` to name a child, or to set the base name for several children.

The copy holds files, including the agent's state folders. Each child starts
cold with a new ID, new secret stand-ins and a new certificate authority.
The child has no copy of the source's terminal output. The source continues
after the copy. On a process wall, all processes in the sandbox pause during
the copy. On a gvisor wall, the same pause covers the workspace and the
current mounted home. On a microvm, the session disk's file system freezes
briefly.

An agent with a fork command starts a new conversation from the copied one.
Other agents use their continue command on the copied state. Each child has
its own branch prefix, so `pull` brings back that child's commits.

Every child goes through admission. When the host has room for only some
children, those children start and the command reports the others as failures.
A failed freeze renewal discards unfinished copies.

New children start with their secrets unarmed. Attaching supplies fresh
values. Private SSH signers are not copied between sandboxes.

For scripts, use `--detach --json`, or `--count 3 --json`. The result lists
the children that started and each failed child's one-based request number.

## Return to a checkpoint

A microvm keeps disk checkpoints where its host file system supports reflinks.
The default keeps the newest 10. `sandbox.checkpoints = 0` turns them off.
Process walls and gvisor walls do not keep checkpoints. A host without
reflinks reports why checkpoints are off.

```sh
simstim rewind box/auth-fix --list
simstim rewind box/auth-fix 2
simstim rewind box/auth-fix 2 -y
```

Numbers start at 1 for the newest checkpoint. Without a number, `rewind`
lists the checkpoints and asks at a terminal. Without a terminal, pass a
number and `-y`. `--list --json` prints the list for scripts.

Before asking, the command shows the checkpoint's time and the branches whose
heads differ. The choice names that exact checkpoint, so a later turn cannot
make the same number select a different disk. If retention removes it before
preparation, the command refuses the restore.

Rewind checks the current image, policy, host floor and room before stopping
the sandbox. It fetches fresh secret values and the currently selected SSH
keys through the same prompts as a cold resume. Use `--secret NAME` for a
secret that this host does not receive by default. `-y` confirms the disk
change; it does not answer a secret-sharing question.

The helper retains the chosen checkpoint, stops every original sandbox
process and makes a safety checkpoint. It then restores the disk, branch
heads and each member's conversation ID, and starts the sandbox cold.
Checkpoint retention runs after the disk transaction settles. A cancelled
restore before that commit rolls back the disk and conversation changes,
then restarts the original sandbox.

A failed native cleanup keeps its original resources for the pending
operation. A pending restore blocks another start or rewind until that
operation settles. Keep its files in place; deleting a pending record does
not repair its runtime.
