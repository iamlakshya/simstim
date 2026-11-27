---
status: proposed
date: 2026-11-12
---

# 0036 Host tiers and the grant gate stay on the client

Task H4 of [the isolation plan](../isolation-plan.md#h4-the-host-id-and-the-tiers-file) adds host identity and tiers. H5 adds the grant gate and ledger specified in [section 14.8](../isolation-spec.md#148-tiers-and-grants).

## Context

Root on a host can read every sandbox's memory and disk. It can read any secret sent there. A sandbox cannot make a shared host as trusted as a machine that only the user controls.

The client already keeps hosts in `hosts.toml`. Older clients rewrite that file from the fields they know, so they would drop a new tier or identity field. Host identity also needs a check beyond the saved address. A machine can be rebuilt at the same address.

## Options

1. Treat all saved hosts alike.
2. Keep tiers in `hosts.toml` and let the host enforce their rules.
3. Keep tiers in a separate client file, check host identity, and check each grant before it leaves the laptop.

## Decision

Option 3, as specified in sections [14.6.1](../isolation-spec.md#1461-tierstoml), [14.7.2](../isolation-spec.md#1472-the-host-id) and [14.8](../isolation-spec.md#148-tiers-and-grants).

The trust order is `shared`, `throwaway`, then `own`:

- `own` means that only the user has root on the machine.
- `throwaway` means the user's own machine, made for one task, which may be lost. It gets only short-lived secrets.
- `shared` means that someone else has root, such as a teammate, a company or a hosted service.

The host never learns its tier. The client keeps `tiers.toml` beside `hosts.toml`, with mode 0600 and atomic writes under a lock. Each entry holds the exact host name, tier, host ID, pinned key fingerprint and time set. Older clients never write this file.

The first read creates the file when it does not exist. It records every saved host as `own` and copies each SSH host's pinned key fingerprint. Existing hosts keep their previous behaviour. After migration, a host without an entry counts as `shared`.

Reads and lock waits use the caller's context. A cancelled migration saves no tier file. A host-list save keeps the tiers lock until it commits or rolls back. Other writers wait, so rollback cannot erase their changes.

Every tier or identity change takes the host's lock first. It checks the saved entry under the hosts file lock before taking the tiers lock. A hello with an unchanged ID needs no write. A changed ID that cannot take the host's lock stops the connection before any grant leaves the client.

A forget keeps the pinned key's fingerprint until the host-list save commits. It then removes the tier row. A failed cleanup is logged, and any row left behind cannot lend trust to a new host with that name.

The final cancellation hook runs before destructive forget cleanup. Cleanup then checks the same owned config files and the remaining saved references under the existing locks. A cancelled or failed publication after cleanup still keeps the key guard for a saved host whose pin is gone.

At `host add`, the tier question has no default. A script that uses `--yes` must also pass `--tier`. Lima and Docker hosts start as `own`. A hosted provider's hosts start as `shared`.

A host added by an older client after migration stays at `shared` until the user answers at its next terminal use. Identity bookkeeping records `needs_answer` so a hello cannot answer the question. The client checks a pinned live connection, asks without policy locks, then checks the saved entry and identity again before saving. Concurrent uses share one question. A failed or cancelled answer keeps the pending state. The launcher asks before foreground work and terminal handoff. Background connections and non-interactive commands never choose a tier.

The helper creates one host ID from 16 random bytes. Concurrent first runs use the same winning ID. The bridge sends it in the hello, and the client records it on first sight. A changed ID warns and sets the tier to `shared`. The ID is a hint. A copied state folder or a compromised host can return the same ID.

The fingerprint in `tiers.toml` supplements the pin of [0035](0035-pinned-host-keys.md). When a recorded fingerprint has no matching readable pin, the client refuses the connection. `host rekey` records the new fingerprint and sets the tier to `shared`.

A rekey saves that restrictive tier and fingerprint before replacing the pin. If the pin write fails, the recorded fingerprint refuses the old pin too. The user can retry the rekey after fixing the save error.

`host show` and the launcher display the tier. In H4, lowering takes effect at once. Raising requires a connection that passes the pinned key and the host's typed name, or `--confirm <host>`.

H5 adds one client grant gate, `client.Allow`, for the table in spec 14.8.2. Commands check before fetching or sending secrets. The client checks again where a grant leaves it. The launcher follows the same rule. Later tasks add checks at their own call sites.

The grant ledger records names and times on the laptop, never values. Lowering a tier cannot revoke a value already sent. H5 adds the question before lowering and the list of grants that the new tier refuses. This record does not claim that H4 alone implements those grant checks.

## Consequences

- Older clients cannot erase a tier while saving `hosts.toml`.
- A changed host ID or an explicit rekey sets the tier to `shared` before the user can raise it again.
- Every outbound grant has one client rule. A host cannot grant itself more trust.
- The migration preserves existing behaviour, so users must review the tiers of hosts they already added.
- A tier and a host ID cannot prove that a host is safe. Its owner can read whatever reaches it.
- Grants already sent need revocation at their source. Removing a host or lowering its tier cannot take their values back.
- Attached terminal output stays byte for byte. The user should set terminal clipboard writes to ask on `shared` and `throwaway` hosts.
