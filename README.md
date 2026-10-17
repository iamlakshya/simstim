# simstim

**Your coding agents, on your hosts, in your own terminal.**

Run Claude Code, Codex, opencode or any terminal program on a Linux host you own. Detach, close your laptop, or change networks without stopping the session. Come back to the agent's real terminal output, not a web UI or a re-rendered pane.

[Get started](docs/tutorials/getting-started.md) · [Documentation](https://simstim.astrel.sh) · [Releases](https://github.com/iamlakshya/simstim/releases) · [Get help](SUPPORT.md) · [Contribute](CONTRIBUTING.md)

[![CI](https://github.com/iamlakshya/simstim/actions/workflows/ci.yml/badge.svg)](https://github.com/iamlakshya/simstim/actions/workflows/ci.yml)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/iamlakshya/simstim/badge)](https://scorecard.dev/viewer/?uri=github.com/iamlakshya/simstim)

> **Early release.** Before 1.0, minor versions may change the CLI, configuration or protocol. Read the [changelog](CHANGELOG.md) and [version policy](docs/contributing/releasing.md#versioning) before upgrading. See [testing](docs/contributing/testing.md) for the checks and their limits.

## Install

Release binaries support **macOS and Linux, on amd64 and arm64**. You need OpenSSH and curl. Agents run on a **Linux host**: an existing SSH server, a local Lima VM, a Docker container for testing, or an E2B sandbox. Agent accounts and provider charges are your own.

```sh
curl -fsSL https://raw.githubusercontent.com/iamlakshya/simstim/main/install.sh | sh
export PATH="$HOME/.local/bin:$PATH"
simstim version
```

The installer verifies the release's SSH signature and SHA-256 checksum, then puts the client in `~/.local/bin`. Prefer to inspect the script first? [Read the installer](install.sh), or download an archive from [Releases](https://github.com/iamlakshya/simstim/releases) and [verify it by hand](docs/contributing/releasing.md#checking-a-release).

With Go installed, you can instead run:

```sh
go install github.com/iamlakshya/simstim@latest
```

The Go client downloads and verifies its matching host helpers on first connection. See [installation, updates and mirrors](docs/how-to/install.md). The Homebrew tap is not published yet.

## Your first session

Try the launcher without a host or network connection:

```sh
simstim demo
```

Then run the guided setup:

```sh
simstim setup
```

It connects a host or creates one with a provider, asks before installing the host helper, and offers the agents' official installers and login flows. You choose where the agents run; simstim has no account or relay service.

For an existing SSH host, here is the manual route. Replace `my-vps` with an alias from your `~/.ssh/config`:

```sh
simstim host add my-vps
simstim new my-vps --name first
```

This opens a persistent shell on the host. **Press `Ctrl+\` to detach**, then reconnect to the same session:

```sh
simstim attach my-vps/first
```

To run an installed agent instead:

```sh
simstim new my-vps --agent claude --name coding
```

Run `simstim` with no arguments to browse hosts and sessions. **Enter** opens the selected row, **`n`** starts a session, **`/`** searches across hosts, and **`?`** shows the keys. While attached, press the detach key twice to send it to the agent. **`Ctrl+V`** uploads an image from your clipboard; without an image, the key passes through.

The [getting-started tutorial](docs/tutorials/getting-started.md) covers setup, the first agent, reattachment and cleanup. [Your first cloud host](docs/tutorials/first-cloud-host.md) starts with renting a server.

## What you get

| Capability | How it works |
| --- | --- |
| **The agent's real terminal** | Live output passes through unchanged: kitty keyboard sequences, truecolour, links, clipboard operations and notifications. No panes, prefix layer or replacement agent UI. |
| **Sessions that outlive the connection** | A host-side holder owns each agent's terminal. The client reconnects after a dropped link and catches up by byte offset when the retained output allows it. |
| **Your hosts and SSH configuration** | Use OpenSSH with ProxyJump, Tailscale, IAP or SSM. Created Docker and E2B hosts use their provider's exec transport. No new network listener or telemetry. |
| **Image paste and file drops** | Clipboard images and dropped local files upload to the session; the agent receives their remote paths. |
| **A worktree per task** | `new --worktree` creates a separate branch and folder. Removing a session keeps a worktree that has changes or new commits. |
| **Status and alerts** | Sessions report working, needs input, idle or ended. Optional host-side alerts go to your own command, ntfy topic or webhook, even with the laptop shut. |
| **Recovery and resource limits** | With systemd, session services have memory caps and can resume eligible conversations after a host reboot. |

### Know the limits

- **Trust the host.** Live escape sequences are not sanitised. Clipboard and link behaviour depends on your terminal's policy. There is no untrusted-host filter or read-only attach mode. See the [security model](docs/explanation/security.md).
- **Replay is bounded.** Each holder keeps 8 MiB of raw output. Outside that window, the shadow terminal restores its screen and retained scrollback, not an unlimited transcript. Kitty graphics and sixel images are not restored. See the [fidelity contract](docs/explanation/fidelity.md).
- **Scrollback depends on the agent.** A full-screen agent uses its own alternate screen. Use `new --inline` where the agent supports it to keep your terminal's native scrollback and search.
- **A reboot ends running processes.** Restore starts the agent again with its resume command where possible; it does not preserve the turn that was running. Sessions started with secrets need those secrets again. See [restore and resume](docs/how-to/restore-after-reboot.md).
- **Memory caps need systemd.** Without a systemd user manager, the holder runs detached but has no systemd memory cap. Docker support is experimental. Multiple attached terminals can answer the same query; multi-device attach is not a read-only sharing feature.

## How it works

![The local client connects over SSH or provider exec to a bridge on the Linux host. A persistent holder owns the agent's PTY, output ring and shadow terminal independently of the connection.](docs/images/architecture.svg)

The bridge lives for the connection; the holder lives for the session. While attached, the local terminal renders the agent's live bytes. On a fresh attach, a separate Ghostty-based shadow terminal can reconstruct the screen. It does not re-render live output.

[How it works, in plain words](docs/explanation/how-it-works.md) · [Architecture and protocol](docs/explanation/architecture.md) · [Full-size diagram](docs/images/architecture.svg) · [Editable Mermaid source](docs/images/architecture.mmd)

## Agents

Any terminal program can run unmodified in a session. The built-in [agent table](docs/reference/agents.md) adds launch and resume commands, setup instructions, hooks and screen rules:

| Support | Agents | Promise |
| --- | --- | --- |
| First-class | Claude Code, Codex | Tested end to end on releases, with status hooks, conversation resume and installation support. |
| Supported | opencode, Antigravity CLI, Shell | In the table; tested when available on the test host. Status uses the signals and rules available for that agent. |
| Best effort | Gemini CLI, Amp, Cursor CLI, Aider, Goose | In the table; contributors keep them working. |

Agent authentication stays with the agent on the host. simstim does not provide model access or bypass agent permissions. [Install and log in to agents](docs/how-to/install-agents.md).

## Daily use

| Task | Start here |
| --- | --- |
| List sessions or attach from a script | `simstim ls --json`, `simstim attach my-vps/first` |
| Work on a separate branch | [Worktrees](docs/how-to/worktrees.md) |
| Bring the remote branch home | [Pull a session's branch](docs/how-to/pull.md) |
| See or forward a development port | [Ports](docs/how-to/ports.md) |
| Know when an agent needs you | [Alerts](docs/how-to/alerts.md) |
| Pass secrets without storing their values | [Per-session secrets](docs/how-to/secrets.md) |
| Diagnose a connection or terminal problem | [Troubleshooting](docs/how-to/troubleshoot.md), `simstim doctor`, `simstim host doctor my-vps` |
| Share diagnostics for a bug report | [Debug a session](docs/how-to/debug-a-session.md) |

### A closer look

![A host's sessions show their state, agent, folder and recent activity, with the selected session's reason below](docs/images/sessions.svg)

<details>
<summary>More launcher screens</summary>

**Start a session:** `n` opens one line with agent and remote-folder completion; `Ctrl+F` opens the full form.

![The quick-start line completes a remote folder and offers the host's installed agents](docs/images/new-session.svg)

**Search all hosts:** `/` finds sessions by name, agent, folder or host.

![Cross-host session search with matching text highlighted](docs/images/search.svg)

**Discover commands:** `Ctrl+O` opens the command palette.

![The command palette lists actions and their keyboard shortcuts](docs/images/commands.svg)

**Attach:** the agent gets the whole terminal, without a frame or status bar.

![An attached agent fills the terminal, without a frame or status bar](docs/images/attached.svg)

</details>

The screens use your terminal's colours. These images come from the offline demo; [the tapes and screenshot tool](docs/contributing/docs.md#images) make them reproducible. Watch the [launcher recording](docs/images/launcher.svg) or the [attach recording](docs/images/attach.svg) for the full interaction.

## Documentation and help

- **Learn:** [Getting started](docs/tutorials/getting-started.md), [how-to guides](docs/how-to/README.md), [common questions](docs/faq.md).
- **Look up:** [commands, configuration, keys and agents](docs/reference/_index.md), generated from the code.
- **Understand:** [design principles](docs/explanation/design.md), [comparison with other tools](docs/explanation/comparison.md), [security](docs/explanation/security.md).
- **Get help:** [Support](SUPPORT.md) explains questions, bug reports and scope. Maintainers respond as time allows; there is no support contract.
- **Report a vulnerability privately:** use the route in [SECURITY.md](SECURITY.md), not a public issue. Security fixes go to the latest minor release.

## Contributing

Start with [CONTRIBUTING.md](CONTRIBUTING.md) for prerequisites, local setup and pull requests. On macOS or Linux, the setup installs the pinned tools and builds and tests once:

```sh
git clone https://github.com/iamlakshya/simstim
cd simstim
scripts/setup.sh
export PATH="$PWD/.tools/bin:$PATH"
just test lint
```

Useful starting points: [good first issues](https://github.com/iamlakshya/simstim/labels/good%20first%20issue), [agent support](internal/agents), [contributor docs](docs/contributing/README.md), and the [roadmap](docs/roadmap.md). Please discuss large changes before implementing them.

Read the [Code of Conduct](CODE_OF_CONDUCT.md), [governance](GOVERNANCE.md) and [maintainer list](MAINTAINERS.md). Contributions use the same licence as the project; no CLA or sign-off trailer is required.

## Licence

[Apache-2.0](LICENSE). Third-party attributions are in [NOTICE](NOTICE).

The working name comes from William Gibson's *Neuromancer*. The code keeps it in [one place](internal/brand), so it can change without renaming the architecture.
