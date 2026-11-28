# Bring a session's work to your laptop

When an agent has committed its work on the host, fetch its branch into your own clone of the repository:

<!-- test: help (needs a session in a repository) -->
```sh
cd ~/src/app
simstim pull my-server/auth-fix
```

`pull` packs the session's branch as a git bundle (with hooks and fsmonitor turned off), streams it over the host's transport under the `sandbox.pull_max` cap (default `512M`), checks it with `git bundle verify`, and fetches it into a quarantine ref (`refs/simstim/sessions/<host>/<id>/<branch>`) with `transfer.fsckObjects=true`, `fetch.fsckObjects=true` and `--no-tags`. Neither working tree changes.

On walled sessions, the host never runs `git` in the sandbox's folder. Guest init packs the bundle inside the sandbox over `internal/link`, and the supervisor enforces the size cap, records the pulled commits in `manifest.json` and logs an audit entry. Pulling from a stopped walled session starts a short-lived bundle-only sandbox with no agent, network or secrets, streams the bundle, tears it down, and leaves the session stopped.

Before moving your local branch, `pull` reviews the diff for files that can run code on your laptop or in CI (such as `.github/workflows/**`, `.githooks/**`, `.envrc`, agent project files from the agent table, or changed `package.json` scripts), hidden Unicode characters, leaked session secrets or gateway sentinels in added lines, and changed files over 1 MiB. A `.gitmodules` file with a carriage return (CVE-2025-48384) is always refused.

- `--into review/auth-fix` updates another branch name.
- `--allow-flagged` updates the branch after you have inspected flagged changes in the quarantine ref.
- `pull` stops if the local branch has commits that the session's branch does not. `--force` replaces it.
- It will not update the branch you have checked out.
- Because `pull` uses git bundles over the host transport, it works over SSH as well as Docker, Lima and E2B hosts.
- Uncommitted changes in the session stay on the host; `pull` says how many there are. Commit them in the session, or copy individual files with [`cp`](copy-files.md).

Sessions started with `--worktree` work best: each has a branch of its own ([worktrees](worktrees.md)).
