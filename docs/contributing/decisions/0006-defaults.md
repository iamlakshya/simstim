---
status: accepted (MemoryHigh part replaced by 0008, memory cap replaced by 0024)
date: 2026-10-15
---

# 0006 Detach key, image key and memory cap defaults

## Context

simstim intercepts two keys while attached, and caps each session's memory.

## Decision

- **Detach:** Ctrl+\. Press it twice quickly to send it to the agent. No agent uses it by default.
- **Image paste:** Ctrl+V when the laptop's clipboard holds an image; otherwise the key passes through. Alt+V on Windows, where the terminal takes Ctrl+V.
- **Memory cap per session:** 8 GiB or 40 % of the host's memory, whichever is smaller. `MemoryHigh` is 85 % of that, and swap is off for the session.

All three can be changed in the config.

## Consequences

- Good, because a leaking agent cannot starve the host or other sessions.
- Bad, because a heavy build inside one session can hit the cap. The error says so, and the cap can be raised.
