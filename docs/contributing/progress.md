# Progress of the isolation and CLI plans

Updated 2026-11-27. This page tracks the tasks of the [isolation plan](isolation-plan.md) and the [CLI plan](cli-plan.md) on the feature branch `docs/isolated-sessions`. Nothing reaches `main` until every task is done.

States:

- **Done:** merged on the feature branch with its tests and docs.
- **Works on a real host:** done, and checked end to end on a real Linux host or Mac.
- **In progress:** being finished now.
- **Started:** code is on the branch, but the task is not yet proven to work.

## Summary

| State | Tasks |
| --- | --- |
| Done | 17 |
| Started | 82 |
| **All** | **99** |

## Current focus

- **Make the branch build and test cleanly.** The unfinished work from the integration stack is on the branch, and package by package its tests are being made to pass. Egress, image and supply, host, the launcher and the docs generator are done. The CLI, the client, the supervisor and the guest are in progress.
- **Set up from a clean checkout.** Setup and the build use the Go toolchain that `go.mod` pins, with no private build steps.
- **CI.** Lint, the Windows tests, the end-to-end tests against a Docker host, the attach benchmark and the image builds are being fixed, one job at a time.
- **Next.** The daily-use path, end to end, on a real Linux host and on a Mac as a host: add a host, start a session with Claude Code or Codex, attach and detach, move files, then stop and destroy.

## Every task

| Task | Phase | State | Title |
| --- | --- | --- | --- |
| H2 | 0 | Done | Turn off forwarding on every connection |
| P0.1 | 0 | Done | Bump the shadow terminal's Ghostty pin, and cap terminal sizes |
| P0.2 | 0 | Done | A hook socket of its own, and a peer check on the holder's socket |
| P0.3 | 0 | Done | Check an agent's session ID before it enters a command |
| P0.4 | 0 | Done | Clean every string that a host sends before the client draws it |
| P0.5 | 0 | Done | A paste guard |
| P0.6 | 0 | Done | Tie setup trust to the script's content |
| P0.7 | 0 | Done | Safe copies and safer git on the trusted side |
| P0.8 | 0 | Done | Lima VMs that cannot reach the laptop's loopback |
| P0.9 | 0 | Done | Watch pins that no package ecosystem covers |
| H3 | 0 | Started | Pinned host keys and `host rekey` |
| H4 | 0 | Started | The host ID and the tiers file |
| H5 | 0 | Started | The grant gate and the grant ledger |
| P1.1 | 1 | Started | Wall levels, the sandbox probe and the floor |
| H7 | 1 | Started | The root step |
| P1.2 | 1 | Done | The link |
| P1.3 | 1 | Started | Jails |
| P1.4 | 1 | Started | The supervisor, the guest init and the holder's guest mode |
| P1.5 | 1 | Started | The process wall |
| P1.6 | 1 | Started | The files door |
| P1.7 | 1 | Started | The egress process, first version |
| P1.8 | 1 | Started | The client and the launcher |
| P1.12 | 1 | Started | Layers, profiles and locks |
| P1.13 | 1 | Started | Shells and commands in a sandbox |
| P1.14 | 1 | Started | Sizes, variables, files and seeds from the laptop |
| P1.15 | 1 | Started | Kept caches |
| P1.9 | 1 | Started | Paste and ports through the supervisor |
| P1.10 | 1 | Started | Pull through a bundle, with a review |
| P1.11 | 1 | Started | Destroy, stop, restore and the phase's checks |
| P1.16 | 1 | Started | Runs for many sessions |
| P2.1 | 2 | Started | DNS policy |
| P2.2 | 2 | Started | Interception, and method and path rules |
| P2.3 | 2 | Started | Bound secrets |
| P2.4 | 2 | Started | The model API guard |
| P2.5 | 2 | Started | Service rules |
| P2.6 | 2 | Started | Holds and approvals |
| P2.11 | 2 | Started | The unknown action and the classifier's fixed steps |
| P2.12 | 2 | Started | The judge |
| P2.13 | 2 | Started | SSH keys |
| P2.14 | 2 | Started | Change policy while sessions run |
| P2.15 | 2 | Started | Editors over SSH |
| P2.16 | 2 | Started | Model usage and budgets |
| P2.17 | 2 | Started | More agent rows |
| P2.7 | 2 | Started | Budgets and stricter targets |
| P2.8 | 2 | Started | Package managers in the guest |
| P2.9 | 2 | Started | A repository's own file |
| P2.10 | 2 | Started | Walls by default |
| P4.1 | 3 | Started | The release manifest and the check before the bridge |
| P3.1 | 3 | Started | The boot pair |
| H9 | 3 | Started | Runtime delivery and the host cache |
| P3.2 | 3 | Started | Images |
| P3.3 | 3 | Started | The Cloud Hypervisor driver |
| P3.4 | 3 | Started | The guest init on a microvm |
| P3.5 | 3 | Started | The agents disk |
| P3.6 | 3 | Started | Lifecycle on a microvm |
| P3.7 | 3 | Started | The microvm probe, the kernel list and Lima |
| H10 | 3 | Started | Prepare in `host add` |
| P3.8 | 3 | Started | Conformance, measurements and release |
| P4.2 | 4 | Started | Run verified bytes only |
| P4.3 | 4 | Started | Reproducible kernel and tools image |
| P4.4 | 4 | Done | A signed-tag release gate |
| P4.5 | 4 | Started | Image policy |
| P4.6 | 4 | Started | Known-bad versions |
| H11 | 4 | Started | Facts and admission |
| H12 | 4 | Started | Named placement |
| H13 | 4 | Started | `new auto` |
| H14 | 4 | Started | Health, runtime upgrades and drain |
| H15 | 4 | Started | Leave and wipe |
| H16 | 4 | Started | Reach a host behind NAT |
| H19 | 4 | Started | Host rules |
| H20 | 4 | Started | Machines that join |
| H21 | 4 | Started | A Mac as a host |
| P5.1 | 5 | Started | Fork |
| P5.2 | 5 | Started | Copy files in and out |
| P5.4 | 5 | Started | Checkpoints |
| P5.5 | 5 | Started | Save an image |
| P6.1 | 6 | Started | The gvisor wall |
| P6.2 | 6 | Started | The Firecracker driver |
| P6.4 | 6 | Started | Nitro Sandboxes on EC2 |
| P6.3 | 6 | Started | Unwalled sessions on the supervisor's path |
| P7.1 | 7 | Started | Registry gates in the egress process |
| P7.2 | 7 | Started | Decoys |
| P7.3 | 7 | Started | Observe terminal output |
| P7.4 | 7 | Started | A machine for each session |
| H17 | 8 | Started | Separate accounts, part 1: the root-owned launcher |
| H18 | 8 | Started | Separate accounts, part 2: slot accounts and the root-owned store |
| C1 | cli | Started | The grammar record |
| C2 | cli | Done | Errors, suggestions and exit codes |
| C4 | cli | Done | Prompts, pickers and machine output |
| C3 | cli | Done | Help that teaches |
| C5 | cli | Started | Session verbs |
| C6 | cli | Started | One meaning for each flag |
| C7 | cli | Started | Host commands |
| C8 | cli | Started | Agents: install and log in |
| C9 | cli | Started | `config check` |
| C10 | cli | Started | Completion |
| C11 | cli | Started | The launcher and the CLI agree |
| C12 | cli | Done | Docs for agents |
| T1 | test reliability | Done | The fake envd hang and the input and kill fixes |
