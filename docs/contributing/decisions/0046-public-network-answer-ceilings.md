---
status: proposed
date: 2026-11-19
---

# 0046 Public network answers keep their address ceiling

## Context

[Held answers](../isolation-spec.md#72-network) open a host at the selected scope. [Network checks](../isolation-spec.md#72-network) still refuse private, shared, link-local and host addresses unless an explicit numeric grant names them. The [local grant gate](0036-host-tiers-and-the-grant-gate.md) may approve an opaque held target only for public destinations. A previous numeric grant or later DNS answer must not widen that answer.

The restriction must survive the same selected scope as the rule. A secret-bearing target must also stay in trusted memory, as [0032](0032-secret-values-in-memory-only.md) requires.

## Options

1. Keep a separate table of restrictions by rule ID.
2. Make every answer last only in memory for one session.
3. Store a restrictive address field on ordinary rules and carry it through their permits.

## Decision

Choose option 3. `public_only = true` restricts a network allowance. It grants no target or address by itself. Its absence preserves existing rules and their IDs. Restricted rules have distinct identities and exact replacement and removal tuples.

The existing matcher and once ledger carry the restriction into each acquired permit. A permit retains it through later policy changes. Resolution, physical dials and live flow checks cannot use a separate numeric exception to open a refused range or host address for that permit. Current locks, denies, expiry, method and service checks still apply.

Helpers must advertise `network-public-only-v1` before receiving the restriction. A reader that ignores the field could widen it. Saved layers, local file writers, live updates and cold starts must all retain the field or refuse the operation.

## Consequences

Ordinary rules keep their existing behaviour. A later explicit ordinary grant is a separate rule and cannot erase a restricted rule's tuple or clear an existing permit's ceiling. Current denies and locks may still refuse either grant.

Non-sensitive rules retain the chosen scope across clone, replacement and restart. Secret-bearing true targets remain memory-only compiled rules in the same engine and ledger; this field does not permit them in public rules or saved files. A display label or target class never becomes grant authority.

Tests cover old IDs and numeric exceptions, saved round trips, exact tuple handling, atomic service and model plans, DNS rebinding and active-flow revocation. Full held-answer integration, independent review and unchanged resource gates remain required.
