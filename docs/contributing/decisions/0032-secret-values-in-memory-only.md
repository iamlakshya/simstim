---
status: proposed
date: 2026-11-10
---

# 0032 Secret values live only in the memory of the trusted side, never in a file

This record says how one of the [rules no decision overrides](../../../GOVERNANCE.md#rules-no-decision-overrides) holds for sandboxes: "the tool stores no secrets". It needs the agreement of every maintainer, as any change to such a rule does. [0029](0029-a-sandbox-per-session.md) depends on it.

## Context

Today a command on the laptop fetches each secret when a session starts. The client sends the values to the holder, which puts them only into the agent's environment. They never reach a file, a log or a command line ([security](../../explanation/security.md)). The holder drops them after the start.

[0029](0029-a-sandbox-per-session.md) keeps secret values out of the sandbox, so that an agent can use a key but not read it:

- **Bound secrets.** A secret with known hosts stays in the session's egress process for the whole session. The guest sees a random sentinel. The egress process swaps the value into requests to the bound hosts only.
- **A TLS authority for each session.** To swap a value into a request, the egress process decrypts TLS for the bound hosts, with a certificate authority made for that session.
- **Restart after a host reboot.** A session that was working starts again after a reboot. Its egress process then has no values, because nothing on the host kept them.
- **Exposed secrets.** A secret that the user exposes by name enters the guest's environment, as all secrets do today.
- **The grant ledger.** The laptop records which grants each host got.

These keep values on the host far longer than today. The rule needs a clear reading before the work starts.

## Options

1. Values only in the guest's environment, as today. No bound secrets.
2. Values in the memory of the trusted side for the session's life, never in a file. The laptop arms a session again after a host reboot.
3. Values in a keyring or an encrypted file on the host, so sessions restart with their keys without the laptop.

## Decision

Option 2.

- **Memory only.** Bound secret values and the session's TLS authority key live only in the memory of that session's egress process. They never reach a file, a log, an audit line, a command line or an environment variable on the trusted side. The egress process runs with no core dumps.
- **The path in.** Values travel from the laptop over the bridge, as today, then to the egress process over the session's private channel.
- **Armed again by the laptop.** After a host reboot, a session starts with no values. Its row shows "not armed" until a client attaches and sends them again. Until then, requests that need a bound secret fail with a clear error.
- **Sentinels are not secrets.** They are random, new at each start, and useless away from the egress process, which refuses them on every host but the bound ones.
- **Exposed secrets** behave as today, and the row shows a badge. The tool never writes them. An agent inside the sandbox may write them to the session's disk, and the docs say so.
- **Names, never values,** in the grant ledger and the audit log.
- **Not now:** an OAuth broker that writes refreshed tokens back to a file, and a disk key that the host keeps. Each needs a new record.

Option 1 leaves every key readable by a poisoned package. Option 3 stores secrets on the host, which the rule forbids.

## Consequences

- Good, because the rule holds: the tool writes no secret value anywhere.
- Good, because a sandbox can use a bound key but cannot read it, so a poisoned package or an injected agent cannot send it out.
- Bad, because values stay in memory for the whole session instead of only at the start. A compromised login account, or a memory bug in the egress process, can read them.
- Bad, because after a host reboot, sessions with bound secrets need an attach from the laptop before their agents can reach bound hosts.
- Bad, because a change to how a governance rule holds needs every maintainer's yes.
