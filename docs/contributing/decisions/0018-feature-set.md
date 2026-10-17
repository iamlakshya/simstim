---
status: accepted (its Now list extended by 0025)
date: 2026-10-15
---

# 0018 The feature set: what we build now, next and later

**Status:** Accepted. The owner said yes at Gate 1 on 15 October 2026.

## Context

v1 and v2 built the core: any agent, any SSH host, the real screen byte for byte, sessions that survive sleep and reboots. The next step must follow what developers ask for, not what other products ship. Four research notes now give the evidence: [developer needs](../../research/developer-needs.md) (132 developer sources, ranked), [competitors](../../research/competitors.md) (28 tools, with their paid features), [sandboxes](../../research/sandboxes.md) and [Go project structure](../../research/go-project-structure.md).

## Options

1. **Follow the competitors:** build what most hosted tools have (sharing, snapshots, managed secrets, a web UI).
2. **Follow the goal's candidate list as written:** snapshots, port previews, watch and sharing, recording, background runs, devcontainers and setup hooks.
3. **Score every candidate** by demand, impact and effort, and build what scores highest, plus what the goal requires.

## Decision

Option 3. [The roadmap](../../roadmap.md) scores each feature as demand (from source counts and reactions) times impact, divided by effort.

- **Now:** the goal's required setup work (one-command install, `host create` for a local VM and for cloud providers, idle stop and wake, `agents install`, per-session secrets, `doctor` fixes), and every feature that scores 4 or more: Antigravity CLI in the agent table, scrollback hints per agent, a "who needs me" jump, a phone path through any SSH app, a host-side alert hook, repo setup hooks, and `pull <session>`.
- **The phone path and the alert hook** are in: the goal keeps phones out unless the owner agrees, and the owner agreed at Gate 1. They are the top need (40 sources) and need no new app, no relay and no web UI.
- **Next and later** are GitHub issues. Sharing, recording, port previews and snapshots score low: the sample has almost no developer asking for them.

## Consequences

- Good, because each feature built now has evidence behind it, with links, and the scores can be checked and changed.
- Good, because the cheap, high-demand features come first.
- Bad, because demand comes from public posts, which over-represent some tools and users. The developer-needs note lists the gaps.
- Bad, because some goal candidates (snapshots, sharing) move to later, where competitors already sell them.
