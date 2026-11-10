# Research

The evidence behind the design, gathered between 14 October and 10 November 2026. Each note marks what it could not verify.

| Note | What it covers |
| --- | --- |
| [Amp UX and Orbs](amp-ux-and-orbs.md) | Amp's public docs, design history and Orbs, and what users say. Source of the UX patterns, from public material. |
| [Amp's TUI, observed](amp-tui.md) | The real Amp CLI watched in a terminal, cell by cell, plus its official images: layout, palette in hex, glyphs, spacing, keys, empty and error states. The reference for the launcher. |
| [Herdr](herdr.md) | The closest existing tool: architecture, fidelity limits, and what to borrow |
| [Host runtime](host-runtime.md) | zmx, libghostty-vt, the holder design, status detection, resume commands, the manifest |
| [Connectivity and providers](connectivity-and-providers.md) | System SSH, multiplexing, bootstrap, provider recipes, reconnect, security |
| [Client stack](client-stack.md) | Language and TUI framework, attach hand-off, input filter, clipboard, distribution |
| [Developer needs](developer-needs.md) | What developers want from remote and background agent sessions: 156 sources (132 from developers), 27 needs ranked by count and GitHub reactions, sentiment per tool, setup pains and paywalled features (15 October 2026) |
| [Competitors](competitors.md) | 28 hosted, sandbox and open tools in one feature matrix: prices, paid features, persistence, pass-through, and the paid features we can give for free (15 October 2026) |
| [Sandboxes and hosts](sandboxes.md) | E2B first, then Daytona, Modal, Fly, Morph, Coder, Codespaces, Hetzner, GCE, EC2 and local VMs: access, persistence, limits, prices, APIs and fit as a host (15 October 2026) |
| [Websites for developer tools](website.md) | 18 landing pages and docs sites of terminal and developer tools, measured: hero, motion, type, navigation, search, weight and accessibility, how ASCII and WebGL art is drawn, and braille in web fonts; then a second round: docs frameworks built against our docs, how 16 projects arrange their docs, words and space on 23 landing pages, and motion that explains (15 October 2026) |
| [An audit of the site](website-audit.md) | The first site, walked in Chrome on a laptop and a phone, in both themes: every problem in its navigation, density, type, motion and copy (15 October 2026) |
| [Go project structure](go-project-structure.md) | Thirteen well-run Go repos: code layout, tests, CI, releases, docs sites and public repo setup; our target layouts and a migration plan (15 October 2026) |
| [Isolated sessions](isolation.md) | A sandbox for each session, and hosts that the user adds: what a session can reach today, the threats and the 2025 and 2026 incidents, Docker Sandboxes and a hosted VM service, walls from namespaces to microVMs, doors, secrets and the supply chain. Scored by two Monte Carlo tree searches, with a red team, a fact check and a check of the weights. A second pass covers configurable policy, shells beside the agent and automatic classifiers in products and research (9 and 10 November 2026) |
| [The command line](cli-dx.md) | Today's commands and the planned ones, audited for names, flags, errors and help. Published CLI guidelines, the verbs of ten session and sandbox tools, and user research. The grammar and command map that the CLI plan builds (10 November 2026) |

A clean-room study of the Amp CLI binary, and the screens and images captured for [Amp's TUI, observed](amp-tui.md), are kept outside git (`private/`, ignored). They must never enter the history of this repo. Only what they show, described in our own words, appears in the notes and in [the UX spec](../contributing/ux-spec.md).
