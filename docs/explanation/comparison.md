# How simstim compares

There are many ways to keep a coding agent working while your laptop is closed. This page says where simstim fits among them, and when another tool is the better choice. The facts come from [the competitor research](../research/competitors.md), read on 15 October 2026, which has a source for each one. Products change fast: in 2025 and 2026 several shut down, changed their licence or changed hands.

## What simstim is for

simstim runs any terminal agent in a session on a Linux host you own, and passes the agent's own screen to your terminal byte for byte. Four things together make it different:

1. **Any agent.** Claude Code, Codex, opencode, Antigravity CLI or any other terminal program. Agents are rows in a table, not code paths.
2. **The real terminal UI.** No multiplexer or web page re-draws the screen, so the kitty keyboard protocol, truecolour, OSC 8 links, OSC 52 clipboard and desktop notifications work as they do locally.
3. **Sessions that last.** They survive sleep, a change of network and a host reboot, and you get back exactly the output you missed.
4. **Your own machines.** Any host you can reach with `ssh`. No account, no relay, and no new open port.

The research found no other tool with all four.

## Terminal multiplexers: tmux, Zellij, screen

The classic way: start the agent inside tmux on a server, and reattach over SSH.

- **They re-draw the screen.** A multiplexer keeps its own model of the terminal and draws that, not the agent's bytes. Its scrollback replaces your terminal's, so `Cmd+F` finds nothing. tmux 3.8 has no kitty keyboard protocol, so Shift+Enter arrives as Enter.
- **Their prefix keys clash** with the agents' own keys.
- **They know nothing about agents.** No status, no "needs input", no resume after a reboot.

**Choose a multiplexer** if you want split panes on the host, or already live in tmux and do not miss the things above. simstim does not split panes; your terminal can.

## Pass-through session tools: zmx, Eternal Terminal, mosh

zmx and Eternal Terminal pass the bytes through and survive a dropped connection, much like simstim does. mosh predicts your typing on slow links.

- zmx and Eternal Terminal lose their sessions when the host reboots, and they do not manage agents.
- mosh has no kitty keyboard protocol, no scrollback and no OSC 9 or 777 notifications.

**Choose mosh** for a very slow or lossy link, where its local echo matters more than fidelity.

## Hosted agents

Amp, Claude Code cloud sessions, Codex cloud, Cursor cloud agents, Jules, Devin, Factory and Warp run an agent for you on the vendor's machines, in a web or app UI.

- **One vendor's agent** in each, in the vendor's own interface rather than the agent's terminal UI.
- **Limits and plans.** Several cap session length or parallel tasks, or need a paid plan for background work.
- **They give you more out of the box:** a phone or web client, sharing with a team, and a fresh sandbox for each task.

**Choose a hosted agent** if you want to start tasks from your phone or browser, share them with others, or never run a machine yourself.

## Vendor agents on your own machine

Several vendors now run their agent on a machine you own, with their control plane: Amp runners, Claude Desktop's SSH sessions, Codex's SSH hosts, Cursor's My Machines, Devin Outposts, and Factory's and Warp's Remote Control.

- The machine is yours, but the account, the agent and the interface are the vendor's.
- Each works with one agent.

**Choose one of these** if you use a single vendor's agent and want its app on every device.

## Cloud development environments: Coder, Codespaces, Daytona, DevPod

These give each developer a workspace on a server, and you reach it with SSH or an editor.

- Workspaces stop when idle, and a session needs tmux to survive a disconnect, which brings the multiplexer's limits back.
- Coder's agent features now need its own agent and API keys. DevPod has stalled. Daytona went closed source in June 2026.

**Choose a cloud development environment** if a team needs the same workspace templates, access control and audit across many people.

## Where simstim is behind

simstim is young, and some things are missing on purpose or not yet built:

- **No phone or web client.** You need a terminal. An alert can reach your phone ([alerts](../how-to/alerts.md)), but you answer from a terminal.
- **No sharing.** A session belongs to one user's account on the host.
- **You bring the host.** `host create` can make a local VM, a test container or an E2B sandbox ([create a host](../how-to/create-a-host.md)); for anything else you need a Linux host with SSH.
- **Isolation is per worktree, not per machine.** Each task can have its own branch and folder ([worktrees](../how-to/worktrees.md)), but tasks on one host share it.
- **Young.** The first release, 0.1.0, came out in October 2026.

## Summary

| You want | A good fit |
| --- | --- |
| Any agent's real terminal UI, on your own host, lasting through sleep and reboots | simstim |
| Split panes on a server, and you live in tmux | tmux or Zellij |
| A usable shell over a very slow link | mosh |
| Tasks from your phone or browser, shared with a team | A hosted agent |
| One vendor's agent and app, on your own machine | That vendor's remote option |
| The same workspaces for a whole team, with access control | Coder or another cloud development environment |
