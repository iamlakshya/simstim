# Shells and commands in a sandbox

Open an interactive shell or run a single command inside the same sandbox as a running agent. Both share the agent's workspace, home files, network policy and installed tools.

## Open a shell

`shell` attaches to a shell inside a session's sandbox:

<!-- test: help (needs a host) -->
```sh
simstim shell my-server/auth-fix
```

If a shell is already running in that sandbox, `shell` attaches to the newest one. Pass `--new` to start a second shell, and `--name` to give it a clear name:

<!-- test: help (needs a host) -->
```sh
simstim shell my-server/auth-fix --new --name tests
```

In a script, pass `--detach --json` to start or pick the shell without attaching:

<!-- test: help (needs a host) -->
```sh
simstim shell my-server/auth-fix --detach --json
```

On an unwalled session (`--wall none`), `shell` opens a normal host shell in the session's folder.

In the launcher, select a walled session and press `t` (or open the command palette and choose `Shell in sandbox`). Child sessions appear indented directly under their primary session.

## Run one command

`exec` runs one command inside a walled session's sandbox without a terminal. It keeps stdout and stderr apart and exits with the command's own exit code:

<!-- test: skip (needs a host, and --help after -- goes to the command) -->
```sh
simstim exec my-server/auth-fix -- git status
simstim exec my-server/auth-fix --timeout 2m -- go test ./...
```

The default timeout is 10 minutes. Pass `--timeout 0` to turn the timeout off. Closing stdin (for example with a pipe) sends EOF to the command without stopping it. Pressing Ctrl+C stops the command and its process group inside the sandbox.

`exec` uses these exit codes:

| Case | Exit code |
| --- | --- |
| The command ran and exited | Its code |
| A signal `N` ended the command | `128 + N` |
| `--timeout` ran out | `124` |
| `exec` failed before the command started | `125` |
| The command was found, but could not run | `126` |
| The command was not found | `127` |

`simstim help exit-codes` and the [exit codes reference](../reference/exit-codes.md) show the full table.

`exec` needs a running walled sandbox. If the sandbox is stopped, wake it first with `simstim resume <host>/<session>`.

## Start another agent or background command in the same sandbox

Use `new --in` to start a second coding agent or a long-running command inside a running sandbox without restarting the sandbox:

<!-- test: help (needs a host) -->
```sh
simstim new my-server --in auth-fix --agent codex
```

You can also start a detached command in the same sandbox:

<!-- test: skip (needs a host, and --help after -- goes to the command) -->
```sh
simstim new my-server --in auth-fix --name watcher --detach -- npm run watch
```

Every installed agent on the host is mounted read-only into process-wall sandboxes at start, so a second agent can join right away. Its allowed model hosts are added to the running network policy.

Because the sandbox is already running, `new --in` refuses flags that would change the sandbox itself, such as `--wall`, `--dir`, `--worktree`, `--mount` or `--memory-max`.

## Stopping, waking and deleting

- `simstim stop <host>/<session>` stops the whole sandbox — the primary session and every child session inside it — and keeps their state for resume or restore.
- `simstim resume <host>/<session>` (or `attach` on a stopped sandbox) wakes the sandbox and starts its primary session and non-shell child sessions again.
- `simstim rm <host>/<child-session>` ends and removes only that child session. The primary session and sandbox keep running.
- `simstim rm <host>/<primary-session>` ends and removes the primary session and every child session inside its sandbox. At a terminal, the confirmation lists the child sessions that will also be removed.

## Choose your shell and keep history

In `~/.config/simstim/config.toml`, you can set which shell `shell` starts and which small data files in `/home/agent` survive a clean stop and restart:

```toml
[sandbox]
shell = "/bin/bash"
keep = [".bash_history", ".zsh_history"]
```

Only regular files under `/home/agent` are copied back and forth without following symlinks. Shell startup and hook files (such as `.bashrc`, `.profile`, `.zshrc`, `.zshenv` and `.claude/settings.json`) are refused with `keep_refused`. Run `simstim config check` to check your configuration.
