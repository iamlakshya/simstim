---
status: proposed
date: 2026-11-10
---

# 0029 A sandbox for each session, on hosts the user owns, with every door closed until the user grants it

This record widens roadmap item X6 ([#13](https://github.com/iamlakshya/simstim/issues/13)) into O2 ([#100](https://github.com/iamlakshya/simstim/issues/100)). It replaces no accepted record. [The research note](../../research/isolation.md) has the evidence and the scores, [the spec](../isolation-spec.md) says what to build, and [the plan](../isolation-plan.md) says in what order.

## Context

Today simstim gives no isolation between sessions on one host. Every session runs as the same Unix user. Each agent can read the user's home folder, the other sessions' worktrees, the agents' logins and the whole network. Four holes go further than that:

- Any process of the user can attach to any holder's socket and type into any session. `Holder.serve` checks no peer and no role, and an agent's hooks use the full socket (`internal/host/holder_conn.go`, `internal/host/hooks.go`).
- The shadow terminal parses hostile output in an unconfined child of the holder. Its Ghostty pin, `ad97d331`, comes before the fix of [GHSA-w9q4-5jvv-mcv4](https://github.com/ghostty-org/ghostty/security/advisories/GHSA-w9q4-5jvv-mcv4). GitHub rates that advisory Moderate, and it may not be reachable with our scrollback setting. Nobody has checked.
- A hook can set the agent's session ID to any text, and a resume puts it into the agent's command (`applyHook`, `resumeArgv`).
- A session can write a laptop path to the clipboard with OSC 52. A paste of it then uploads that laptop file (`internal/client/attach_paste.go`).

The threats are real and current. In August 2025, the Nx compromise ran installed agent CLIs with their approvals turned off to list secrets, and pushed them to public repositories in the victim's account ([Snyk](https://snyk.io/blog/weaponizing-ai-coding-agents-for-malware-in-the-nx-malicious-package/)). The Shai-Hulud worm compromised more than 500 npm packages ([CISA](https://www.cisa.gov/news-events/alerts/2025/09/23/widespread-supply-chain-compromise-impacting-npm-ecosystem)). Agents now run for hours without a person, and people run them with approvals off.

The owner asked on 9 November 2026 for each session to run in its own sandbox, with no access to the host or to other sessions. A folder, a network host, a secret, the SSH agent and a port each stay closed until the user opens them with an explicit grant. Users also add machines of their own as hosts: a desktop, a homelab, a Mac, a cloud VM, a bare-metal server or a machine they borrow. They open sandboxes on any of them, as Docker Sandboxes ([architecture](https://docs.docker.com/ai/sandboxes/architecture/)) and a hosted VM service do. Everything stays on machines that the user owns.

The project's rules stand: the agent's bytes reach the terminal unchanged, nothing new listens on a host, there is no relay or telemetry, and the tool stores no secrets ([governance](../../../GOVERNANCE.md)). Agents are data. The code is clean-room. The project has one maintainer.

Hosts differ a lot. Some have KVM. Cloud VMs mostly have none: Hetzner Cloud has none ([FAQ](https://docs.hetzner.com/cloud/servers/faq/)), and EC2 gives it only on some Intel types ([AWS](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/amazon-ec2-nested-virtualization.html)). A Mac host is a Lima VM, which gets KVM only on M3 or later with macOS 15 or later. Ubuntu 23.10 and later restrict unprivileged user namespaces ([Ubuntu](https://ubuntu.com/blog/ubuntu-23-10-restricted-unprivileged-user-namespaces)).

## Options

Two Monte Carlo tree searches found and scored the options. Five researchers expanded each node, and two judges scored each option for security, fit, convenience, scale and effort. The value was (0.35 security + 0.15 fit + 0.20 convenience + 0.15 scale + 0.15 effort) / 10. One rule changed the main search. After its first iteration, the two best options for the wall were both expanded before the search went deeper. The best one by value shares the host kernel, so it needed a comparison with the next one. A red team and a fact check then reviewed each best path. [The research note](../../research/isolation.md#3-method) has the method, every score and a check of the weights.

Each value below is the option's own, then the best value found below it, where the search went deeper.

**1. The wall: what isolates a session.**

1. A hardened process: user, mount, PID and network namespaces, Landlock and seccomp. 0.597, then 0.612.
2. gVisor for each session. 0.535.
3. A KVM microVM for each session. 0.553.
4. A full VM for each session, local or at a provider. 0.482.
5. Wall drivers by level, with a floor pinned for each host. 0.575, then 0.687.

**2. Control: where the trusted parts run.**

1. The whole helper inside the wall. 0.658.
2. The holder outside, and an untrusted guest process inside that owns the PTY. 0.670, then 0.671.
3. The holder outside, reaching the wall through OpenSSH. 0.523.
4. The holder inside the wall, and a thin trusted supervisor outside. 0.678, then 0.687.
5. A provider machine for each session. 0.423.

**3. Lifecycle: how a sandbox is made, kept and stopped.**

1. A cold boot from a verified image, with state on disk only. 0.645, then 0.687.
2. Snapshots first: restore, stop and fork with memory. 0.568.
3. Warm pools, throwaway sandboxes, and git as the only exit. 0.582.
4. Declared environments as signed, layered images. 0.563.
5. A fleet of the user's hosts with placement by the client. 0.630.

**4. Doors: how things cross the wall.**

1. No network card, an egress process on the trusted side, and sentinels in place of secret values. 0.687.
2. An allowlist without interception, and short-lived tokens that the laptop mints. 0.518.
3. A broker of typed doors that exist only while granted. 0.630.
4. Git only: bundles in, a quarantined fetch out. 0.607.
5. A key broker on the laptop that signs each request. 0.435.

**5. Supply chain and autonomy.**

1. A registry firewall in the egress process. 0.600.
2. A flight recorder with trust labels, tripwires and a kill switch. 0.602.
3. Throwaway sessions with one exit: a reviewed git bundle. 0.617.
4. The laptop holds the digests: a signed, reproducible boot chain. 0.675.
5. Agent rows as policy: a model API guard, caps and split roles. 0.526.

**6. Attach: how a machine becomes a sandbox host.**

1. `host add` over SSH as today, then prepare: probe, runtime, one shown and reversible root step, a test boot. 0.615, then 0.732.
2. Rootless only, with no guest network card. 0.513.
3. A one-line join on the machine that makes a dedicated account. 0.585.
4. Slot accounts and a small root-owned launcher. 0.570.
5. Hosts made by providers, and adopted fleets. 0.515.

**7. Placement: which host a sandbox opens on.**

1. Named placement: `new <host>` or a saved default, with a fit check on the host. 0.682, then 0.715.
2. `new auto`: the client ranks the user's hosts from live facts, and the host admits or refuses. 0.643, then 0.732.
3. Layered templates fetched by digest. 0.573.
4. Warm pools on each host. 0.535.
5. Sessions that move between hosts. 0.403.

**8. Fleet: what keeps many hosts safe and healthy.**

1. Host tiers (`own`, `throwaway`, `shared`) as a ceiling on grants, enforced on the laptop. 0.732.
2. Leased hosts that wipe themselves at the end. 0.470.
3. Runtime upgrades, health and drain. 0.630.
4. A ladder of ways to reach a host behind NAT, with no project relay. 0.620.
5. Team hosts with one account for each person. 0.495.

## Decision

Option 5 for the wall, option 4 for control, option 1 for the lifecycle, option 1 for the doors and option 4 for the supply chain. For hosts: option 1 to attach, option 1 then option 2 for placement, and option 1 for the fleet, with option 3 grafted from the fleet decision. These are the paths with the best values, 0.687 and 0.732. The last step of the main path secures only the project's own artefacts, so this record adds grafts from the other options of decision 5 and from the red team.

### The wall and its floor

- **Four levels:** `none` (as today), `process` (namespaces, Landlock and seccomp on the host kernel), `gvisor` (gVisor's kernel in front of the host's) and `microvm` (a guest kernel on KVM). A level counts the driver, its mode and the host's features together.
- **The floor.** `host add`, `host create` and `host wall` probe the host, and the client pins the strongest level that works as the host's floor in `hosts.toml`. A new session never starts below it unless the user names the downgrade, with `--downgrade` or a typed confirmation. The manifest records each downgrade, the row shows it, and an alert reports it. A repository's file, the agent table or a host's answer can raise a level, never lower it.
- **The first drivers:** `process`, then microvm with Cloud Hypervisor v52.0 or later on host kernels 6.8 or later. `gvisor`, and Firecracker for older kernels, come later. libkrun and Kata are not drivers: libkrun gives the guest its VMM's host permissions ([security model](https://github.com/libkrun/libkrun/blob/main/docs/security-model.md)), and Kata adds root daemons.
- **A machine for each session** is a placement for later, not a level. The machine runs the helper, and the session runs inside it at that machine's own floor.

### Where the trusted parts run

- **The holder goes inside the wall,** with its ring, its shadow terminal and its status engine. The rule behind it: no memory-unsafe parser of hostile bytes runs on the trusted side. The Ghostty advisory shows the risk, but the rule does not rest on that one advisory.
- **A supervisor for each session** runs outside, as the main process of the session's systemd unit. It owns the sandbox, the limits, the trusted manifest, a new audit log, alerts and their settings, the policy and the egress process. It serves the session's existing socket, and passes each connection's frames to the holder with checked frame types and sizes. It never reads an output payload.
- **One link** joins the supervisor and the guest. It carries many streams with flow control. The trusted side never listens where a guest can connect: the supervisor dials into a microvm through its VM socket, and a process wall's guest init inherits one end of a socket pair ([0030](0030-local-sockets-do-not-listen.md)).
- **Claims:** what the guest reports, such as the agent's state, is kept apart and shown as claimed. Only the trusted side decides a session's end, its out-of-memory kills, its level and its policy.
- **Jails:** the VMM, the image converter and any host-side git run confined: a subuid where one exists, an empty network namespace, a minimal mount namespace, Landlock, seccomp and no core dumps. A VMM escape then does not land as the user who runs every supervisor.

### Lifecycle

- **Every start is a cold boot.** No memory snapshot is ever made, so no secret is written and no clone shares random state. A stop loses the processes and the screen. The agent comes back with its resume command, as restore does today.
- **Microvm sandboxes** boot a project-built kernel and tools image, and the user's image, each checked by dm-verity against root hashes that the client sends. One writable session disk holds `/work` and the agent's state folders.
- **The guest's root and home reset at each start.** Only `/work` and the agent's state folders stay, so a dropper in `~/.bashrc` goes.
- **A new state, `stopped`:** files kept, no memory used. Attach wakes it. After a host reboot, only sessions that were working start again, two at a time.
- **Fork** copies the session's files and starts the agent's own fork command, from the agent table.

### The doors

- **Files.** A session works on its own clone, seeded from a git bundle that the trusted side makes. Work leaves through `pull`, now a bundle over the bridge into quarantine refs, with a summary that flags files that run code on the laptop or in CI. The host never runs git or follows a symlink in a folder that a sandbox can write.
- **Network.** No wall has a network card. Each flow becomes a stream to a per-session egress process, which checks the policy, refuses private, loopback, link-local and metadata addresses, and logs each flow. Presets: `locked`, `balanced` (the default) and `open`, which counts as a downgrade. DNS answers only granted names. The egress process is not a relay ([0031](0031-the-egress-process-is-not-a-relay.md)).
- **Secrets.** A secret with known hosts is bound: its value stays in the egress process, and the guest sees a sentinel. The proxy puts the value into one header on those hosts only, refuses any other credential there, and refuses the sentinel anywhere else. Any other secret enters the guest only when the user exposes it by name, and the row shows it. Login files of the host never enter a sandbox. Values stay in memory only ([0032](0032-secret-values-in-memory-only.md)).
- **SSH agent.** The client turns off agent forwarding on every connection, over the user's own SSH config. A sandbox gets no SSH agent unless the user grants one.
- **Ports, paste, devices.** A forward is a grant for one port, over a Unix socket. Paste takes images and PDF files by default, and never a file whose path the session wrote to the clipboard. No device crosses the wall.
- **Terminal output** stays byte-exact. The client only observes and warns.
- **Interception.** The egress process decrypts TLS only for hosts that need it: bound hosts, model APIs, package registries and object stores. This is a new trust decision. It gets its own record, which needs every maintainer's yes, before it is built.

### The supply chain and autonomy

- **The laptop holds the digests, the hosts hold the bytes.** The client embeds a release manifest with the digest of each trusted artefact. The client checks the helper before each bridge. The supervisor runs the VMM only from a descriptor whose digest it checked, and boots only verified images. The kernel and the tools image are built reproducibly, and a signed tag gates each release. A daily job watches pins that no package ecosystem covers.
- **Grafts.** From the registry firewall: package manager settings in the guest now, and registry gates in the egress process later. From throwaway sessions: the review gate on `pull`. From the flight recorder: tripwires on sentinels, the audit log, and alerts for misuse. From agent rows as policy: a guard that refuses model API requests that would make the provider fetch an address of the caller's choosing.

### Your own hosts

- **Attach.** `host add` stays the way in, over the user's own SSH. Then it prepares the host: a read-only sandbox probe, the runtime checked against the client's digests, one optional root step that the user sees line by line with its undo, and a test boot. A host without sudo still works, at the level that it can run without root.
- **Identity.** Each saved host has a pinned host key that the user's SSH config cannot undo, and a host ID that the helper keeps. A changed ID warns.
- **Tiers.** Each host has a tier: `own`, `throwaway` or `shared`. The tier caps what may leave the laptop for that host: secrets, the SSH agent, alert tokens and repositories. One grant gate on the laptop checks each grant, and a grant ledger records the names, never the values. A new host added with `--yes` is `shared`.
- **Placement.** `new <host>` and a saved default come first. `new auto` follows: the client ranks the user's hosts from live facts and explains its choice, and the chosen host admits or refuses the sandbox under a lock. A sandbox that does not fit is refused, not shrunk.
- **Fleet care.** Each host reports its health, gets runtime upgrades, and can be drained before a restart. `host rm --purge` removes everything that the tool put on a host, from a footprint ledger, and says what it could not remove. For hosts behind NAT, the docs show only paths that the user owns. The tool runs no tunnel.

### Phases

Phase 0 fixes today's holes for every session: agent forwarding off, a hook socket of its own and a peer check, the Ghostty pin, checked session IDs, cleaned host text, a paste guard, trust tied to a setup script's content, safe copies, and Lima VMs that cannot reach the Mac's loopback. It also adds pinned host keys, host tiers and the grant gate. Phase 1 is a process wall that users can try. Phase 2 keeps secret values outside and closes DNS. Phase 3 adds the release manifest, runtime delivery and microvm. Phase 4 checks the project's own artefacts, and adds placement, health, drain, purge and hosts behind NAT. Phases 5 to 7 add fork, `cp`, `exec`, gVisor, Firecracker, registry gates and decoys. Phase 8 adds separate accounts.

Walls stay opt-in until phase 2 ends. Then, with the owner's yes, new sessions run at the host's floor by default. Decisions that only the owner can make are in [the spec's open questions](../isolation-spec.md#15-open-questions), each with a recommended default.

## Consequences

- Good, because a poisoned package or an injected agent meets an empty home, no host logins, no other sessions and a closed network. The 2025 attacks needed all of those.
- Good, because bound secrets can be used but not stolen, and an attacker's own key cannot ride an allowed model API.
- Good, because each host gets its strongest wall, and no session falls below the floor in silence.
- Good, because a user can add any machine they own and open sandboxes on it, with no hosted control plane, no relay and no account.
- Good, because a less trusted host gets fewer grants by default, and the grant ledger says what each host ever got.
- Good, because byte-exact attach, replay by offset, alerts and resume keep one code path, now behind a supervisor that checks frames.
- Good, because today's holes close in phase 0 for every user, before any wall ships.
- Good, because `pull` now works over every transport, and it shows what could run code on the laptop before a branch moves.
- Bad, because the control decision is close. Holder inside (0.677) beat holder outside (0.670) by 0.007, and holder outside wins under weights that favour convenience or effort. The choice rests on the parser rule above, not on the score alone.
- Bad, because principle 7, one binary on each side, no longer holds for hosts with microvm walls: they also need a VMM, a guest kernel, a tools image, an agents disk and images. This needs the owner's yes.
- Bad, because host set-up gains root steps that need consent: an `AppArmor` profile, the group for `/dev/kvm`, an `io_uring` setting and blocked modules. This needs the owner's yes.
- Bad, because the egress process decrypts session TLS and holds secret values for the whole session, where the holder drops them at the start today. Values are still never written ([0032](0032-secret-values-in-memory-only.md)). Interception needs its own record and every maintainer's yes.
- Bad, because a process wall shares the host kernel, and a microvm wall depends on KVM and the VMM. A bug in either breaks the wall.
- Bad, because a stop loses the running turn and the screen, and agents without a resume command lose more.
- Bad, because a process wall cannot run Docker or the agents' own bubblewrap sandboxes, and a microvm cannot share a host folder in its first version.
- Bad, because terminal escape sequences still reach the laptop, and granted secrets and hosts can still be misused while a session runs.
- Bad, because on the first path every sandbox on a host runs as the login user, so a VMM escape reaches that account. Separate accounts come later, as an option.
- Bad, because agent rows, service rules and the model API guard go stale as agents change, and one maintainer must keep a kernel, a VMM and images up to date.
- Bad, because the plan is large: nine phases and 71 tasks, most of them M or L on the roadmap's scale.
- This record replaces no accepted record. It extends [0002](0002-own-holder-per-session.md) (a supervisor and an egress process beside each holder), [0003](0003-system-openssh.md) (options on the command line over the user's SSH config), [0013](0013-host-providers.md) (a stopped session keeps files, not processes), [0017](0017-release-signatures.md) (more signed artefacts) and [0024](0024-memory-cap.md) (a microvm's memory sits inside the session's cap, and admission refuses what does not fit). It needs [0030](0030-local-sockets-do-not-listen.md), [0031](0031-the-egress-process-is-not-a-relay.md) and [0032](0032-secret-values-in-memory-only.md) for the rules that it touches. Records that follow it, in the plan: TLS interception on the trusted side before phase 2 needs it, the memory of microvm guests before phase 3, the signed-tag release gate in phase 4, and the records for sandbox hosts.
