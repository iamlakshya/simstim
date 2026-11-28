# Progress of the isolation and CLI plans

Updated 2026-11-28. This page tracks the tasks of the [isolation plan](isolation-plan.md) and the [CLI plan](cli-plan.md) on the feature branch `docs/isolated-sessions`. Nothing reaches `main` until every task is done.

States:

- **Done:** merged on the feature branch with its tests and docs.
- **Works on a real host:** done, and checked end to end on a real Linux host or Mac.
- **In progress:** being finished now.
- **To do:** not built on the branch yet.

When: **Day 1** tasks make the first working product (the daily-use path). **Later** tasks stay in scope and come after it, at the end of the roadmap, one at a time. A Later task that a maintained library makes easy may come earlier. See [reuse](../research/reuse.md).

## Summary

| State | Tasks |
| --- | --- |
| Done | 55 |
| To do | 44 |
| **All** | **99** |

## Current focus

- **A new start on the published base (27 November 2026).** The branch's code is back to `main` plus the 17 finished tasks: a small product that works. The research, the specs, the plans and the decision records are kept as they were.
- **Reuse first.** Each remaining task is built with maintained open-source parts where they meet the need. [Reuse](../research/reuse.md) gives the choice for every area: bubblewrap for the process wall, one systemd unit per session, a model gateway route in the egress process, SFTP and git bundles for files, the system `ssh` with pinned host keys, and Lima for a Mac as a host.
- **Day 1 first.** The first working product is the daily-use path: a Mac client and a Linux server host, a session with Claude Code or Codex, attach and detach, files in and out, then stop and destroy. Later tasks stay in scope and follow it.
- **Milestone M1, checked on a real Linux host (28 November 2026).** From a Mac client to a Debian 13 host, this works: host add with the sandbox report; sessions at the process wall (bubblewrap with seccomp and Landlock), where the session cannot see the host's home or keys; the egress allowlist, which refuses unknown hosts; Claude Code and Codex reaching their providers only through the model gateway, with the key held outside the sandbox; copying files and folders in and out; stop, resume and remove, with nothing left behind. The last step, a logged-in run with real accounts, waits on the owner's tokens.
- **The gVisor wall, checked on a real Linux host (28 November 2026).** The host helper installs the pinned upstream runsc. A session at the gvisor level runs on gVisor's own kernel, with the same egress policy and file copying as the process wall. The microVM wall is next.
- **The microVM wall, checked on a real Linux host (28 November 2026).** One host command installs the pinned Cloud Hypervisor and kernel, and one client command pulls a pinned base image. A microVM session runs on its own kernel, behind the same egress policy, with copying, stop, resume from its kept disk, and removal all working.

## Every task

| Task | Phase | When | State | Title |
| --- | --- | --- | --- | --- |
| H2 | 0 | Day 1 | Done | Turn off forwarding on every connection |
| P0.1 | 0 | Day 1 | Done | Bump the shadow terminal's Ghostty pin, and cap terminal sizes |
| P0.2 | 0 | Day 1 | Done | A hook socket of its own, and a peer check on the holder's socket |
| P0.3 | 0 | Day 1 | Done | Check an agent's session ID before it enters a command |
| P0.4 | 0 | Day 1 | Done | Clean every string that a host sends before the client draws it |
| P0.5 | 0 | Day 1 | Done | A paste guard |
| P0.6 | 0 | Day 1 | Done | Tie setup trust to the script's content |
| P0.7 | 0 | Day 1 | Done | Safe copies and safer git on the trusted side |
| P0.8 | 0 | Day 1 | Done | Lima VMs that cannot reach the laptop's loopback |
| P0.9 | 0 | Day 1 | Done | Watch pins that no package ecosystem covers |
| H3 | 0 | Day 1 | Done | Pinned host keys and `host rekey` |
| H4 | 0 | Day 1 | To do | The host ID and the tiers file |
| H5 | 0 | Day 1 | To do | The grant gate and the grant ledger |
| P1.1 | 1 | Day 1 | Done | Wall levels, the sandbox probe and the floor |
| H7 | 1 | Day 1 | Done | The root step |
| P1.2 | 1 | Day 1 | Done | The link |
| P1.3 | 1 | Day 1 | To do | Jails |
| P1.4 | 1 | Day 1 | Done | The supervisor, the guest init and the holder's guest mode |
| P1.5 | 1 | Day 1 | Done | The process wall |
| P1.6 | 1 | Day 1 | Done | The files door |
| P1.7 | 1 | Day 1 | Done | The egress process, first version |
| P1.8 | 1 | Day 1 | To do | The client and the launcher |
| P1.12 | 1 | Day 1 | To do | Layers, profiles and locks |
| P1.13 | 1 | Day 1 | Done | Shells and commands in a sandbox |
| P1.14 | 1 | Day 1 | Done | Sizes, variables, files and seeds from the laptop |
| P1.15 | 1 | Day 1 | Done | Kept caches |
| P1.9 | 1 | Day 1 | Done | Paste and ports through the supervisor |
| P1.10 | 1 | Day 1 | Done | Pull through a bundle, with a review |
| P1.11 | 1 | Day 1 | Done | Destroy, stop, restore and the phase's checks |
| P1.16 | 1 | Day 1 | Done | Runs for many sessions |
| P2.1 | 2 | Day 1 | Done | DNS policy |
| P2.3 | 2 | Day 1 | Done | Bound secrets |
| P2.4 | 2 | Day 1 | Done | The model API guard |
| P2.13 | 2 | Day 1 | To do | SSH keys |
| P2.16 | 2 | Day 1 | Done | Model usage and budgets |
| P2.8 | 2 | Day 1 | Done | Package managers in the guest |
| P2.10 | 2 | Day 1 | To do | Walls by default |
| P4.1 | 3 | Day 1 | Done | The release manifest and the check before the bridge |
| H10 | 3 | Day 1 | To do | Prepare in `host add` |
| P4.4 | 4 | Day 1 | Done | A signed-tag release gate |
| H15 | 4 | Day 1 | To do | Leave and wipe |
| H16 | 4 | Day 1 | To do | Reach a host behind NAT |
| H20 | 4 | Day 1 | To do | Machines that join |
| H21 | 4 | Day 1 | Done | A Mac as a host |
| C1 | cli | Day 1 | To do | The grammar record |
| C2 | cli | Day 1 | Done | Errors, suggestions and exit codes |
| C4 | cli | Day 1 | Done | Prompts, pickers and machine output |
| C3 | cli | Day 1 | Done | Help that teaches |
| C5 | cli | Day 1 | Done | Session verbs |
| C6 | cli | Day 1 | Done | One meaning for each flag |
| C7 | cli | Day 1 | Done | Host commands |
| C8 | cli | Day 1 | Done | Agents: install and log in |
| C9 | cli | Day 1 | Done | `config check` |
| C10 | cli | Day 1 | Done | Completion |
| C11 | cli | Day 1 | Done | The launcher and the CLI agree |
| C12 | cli | Day 1 | Done | Docs for agents |
| T1 | test reliability | Day 1 | Done | The fake envd hang and the input and kill fixes |
| P2.2 | 2 | Later | Done | Interception, and method and path rules |
| P2.5 | 2 | Later | To do | Service rules |
| P2.6 | 2 | Later | To do | Holds and approvals |
| P2.11 | 2 | Later | To do | The unknown action and the classifier's fixed steps |
| P2.12 | 2 | Later | To do | The judge |
| P2.14 | 2 | Later | To do | Change policy while sessions run |
| P2.15 | 2 | Later | To do | Editors over SSH |
| P2.17 | 2 | Later | To do | More agent rows |
| P2.7 | 2 | Later | To do | Budgets and stricter targets |
| P2.9 | 2 | Later | To do | A repository's own file |
| P3.1 | 3 | Later | Done | The boot pair |
| H9 | 3 | Later | Done | Runtime delivery and the host cache |
| P3.2 | 3 | Later | Done | Images |
| P3.3 | 3 | Later | Done | The Cloud Hypervisor driver |
| P3.4 | 3 | Later | Done | The guest init on a microvm |
| P3.5 | 3 | Later | Done | The agents disk |
| P3.6 | 3 | Later | Done | Lifecycle on a microvm |
| P3.7 | 3 | Later | Done | The microvm probe, the kernel list and Lima |
| P3.8 | 3 | Later | To do | Conformance, measurements and release |
| P4.2 | 4 | Later | To do | Run verified bytes only |
| P4.3 | 4 | Later | To do | Reproducible kernel and tools image |
| P4.5 | 4 | Later | To do | Image policy |
| P4.6 | 4 | Later | To do | Known-bad versions |
| H11 | 4 | Later | To do | Facts and admission |
| H12 | 4 | Later | To do | Named placement |
| H13 | 4 | Later | To do | `new auto` |
| H14 | 4 | Later | To do | Health, runtime upgrades and drain |
| H19 | 4 | Later | To do | Host rules |
| P5.1 | 5 | Later | To do | Fork |
| P5.2 | 5 | Later | To do | Copy files in and out |
| P5.4 | 5 | Later | To do | Checkpoints |
| P5.5 | 5 | Later | To do | Save an image |
| P6.1 | 6 | Later | Done | The gvisor wall |
| P6.2 | 6 | Later | To do | The Firecracker driver |
| P6.4 | 6 | Later | To do | Nitro Sandboxes on EC2 |
| P6.3 | 6 | Later | To do | Unwalled sessions on the supervisor's path |
| P7.1 | 7 | Later | To do | Registry gates in the egress process |
| P7.2 | 7 | Later | To do | Decoys |
| P7.3 | 7 | Later | To do | Observe terminal output |
| P7.4 | 7 | Later | To do | A machine for each session |
| H17 | 8 | Later | To do | Separate accounts, part 1: the root-owned launcher |
| H18 | 8 | Later | To do | Separate accounts, part 2: slot accounts and the root-owned store |
