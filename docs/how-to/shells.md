# Shells and commands in a sandbox

Open a shell beside an agent:

```sh
simstim shell my-vps/auth-fix
```

It uses the same wall, workspace, files and network policy. It runs as the
sandbox's `agent` account. The agent sees files that you change in the shell.
In the launcher, select a session and press `t`.

The command attaches to the newest running shell. Use `--new` for another
terminal, and `--name` to name a new terminal:

```sh
simstim shell my-vps/auth-fix --new --name inspect
```

Each terminal has its own screen, status and agent session ID. The launcher
puts the first session above its other terminals. `ls --json` includes
`sandbox_of` for each member.

## Run one command

```sh
simstim exec my-vps/auth-fix -- git status
simstim exec my-vps/auth-fix --timeout 30s -- make test
```

Arguments after `--` go directly to the program. To use a pipeline, name a
shell explicitly, for example `-- sh -c 'printf out; printf err >&2'`.
Standard output and standard error stay separate. Input and output bytes,
including escape sequences and NULs, pass unchanged.

The default timeout is ten minutes. A timeout kills the command's process
group. Closing the client also ends its command. Ctrl+C forwards an interrupt;
the client bounds the wait before killing a command that does not end.

| Result | Exit code |
| --- | --- |
| Program exits | The program's code |
| A signal ends it | 128 plus the signal number |
| Timeout | 124 |
| Connection, policy or start failure | 125 |
| Program exists but cannot run | 126 |
| Program is missing | 127 |

A stopped sandbox reports `sandbox_stopped`. Resume it before using `exec`.
Commands run inside the wall as the agent. They do not fall back to a command
on the host when a wall or helper refuses them.

## Add another agent or command

```sh
simstim new my-vps --in auth-fix --agent codex
simstim new --in my-vps/auth-fix --detach -- npm run dev
```

`new --in` accepts `--agent`, `--name`, `--task`, `--detach` and a command.
`--json` requires `--detach`. Flags that change the sandbox, including a size,
wall, mount, profile, setting or new secret exposure, are usage errors. The
sandbox keeps its current applied policy when another terminal joins.

After a host shutdown, automatic restore brings back the members that were
active before the shutdown. A primary agent that had already ended stays
ended while its active children return. An explicit resume remains a separate
request to start saved commands again.

`--image`, `--keep-system` and `--docker` set the new sandbox's session layer.
Use them with an ordinary new sandbox. They cannot change a sandbox through
`new --in`. Docker and sudo require a microvm wall.

Secret values still need this laptop's original creation permission. A row
from a host cannot add that permission. A stopped wake checks the current
repository policy before fetching values, then restores the saved commands.
A command you started with `new --in` comes back with that command. A shell
gets a fresh prompt.

## Stop, remove and retain files

`stop` on any member stops the whole sandbox and lists its sessions. Its
workspace and configured state stay. Removing a child terminal removes only
that terminal. Removing the first session destroys the sandbox and its other
terminals after listing them and asking. If another terminal joins after the
list was shown, removal refuses so you can review the new group.

The home resets at each start. Keep ordinary home files in `config.toml`:

```toml
[sandbox]
shell = "/bin/bash"
keep = [".bash_history"]
```

An empty `shell` uses the image's login shell. The executable must exist in
the guest. Kept files use the same restore and clean capture as agent state.
Shell startup files such as `.bashrc`, `.profile`, `.bash_profile`, `.zshrc`
and `.zprofile` are refused with `keep_refused`.

An older, unwalled session has no sandbox. For that deprecated case, `shell`
starts a host shell in the session's folder and says so before attaching. A
locally recorded wall cannot be changed to that mode by a host's list row.
