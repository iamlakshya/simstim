---
status: accepted (the sentence "Other sessions are untouched" replaced by 0024)
date: 2026-10-15
---

# 0008 Cap sessions with MemoryMax only, not MemoryHigh

This record replaces the `MemoryHigh` rule of [0006](0006-defaults.md). The rest of 0006 stays.

## Context

0006 set `MemoryHigh` at 85 % of `MemoryMax`. On the test host, a session that used memory without limit did not stop: the kernel throttled it at `MemoryHigh`, and with swap off it could not reclaim, so it hung at about 90 % of the cap for minutes. A hung agent is worse than a stopped one: the user sees nothing, and the session cannot recover.

## Options

1. Keep `MemoryHigh` below `MemoryMax`
2. `MemoryMax` only, with swap off
3. No cap

## Decision

Option 2. At the cap, the kernel reclaims what it can, then the out-of-memory killer stops the biggest process in the session (the agent is made the first choice). The holder sees the kill in the cgroup's `memory.events`, marks the session, and keeps its last screen. Other sessions are untouched.

## Consequences

- Good, because a runaway agent ends quickly and visibly.
- Bad, because there is no soft warning before the stop. A later version can watch `memory.current` and warn at 90 %.
