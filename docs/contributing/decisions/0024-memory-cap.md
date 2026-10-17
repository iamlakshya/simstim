---
status: accepted
date: 2026-10-16
---

# 0024 A session's memory cap is the host's memory minus a reserve

**Status:** Accepted. The owner chose the direction for [#40](https://github.com/iamlakshya/simstim/issues/40) on 16 October 2026, and accepted the record the same day. It replaces two parts of earlier records:

- from [0006](0006-defaults.md), the memory cap of a session: "8 GiB or 40 % of the host's memory, whichever is smaller". The rest of 0006 stays: the detach key and the image key.
- from [0008](0008-memory-max-only.md), the sentence "Other sessions are untouched". A stop at the limit of all sessions together can reach another session (see Consequences). The rest of 0008 stays: `MemoryMax` only, swap off for sessions, and the agent as the first choice of the out-of-memory killer.

## Context

On a host with 4 GB of memory, the memory cap stopped Claude Code during normal work. The kernel's out-of-memory killer stopped the agent at the cap of 1.5 GiB ([#40](https://github.com/iamlakshya/simstim/issues/40)).

- **The rule is too tight for small hosts.** The cap is 8 GiB or 40 % of `MemTotal`, whichever is smaller (`DefaultMemoryMax` in `internal/host/launch.go`). A VM sold as 4 GB reports about 3.8 GiB, so its cap is about 1.5 GiB. With one session, 60 % of the memory stays unused.
- **Our own docs call 4 GB the minimum.** [Your first cloud host](../../tutorials/first-cloud-host.md) asks for "at least 2 vCPUs and 4 GB of memory". `host create lima` makes 4 GiB machines.
- **Claude Code asks for more.** Its system requirements say "4 GB+ RAM" ([setup](https://code.claude.com/docs/en/setup#system-requirements)). It shows its own warning only "if a session's heap memory passes 2.5GB" ([troubleshooting](https://code.claude.com/docs/en/troubleshooting)). Under a cap of 1.5 GiB, the kernel stops it before it can warn.
- **The cap covers the whole unit:** the holder, its shadow terminal, the agent, and every tool, MCP server and language server that the agent starts. [The architecture](../../explanation/architecture.md#83-ssh-recipes) says that 5 or 6 busy Claude sessions fit in 6.5 GiB. That is more than 1 GiB for each session before a build or a language server.
- **Nobody can change the cap.** `NewArgs.MemoryMax` exists, and the host reads it. But no flag and no config key sets it. 0006 says "All three can be changed in the config", and the [UX spec](../ux-spec.md#7-config) plans `[sessions] memory_max = "auto"`.
- **The docs ask for swap that cannot help.** Each session runs with `MemorySwapMax=0` (0008), but the architecture asks for "4 GB plus swap".
- **The caps do not add up.** Each session gets its own cap, and nothing limits them together. Three sessions on a 4 GB host may use 4.5 GiB between them, more than the host has.

## Options

1. **Keep 8 GiB or 40 %.** No change. A 4 GB host cannot run one Claude Code session through a normal day.
2. **A larger share,** such as 70 %. Better on small hosts, but one share cannot fit both ends. A share of 70 % leaves 0.6 GiB for the system on a 2 GiB host, and holds back 19 GiB on a 64 GiB host. It does not add up across sessions either.
3. **A reserve for the system.** A session may use the host's memory minus a reserve that grows slowly with the host. All sessions together get the same limit, on their slice. A config key and a flag change the cap of a session.
4. **No cap.** Only the kernel's global out-of-memory killer protects the host. It may stop sshd, the user's own programs or a holder, and "One agent cannot take down the host" ([design](../../explanation/design.md)) no longer holds.

## Decision

Option 3, chosen by the owner on 16 October 2026.

### The reserve

The host keeps a reserve for itself. It follows the tiers that GKE keeps on each node for the operating system and the kubelet ([GKE](https://docs.cloud.google.com/kubernetes-engine/docs/concepts/plan-node-sizes)):

| Memory | Reserve |
| --- | --- |
| The first 4 GiB | 25 % |
| The next 4 GiB | 20 % |
| The next 8 GiB | 10 % |
| The next 112 GiB | 6 % |
| Above 128 GiB | 2 % |
| A host with less than 1 GiB | 255 MiB, in place of the tiers |

On top of the tiers, the host keeps a margin of 100 MiB, as GKE does for its eviction threshold.

The default cap of a session is `MemTotal` minus the reserve, rounded down to a whole MiB. On a very small host it is never less than half of `MemTotal`. The helper works it out again at each start, from `/proc/meminfo`, so a VM that was made larger gets a larger cap.

| Host memory (`MemTotal`) | Old cap | Reserve | New cap |
| --- | --- | --- | --- |
| 2 GiB | 0.80 GiB | 0.60 GiB | 1.40 GiB |
| 3.8 GiB, a VM sold as 4 GB | 1.52 GiB | 1.05 GiB | 2.75 GiB |
| 4 GiB | 1.60 GiB | 1.10 GiB | 2.90 GiB |
| 8 GiB | 3.20 GiB | 1.90 GiB | 6.10 GiB |
| 16 GiB | 6.40 GiB | 2.70 GiB | 13.30 GiB |
| 32 GiB | 8.00 GiB | 3.66 GiB | 28.34 GiB |
| 64 GiB | 8.00 GiB | 5.58 GiB | 58.42 GiB |

Claude Code warns at 2.5 GB of heap, which is 2.33 GiB. On a VM sold as 4 GB, the cap of 2.75 GiB is now just above that. The cap also covers the agent's memory outside its heap, the holder and the shadow terminal. So less than 0.4 GiB is left for the agent's tools.

### All sessions together

The sessions' slice, `simstim.slice` in the user's systemd manager, gets the same `MemoryMax`. All sessions together then stay below the host's memory minus the reserve, so one agent still cannot take down the host. A slice's limits "apply to all processes of all units contained in that slice" ([systemd.slice](https://www.freedesktop.org/software/systemd/man/latest/systemd.slice.html)).

The helper sets the limit each time it starts a holder, before `systemd-run`:

1. It reads the slice's `memory.max` in the cgroup tree. If the slice runs and already has the right limit, it does nothing more. A start then costs one small file read.
2. It goes on only where the memory controller is delegated to the user's manager, the same check as the "memory caps" line of `host doctor`. On cgroup v1, or without delegation, no limit would hold, so the helper leaves the slice alone.
3. It reads the slice's `memory.current`. If the running sessions already use more than the new limit together, it does not set the limit. The kernel would stop one of them at once. It writes a warning, and a later start sets the limit once the sessions use less. `memory.current` counts page cache too, so this check is careful. It may wait where the kernel could have made room.
4. Otherwise it runs `systemctl --user set-property simstim.slice MemoryMax=<bytes>`. systemd applies this at once to a running slice, and stores it on disk for future boots. For a slice that is not running, it only stores it, and the limit applies when the slice starts ([systemctl](https://www.freedesktop.org/software/systemd/man/latest/systemctl.html)). The setting goes in a drop-in under `~/.config/systemd/user.control/`, the folder for "configuration created using the dbus API" ([systemd.unit](https://www.freedesktop.org/software/systemd/man/latest/systemd.unit.html)).
5. A failure does not stop the start. The helper writes it to the session's log, but only after the holder answers, so it never reads as the reason that a start failed. `host doctor` shows what sessions may use together, and what is kept for the system. It fails when a running slice has another limit, and says why. When the slice does not run, it says that the limit applies from the next start.

Other ways to set the limit were weighed:

- **A slice unit file** in `~/.config/systemd/user/`, with `systemctl --user daemon-reload` after each change. A reload re-reads every unit of the user's manager. The man pages promise that `set-property` applies at once, but they make no such promise for a reload.
- **`set-property --runtime`.** The limit would be lost at a reboot. The helper sets it again at each start, so this would work too. But a stored limit also holds for anything else that runs in the slice after a reboot. `host doctor` can also read a stored limit while no session runs (`systemctl --user show`).
- **A write to `memory.max`** in the cgroup tree. systemd owns that file, and writes its own value back when it next applies the unit's settings.

Inside the slice, the kernel's out-of-memory killer picks a process by its "badness": its memory as a share of the limit, plus its `oom_score_adj` ([proc](https://docs.kernel.org/filesystems/proc.html)). The holder gives each agent an `oom_score_adj` of 500, and the agent's tools inherit it. So a process from an agent's tree goes before a holder or a shadow terminal, and among those, the biggest goes first. In a session that leaks, that is normally the leaking process. The holder still sees the stop. `memory.events` counts the kills in a cgroup and in all cgroups below it ([cgroup v2](https://docs.kernel.org/admin-guide/cgroup-v2.html)). So the stopped process's own unit counts the kill.

**No ceiling per session.** The 8 GiB ceiling goes, because the slice now does its job: it keeps the host safe however many sessions run. The research note gives a reason to keep it. Agents "can leak to 10 to 14 GiB" ([host runtime](../../research/host-runtime.md#63-memory-caps-and-oom)), and a ceiling stops such a leak early. But a fixed ceiling also stops a real build on a 64 GiB host at 8 GiB. That is the failure of #40 at a larger size. The user knows the work, and `memory_max = "8G"` brings the ceiling back for those who want it.

### A setting for the user

- **Where:** `[sessions] memory_max` in `config.toml`, `"auto"` by default. `new --memory-max <size>` sets it for one session, and wins over the key. The launcher's starts send the key too. All of them reach the host in `NewArgs.MemoryMax`. The helper's own `new` command takes `--memory-max` as well.
- **Syntax:** `auto`, or a number and a unit, as systemd reads `MemoryMax=`: `K`, `M`, `G` or `T`, in powers of 1024. `512M`, `4G` and `1.5G` work. So do `4Gi`, `4GiB` and `4GB`, which all mean 4 GiB. Case does not matter. A number without a unit is refused, because `4` would mean 4 bytes.
- **No `max` or `infinity`.** systemd reads them as no limit. Here they are refused, with a message that `auto` already lets a session use all that the host gives its sessions. A session without a cap of its own would differ from `auto` only while the slice has no limit. Then it would have no cap at all.
- **Smallest:** 256 MiB. A session's holder and shadow terminal share its cap, and below that even a shell has little room. The client refuses a smaller size, with a hint, and so does the helper.
- **Above the slice's limit:** the helper lowers the size to the limit. A unit cannot use more than its slice anyway. Also, one `config.toml` serves hosts of every size, and a smaller host should not refuse a start for that. The session's row shows the cap that applies. A helper from before this change takes the size as it is.

### Swap

Sessions keep `MemorySwapMax=0`, as 0008 decided. Their memory never goes to swap, so swap does not raise a cap, and it does not save a session at its cap. Swap on the host still helps everything outside the sessions. The system and the user's own programs can page out, which leaves the reserve for what needs it. The docs stop asking for "4 GB plus swap", and say this instead.

### Sessions that run now

A running session keeps the cap that its unit got: the helper does not change the unit. The next start works the cap out again, for a new session and for a session that `host restore` starts after a reboot. The manifest keeps the size the user asked for, and a session from before this change counts as `auto`.

The slice gets its limit at the first start after the helper is updated, if the running sessions use less than the limit together. If they use more, the helper waits, so that it stops no running session. Each later start tries again, and until then the old sessions keep only their own caps. Once the slice has its limit, the running sessions share it too. When they reach it together, the kernel stops one of them.

### Not in this record

Later work, from step 4 of the owner's plan in #40 on:

- A warning near the cap, and an offer to resume the agent after a stop.
- Cleaning up a session's unit after its agent ends.
- `doctor` checks that the cap fits the agent, and that show the swap.

## Consequences

- Good, because a VM sold as 4 GB gives a session 2.75 GiB, almost twice the old 1.5 GiB.
- Good, because bigger hosts give their memory to their sessions: 13.3 GiB on a 16 GiB host, not 6.4 GiB.
- Good, because the reserve holds however many sessions run. Under the old rule, three sessions on a 4 GB host could use more memory than the host has.
- Good, because the cap can now be changed, for one session or in the config, as 0006 said it could.
- Bad, because sessions share one limit. When they reach it together, the kernel stops the biggest process from any agent's tree in the slice. That can be in a session that did not grow. Before the stop, every session feels the pressure: the kernel takes back their page cache, and they slow down. Under the old rule, each session had its own box. A user who wants that back can give each session a `memory_max`, so that the caps add up to less than the limit.
- Bad, because a leak grows further before the stop: on a 64 GiB host, to 58.4 GiB where it stopped at 8 GiB.
- Bad, because the helper now changes a unit that it did not start: the slice's limit lives in a drop-in under `~/.config/systemd/user.control/`. A limit that the user sets on the slice by hand is replaced at the next start. An uninstall must remove the drop-in too, with `systemctl --user revert simstim.slice`.
- Bad, because the reserve does not know about big programs outside the sessions, such as a database that the user runs on the same host. The kernel's global out-of-memory killer still protects the host then, and agents stay its first choice.
- Hosts without a systemd user manager, such as a Docker container, still run sessions without caps.
