# Bring a session's work to your laptop

When an agent has committed its work on the host, fetch its branch into your own clone of the repository:

<!-- test: help (needs a session in a repository) -->
```sh
cd ~/src/app
simstim pull my-server/auth-fix
```

`pull` asks the host which branch the session's folder has checked out, then runs `git fetch` through the client's own SSH connection, into a branch of the same name here. Neither working tree changes. Review the branch, merge it or check it out as you like.

`pull` checks the pinned key of the session's host. It uses the host and repository reported by that session. A Git URL rewrite or named remote cannot redirect the fetch. The SSH config still selects the connection's port and user.

- `--into review/auth-fix` fetches into another branch name.
- `pull` stops if the local branch has commits that the session's branch does not. `--force` replaces it.
- It will not fetch into the branch you have checked out.
- It needs SSH, so it does not work with hosts made by `host create docker` or `e2b`.
- Uncommitted changes in the session stay on the host; `pull` says how many there are. Ask the agent to commit them, or commit them yourself in the session.

Sessions started with `--worktree` work best: each has a branch of its own ([worktrees](worktrees.md)).
