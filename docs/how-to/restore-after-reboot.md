# Bring sessions back after the host restarts

When the host restarts, its processes stop, and so do the agents. At boot, a user unit that `host add` installed starts each session again, in its folder, with the agent's own resume command: Claude Code and Codex continue the same conversation. A session that runs a command of its own runs that command again ([what comes back](#what-comes-back)). Above it, the session shows the screen it had before.

To do it by hand, for example when the host has no systemd user manager:

<!-- test: help (restarts sessions) -->
```sh
simstim host restore my-server
```

`--all` also restarts sessions whose holder stopped for another reason. A process that was running when the host stopped does not come back; the conversation does.

A session that had [secrets](secrets.md) comes back without them, because nothing on the host keeps them. Its terminal and the launcher say so; start a new session to pass them again.

## What comes back

What a session runs again depends on how it started. `host restore` and `resume` follow the same rules, each time they bring a session back.

| The session started with | It comes back with |
| --- | --- |
| An agent from the [agent table](../reference/agents.md), such as `--agent claude` | The agent's resume command, with the agent's own session ID when the host knows it. Otherwise, its continue command, which picks up the latest conversation in the folder. |
| The shell: `--agent shell`, or neither `--agent` nor a command | A new login shell |
| A command of its own: `new <host> -- <command>`, or "other…" in the launcher | The same command, from its start, after any number of restores and resumes. With `--agent`, one case differs: see below. |

A session with an agent from the table and a command of its own can keep its conversation. That needs the agent's resume command and the agent's session ID. The host learns the ID from the agent, or chooses it for an agent such as Claude Code. Your command must then pass the chosen ID with the `{session_uuid}` placeholder, as in `--agent claude -- claude --session-id {session_uuid} --model opus`. With the ID, the agent's resume command runs. It drops the extra arguments of your command, such as `--model opus`, as it did before. Without the ID, your command runs again and starts a new conversation.

The agent table lists each agent's resume command. An older helper did not keep a session's own command. So a session that it started with `new <host> -- <command>` and no `--agent` still comes back as a login shell. Start such a session again with `new` to keep its command.

## Bring back one session

`resume` starts the agent of one session again, whenever its agent has ended. That can be after the [memory cap](memory.md#when-the-cap-stops-an-agent) stopped it, after a crash or a quit, or after its holder was lost. In the launcher, select the session and press `u`.

<!-- test: help (resumes a session) -->
```sh
simstim resume my-server/app
```

Both start the agent again as [what comes back](#what-comes-back) says, in the same session: the same ID, name, folder and worktree. Both run the session's [resume hook](setup-hooks.md) first, if it has one. They differ in these ways:

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
