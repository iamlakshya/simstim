---
status: proposed
date: 2026-11-15
---

# 0045 Decoy opt outs keep policy locks

## Context

[Policy locks](../isolation-spec.md#82-layers-and-precedence) are checked before layer precedence. A conflicting value fails with `policy_locked`. [Decoys](../isolation-spec.md#217-decoys-p72) must be off when any layer sets `sandbox.decoys = false`. A later `true` must not silently enable them again.

These rules meet when a lock requires decoys and a narrower layer disables them. Both rules must apply before generating values or writing files.

## Options

1. Use the last value for decoys, as for an ordinary setting.
2. Let a decoy opt out bypass a lock.
3. Check locks first, then keep every valid opt out.

## Decision

Choose option 3. A lock conflict refuses the whole policy before any decoy side effect. Among valid layers, any explicit `false` disables both files and tripwires. A later `true` cannot enable them again.

| Lock | Layer values | Result |
| --- | --- | --- |
| None | No explicit value | Decoys enabled |
| None | `false`, then `true` | Decoys disabled |
| None | `true`, then `false` | Decoys disabled |
| `true` | A conflicting `false` | `policy_locked` |
| `false` | A conflicting `true` | `policy_locked` |

## Consequences

The resolver keeps one lock checker and one layer merge. A refused policy generates no decoy entropy, writes no files and registers no tripwires. Existing lock errors still name the lock. Changing a lock remains an explicit edit to its trusted layer.

The tests cover later values in both orders, both lock conflicts and the absence of startup side effects after refusal.
