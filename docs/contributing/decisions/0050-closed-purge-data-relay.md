---
status: rejected
date: 2026-11-21
---

# 0050 A closed data relay to the original purge owner

## Context

Purge must keep the same native objects after its installed helper or host roots are removed. The original process owns its domain, operations, preparations and physical cleanup. A saved report, endpoint or removal nonce cannot reconstruct those objects. A fresh connector may also run from another inode after the installed code is removed.

The [isolation spec](../isolation-spec.md) keeps guests from host files, other walls and the user manager under G1, G5 and G6. Section 14.4.1 checks actual native credentials and cgroup placement. Unknown placement counts as inside. A compromised outside host login is excluded by N5 in section 3.4. Public code descriptors do not attest another process's current memory image.

## Decision

Use one closed relay which sends request data to the existing original owner. It carries only the authorised host ID, removal nonce, force choice and a fixed continuation operation. The owner retains and rechecks its own original domain and native objects. A new process cannot adopt destructive custody from saved metadata.

The actual raw relay pins its own mapped image and readable descriptor before sending a public code and namespace hello. The owner compares bounded direct code bytes and native metadata against its own retained original descriptor. This may accept a different inode. The comparison restricts this data endpoint. It proves neither the peer's current memory image nor ownership of a session or root. The endpoint grants no private descriptor.

Every connection and frame still needs the actual outside operator's credentials, pinned native lifetime, cgroup placement, namespace observations and current SCM credentials. A root-only, unreadable or unknown placement is refused. The connection uses a checked private target directory and socket. The original owner checks the same host, request and force choice on every continuation. Generic native helper and ordinary caller checks retain their stricter existing rules.

Close completes physical cleanup before its final response. The owner briefly remains live for one acknowledgement on the same connection, so a fresh relay can check the final sender while its pidfd is live. This acknowledgement permits no cleanup or release. An original invocation also retains its real command and atomic child pidfd through a successful native wait.

## Consequences

- A disconnect can cancel a reversible request while the original owner retains irreversible cleanup.
- A fresh connector can continue an existing removal after installed code disappears. It cannot recreate a lost vector.
- Exact public code bytes remain a data filter. They must never become a general executable override or private source grant.
- Guests, sibling walls, subuid and slot actors still have to fail the native boundary. A compromised host login remains excluded by N5.
- Wrong objects, native uncertainty and expired custody preserve resources and keep the host saved. Full native and adversarial tests remain required before public activation.
