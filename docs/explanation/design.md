# Why it exists, and its design rules

What the project is for, what it will not do, and the rules that settle trade-offs. [The architecture](architecture.md) says how it works; [the roadmap](../roadmap.md) says what comes next.

## In one paragraph

simstim lets you run any coding agent in a session on a host you own, on any provider, and use it from your laptop as if it ran there. The agent can be Claude Code, Codex, opencode, Antigravity CLI or any other terminal program. Close the laptop, change networks or switch devices: the session keeps running on the host. When you come back, you flip straight back in. Your own terminal draws the agent's real screen, byte for byte, so keys, colours, scrollback, clipboard and notifications work as they do locally.

The name comes from William Gibson's *Neuromancer* (1984). Simstim lets Case flip into Molly's senses, live, from far away, as if he were there.

## Why build it

Today all work is tied to one laptop. If the lid closes or the owner travels, the agent stops.

Research in October 2026 found no tool that meets the whole brief:

| Tool | What breaks the brief |
| --- | --- |
| tmux, Zellij, screen | Their own scrollback replaces the terminal's (no Cmd+F). Their prefix keys clash with agent keys. tmux has no kitty keyboard protocol (still none in 3.8). Zellij passes OSC 9 and 777 notifications on since 0.45, but redraws the screen. |
| mosh | No kitty keyboard protocol (Shift+Enter becomes Enter), no scrollback, no OSC 9/777 notifications. |
| Herdr | A full multiplexer. It redraws panes from its own terminal model, direct attach uses the alternate screen, and it drops OSC 52 and OSC 9/777. Pull requests from outside are closed automatically. |
| Web and phone UIs (CloudCLI, Happy, Paseo) | They replace the agent's real interface. Several had critical security bugs in 2026. |
| Amp Orbs, Claude Remote Control, Codex remote | One vendor's cloud and one agent each. |
| Coder, DevPod, Daytona | Coder's agent features now need its own agent and API keys. DevPod has stalled, and Daytona went closed source in June 2026 ([competitors](../research/competitors.md)). |

[How simstim compares](comparison.md) goes through each kind of tool, and says when to choose one instead.

The gap: **any agent, on any host you can reach by SSH, with the agent's bytes passed through untouched, sessions that survive sleep, and image paste that works remotely.**

## Goals

The brief the project started from:

1. Install the CLI on any device.
2. At setup, connect a host: a VM on any provider (GCP, AWS, a homelab, a VPS).
3. Browse connected hosts.
4. Select a host and press Enter to start a session.
5. View running sessions, delete them, select one and interact with it.
6. After exit, the session keeps running.
7. Run Codex, Claude Code or any other agent, with full access to the session runtime, as if it ran on the device.
8. See the actual colours and TUI, as if it ran natively.

Added along the way:

9. The client reconnects by itself after sleep or a network change, and shows the output that was missed.
10. Ctrl+V pastes a screenshot from the laptop's clipboard into a remote agent.
11. The session list shows which sessions are working, which need input and which have finished.
12. One command attaches straight to a session, for scripts and terminal keybindings.
13. One agent cannot take down the host: each session has a memory cap, and all sessions together leave a reserve free for the system.
14. simstim's own screens feel as good as the Amp CLI: calm, fast, keyboard first.

## Non-goals

- **Not a terminal multiplexer.** No panes, splits or tabs. The terminal does that. Amp removed its TUI sidebar in August 2026 for the same reason.
- **Not an agent or an agent wrapper.** simstim never re-renders an agent, sends prompts for it or changes its permissions.
- **No cloud service.** No account, no relay and no telemetry.
- **No phone app or web client.** A phone can reach a session through an ordinary SSH app (scaffolded, not launched yet: [0019](../contributing/decisions/0019-alerts-and-hooks.md)).
- **Hosts are yours.** `host create` makes one on a provider you choose, with your own account; simstim runs no machines of its own.
- **No promise that a process survives a host reboot.** v2 brings the conversation back, not the turn that was running.

## Principles

Ranked. Use them to settle trade-offs.

1. **Byte-exact while attached.** simstim adds no modes, colours or key handling. It watches for two keys only: detach, and Ctrl+V when the clipboard holds an image.
2. **The session outlives the client.** A closed tab, a crash, sleep or a network change never stops a session.
3. **Plain OpenSSH is the only network path.** If `ssh host` works, simstim works.
4. **Any agent.** What simstim knows about each agent is data in a table, not code paths.
5. **Nothing listens and nothing phones home.** No open ports on hosts, no relay, no telemetry, no stored secrets.
6. **Amp-grade screens.** Colours come from the terminal's own palette. Keyboard first. Every error suggests a next step.
7. **Small and boring.** One binary on each side. Files, not databases. Everything carries a version.

## Risks

| Risk | Mitigation |
| --- | --- |
| libghostty-vt has no stable API and no tags | Pin one Ghostty commit. Keep the interface tiny (bytes in, resize, snapshot, events out). Run it in its own process. Replay tests on every bump. |
| Agents change their signals and flags often | Keep agent knowledge as data. Show the source of each status in the UI. Test against recorded fixtures. |
| A terminal or agent update breaks fidelity | The fixture suite and `doctor keys` catch it. Pin and test before release. |
| Provider tunnels drop sessions (IAP after 1 hour idle, EICE at 1 hour, Cloudflare on edge restart) | Auto-reconnect with replay. Warn about each recipe's limit in the host details view. |
| Scope creep towards a multiplexer or an agent UI | Principles 1 and 4, and the non-goals. |
| Clean-room and licence mistakes | No code or assets from closed tools, ever. Attribution in NOTICE for anything taken from open projects. A review before going public ([going public](../contributing/going-public.md)). |
| Bus factor of one | Small code base, data-driven agent table, good tests, plain docs. |
| A compromised host attacks the client through escape sequences | An "untrusted host" mode that filters OSC 52, OSC 8 and report sequences. Never forward the SSH agent by default. |
