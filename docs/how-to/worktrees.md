# Keep each task in its own worktree

A session can get its own git worktree and branch, so several agents can work in one repository without touching each other's files.

```sh
simstim new my-server --agent claude --dir ~/src/app --name auth-fix --worktree --detach
```

The session starts in a new worktree next to the repository, on a branch named after the session. `--base main` starts the branch from `main` instead of the current `HEAD`.

- The worktree is locked while the session lives, so `git worktree prune` leaves it alone.
- Files listed in the repository's `.worktreeinclude` (one pattern a line, for example `.env`) are copied into the new worktree. The rules for the copy are in [what `.worktreeinclude` copies](#what-worktreeinclude-copies).
- If the repository has a [setup script](setup-hooks.md), it runs in the new worktree after the copy, before the agent, once you trust the repository.
- When you delete the session, the worktree is removed only if nothing would be lost: no uncommitted changes and no commits that are not on another branch. Otherwise it stays, and the client says where.

In the launcher, press `n`, then `+wt` at the end of the quick-start line, or `ctrl+f` for the form, which has a worktree switch.

## What `.worktreeinclude` copies

The copy takes files from inside the repository only, and writes them only inside the new worktree:

- A pattern is a path in the repository, as in `.env` or `config/*.env`. It may hold `*`, `?` and `[…]`. A leading `/` anchors it to the repository's top folder, as in `.gitignore`: `/.env` is the `.env` there.
- It copies regular files, with their permissions. It skips a symbolic link, also one that points to a file in the repository, and a special file such as a FIFO. A link to `~/.ssh/id_ed25519` copies nothing.
- It skips a pattern that leaves the repository, such as `../shared.env`, and a file under a folder that is a link out of the repository.
- It skips a pattern with `.` or `..` as a part, such as `./.env` or `config/../.env`. Write the plain path: `.env`.
- It never replaces a file that the worktree already has, and never writes through a link there.
- It reads `.worktreeinclude` only when that is a regular file of 64 KiB or less, with 1000 patterns or fewer. Otherwise it copies nothing, and the log says why.

The session still starts when the copy skips a file. The session's [log](troubleshoot.md#logs) on the host has one line for each skipped file, with the reason:

```text
level=WARN msg="did not copy an entry of .worktreeinclude into the worktree" file=id why="it is a symbolic link, and only regular files are copied"
```

## How the host runs git

A session can write to its worktree, and to the repository's `.git` folder, which the worktree shares. So a session can set a hook, an fsmonitor or a filter there, and git would run it the next time the host runs git. The host helper guards against this:

- Each git command of the helper turns off the repository's fsmonitor and its hooks: the scripts in `.git/hooks`, and, from git 2.54, the hooks that the repository's config defines (`hook.<name>.command`). If the helper cannot read those hooks, or cannot turn one off, git runs nothing, and the step fails with the code `git_hooks`.
- Git also runs a filter of the repository when it reads a changed file, and no option turns all filters off. So each git step runs in a process of its own. On a host with a systemd user manager, that process runs in a scope under the sessions' slice, with whatever git starts.
- Each git step has a deadline: 10 minutes to make a worktree, and 1 minute for the others. A step that runs past it is stopped, with all that it started, and fails with the code `git_timeout`. A filter that hangs is the usual cause. The deadline holds also when your connection drops, and a dropped connection stops its git steps at once. When a worktree's checkout fails or is stopped, the helper removes the half-made worktree and its branch. When it cannot, the error says where they stay. After a dropped connection, the helper waits up to a minute for that removal before it ends. If the removal fails then, no one gets the error, and `git worktree list` in the repository shows the worktree.

This covers the steps that make and remove a worktree, the check of a repository's setup script, and the question that `pull` asks the host. If systemd cannot make the scope, or the helper cannot ask systemd whether it runs, the step fails with the code `git_scope`, and no git runs. The "git scope" line of `simstim host doctor` checks that systemd can make the scope. Git that you or an agent run yourselves is not changed.

### Git 2.54

Git 2.54 runs the hooks that a repository's config defines, and has no switch that turns them all off. A session could add such a hook while the helper's git runs, so on a host with git 2.54 the helper does not make or remove worktrees, and `pull` from that host fails. Each step fails with the code `git_version`. Update git on the host to 2.55 or later. Sessions without a worktree still start there. Check the version with this command on the host:

<!-- test: skip (runs on the host) -->
```sh
git version
```
