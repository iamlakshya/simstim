---
status: proposed
date: 2026-11-21
---

# 0044 A joined Mac exposes only its VM's fixed loopback SSH port

## Context

A Mac host runs the normal Linux helper in a Lima VM. Lima's host agent listens
on the Mac's loopback to carry SSH into that VM. [0030](0030-local-sockets-do-not-listen.md)
counts a loopback TCP port as listening, so this is an explicit exception to
that rule. No network listener is added on any other interface.

## Options

1. Wait for a Lima transport that reaches the VM without a loopback TCP port.
2. Permit the one fixed Lima port, behind the Mac's existing sshd and a narrow key.

## Decision

Propose option 2, for the Mac host path of isolation spec 18.4 only. Joining
requires the machine owner's local approval. It never enables Remote Login.
The invitation key can open only the literal `127.0.0.1:<VM port>` target or the
closed `facts-v1` action. A root-owned dispatcher rejects other exec and shell
actions. Effective sshd policy closes TTYs, agent and X11 forwarding, remote
TCP forwarding and all Unix-socket forwarding. A mismatch stops the join.

The VM uses plain mode, with no host mounts, port forwards or containerd and
with `networkOpts.hostLoopback: false`. A fixed root-owned startup guard checks
the original instance, pinned runtime and frozen profile at each restart.
No probe, fingerprint, reply, ledger row or instance name alone creates setup
or runtime authority.

## Consequences

The laptop starts both SSH connections and checks each host key. It opens no
listener and runs no relay. Other Macs and Linux hosts keep the ordinary
rules. The VM still needs the Linux helper's normal sandbox admission.

The system launch-daemon case remains unproved for an account that has never
logged in. The owner-decided user launch-agent fallback is implemented as a
separate explicit mode. That user must stay logged in, and anyone at its
unlocked screen can read the VM disk. Both join and doctor state this change
of custody. Neither branch changes battery policy, FileVault, MDM or reboot
behaviour. Manual M1/M2 and M3-or-later assurance remains required.
