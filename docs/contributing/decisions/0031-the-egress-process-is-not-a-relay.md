---
status: proposed
date: 2026-11-10
---

# 0031 A sandbox's egress process is not a relay, and neither a host's download of the runtime nor a judge that the user names is telemetry

This record says what part of one of the [rules no decision overrides](../../../GOVERNANCE.md#rules-no-decision-overrides) means: "there is no relay and no telemetry". It needs the agreement of every maintainer, as any change to such a rule does. [0029](0029-a-sandbox-per-session.md) depends on it.

## Context

The design page lists "no cloud service: no account, no relay and no telemetry" as a non-goal ([design](../../explanation/design.md#non-goals)). Today the laptop reaches each host over the user's own SSH, or a provider's exec API, and nothing sits between them that the project runs. Hosts download nothing from the project: the client copies the helper to them.

[0029](0029-a-sandbox-per-session.md) adds four things that a reader could call a relay or telemetry:

- **The egress process.** No sandbox has a network card. A per-session egress process on the trusted side of the host makes every network connection for the sandbox, under the user's grants. It carries traffic for another program, which is what a proxy does.
- **Reaching a host behind NAT.** The hosts search scored a ladder of ways to reach such a host: a direct connection, a jump host, the user's own overlay network, then a reverse SSH tunnel to a machine that the user owns (0.620, [research](../../research/isolation.md#your-own-hosts)).
- **Runtime downloads.** A host may fetch the runtime (the VMM, the guest kernel and the tools image) from the project's release, instead of a copy from the laptop ([question 39](../isolation-spec.md#15-open-questions)).
- **The classifier's judge.** When the user turns on a model judge, the egress process sends the judge the name of each site that no rule covers, with a few facts about the session ([spec 7.8](../isolation-spec.md#78-the-classifier-p211-p212)).

## Options

1. Any process that carries traffic for another is a relay. Sandboxes then get a network card and direct access, and NAT recipes go.
2. A relay is a machine or service that the project runs, or that the user does not own, which carries the user's traffic. The egress process and the user's own jump hosts and tunnels are not relays. A download of a public release file, with no identifier and no user data, is not telemetry. Neither is a request to a judge that the user turns on and names.
3. Option 2, plus a rendezvous service that the project runs for hosts behind NAT.

## Decision

Option 2.

- **The egress process** runs on the user's own host, for one session, under that session's grants. It connects straight to each destination. It has no listener that a network can reach ([0030](0030-local-sockets-do-not-listen.md)). It sends nothing to the project.
- **The tool runs no tunnel, rendezvous or relay service,** and depends on none. For hosts behind NAT, the docs show only paths through machines that the user owns or already uses, such as their own overlay network or a jump host. A reverse tunnel recipe ends on a machine that the user owns.
- **A host's download** of the runtime fetches public release files by digest, with no identifier, no cookie and no user data. The client checks each file against the digests that it embeds. A user can keep every download on the laptop with `fetch = "laptop"`.
- **A judge's requests** go only to the endpoint that the user names in `[classifier]`, and only when the user sets `judge = "model"`. They hold the fields of spec 7.8 and nothing else, and the audit log records each one. The built-in value is `judge = "none"`, and a local model keeps every name on the host.
- **No telemetry,** as today: no part of the tool reports use, errors or crashes to anyone.

Option 1 would weaken the sandbox: a network card in each sandbox is the path that the 2025 attacks used to send secrets out. Option 3 is a cloud service, which the design rules out.

## Consequences

- Good, because the rule keeps its purpose: no machine that the user does not own sits between the user and their hosts, and nothing reports on the user.
- Good, because a closed network is possible: the egress process is the only way out of a sandbox, and the user's grants decide what it reaches.
- Good, because hosts behind NAT have documented ways in that need no project service.
- Bad, because a host that downloads the runtime contacts the project's release host, which can see the host's address. `fetch = "laptop"` avoids it.
- Bad, because a remote judge learns which sites the user's sessions reach. It is off by default, and a local model avoids it.
- Bad, because the egress process parses hostile traffic on the trusted side. Its jail, its fuzz tests and one process for each session limit the damage of a bug.
- Bad, because a change to the meaning of a governance rule needs every maintainer's yes.
