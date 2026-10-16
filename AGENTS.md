# AGENTS.md

Short rules for AI agents that work on this repo. People and agents follow the same rules: [CONTRIBUTING.md](CONTRIBUTING.md) and [docs/contributing/](docs/contributing/README.md) have the full guide.

## What this repo is

A client and a host helper that run AI coding agents in sessions on hosts the user owns, and show the agent's real terminal UI locally, byte for byte. Start with [the design page](docs/explanation/design.md) and [the architecture](docs/explanation/architecture.md).

| Path | Contents |
| --- | --- |
| `main.go`, `internal/cli` | The client's main and its command tree |
| `cmd/host` | The host helper, which the client installs on hosts |
| `cmd/docgen` | Writes `docs/reference/` from the code (`just docs`) |
| `internal/brand` | The product name. **The only place that spells it.** |
| `internal/client` | Config, SSH, bootstrap, the attach pump, the clipboard |
| `internal/host` | Holder, bridge, session operations, doctor |
| `internal/alerts` | Alerts and event hooks on the host: events, destinations, noise control |
| `internal/bundle` | The debug bundle: what it gathers, its replacements and its last check, the archive |
| `internal/agents` | The agent table, as data |
| `internal/tui` | The launcher (Bubble Tea v2) |
| `internal/proto`, `ring`, `term`, `keys` | Wire protocol, output ring, terminal modes, input filter |
| `test/e2e`, `test/ttyshot` | End-to-end tests, and a tool that renders a TUI |
| `docs/` | User docs (tutorials, how-to, reference, explanation), contributor docs, research, roadmap |
| `site/` | The docs site (Astro with Starlight; built from `docs/` by `cmd/docgen -site`) |

## Rules

1. **Never spell the product name** outside `internal/brand`, the docs and the module path. `just check-name` enforces this ([renaming](docs/contributing/renaming.md)).
2. **Byte-exact while attached.** Only the detach and image-paste keys may be intercepted. Never send RIS (`ESC c`) to the user's terminal.
3. **Agents are data** in `internal/agents`.
4. **Nothing listens, nothing phones home.** No new ports, relays or telemetry.
5. **Clean-room and public-safe.** Never copy code, prompts or assets from closed-source tools. Private material (goals, status notes, logs, host names, addresses, personal paths) stays in the git-ignored `private/` and `docs/research/private/` folders; `just check-private` checks the rest.
6. **Decisions** go in `docs/contributing/decisions/`. Do not edit an accepted record; replace it with a new one.
7. **Errors** carry a message and a "Try:" hint.
8. **Docs ship with the change:** the docs and a CHANGELOG line in the same commit, and `just docs` after changing a command, a flag, a config key or a key. Plain English, short sentences, British spelling.
9. **Commits:** one logical change each. Run `just test lint` first. The message is a subject, a blank line and a body, and nothing more: no `Signed-off-by:`, `Co-Authored-By:` or any other trailer, and never `git commit -s` ([0010](docs/contributing/decisions/0010-no-commit-trailers.md)).
10. **Test hosts are other people's machines.** Log every change you make on one in a log outside the repo, and ask the host's owner before a reboot.

## Commands

`just` lists them all. The ones you need most: `just setup` (tools), `just build`, `just test`, `just lint`, `just docs`, `just docs-check`, `just e2e` (with `E2E_HOST` set), and `just screenshots`. [Testing](docs/contributing/testing.md) explains each.
