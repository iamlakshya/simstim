---
status: accepted
date: 2026-11-10
---

# 0030 Only network ports count as listening on a host: Unix sockets and VM sockets do not

This record says what one of the [rules no decision overrides](../../../GOVERNANCE.md#rules-no-decision-overrides) means: "nothing new listens on a host". It changes no behaviour of today's code. It needs the agreement of every maintainer, as any change to such a rule does. [0029](0029-a-sandbox-per-session.md) depends on it.

## Context

The rule appears three times. GOVERNANCE.md says "nothing new listens on a host". The design's principle 3 says "no new network listener", and principle 5 says "no open ports on hosts" ([design](../../explanation/design.md#principles)). The rule keeps a host's attack surface where it was: sshd, or a provider's own authenticated exec API ([security](../../explanation/security.md)).

The code already reads the rule as "no network port". Each holder listens on a Unix socket in a private folder of the user's runtime directory, with mode 0600 (`internal/host/holder.go`). The bridge, hooks and the doctor dial it.

[0029](0029-a-sandbox-per-session.md) adds more local sockets:

- A hook socket for each agent, so that hooks reach only their own session.
- The session socket, now served by the supervisor instead of the holder.
- A Unix socket for each port forward, in place of a port.
- The VMM's VM socket (vsock). Cloud Hypervisor exposes it on the host as a Unix socket that the host side connects through ([vsock](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/vsock.md)).
- A vsock port inside each microvm guest, where the guest init accepts the supervisor's connection.

Without a record, a reader could say that any of these breaks the rule. The rule needs one meaning before the work starts.

## Options

1. Every socket counts as listening, Unix sockets and VM sockets included.
2. Only network ports count: TCP, UDP and SCTP on any interface, loopback included. A Unix socket in a folder that only the user can reach, and a VM socket that only one sandbox's VMM can reach, do not count.
3. Option 2, plus one more rule: the trusted side never listens where a guest can connect.

## Decision

Option 3.

- **No network port.** No part of the tool opens a TCP, UDP or SCTP port on a host, on any interface, loopback included. Ports that the client forwards still bind to `127.0.0.1` on the laptop only, as today.
- **Local sockets are private.** Each new Unix socket lives in a folder with mode 0700, under the user's runtime directory or the session's folder, and has mode 0600. Each one checks its peer where the design says so (the hook socket and the session socket).
- **The trusted side never listens where a guest can connect.** The supervisor dials into a microvm through its VM socket. A process wall's guest init inherits one end of a socket pair. A guest can open no connection to the trusted side. It can only answer the one that the supervisor opened, and every stream inside that link is checked.
- **Words.** When this record is accepted, the security page and principle 5 say this in one sentence each. GOVERNANCE.md keeps its words.

Option 1 would forbid today's holder, so it is not what the rule meant. Option 2 alone would allow a design where a guest connects to a socket on the trusted side, which is the attack surface that the rule exists to prevent.

## Consequences

- Good, because the rule keeps its purpose: nothing on the network can reach a new door on the host.
- Good, because the design can use local sockets for each session, which keep sessions apart better than one shared door.
- Good, because a guest has no listener on the trusted side to attack, only the frames of a link that the supervisor opened.
- Bad, because any process of the same login user can still reach these sockets, as it can reach the holder's socket today. Peer checks narrow this, and a compromised login account defeats them.
- Bad, because the VMM's host-side Unix socket is one more file that a process of the user can connect to. Its folder's mode and the VMM's jail limit who can.
- Bad, because a change to the meaning of a governance rule needs every maintainer's yes.
