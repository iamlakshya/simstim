# Troubleshoot

## Start here

```sh
simstim doctor
simstim doctor terminal
simstim host test my-server
simstim host doctor my-server
```

- `doctor` checks this computer: `ssh`, the config files, and the commands of your [secrets](secrets.md).
- `doctor terminal` asks your terminal what it supports: the keyboard protocol, colours, synchronized output.
- `host test` says whether the host answers, and how fast.
- `host doctor` checks the host: the helper, systemd and linger, the agents, and memory.

## Let the doctor fix it

Both doctors can fix the common problems they find. Each fix is shown first, with what it does and the command it runs, and runs only when you say yes:

<!-- test: help (it changes the host) -->
```sh
simstim host doctor my-server --fix
simstim doctor --fix
```

| Doctor | Fixes |
| --- | --- |
| `host doctor --fix` | Linger off, so sessions stop when you log out: turns it on with `loginctl`, without sudo. |
| | The unit that restores sessions after a reboot is missing, out of date or off: writes and enables it. |
| | Ghostty's terminfo is missing: compiles this computer's `xterm-ghostty` entry into `~/.terminfo` on the host. Run it from Ghostty, which has the entry. |
| | The `current` helper link is broken, so hooks and the restore unit cannot find the helper: points it at the helper in use. |
| | Session sockets with no session behind them: removes them. |
| | Old helper versions that nothing uses: removes them, keeping the newest three and every one in use. Each install does this by itself too. |
| `doctor --fix` | SSH configs generated for hosts you removed: removes them. |
| | ControlMaster sockets whose SSH master died, which can make connections hang: removes them. |

The doctor never uses sudo. When a fix needs root, for example linger on some hosts, it prints the command for you to run. `--yes` applies every fix without asking.

## Common problems

| What you see | Try |
| --- | --- |
| `cannot reach my-server` | Run `ssh my-server true`. If that fails, the problem is in SSH, not here. |
| Asked for a password every time | Load your key into an SSH agent, or use a key without a passphrase for this host. |
| A key does nothing in the agent | `simstim doctor keys` shows what your terminal sends. Some terminals keep keys for themselves. |
| An agent is "not installed" | `simstim agents install <agent> --host my-server` runs its installer in a session ([install agents](install-agents.md)). An agent installed by hand must be on your `PATH` in a login shell. |
| An agent asks you to log in | Run its own login in a session; `host doctor` prints the command. |
| Sessions stop when you log out | Linger is off: `host doctor` says so, and `host doctor --fix` turns it on. |
| The screen looks wrong after attaching | Detach and attach again. If it stays wrong, open an issue with your terminal's name and version. |
| A session `ended (out of memory)` | The session's memory cap stopped the agent. Start a new session in the same folder: `simstim new my-server --agent claude --dir ~/src/app`, or `n` in the launcher. An agent with a resume command can then pick up its conversation, for example with `/resume` in Claude Code. |
| "A process in this session ran out of memory and was stopped" | The cap stopped a process that the agent started, such as a build. The agent runs on, and usually reports the failed step itself. |

## Logs

On the host, each session keeps a log at `~/.local/state/simstim/sessions/<id>/holder.log`. Each line is one event: the time, a level (`INFO`, `WARN` or `ERROR`), a short message, and its details as `key=value` pairs:

```text
time=2026-10-15T09:12:03.512+00:00 level=WARN msg="snapshot failed, replaying the ring" err="the shadow terminal did not answer within 3s. Try: attach again"
```

The log never holds what the agent prints, or the value of a secret.

## Still stuck

Open an issue with the bug form, and include the output of the three commands at the top, with anything private removed.
