# How it works

The ideas behind simstim, in plain words, in the order you meet them. Each one links to the guide that uses it. [The architecture](architecture.md) has the details.

```text
 your laptop                             your host (Linux)
┌────────────────┐                      ┌───────────────────────────────────┐
│ client         │      your ssh        │ helper                            │
│  the launcher  │ ───────────────────▶ │  ├─ holder ── claude   a session  │
│  and commands  │                      │  ├─ holder ── codex    a session  │
└────────────────┘                      │  └─ holder ── zsh      a session  │
                                        └───────────────────────────────────┘
```

## Host

A host is a Linux machine you can reach with `ssh`: a VPS, a cloud VM, a machine at home, or a VM on your laptop. simstim connects with your own OpenSSH and reads your `~/.ssh/config`, so jump hosts, keys, certificates and Tailscale work as they do for `ssh`. It never edits that file.

[Add a host](../how-to/add-a-host.md) connects one you have. [Create a host](../how-to/create-a-host.md) makes one for you.

## The helper

When you add a host, the client copies a small program to it: the helper, `simstim-host`, in `~/.local/share/simstim/bin/`. The helper checks the host, starts and stops sessions, and carries your bytes while you are connected.

The helper opens no port. Each time the client connects, it starts the helper through your SSH connection, and the helper stops when the connection ends.

## Session

A session is one program, an agent or a shell, running on a host in a terminal of its own. It does not belong to your SSH connection. So it keeps running when you leave, when your laptop sleeps, and when the network drops.

Each session has a name, a folder and an agent. This starts one and puts you in it:

```sh
simstim new my-server --agent claude
```

## Holder

One process on the host keeps each session alive: its holder. The holder owns the agent's terminal, keeps the last 8 MiB of what the agent wrote, and works out what the agent is doing.

Each holder runs as a systemd user service with a memory cap. All sessions together leave a reserve free for the system, so agents cannot take down the host ([memory](../how-to/memory.md)). When the host restarts, it starts each holder again, and the agent picks up its conversation with its own resume command ([restore after a reboot](../how-to/restore-after-reboot.md)).

## Attach and detach

To attach is to connect your terminal to a session. From then on the session owns your whole terminal. simstim draws nothing of its own, and passes bytes both ways without changing them.

Only two keys belong to simstim while you are attached:

- `Ctrl+\` detaches. The session keeps running. Press it twice to send it to the agent.
- `Ctrl+V` with an image on your clipboard [pastes the image](../how-to/image-paste.md) into the agent.

You can [change both keys](../how-to/rebind-keys.md).

## Byte for byte

A terminal multiplexer such as tmux keeps its own copy of the screen, and draws that copy on your terminal. Some features do not survive the copy: tmux 3.8 has no kitty keyboard protocol, so the agent cannot tell Shift+Enter from Enter.

simstim draws no copy. Your terminal gets the agent's own bytes. So Shift+Enter, truecolour, links, the clipboard, notifications and your terminal's own scrollback and search all work as they do on your laptop. [The fidelity contract](fidelity.md) lists what this covers, and the test behind each part.

## Reconnect and replay

If the connection drops, because the laptop slept or the network changed, the client reconnects by itself. It tells the holder the last byte it saw, and the holder sends everything after it. Your scrollback goes on with nothing lost and nothing twice.

If you come back from another machine, or after so long that the holder no longer has that byte, the screen is drawn again from the shadow terminal. So is a full-screen agent, such as Claude Code in its fullscreen mode, when you attach again after a detach: your terminal does not keep a full-screen program's screen once you leave it.

## Shadow terminal

Next to each holder runs a small terminal of its own, `simstim-vt`, which reads everything the agent writes and remembers the screen. It draws the current screen for a client that attaches fresh, and answers the agent's questions to the terminal while nobody is attached. A build without it replays the raw output instead.

## The launcher

Run `simstim` with no arguments for the launcher. It lists your hosts and their sessions. Each session shows its agent, its folder, its state, and a braille trace of its output over the last half minute. `/` searches every host, and Enter attaches. [The keys](../reference/keymap.md) lists the rest.

## State

The launcher shows what each agent is doing: working, needs input, idle or exited. The holder reads the state from the agent's own hooks, from rules that read the agent's screen, and from the process itself.

When another session needs you, your laptop shows a notification. With the laptop shut, the host can send an [alert](../how-to/alerts.md) to your phone instead.

## Agents

An agent is any program that runs in a terminal. Some agents get more: simstim knows how to start them, how to resume them after a reboot, and how to read their state. That knowledge is data, in [the agent table](../reference/agents.md), not code, so adding an agent is one row. [Install agents](../how-to/install-agents.md) sets them up on a host.
