# Bring sessions back after the host restarts

When the host restarts, its processes stop, and so do the agents. At boot, a user unit that `host add` installed starts each session again, in its folder, with the agent's own resume command: Claude Code and Codex continue the same conversation. Above it, the session shows the screen it had before.

To do it by hand, for example when the host has no systemd user manager:

<!-- test: help (restarts sessions) -->
```sh
simstim host restore my-server
```

`--all` also restarts sessions whose holder stopped for another reason. A process that was running when the host stopped does not come back; the conversation does.

A session that had [secrets](secrets.md) comes back without them, because nothing on the host keeps them. Its terminal and the launcher say so; start a new session to pass them again.

Which agents resume, and with which command, is in the [agent table](../reference/agents.md).

## Bring back one session

`resume` starts the agent of one session again, whenever its agent has ended. That can be after the [memory cap](memory.md#when-the-cap-stops-an-agent) stopped it, after a crash or a quit, or after its holder was lost. In the launcher, select the session and press `u`.

<!-- test: help (resumes a session) -->
```sh
simstim resume my-server/app
```

Both start the agent with its own resume command, in the same session: the same ID, name, folder and worktree. Both run the session's [resume hook](setup-hooks.md) first, if it has one. They differ in these ways:

| | `host restore` | `resume` |
| --- | --- | --- |
| Sessions | Every session that a host restart stopped, or with `--all`, a crash | The one you name, however its agent ended |
| When | At boot, or when you run it | When you run it |
| Tries | Three for each session, then it skips the session | As many as you like. A resume also gives restore its three tries again. |
| Secrets | None: nothing can fetch them from your laptop | Fetched again on your laptop: those that `hosts.toml` gives the host, and those you pass with `--secret` ([secrets](secrets.md#after-a-resume)) |
| Alert | `restored` | `started`, as for a new session |
| First | Nothing to stop: the restart stopped it all | Stops the old holder. With systemd, it also stops any process that the agent left in the session. |
| At the same time | One at a time for each session, with a resume or a delete | The same: another one says that the session is busy |
| Memory cap | The size the session asked for, or the host's default | The same, or the size that `--memory-max` gives |
