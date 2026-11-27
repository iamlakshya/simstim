# Isolated sessions

Researched on 9 and 10 November 2026.

This note asks how each session could run in its own sandbox. The sandbox must not reach the host, other sessions or the laptop unless the user grants it. It is the evidence behind roadmap item O2, which widens X6 ([roadmap](../roadmap.md), [#100](https://github.com/iamlakshya/simstim/issues/100), [#13](https://github.com/iamlakshya/simstim/issues/13)). It builds on the [sandboxes note](sandboxes.md), which covers providers as hosts, and on the [security model](../explanation/security.md), which says what the tool does today.

Two Monte Carlo tree searches ran. The main search looked at the sandbox design, in sections 1 to 15. The hosts search looked at the machines that a user adds as hosts, in [Your own hosts](#your-own-hosts). A second pass, without a search, looked at configurable policy, shells and automatic classifiers, in [Policy for parallel work](#policy-for-parallel-work). The design that follows from both is in decision [0029](../contributing/decisions/0029-a-sandbox-per-session.md), [the spec](../contributing/isolation-spec.md) and [the plan](../contributing/isolation-plan.md).

The rules for the note:

- Facts come from public docs, specifications, security advisories, papers and public source code, and from this repository's code on 9 November 2026. Each fact links its source.
- No account was made, no API key was used and nothing was installed. No VM, container or sandbox was started. Figures come from the sources, not from our own runs. Two sets of figures were fetched from public registry endpoints on 9 November 2026, and the text says so.
- Closed products (Docker Sandboxes, a hosted VM service, Claude Code, Codex) are described in our own words, with links. No text, code or prompt was copied from them.
- **UNVERIFIED** marks a claim that no primary source confirmed.
- A fact check and a red team read the first draft of the design. Where the fact check corrected a claim, this note uses the correction. Every gap that the red team found has a fix in the design or a line in [the risks](#14-risks-that-remain).
- Scores run from 0 to 10. A value is a weighted score from 0 to 1 ([section 3.2](#32-the-rubric)).
- Both searches follow these rules. The hosts search adds a few of its own ([Your own hosts](#your-own-hosts)).
- The full list of open questions is in [the spec](../contributing/isolation-spec.md#15-open-questions). This note cites each one by its number there, such as [question 23](../contributing/isolation-spec.md#15-open-questions).

## Summary

1. **Today a session can reach everything.** All sessions run as one Unix user. Each can read the home folder, the agent logins and other sessions' worktrees, and each can reach the whole network. Any agent can also drive any other session, because holder sockets share one uid and `serve` checks no peer ([holder_conn.go](../../internal/host/holder_conn.go)). Fix this first, whatever wall wins.
2. **The threat is real and aimed at developers.** In 2025 and 2026, poisoned npm and PyPI packages stole tokens and published themselves with them. One ran the victim's AI coding CLIs with their approvals off to hunt for secrets (Nx, August 2025). A worm wrote CI workflows through the GitHub API (Shai-Hulud 2.0, November 2025). A prompt injection used an allowed model API, with the attacker's own key, to carry files out ([section 1.3](#13-supply-chain-incidents-aimed-at-developers)).
3. **Permission prompts do not scale.** Users approved about 93% of prompts in Anthropic's data. Malware starts the agent with its own flags, so no prompt appears at all. Only the wall and the closed doors held ([section 1.5](#15-why-permission-prompts-do-not-scale)).
4. **The search.** A Monte Carlo tree search over five layers (wall, control, lifecycle, doors, supply chain) ran 7 iterations. Researchers studied 35 options, and two judges scored each one. The best path reached 0.687 at its best node and 0.675 at its leaf. The choices at L1 and at L3 to L5 hold under six weight sets, and L2 is a close call ([section 3.6](#36-weight-sensitivity)).
5. **Wall (L1): levels with a pinned floor.** The levels are `none` (W0), `process` (W1, a hardened process), `gvisor` (W2) and `microvm` (W3), and `none` needs a named downgrade. W4, one provider machine per session, is a placement, not a level. The laptop pins each host's strongest working level at setup as its floor. A session never starts below it unless the user names a downgrade. The option won through its subtree: the hardened process scored higher alone (0.597 against 0.575), but its security of 4.5 capped its subtree at 0.612.
6. **Control (L2): the holder inside the wall, a thin supervisor outside.** Every parser of hostile bytes (the shadow terminal, the screen scanner, hook JSON) stays inside. The supervisor keeps the limits, the policy, the audit log, the alert destinations and the real keys.
7. **Lifecycle (L3): cold boot from a verified image, state on disk only.** Start, wake and restore after a reboot share one path: a boot, then the agent's own resume command. A stop frees all RAM, and no memory image exists. The price: a stop loses the running turn and the live screen.
8. **Doors (L4): no network card, one egress process per session, sentinel keys.** The guest sees random sentinels. The trusted side swaps in the real key only for the hosts bound to it, and refuses any other credential there. This closes key theft and the "attacker's own key" path.
9. **Supply chain (L5): the laptop holds the digests.** Every binary, kernel and image that a session boots is named by a digest that the client sends. Release builds are reproducible and signed. A daily job watches advisories for pins that no scanner covers. The current Ghostty pin sits inside two October 2026 advisory ranges.
10. **The red team found 23 gaps. The worst is the login user.** The VMM, the egress process and the image converter all ran as the login user in the first draft. A VMM escape would then reach every session's keys and the shell rc files. The design now gives each of them its own identity and jail ([section 11](#11-the-red-teams-gaps)).
11. **Platforms.** Linux with KVM gets W3. EC2 instances that support Nitro Enclaves, Graviton included, also get W3 from 9 November 2026, through AWS Nitro Sandboxes and Firecracker 1.18, with no KVM inside the instance ([section 10.1](#101-aws-nitro-sandboxes-on-ec2)). Other cloud VMs without nested virtualisation (Hetzner Cloud, GCE Arm) get W2. A Mac gets W3 only through Lima on an M3 or later chip with macOS 15 or later, and W2 in Lima otherwise. Hosts made by the docker provider give W0 and need a named downgrade.
12. **The design bends rules, and says so.** No network port opens, and the supervisor dials into the guest, so no host socket waits for a guest ([0030](../contributing/decisions/0030-local-sockets-do-not-listen.md)). Real keys live in host memory for the whole session ([0032](../contributing/decisions/0032-secret-values-in-memory-only.md)). The trusted side opens TLS to some hosts. The host gains a VMM, runsc, a guest kernel and images, plus a one-time root step ([section 13](#13-rules-the-design-bends)).
13. **Your own hosts.** The hosts search ran 4 iterations over three layers: attach, placement and fleet. Its best path, at 0.732, is SSH add, then client placement with host admission, then tiers as a grant ceiling. Only the client enforces the tiers: `own`, `throwaway` and `shared`. The path holds under all six weight sets ([Your own hosts](#your-own-hosts)).
14. **Policy for parallel work.** A second pass, on 10 November 2026, answered the owner's request for one config with overrides, one-off exceptions, shells beside the agent and automatic classifiers. Every product that decides on its own puts rules before a model, and every model guard in the research broke under adaptive attack. So the design adds six layers of policy with locks, an unknown action for each layer, and a classifier with fixed checks first and an optional judge ([Policy for parallel work](#policy-for-parallel-work)).

## 1. Why

### 1.1 What a session can reach today

The code on 9 November 2026 gives a session these paths. Each one is a fact about this repository, read in the file named.

| Path | Where | Effect |
| --- | --- | --- |
| All holder sockets are mode 0600 in one folder of one user, and `Holder.serve` checks no peer and no `Hello.Role` | [holder_conn.go](../../internal/host/holder_conn.go), [holder_request.go](../../internal/host/holder_request.go) | An agent in one session can attach to, type into or kill any other session |
| The agent's environment comes from the host login shell (`LoginEnv`) | [ops.go](../../internal/host/ops.go), [env.go](../../internal/host/env.go) | Every host variable reaches every agent |
| A hook's `session_id` becomes `AgentSessionID` with no check, and `resumeArgv` puts it into the resume command | [hooks.go](../../internal/host/hooks.go), [restore.go](../../internal/host/restore.go) | A hostile value could add a flag to the agent's next start |
| Setup trust is a yes or no for each host and repository, not tied to the script's content | [trust.go](../../internal/client/trust.go) | A changed setup script runs under the old yes |
| `copyWorktreeInclude` checks only for `..` and follows symlinks | [worktree.go](../../internal/host/worktree.go) | A symlink in the checkout can copy a host file into a session |
| `removeWorktree` and `pull` run `git status` in the session's repository on the host | [worktree.go](../../internal/host/worktree.go), [pull.go](../../internal/client/pull.go) | A session that writes `core.fsmonitor` runs a command as the host user |
| `oscSafe` strips only C0 and DEL, and the launcher shows `Status.Reason` raw | [attach_notify.go](../../internal/client/attach_notify.go), [detail.go](../../internal/tui/detail.go) | A session can send escape sequences into the launcher |
| `dial` runs the installed helper with no hash check, from a folder that the agent's user can write | [conn.go](../../internal/client/conn.go) | A session can replace the helper for every later session |
| Live terminal output is not filtered | [security model](../explanation/security.md) | OSC 52, OSC 8 and queries reach the laptop's terminal |
| `host create lima` passes only `--plain`, so Lima's `hostLoopback` default stays on | [lima.go](../../internal/provider/lima/lima.go), [Lima user network](https://lima-vm.io/docs/config/network/user/) | A Lima guest reaches services on the Mac's `127.0.0.1` |
| The shadow terminal pins Ghostty commit `ad97d331`, 190 commits before the fix `e257bb45` | [build.zig.zon](../../vt/build.zig.zon), [compare](https://api.github.com/repos/ghostty-org/ghostty/compare/e257bb45e6d4d61c14eacdc717be89c6110f99ff...ad97d331459a0762f7aea5f56fe86748e03b860f) | The parser of hostile bytes is in two advisory ranges |

### 1.2 The threat model

- **T1. A malicious dependency or tool runs code inside a session.** A postinstall script, a `.pth` file or a poisoned binary runs as the agent.
- **T2. Prompt injection makes the agent exfiltrate code or secrets.** Text that the agent reads turns it against the user, and it sends data out through a door that is open.
- **T3. An autonomous agent does destructive things.** Examples: `rm -rf`, a force-push, or deleting cloud resources with a granted credential.
- **T4. A compromised session attacks other sessions, the host or the laptop.** This includes terminal escape sequences, which reach the user's terminal unchanged by rule.
- **T5. Credentials are stolen,** as distinct from credentials that are misused while the session runs. A stolen token keeps working after the session ends. A misused one does harm only while the session lives.

### 1.3 Supply-chain incidents aimed at developers

| When | What happened | Threats | Sources |
| --- | --- | --- | --- |
| March 2025 | tj-actions/changed-files: attackers moved the action's version tags to a malicious commit (CVE-2025-30066). Every workflow that used a tag ran it. | T1, T5 | [GHSA-mw4p-6x4p-x5m5](https://github.com/tj-actions/changed-files/security/advisories/GHSA-mw4p-6x4p-x5m5) |
| July 2025 | Amazon Q Developer for VS Code 1.84.0 shipped with injected code (CVE-2025-8217). AWS says the code failed to run because of a syntax error. Secondary reports say it told the agent to wipe local files and cloud resources (**UNVERIFIED** from a primary source). | T1, T3 | [AWS-2025-015](https://aws.amazon.com/security/security-bulletins/AWS-2025-015/) |
| 26 August 2025 | Nx "s1ngularity": malicious Nx versions ran a postinstall script. It started the installed AI CLIs with their approvals off (`claude --dangerously-skip-permissions`, `gemini --yolo`, `q --trust-all-tools`) to list secret files. It posted them to a new public repository in the victim's own GitHub account, and added a shutdown command to shell rc files. A flawed GitHub Actions workflow had leaked the publish token. | T1, T2, T5 | [GHSA-cxm3-wv7p-598c](https://github.com/nrwl/nx/security/advisories/GHSA-cxm3-wv7p-598c), [Snyk](https://snyk.io/blog/weaponizing-ai-coding-agents-for-malware-in-the-nx-malicious-package/), [StepSecurity](https://www.stepsecurity.io/blog/supply-chain-security-alert-popular-nx-build-system-package-compromised-with-data-stealing-malware) |
| 8 September 2025 | chalk and debug: a phished maintainer's account published 18 very popular packages with a browser wallet stealer. | T1 | [Aikido](https://www.aikido.dev/blog/npm-debug-and-chalk-packages-compromised) |
| September 2025 | Shai-Hulud: a self-replicating worm compromised more than 500 npm packages. It ran TruffleHog to find secrets, uploaded them to public repositories, pushed a workflow that sent repository secrets out, and published itself with the victim's tokens. | T1, T2, T5 | [CISA](https://www.cisa.gov/news-events/alerts/2025/09/23/widespread-supply-chain-compromise-impacting-npm-ecosystem), [Wiz](https://www.wiz.io/blog/shai-hulud-npm-supply-chain-attack) |
| 24 November 2025 | Shai-Hulud 2.0: 796 packages and 1,092 versions, through a new preinstall script. It wrote a workflow through the GitHub contents API that registers the machine as a self-hosted runner. Without credentials, it tried to wipe the home folder. | T1, T3, T5 | [Datadog](https://securitylabs.datadoghq.com/articles/shai-hulud-2.0-npm-worm/), [Unit 42](https://unit42.paloaltonetworks.com/npm-supply-chain-attack/), [Sysdig](https://www.sysdig.com/blog/return-of-the-shai-hulud-worm-affects-over-25-000-github-repositories) |
| 24 March 2026 | litellm 1.82.7 and 1.82.8 on PyPI were live for about 40 minutes. 1.82.8 shipped a `.pth` file that runs at every Python start. | T1 | [litellm](https://docs.litellm.ai/blog/security-update-march-2026) |
| April to July 2026 | npm waves through preinstall hooks: the Bitwarden CLI (live about 1.5 hours on 22 April) and @antv (639 versions of 323 packages). The AsyncAPI wave in July ran at import time, so install-script checks missed it. | T1 | [Unit 42](https://unit42.paloaltonetworks.com/monitoring-npm-supply-chain-attacks/) |
| 2026 | TanStack wave: an optional dependency on a GitHub commit whose `prepare` script runs at install. The versions carried valid SLSA provenance. | T1 | [StepSecurity](https://www.stepsecurity.io/blog/mini-shai-hulud-is-back-a-self-spreading-supply-chain-attack-hits-the-npm-ecosystem) |
| 2026 | Phantom Gyp: a 157-byte `binding.gyp` makes npm run `node-gyp rebuild` with no install script in `package.json`. | T1 | [StepSecurity](https://www.stepsecurity.io/blog/binding-gyp-npm-supply-chain-attack-spreads-like-worm) |
| 2025 and 2026 | A committed `.claude/settings.json` or `.mcp.json` ran commands or leaked the API key when a user opened the repository in Claude Code (CVE-2025-59536, CVE-2026-21852). | T1, T4, T5 | [Check Point](https://research.checkpoint.com/2026/rce-and-api-token-exfiltration-through-claude-code-project-files-cve-2025-59536/) |
| 2025 and 2026 | A prompt injection uploaded files to the attacker's Files API account over the allowed `api.anthropic.com`, with the attacker's key. Anthropic later reported the same path in Cowork, and fixed it with a proxy that passes only the VM's own token. | T2 | [Embrace The Red](https://embracethered.com/blog/posts/2025/claude-abusing-network-access-and-anthropic-api-for-data-exfiltration/), [Anthropic](https://www.anthropic.com/research/how-we-contain-claude) |
| 3 November 2026 | Docker Sandboxes CVE-2026-103916: `sbx cp` followed symlinks that a sandbox planted in its workspace, so a copy could read or write host files outside the allowlist. Fixed in sbx 0.51.0. An earlier `sbx cp` bug, CVE-2026-17106, was fixed in 0.38.0. | T4 | [Endor Labs](https://www.endorlabs.com/learn/docker-sandboxes-vulnerability-lets-ai-agents-access-sensitive-files-outside-their-sandbox-cve-2026-103916), [CVE record](https://www.cve.org/CVERecord?id=CVE-2026-103916) |

### 1.4 Walls break too

A wall is code, and code has bugs. These bugs set the design's limits.

| Bug | What it shows | Sources |
| --- | --- | --- |
| CVE-2024-1086, a use-after-free in nf_tables, is in CISA's KEV catalogue. Public exploits reach it through unprivileged user namespaces. | A shared kernel is one bug from a full escape | [Red Hat](https://access.redhat.com/security/cve/cve-2024-1086) |
| CVE-2026-5747: an out-of-bounds write in Firecracker's virtio PCI transport (1.13.0 to 1.14.3, and 1.15.0) lets guest root crash the VMM or run code on the host. CVSS 4.0 score 8.7. Fixed in 1.14.4 and 1.15.1. PCI is opt-in. | The VMM is attack surface | [NVD](https://nvd.nist.gov/vuln/detail/CVE-2026-5747) |
| CVE-2026-122192: in Firecracker 0.23.0 to 1.16.1, a failed memory discard can let guest root read or change the memory of the Firecracker process. It applies only to a VM restored from a snapshot with the File memory backend and a balloon or virtio-mem device ([advisory](https://github.com/firecracker-microvm/firecracker/security/advisories/GHSA-48ww-rqpp-2mgm)). Fixed in 1.16.2 and 1.17.0. | Every VMM line needs a minimum version, and the minimum moves | [AWS](https://aws.amazon.com/security/security-bulletins/2026-169-aws/) |
| CVE-2026-45782 (GHSA-f47p-p25q-83rh): a use-after-free in Cloud Hypervisor's virtio-block async I/O, from 21.0 to 51.1. Fixed in 51.2 and 52.0. | The same, for the other VMM | [advisory](https://github.com/cloud-hypervisor/cloud-hypervisor/security/advisories/GHSA-f47p-p25q-83rh), [v52.0](https://www.cloudhypervisor.org/blog/cloud-hypervisor-v52.0-released/) |
| CVE-2026-27211 (GHSA-jmr4-g2hv-mjj6): a guest writes a QCOW2 header with a host path as its backing file, and Cloud Hypervisor serves that host file at the next boot. v51.0 added a `backing_files` switch. | Never let a VMM probe the format of a guest-written disk | [advisory](https://github.com/cloud-hypervisor/cloud-hypervisor/security/advisories/GHSA-jmr4-g2hv-mjj6), [v51.0](https://www.cloudhypervisor.org/blog/cloud-hypervisor-v51.0-released/) |
| VsockDrop, CVE-2026-53365 (August 2026): an unprivileged local user becomes root through vsock and io_uring. Red Hat's mitigation unloads and blocks `vhost_vsock`. | Host kernel modules are attack surface, with no VM needed | [Red Hat](https://access.redhat.com/security/cve/cve-2026-53365), [CVE record](https://www.cve.org/CVERecord?id=CVE-2026-53365) |
| PwnEscape, CVE-2026-99999 (October 2026): a host-kernel IPv4 checksum flaw, turned into a Firecracker guest escape on a setup without the jailer. Patched kernels shipped in mid-October. That its trigger needs guest networking through TAP or vhost-net is **UNVERIFIED** ([TuxCare](https://tuxcare.com/blog/pwnescape-kvm-guest-escape/)). | A guest network card is a path to the host kernel | [write-up](https://pwn.ai/blog/firecracker-escape), [AlmaLinux](https://almalinux.org/blog/2026-10-16-pwnescape-cve-2026-99999/) |
| Vghost, CVE-2026-74580 (October 2026): a user who can open a `/dev/vhost-*` node becomes root, and in some setups a guest can write host memory. | Membership of the kvm group is not harmless | [Tencent](https://matrix.tencent.com/en/2026/10/12/vghost-cve-2026-74580) |
| GHSA-w9q4-5jvv-mcv4 (27 October 2026): terminal output alone can corrupt memory in libghostty-vt. GitHub rates it Moderate. Its advisory says that libghostty-vt is affected in its default configuration ([build facts](build-facts.md#61-ghostty-and-its-advisories)). | Terminal parsers are memory-unsafe attack surface | [advisory](https://github.com/ghostty-org/ghostty/security/advisories/GHSA-w9q4-5jvv-mcv4) |
| CVE-2024-56803 (Ghostty) and CVE-2024-38396 (iTerm2): a title report turned output into command input. | Answer-back is an injection path to the laptop | [NVD](https://nvd.nist.gov/vuln/detail/CVE-2024-56803), [NVD](https://nvd.nist.gov/vuln/detail/CVE-2024-38396) |

### 1.5 Why permission prompts do not scale

- **People approve almost everything.** Anthropic reports that users approved about 93% of permission prompts. In its test, a phished prompt exfiltrated `~/.aws/credentials` in 24 of 25 runs, and only the egress and file system controls held ([Anthropic](https://www.anthropic.com/research/how-we-contain-claude)).
- **Malware does not ask.** The Nx payload started each AI CLI with its own approval flags off ([Snyk](https://snyk.io/blog/weaponizing-ai-coding-agents-for-malware-in-the-nx-malicious-package/)). A prompt only guards the paths that the attacker chooses to use.
- **One boundary removes most prompts.** Sandboxing cut Claude Code's permission prompts by 84% in Anthropic's own use ([Anthropic](https://www.anthropic.com/research/claude-code-sandboxing)).
- **Harm comes from combinations.** An agent with untrusted input, private data and an outbound channel at once can be turned against its user. Meta's Rule of Two says an agent should hold at most two of the three ([Meta](https://ai.meta.com/blog/practical-ai-agent-security/), [design patterns paper](https://arxiv.org/abs/2506.08837)). A prompt judges one action, not the combination.
- **Nobody is there.** The owner wants many sessions on many hosts that run while the laptop sleeps. An autonomous run at night has no one to answer a prompt, so the safe default must hold without one.

## 2. The references

### 2.1 Docker Sandboxes

Docker Sandboxes (the `sbx` CLI) is the closest product to the owner's goal. It is closed source. This summary comes from its public docs.

- **Wall.** Each sandbox is a microVM with its own kernel and its own private Docker engine. The microVM is the main trust boundary, and the agent has full control inside it. Sandboxes share no images or layers ([isolation](https://docs.docker.com/ai/sandboxes/security/isolation/), [security](https://docs.docker.com/ai/sandboxes/security/), [architecture](https://docs.docker.com/ai/sandboxes/architecture/)). A Hacker News comment says the VMM is Docker's own and not open source (**UNVERIFIED**, [thread](https://news.ycombinator.com/item?id=49245030)).
- **Files.** Direct mode shares the current folder at the same path over virtiofs, with caching. Clone mode mounts the repository read-only at `/run/sandbox/source`, including untracked and ignored files such as `.env`. The agent edits a private clone, and a git daemon in the sandbox serves it on a published port that changes at each restart. The host repository gets a remote named `sandbox-<name>`, and a fetch fails while the sandbox is stopped ([git workflows](https://docs.docker.com/ai/sandboxes/workflows/git/)). Since sbx 0.42.0 a workspace is optional, and `sbx cp` copies files ([credentials](https://docs.docker.com/ai/sandboxes/configuration/credentials/), fact check).
- **Network.** All outbound TCP goes through a proxy on the host that checks a policy. UDP is off unless an experimental feature is on, and ICMP is always blocked. DNS goes through a resolver that enforces the policy. The presets are Open, Balanced and Locked Down. Under Balanced and Locked Down, an unmatched request asks the user ([local governance](https://docs.docker.com/ai/governance/local/)). Rules can match method, path and query, and a connection that the proxy cannot inspect is blocked when such a rule applies ([network rules](https://docs.docker.com/ai/governance/reference/network-rules/)). `sbx policy log` lists what the proxy allowed and blocked ([policy log](https://docs.docker.com/reference/cli/sbx/policy/log/)).
- **Credentials.** A secret store on the host holds the values. The proxy replaces a placeholder with the real value in request headers only, never in bodies, and only for the hosts that the secret names. A dynamic source is cached for 55 minutes. For OAuth, the user signs in on the host and the proxy refreshes the token. A kit can set OAuth passthrough, which sends the real token into the sandbox. When no credential is stored, Claude Code logs in inside the sandbox, so its token lives in the VM ([credentials](https://docs.docker.com/ai/sandboxes/configuration/credentials/)). SSH agent forwarding is on by default ([git workflows](https://docs.docker.com/ai/sandboxes/workflows/git/)).
- **Lifecycle.** Sandboxes persist by default: installed packages, images, configuration and history survive a stop and a restart, and removal deletes them ([usage](https://docs.docker.com/ai/sandboxes/usage/)). A sandbox can be saved as a template. Kits declare tools, network rules, credentials and the agent in files ([customise](https://docs.docker.com/ai/sandboxes/customize/)). `sbx exec` starts a stopped sandbox first and works on cloud sandboxes with `--cloud` ([exec](https://docs.docker.com/reference/cli/sbx/exec/)).
- **Platforms.** Linux needs Ubuntu 24.04 or later, KVM, and the user in the kvm group. Inside a VM it needs nested virtualisation. macOS needs version 14 or later on Apple silicon. Windows needs Windows 11 with the Windows Hypervisor Platform ([install](https://docs.docker.com/ai/sandboxes/install/)).
- **A recent bug.** CVE-2026-103916 showed that a second file channel (`sbx cp`) bypassed the allowlist that guarded the first ([section 1.3](#13-supply-chain-incidents-aimed-at-developers)).

### 2.2 A hosted VM service

This hosted, closed product is summarised from its site and docs. This note does not name it or link to it.

- **Machines.** Full Ubuntu VMs, 4 vCPU and 8 GB by default, at $0.036 an hour. Each has SSH, Docker inside, a public IPv6 address and a virtual desktop at 60 fps. Some sandboxes have no IPv4 address of their own.
- **Commands.** `new`, `exec`, `ssh`, `scp`, `prompt` and `steer` (run Claude Code or Codex inside, from outside), `fork` (a disk-level fork from a snapshot), `stop`, `resume` and `list`.
- **Persistence.** The disk is saved every minute while running and at each stop. Memory is not saved. A stopped disk is kept for at least 14 days, and longer only as a saved snapshot. The IP address changes at each resume. A sandbox stops after 1 hour by default (fact check).
- **Environments** set the repositories, secrets and credentials that each new sandbox starts with.
- **Automation.** API keys have presets: read-only, full-sandbox, ci (create, resume, fork and delete only its own sandboxes) and admin. Scoped keys deny by default, and each sandbox gets a key limited to itself. There is an HTTP API, Python and TypeScript SDKs, `--json` output, webhooks and an OTLP export.

### 2.3 What the design takes, and where it differs

| Topic | Docker Sandboxes | Hosted VM service | This design |
| --- | --- | --- | --- |
| Where it runs | The laptop, or Docker's cloud | The vendor's cloud | Hosts the user owns, any number of them |
| Wall | A microVM | A full VM | A level per host (W1 to W4), pinned on the laptop. W3 is a microVM where KVM exists |
| Control | A closed proxy and VMM | The vendor's control plane | A small supervisor per session, on the user's host |
| Files | Direct, clone or none | The VM's disk, plus `scp` | A clone seeded by bundle. Work leaves by a fetch into quarantine refs |
| Network | Host proxy, presets, approvals | Open, with public IPv6 | No network card. A proxy per session, presets, approvals |
| Credentials | Header placeholders. SSH agent forwarded by default | Injected by environments | Sentinels, an identity pin and a tripwire. No agent forwarding |
| Persistence | Persistent, or removed | Disk saved every minute | Disk only, cold boot, reflink checkpoints |
| Fork | Templates | Disk fork from a snapshot | Reflink disk copy plus the agent's own fork command |
| Terminal | The agent's UI inside the sandbox | A shell, a desktop, or `prompt` | The agent's real bytes in the user's terminal |

## 3. Method

### 3.1 The layers

Each layer of the tree is one decision:

- **L1 Wall.** What isolates one session from the host and from other sessions?
- **L2 Control.** Where do the trusted parts (holder, PTY, output ring, shadow terminal, status, alerts) run, and how does the client reach a session inside the wall?
- **L3 Lifecycle.** How is a sandbox made, started on demand, kept, forked, stopped and spread across hosts?
- **L4 Doors.** How do files, network, credentials, ports and devices cross the wall, closed by default and opened only by an explicit grant?
- **L5 Supply chain and autonomy.** What stops a poisoned package, a prompt injection or an autonomous agent from doing harm through the open doors? How does the project secure its own supply chain (helper, guest kernel, images, VMM)?

### 3.2 The rubric

Two judges scored each option from 0 to 10. The security judge scored security. The builder judge scored the other four.

| Criterion | Weight | Judge | What a high score means |
| --- | --- | --- | --- |
| Security | 0.35 | Security | Few and hard attack paths for T1 to T5. The judge's notes place a design like Docker Sandboxes at about 7 to 8 |
| Fit | 0.15 | Builder | Fits the project's rules, decisions and existing code |
| Convenience | 0.20 | Builder | Little setup, fast starts, little change to how users work |
| Scale | 0.15 | Builder | Many sessions per host, many hosts, small hosts |
| Effort | 0.15 | Builder | Small to build and to keep up. 10 is the least work |

Value = (0.35 × security + 0.15 × fit + 0.20 × convenience + 0.15 × scale + 0.15 × effort) / 10.

For example, node 26 scored 8.5, 6, 6, 8 and 4: (2.975 + 0.9 + 1.2 + 1.2 + 0.6) / 10 = 0.6875, shown as 0.687.

### 3.3 The search

- **A node** is one option at one layer. Its parent is the option it builds on.
- **A rollout** is the two judges' scores for one option. Each judge also wrote down which later choices it assumed. That score is the node's own value, v.
- **Selection** uses UCT: at each node, go to the child with the highest Q + c × √(ln N(parent) / N(child)), with c = 0.1.
- **Backup** is max, not mean: Q is the best value anywhere in a node's subtree, the node included. Only one path will be built, so the best leaf matters, not the average one.
- **Expansion** sends five researchers in parallel, one per option for the chosen leaf. The two judges then score all five.
- **Pure exploitation** (c = 0) starts when the iterations left are just enough to reach L5. In this search, that was iterations 6 and 7.
- **Minimum breadth.** At L1, the search expands the top two nodes before it goes deeper. The search stopped after iteration 1, we added the rule, and the search resumed with iteration 1's results cached. The reason: the best L1 node by value, the hardened process (0.597), has a security score of 4.5. It shares the host kernel, so it is the weakest against the goal. The search needed one more expansion to compare it with the runner-up, the wall drivers (0.575).
- **Review.** A red team then attacked the best path and found 23 gaps ([section 11](#11-the-red-teams-gaps)). A fact check re-read the claims that the design leans on and made 15 corrections ([section 12](#12-the-fact-checks-corrections)).
- **Size.** 7 iterations, 35 options and 35 rollouts. Each expansion used 5 researchers and 2 judges: 35 researchers and 14 judges in all. With the red team, the fact check and two writers, the main search used 53 agents.

The log of the search, as it ran. The UCT sums are computed from the counts and values in [the tree](#34-the-tree).

| Iteration | c | Leaf expanded | Why | New children, with their values |
| --- | --- | --- | --- | --- |
| 1 | 0.1 | The root, into L1 | Start | Hardened process 0.597, gVisor 0.535, microVM 0.553, VM per session 0.482, wall drivers 0.575 |
| 2 | 0.1 | Node 1, hardened process, into L2 | Minimum breadth | Whole helper inside 0.490, trusted holder outside with a tiny init inside 0.612, OpenSSH door 0.515, holder in the wall's outer ring 0.467, child host 0.584 |
| 3 | 0.1 | Node 5, wall drivers, into L2 | Minimum breadth | Whole helper inside 0.658, holder outside with an untrusted vsock guest agent 0.670, OpenSSH over vsock 0.523, holder inside with a thin trusted supervisor 0.678, one provider machine per session 0.423 |
| 4 | 0.1 | Node 14, holder inside and supervisor, into L3 | UCT at the root: node 5 at 0.678 + 0.067 = 0.745, against node 3 at 0.718 and node 1 at 0.679. Then node 14 beats node 12 on Q | Cold boot from a verified image 0.645, snapshot first 0.568, warm pools 0.582, declared environments 0.563, fleet of owned hosts 0.630 |
| 5 | 0.1 | Node 12, holder outside and guest agent, into L3 | UCT at the root: node 5 at 0.730, against node 3 at 0.726. Then node 12 at 0.670 + 0.155 = 0.825, against node 14 at 0.741 | Cold boot from a pinned image 0.667, snapshot first 0.540, one-shot warm pool 0.584, environments 0.576, laptop-routed fleet 0.671 |
| 6 | 0 | Node 16, cold boot from a verified image, into L4 | Pure exploitation: two iterations left, two layers left. Node 14 (0.678) beats node 12 (0.671) | NIC-less wall with an egress proxy and sentinel keys 0.687, allowlist with short tokens 0.518, grant broker 0.630, git-only workspace 0.607, laptop key broker 0.435 |
| 7 | 0 | Node 26, NIC-less wall, into L5 | Pure exploitation | Registry firewall 0.600, flight recorder 0.602, throwaway sessions with a reviewed bundle 0.617, laptop-held digests 0.675, agent rows as policy 0.526 |

All five L5 options scored below node 26. So the path's value fell at the last layer, from 0.687 to 0.675. Every L5 option adds work, and none adds much coverage on top of L4 alone, so the builder judge marked them down.

### 3.4 The tree

S, F, C, Sc and E are security, fit, convenience, scale and effort. v is the node's own value, Q the best value in its subtree and N its rollouts. The best path is in bold.

| Id | Parent | Layer | Option | S | F | C | Sc | E | v | Q | N |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | root | L1 | Hardened process: a namespace sandbox per session | 4.5 | 8 | 7 | 6 | 6 | 0.597 | 0.612 | 6 |
| 2 | root | L1 | gVisor per session, holder outside the wall | 6 | 6 | 5 | 5 | 4 | 0.535 | 0.535 | 1 |
| 3 | root | L1 | KVM microVM per session (Cloud Hypervisor, vsock only) | 7.5 | 4 | 4 | 7 | 3 | 0.553 | 0.553 | 1 |
| 4 | root | L1 | One full VM per session, local or provider | 5.5 | 5 | 4 | 4 | 5 | 0.482 | 0.482 | 1 |
| **5** | **root** | **L1** | **Wall drivers with levels and a pinned floor per host** | **7** | **5** | **6** | **7** | **2** | **0.575** | **0.687** | **26** |
| 6 | 1 | L2 | Sandbox as a small host: whole helper inside | 5 | 4 | 6 | 6 | 3 | 0.49 | 0.49 | 1 |
| 7 | 1 | L2 | Trusted holder outside, tiny init inside owns the PTY | 5.9 | 8 | 6 | 5 | 6 | 0.612 | 0.612 | 1 |
| 8 | 1 | L2 | OpenSSH door to a session, SSH ended outside the wall | 4.7 | 5 | 7 | 5 | 4 | 0.515 | 0.515 | 1 |
| 9 | 1 | L2 | Holder in the wall's outer ring, supervisor outside | 6.2 | 3 | 5 | 4 | 3 | 0.467 | 0.467 | 1 |
| 10 | 1 | L2 | Sandbox as a child host: a cell provider on a parent | 5.4 | 6 | 7 | 7 | 4 | 0.584 | 0.584 | 1 |
| 11 | 5 | L2 | Box is a small host: whole helper inside the wall | 7.5 | 5 | 7 | 8 | 4 | 0.658 | 0.658 | 1 |
| 12 | 5 | L2 | Holder outside, untrusted vsock guest agent inside | 7 | 8 | 7 | 6 | 5 | 0.67 | 0.671 | 6 |
| 13 | 5 | L2 | Holder outside, OpenSSH over vsock or a socketpair | 6.5 | 5 | 5 | 4 | 4 | 0.523 | 0.523 | 1 |
| **14** | **5** | **L2** | **Holder inside the wall, thin trusted supervisor outside** | **8.5** | **6** | **7** | **7** | **3** | **0.678** | **0.687** | **16** |
| 15 | 5 | L2 | Session hosts: one provider machine per session | 5.5 | 4 | 4 | 4 | 2 | 0.423 | 0.423 | 1 |
| **16** | **14** | **L3** | **Cold boot from a verified image, state on disk only** | **7.5** | **6.5** | **6** | **6** | **5** | **0.645** | **0.687** | **11** |
| 17 | 14 | L3 | Snapshot first: restore, stop and fork | 6.5 | 3.5 | 6.5 | 8.5 | 2 | 0.568 | 0.568 | 1 |
| 18 | 14 | L3 | Warm and primed pools, throwaway sandboxes, git-only exit | 8.5 | 4.5 | 4.5 | 4.5 | 4 | 0.582 | 0.582 | 1 |
| 19 | 14 | L3 | Declared environments as signed, layered, hashed images | 8 | 4 | 5.5 | 5 | 2.5 | 0.563 | 0.563 | 1 |
| 20 | 14 | L3 | Fleet of owned hosts: client placement, SSH API | 7 | 6 | 5 | 7.5 | 5.5 | 0.63 | 0.63 | 1 |
| 21 | 12 | L3 | Cold boot from a pinned image, disk-only persistence | 7.9 | 7 | 6 | 6 | 5 | 0.667 | 0.667 | 1 |
| 22 | 12 | L3 | Snapshot first: warm templates, sealed suspend, fork | 7 | 4 | 5 | 7 | 2 | 0.54 | 0.54 | 1 |
| 23 | 12 | L3 | One-shot warm pool, throwaway sandboxes, git-only exit | 8.4 | 5 | 4 | 5 | 4 | 0.584 | 0.584 | 1 |
| 24 | 12 | L3 | Per-repo and personal environments, built and cached | 7.3 | 4 | 7 | 5 | 3 | 0.576 | 0.576 | 1 |
| 25 | 12 | L3 | Laptop-routed fleet: nodes, cells, pools, host admission | 7.6 | 6 | 6 | 8 | 5 | 0.671 | 0.671 | 1 |
| **26** | **16** | **L4** | **NIC-less wall, supervisor egress proxy, sentinel keys** | **8.5** | **6** | **6** | **8** | **4** | **0.687** | **0.687** | **6** |
| 27 | 16 | L4 | No-MITM allowlist with short-lived tokens minted on the laptop | 6.5 | 5 | 4 | 6 | 3 | 0.518 | 0.518 | 1 |
| 28 | 16 | L4 | Grant broker: typed doors that exist only while granted | 8 | 5 | 7 | 7 | 2 | 0.63 | 0.63 | 1 |
| 29 | 16 | L4 | Git-only workspace: bundles in, quarantined fetch out | 5.5 | 7 | 5 | 8 | 6 | 0.607 | 0.607 | 1 |
| 30 | 16 | L4 | Laptop key broker: signing per request over the bridge | 6 | 4 | 3 | 3 | 4 | 0.435 | 0.435 | 1 |
| 31 | 26 | L5 | Registry firewall inside the per-session egress | 8.7 | 4 | 5 | 6 | 3 | 0.6 | 0.6 | 1 |
| 32 | 26 | L5 | Flight recorder with trust labels, tripwires, kill switch | 8.2 | 5 | 6 | 6 | 2 | 0.602 | 0.602 | 1 |
| 33 | 26 | L5 | Throwaway sessions, one exit: a reviewed git bundle | 8.5 | 5 | 4 | 8 | 3 | 0.617 | 0.617 | 1 |
| **34** | **26** | **L5** | **Laptop-held digests: a signed, reproducible boot chain** | **8** | **6** | **7** | **7** | **4** | **0.675** | **0.675** | **1** |
| 35 | 26 | L5 | Agent rows as policy: model-API guard, caps, split roles | 8.6 | 4 | 3 | 5 | 2 | 0.526 | 0.526 | 1 |

### 3.5 The best path

```text
root
  5   L1  Wall drivers with levels and a pinned floor       v 0.575  Q 0.687
  14  L2  Holder inside the wall, thin supervisor outside   v 0.678  Q 0.687
  16  L3  Cold boot from a verified image, disk only        v 0.645  Q 0.687
  26  L4  NIC-less wall, egress proxy, sentinel keys        v 0.687  Q 0.687
  34  L5  Laptop-held digests, signed boot chain            v 0.675  Q 0.675
```

The second-best region sits under node 12 (holder outside, guest agent inside), at 0.671. It is less than the judges' noise below the best path, so its ideas matter. The design grafts several of them ([section 9](#9-the-design-after-review)).

### 3.6 Weight sensitivity

The weights in [the rubric](#32-the-rubric) are a choice. To test it, we kept each node's five scores and computed its value again under six weight sets. We then found the best path again by max backup over the same searched tree. The base weights gave the tree its shape. So this tests the choice among the options that were searched, not options that were never expanded. The table covers both searches, and [Your own hosts](#your-own-hosts) describes the hosts search.

| Weights (S, F, C, Sc, E) | Main best path | Best value | Hosts best path, and its value |
| --- | --- | --- | --- |
| Base (0.35, 0.15, 0.20, 0.15, 0.15) | Wall drivers → holder inside, supervisor outside → cold boot → NIC-less wall with an egress process → laptop-held digests | 0.687 | SSH add → client placement with host admission → tiers as a grant ceiling, 0.732 |
| Security first (0.50, 0.10, 0.15, 0.10, 0.15) | Same as base | 0.715 | Same, 0.725 |
| Convenience first (0.25, 0.15, 0.30, 0.15, 0.15) | Wall drivers → holder outside, guest agent inside → laptop-routed fleet | 0.670 | Same, 0.738 |
| Equal (0.20 each) | Wall drivers → holder outside, guest agent inside → laptop-routed fleet | 0.660 | Same, 0.740 |
| Builder first (0.25, 0.20, 0.20, 0.15, 0.20) | Wall drivers → holder outside, guest agent inside → laptop-routed fleet | 0.665 | Same, 0.742 |
| No effort (0.40, 0.20, 0.25, 0.15, 0) | Same as base | 0.740 | Same, 0.735 |

What the table shows:

- **L1: wall drivers win under all six weight sets, but only through what lies below them.** By its own value, the hardened process leads under the base, convenience-first, equal and builder-first weights (0.597, 0.623, 0.630 and 0.623). Under security-first weights, the microVM and the wall drivers tie at 0.590, and the hardened process falls to 0.560. Under no-effort weights, the wall drivers lead at 0.635.
- **L2 is the close call.** At base weights, the holder inside with a supervisor scores 0.6775, shown as 0.678 in the tree. The holder outside with a guest agent scores 0.670, only 0.0075 less. With more weight on convenience, with equal weights or with builder-first weights, the holder outside wins.
- **Why the design keeps the holder inside.** The reason is outside the rubric: no memory-unsafe parser of hostile bytes may run on the trusted side. The 2026 libghostty-vt advisories show the risk ([section 5](#5-l2-control)). The search expanded the holder-outside subtree only to L3, so its deeper value is unknown.
- **Below L2, the choices hold under every weight set.** Cold boot wins at L3 (tied with the fleet option at 0.620 under equal weights). The NIC-less wall with an egress process wins at L4, and laptop-held digests win at L5.
- **Hosts: the path holds under every weight set.** Under security-first weights, the machine-side join has the highest own value at the Attach layer: 0.610, against 0.560 for SSH add. It makes a dedicated account on the host. The search never expanded it. Its strongest idea, separate accounts for sandboxes, is [question 27](../contributing/isolation-spec.md#15-open-questions).

## 4. L1: the wall

| Node | Option | S | F | C | Sc | E | v | Q |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | Hardened process | 4.5 | 8 | 7 | 6 | 6 | 0.597 | 0.612 |
| 2 | gVisor per session | 6 | 6 | 5 | 5 | 4 | 0.535 | 0.535 |
| 3 | KVM microVM per session | 7.5 | 4 | 4 | 7 | 3 | 0.553 | 0.553 |
| 4 | One full VM per session | 5.5 | 5 | 4 | 4 | 5 | 0.482 | 0.482 |
| 5 | Levels with a pinned floor | 7 | 5 | 6 | 7 | 2 | 0.575 | 0.687 |

### 4.1 W1: a hardened process (node 1)

**The option.** The holder stays outside as today. It starts a small init (the helper, run again as `sandbox-init`) in new user, mount, PID, network, IPC, UTS and cgroup namespaces. The init mounts only the granted paths, then applies Landlock and seccomp, drops every capability and sets `no_new_privs`. The session sees its worktree, a private home and read-only tools. Its network namespace is empty, and egress goes through a proxy in the holder behind a Unix socket.

**Evidence.**

- systemd user units cannot use `User=` or `DynamicUser=`, and file system sandboxing in user units needs `PrivateUsers=` ([systemd.exec](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html)). `IPAddressDeny=` is ignored in user services ([systemd#38588](https://github.com/systemd/systemd/issues/38588)). So the holder must build the sandbox itself, as bubblewrap, Claude Code and Codex do ([Claude Code sandboxing](https://code.claude.com/docs/en/sandboxing), [Codex PR 13996](https://github.com/openai/codex/pull/13996)).
- Landlock grows by kernel. ABI 4 (Linux 6.7) adds TCP bind and connect rules. ABI 6 (6.12) scopes abstract Unix sockets and signals. ABI 9 (7.1) covers connects to path Unix sockets ([landlock(7)](https://man7.org/linux/man-pages/man7/landlock.7.html)). Before ABI 9, a Landlocked process can still reach the systemd user bus. A Landlocked thread cannot mount, so mounts come first.
- Ubuntu 23.10 and later restrict unprivileged user namespaces through `AppArmor` ([Ubuntu](https://ubuntu.com/blog/ubuntu-23-10-restricted-unprivileged-user-namespaces)). Qualys published three bypasses in January 2025 ([Qualys](https://www.qualys.com/2025/three-bypasses-of-Ubuntu-unprivileged-user-namespace-restrictions.txt)). A profile that matches a helper path the user can write is itself a bypass.
- The pure-Go libraries exist: go-landlock (MIT) and go-seccomp-bpf (Apache-2.0, no cgo) ([go-seccomp-bpf](https://pkg.go.dev/github.com/elastic/go-seccomp-bpf)).
- Hosts from the docker provider block nested namespaces with the default seccomp profile ([Docker seccomp](https://docs.docker.com/engine/security/seccomp/)).

**Why it lost.** One shared kernel puts security at 4.5. The CVE-2024-1086 class breaks every session at once, and an escape lands as the user's own uid. Its best child was node 7 at 0.612, so its subtree could not pass 0.612.

**Kept.** A hook-only socket per session with an enforced `Hello.Role`. The egress process outside the wall, with no resolver inside. Port forwarding with `ssh -L` to a Unix socket. A mount checklist: hide `$XDG_RUNTIME_DIR`, make `.git/hooks` and `.git/config` read-only, private home, no writable cgroupfs. W1 as one level, and as an inner layer inside a microVM.

### 4.2 W2: gVisor (node 2)

**The option.** One `runsc` sandbox per session, started by the holder with no daemon. The agent talks to gVisor's user-space kernel, the Sentry, not to the host kernel.

**Evidence.**

- `runsc --rootless` refuses `create`, save and restore, and netstack ([rootless](https://gvisor.dev/docs/user_guide/rootless/)). So the spec runs `runsc` as uid 0 in a user and network namespace that the helper or the namespace launcher makes ([spec 21.4](../contributing/isolation-spec.md#214-the-gvisor-wall-p61)). It runs with `--network=none` and `--directfs=false` ([gvisor#16342](https://github.com/google/gvisor/issues/16342)). The OCI user-namespace mode needs `newuidmap`, and a non-root `runsc` makes its own user namespace there ([facts](build-facts.md#4-linux-walls)). A gVisor maintainer says rootless mode skips defences that only root can set up ([gvisor#10359](https://github.com/google/gvisor/issues/10359)).
- The systrap platform is the default since mid-2023 and needs no virtualisation. gVisor calls it the better choice inside a VM ([platforms](https://gvisor.dev/docs/architecture_guide/platforms/)). gVisor runs on x86_64 and ARM64 with Linux 5.6 or later ([install](https://gvisor.dev/docs/user_guide/install/)).
- gVisor treats itself as two layers: a Sentry bug alone is not an escape ([security policy](https://github.com/google/gvisor/security)). Its record page lists the high-impact Linux CVEs it tracks as 96% defended ([track record](https://gvisor.dev/security-track-record/)).
- An empty container uses 23.7 MB under runsc and 4.1 MB under runc in gVisor's own density data, with no date ([density.csv](https://raw.githubusercontent.com/google/gvisor/master/website/performance/density.csv)). A third party measured about 30 µs per syscall on 1 vCPU (**UNVERIFIED**, [tacy](https://github.com/tacyhq/tacy/blob/main/docs/22-sandbox-runner.md)).
- inotify does not see edits made outside the sandbox ([gvisor#8089](https://github.com/google/gvisor/issues/8089)). Docker runs inside with extra flags ([Docker in gVisor](https://gvisor.dev/docs/tutorials/docker-in-gvisor/)).

**Why it lost.** Security 6 (a Sentry bug plus a host kernel bug reaches the host user), convenience 5 (slow syscall-heavy work such as `npm install`, no inotify from outside), effort 4.

**Kept.** gVisor as the W2 level for hosts without KVM. Secrets never go into a runtime config file. Clone mode in place of shared worktrees. Host git always runs with `core.hooksPath=/dev/null` and `core.fsmonitor=false`.

### 4.3 W3: a KVM microVM (node 3)

**The option.** One microVM per session, with its own guest kernel, its own disk and no network card. A small guest init owns the PTY. Every door is a vsock stream that the host side brokers.

**Evidence.**

- Firecracker reaches guest init in 125 ms or less, with VMM threads of 5 MiB or less. The VMM starts in 6 to 60 ms, typically 12 ms ([SPECIFICATION.md](https://github.com/firecracker-microvm/firecracker/blob/main/SPECIFICATION.md)). The fact check adds: these figures hold for 1 vCPU and 128 MiB, a minimal kernel and the serial console off. A session guest with 1 to 2 GiB and a fuller kernel starts slower.
- Firecracker and Cloud Hypervisor use the same hybrid vsock. The host connects to a Unix socket and writes `CONNECT <port>`. A guest connection reaches a host socket at `<uds>_<port>`, and gets RST if nothing listens there ([Firecracker vsock](https://github.com/firecracker-microvm/firecracker/blob/main/docs/vsock.md), [Cloud Hypervisor vsock](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/vsock.md)). No network port is involved.
- Firecracker has no virtio-fs, and maintainers said in 2023 that it is not on the roadmap ([firecracker#1180](https://github.com/firecracker-microvm/firecracker/issues/1180)). It tests host kernels 5.10, 6.1 and 6.18 only ([kernel policy](https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md)). Its jailer runs as root ([jailer](https://raw.githubusercontent.com/firecracker-microvm/firecracker/main/docs/jailer.md)). Its CI guest kernel has no netfilter, so dockerd fails there ([firecracker#4791](https://github.com/firecracker-microvm/firecracker/issues/4791)).
- Cloud Hypervisor runs on KVM or MSHV, on x86-64 and AArch64, and needs host kernel 6.8 or later. v54.0 came out on 14 October 2026 ([README](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/README.md)). Its licence is Apache-2.0 and BSD-3-Clause together ([CONTRIBUTING](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/CONTRIBUTING.md)). virtio-fs needs the virtiofsd daemon and shared guest memory ([fs.md](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/fs.md)).
- libkrun calls itself a partly isolated environment. Its VMM serves devices with its own host permissions, and its TSI networking uses the VMM's own host network ([security model](https://github.com/libkrun/libkrun/blob/main/docs/security-model.md)).
- Docker Sandboxes on Linux needs the same: KVM and the kvm group ([install](https://docs.docker.com/ai/sandboxes/install/)).

**Why it lost.** It had the best security of the five (7.5), but fit 4 and convenience 4: no `/dev/kvm` on M1 and M2 Macs, Hetzner Cloud or most VPS. Effort 3.

**Kept.** No network card, with egress over vsock to a broker outside. The VMM confined with Landlock and a seccomp filter that refuses `AF_INET` and `AF_INET6`. The PTY carried as a stream that resumes by offset ([0004](../contributing/decisions/0004-replay-by-offset.md)). Guest RAM equal to the session cap, so the guest's OOM killer acts first. Spec 20.10 replaced this: guest RAM is the session's size, and the cap adds a reserve. dockerd started only on first use.

### 4.4 W4: one full VM per session (node 4)

**The option.** Each session is its own machine: a Lima VM or an Apple `container` VM on the Mac, or an E2B sandbox. The unchanged helper runs inside.

**Evidence.**

- Apple `container` runs each container in a lightweight VM with sub-second starts, needs Apple silicon and macOS 26, and does not give freed guest memory back to macOS ([container](https://github.com/apple/container), [technical overview](https://github.com/apple/container/blob/main/docs/technical-overview.md)). `container machine` mounts the macOS home folder read-write and forwards the SSH agent ([container machine](https://github.com/apple/container/blob/main/docs/container-machine.md)).
- Lima's user-mode network lets a guest reach the host's `127.0.0.1` as `host.lima.internal`. `networkOpts.hostLoopback: false` blocks this on vz from Lima 2.3 ([Lima user network](https://lima-vm.io/docs/config/network/user/)).
- E2B pause takes about 4 s per GiB of RAM and keeps memory, and resume takes about 1 s ([persistence](https://docs.e2b.dev/sandbox/persistence)).
- A local VM pauses when the laptop sleeps, which breaks "the session outlives the client".

**Why it lost.** Security 5.5 (no trusted side, default NAT egress), convenience 4 and scale 4 (a 16 GB Mac holds about 2 or 3 sessions, an estimate).

**Kept.** Set `hostLoopback: false` on Lima hosts now. Treat every bridge reply as hostile in the client. Use provider-native egress controls first on E2B. `doctor` prints each host's isolation level, and a downgrade is never silent.

### 4.5 The winner: levels with a pinned floor (node 5)

**The design.**

1. **Levels.** W0 is no wall, as today, and only an explicit opt-out gives it. W1 is a hardened process, W2 is gVisor with systrap and `--network=none`, W3 is a microVM on `/dev/kvm`, and W4 is one provider machine per session. A level comes from the driver, its mode and the host's features together. Rootless runsc ranks below rootful runsc, and W1 without Landlock ABI 4 cannot claim TCP confinement.
2. **One interface** in a new `internal/wall` package. A `Driver` has `Probe`, `Prepare` and `Load`. A `Box` has `Start`, `Exec`, `Dial`, `Ports`, `Stats`, `Stop` and `Destroy`, with `Pause`, `Resume` and `Snapshot` optional. A `Spec` carries the image, the granted mounts, the network policy, the memory limit and the CPUs.
3. **Probes** in `doctor`: `/dev/kvm` opened read-write with `KVM_GET_API_VERSION`, `systemd-detect-virt`, a `runsc do true` run, user namespaces and the `AppArmor` sysctl, the Landlock ABI, and seccomp. The results are cached for each boot.
4. **The floor.** `host add` and `host create` store the strongest working level as `wall.floor` in `hosts.toml` on the laptop. `new` refuses a lower level with a "Try:" hint. A downgrade needs `--wall=<level> --downgrade` or a typed confirmation. The manifest records it, the session row shows a badge, and an alert reports it. Repository files and agent hints may raise the level, never lower it.
5. **Limits.** The VMM or the Sentry runs inside the session's systemd unit, so `MemoryMax`, kill and OOM accounting cover the whole session ([launch.go](../../internal/host/launch.go)).

**Why it won.** On its own it scored 0.575: effort 2, because it is the sum of the other four. But it is the only wall that reaches W3 on KVM hosts and still gives every other host a level. Its children therefore inherit a high security ceiling, and node 14 reached 8.5. The security judge gave it 7, not 7.5, for four reasons. A host's real level is the weakest driver it runs. libkrun as a default W3 is weaker than its badge. Kata brings root daemons. The downgrade flag gives an injected agent a social path to a weaker wall. The builder judge saw its real value as a frame that lets the project ship one level first and add others later.

**Changed after review.**

- One W3 VMM per host, chosen by the probe ([section 10](#10-platforms), [question 3](../contributing/isolation-spec.md#15-open-questions)). On host kernels 6.8 and later, it is Cloud Hypervisor v52.0 or later. On the kernels that Firecracker tests, it is Firecracker 1.14.4, 1.15.1 or later with the default MMIO transport. The fact check later raised this to 1.17.0 ([9.2](#92-the-wall)). Both speak the same hybrid vsock, so the driver code is shared.
- libkrun is not a default. It may serve only with TSI and implicit vsock off, in an empty network namespace, behind a conformance test ([section 11](#11-the-red-teams-gaps)).
- No Kata in the first release: it adds a root containerd, a root shim and a root virtiofsd ([Kata rootless](https://github.com/kata-containers/kata-containers/blob/86dcc18971958caf58325bb196e349f7d5cc80cc/docs/how-to/how-to-run-rootless-vmm.md)).
- New sessions below W1 are refused by default. A downgrade is offered only for a new session, through a sheet that lists what the user loses.
- **Names.** The spec names the levels `none`, `process`, `gvisor` and `microvm` (W0 to W3). W4 is a placement, not a level: each session gets a provider machine of its own. The level of an E2B host is the probe's answer, as on every host. P7.4 gives one machine to each session ([spec 21.8](../contributing/isolation-spec.md#218-a-machine-for-each-session-p74), [question 22](../contributing/isolation-spec.md#15-open-questions)).

## 5. L2: control

| Node | Option | S | F | C | Sc | E | v |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 11 | Box is a small host: whole helper inside | 7.5 | 5 | 7 | 8 | 4 | 0.658 |
| 12 | Holder outside, untrusted vsock guest agent inside | 7 | 8 | 7 | 6 | 5 | 0.670 |
| 13 | Holder outside, OpenSSH over vsock or a socketpair | 6.5 | 5 | 5 | 4 | 4 | 0.523 |
| 14 | Holder inside, thin supervisor outside | 8.5 | 6 | 7 | 7 | 3 | 0.678 |
| 15 | One provider machine per session | 5.5 | 4 | 4 | 4 | 2 | 0.423 |

One fact shaped this layer. libghostty-vt, which the shadow terminal embeds, had memory-corruption advisories in October 2026 that terminal output alone can trigger ([GHSA-w9q4](https://github.com/ghostty-org/ghostty/security/advisories/GHSA-w9q4-5jvv-mcv4), [GHSA-qjfx](https://github.com/ghostty-org/ghostty/security/advisories/GHSA-qjfx-r533-6vx3)). A third, GHSA-jqr7, matters when an application takes terminal sizes from an untrusted peer. The holder clamps client sizes only to 65535 (`clamp16`) ([GHSA-jqr7](https://github.com/ghostty-org/ghostty/security/advisories/GHSA-jqr7-69mm-v35h), [holder_conn.go](../../internal/host/holder_conn.go)). GitHub rates GHSA-w9q4 Moderate, and its advisory says that libghostty-vt is affected in its default configuration. The pin already holds the fix of GHSA-qjfx, so P0.1 moves it past GHSA-w9q4 and GHSA-jqr7 ([build facts](build-facts.md#61-ghostty-and-its-advisories)). The layer therefore rests on a general rule, not on this one advisory: keep memory-unsafe parsers of hostile bytes inside the wall.

### 5.1 Box is a small host (node 11)

The whole helper runs inside each box: bridge, holder, ring, shadow terminal, hooks, uploads and restore. Outside, a box runner owns only the VMM, its limits, start and stop, a byte pipe and a one-way event sink. The client reaches the inner bridge through a byte mux in the outer bridge. Apple's `Containerization` works the same way: a static init, `vminitd`, answers gRPC over vsock in each VM ([`containerization`](https://github.com/apple/containerization)).

It scored well (0.658) for scale and convenience. It lost on fit: the pure form forbids an outside proxy, but T2 and T5 need one. Guest root can also forge status, alerts and the holder log, with no record outside.

**Kept.** Never mount or read a guest disk on the host. A byte mux in the outer bridge keeps many boxes under sshd's `MaxSessions` default of 10 ([sshd_config](https://man.openbsd.org/sshd_config)). Strip control characters from every string a host sends.

### 5.2 Holder outside, untrusted guest agent inside (node 12)

The holder, the ring, the shadow terminal, status and alerts stay on the host. A small guest agent is PID 1 in each microVM and owns only the PTY and the process tree. The holder dials three streams over hybrid vsock (control, a raw console that resumes by ring offset, and one per port). It never creates a `<uds>_<port>` listener, so the guest cannot open any channel to the host. Kata's agent and E2B's envd are precedents for a guest agent with the lifecycle owner outside ([Kata architecture](https://github.com/kata-containers/kata-containers/blob/main/docs/design/architecture/README.md)).

It was the closest rival (0.670, and 0.671 with node 25 below it). Fit was the best of the five (8), because it keeps L1's rule that the holder stays outside. It lost on security: the shadow terminal and the screen scanner parse hostile bytes on the trusted side, and the pinned commit was vulnerable.

**Kept.** The host dials and the guest never dials. The console resumes by ring offset. A role allowlist on the holder socket now. A pause and kill switch that acts from the trusted side.

### 5.3 Holder outside, OpenSSH over vsock (node 13)

The holder speaks SSH with `golang.org/x/crypto/ssh` to an `sshd -i` inside the wall. systemd's generator binds a per-connection sshd to vsock port 22 ([systemd-ssh-generator](https://www.freedesktop.org/software/systemd/man/latest/systemd-ssh-generator.html)). It lost (0.523) for three reasons. The agent's PTY lives in sshd's session and dies when the link drops. The holder parses a large protocol from a hostile peer, and a peer-triggered blocking bug was fixed in May 2026 ([GO-2026-5017](https://pkg.go.dev/vuln/GO-2026-5017)). Every guest needs a static OpenSSH that the project must patch fast.

**Kept.** Door policy lives on the trusted side, never in config files inside the wall.

### 5.4 One provider machine per session (node 15)

Each session is a child host in `hosts.toml`, cloned from a base machine. It lost (0.423). It reverses the repository's own sizing: about 5 busy sessions fit in an 8 GB host, against about 4 with a VM each ([architecture](../explanation/architecture.md)). A Lima child boots in tens of seconds (**UNVERIFIED**). Clones copy agent logins and alert tokens into every child.

The spec keeps a narrower form for provider machines only. The supervisor and the egress process run on the machine, and no login is copied ([spec 21.8](../contributing/isolation-spec.md#218-a-machine-for-each-session-p74), [question 86](../contributing/isolation-spec.md#15-open-questions)).

### 5.5 Options under the hardened-process wall (nodes 6 to 10)

Under node 1 the best control option was node 7 (0.612): the trusted holder outside, with a tiny init inside that owns the PTY and hands file descriptors out over a socketpair. Node 10 (0.584) made each sandbox a child host of a "cell" provider, with one bridge per parent. Node 8 (0.515) added an OpenSSH door that ends SSH on the trusted side, which Docker Sandboxes also ships as `ssh <name>.sbx`. Node 9 (0.467) put the holder in an outer ring of the wall. Node 6 (0.49) put the whole helper inside. All of them inherit W1's security of 4.5 or so, which capped them.

### 5.6 The winner: holder inside, thin supervisor outside (node 14)

**The design.**

1. **Split.** Inside the wall, a static guest init (the helper) starts the holder. The holder keeps the PTY, the 8 MiB ring, the shadow terminal, the scanner, the screen rules, the hook socket and the paste folder. These are today's code, moved. Where the agent does not need guest root, the holder runs as a guest uid apart from the agent's.
2. **Outside,** a per-session supervisor (`supervise <id>`) is the main process of the session's systemd unit. It owns the box, the limits, memory and OOM facts from the host cgroup, the egress and credential endpoint (L4), the trusted manifest, an append-only `audit.jsonl`, alert delivery and secrets in memory.
3. **Channel.** Each connection is one framed stream over `Box.Dial`: hybrid vsock on W3, a socket pair on W2 too, passed with `runsc --pass-fd` ([spec 21.4](../contributing/isolation-spec.md#214-the-gvisor-wall-p61)), an inherited socketpair on W1. No TCP port exists anywhere.
4. **Protocol.** Frame types in `internal/proto` are append-only. The supervisor answers `info`, `kill`, `ls` and `subscribe` from the trusted manifest. It passes attach, input, resize, output and snapshot frames unchanged, after a type allowlist and the existing size limits. New control frames carry status, alerts, ports and the session's end from the guest, and start, paste, identity and stop to it.
5. **Trust rules.** The supervisor never runs, interpolates or renders guest data. Every guest field has a type, a size cap and a filter for control characters. The launcher marks the agent's state as "claimed by the session". Only the supervisor decides end, OOM and kill, from the VMM's exit and the host cgroup's `memory.events`.

**Why it won.** Security 8.5, the best of the layer: the trusted side parses no terminal bytes, and policy lives where the guest cannot reach it. The client path barely changes: the bridge still dials the session socket, which the supervisor now serves. The cost is fit 6 (it amends L1's placement of the holder) and effort 3 (a new package, a manifest split, moved alert and OOM code, two versioned peers).

**Changed after review.**

- **No memory snapshot on this path.** The research for node 14 counted on a W3 snapshot to keep the PTY, ring and screen across a stop. The chosen L3 cold-boots and saves no memory. So a stop loses the running turn and the live screen. The replay after a wake shows the saved ring tail first, then the new boot's output.
- **No host listener for the guest.** In the first draft, the guest dialled the supervisor's control port and its egress port. These were Unix sockets at `<uds>_<port>`: not network ports, but host listeners that a guest could reach. Node 12's rule (the host dials, the guest never dials) removes them, with the egress flows carried on a link that the supervisor opens. The spec takes this rule ([question 23](../contributing/isolation-spec.md#15-open-questions), decision [0030](../contributing/decisions/0030-local-sockets-do-not-listen.md)).
- **Guest root voids the uid split.** If the agent needs guest root (apt, sudo, Docker inside), the in-guest holder is fully hostile. The trusted manifest, OOM facts and audit log stay correct, but status, replay and the port list are only claims.
- **Validate claims before they act.** A guest-claimed agent session ID must match a per-agent pattern (a UUID for Claude Code) before it enters an argv. An argv with a danger flag that the user did not type is refused ([section 11](#11-the-red-teams-gaps)).
- **Bump the Ghostty pin past `e257bb45` and `0af5c9e55` now,** and cap client sizes at about 4096 columns and rows. This is needed on every path. Spec 19.1 sets 4096 columns and 2048 rows.

## 6. L3: lifecycle

| Node | Option | S | F | C | Sc | E | v |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 16 | Cold boot from a verified image, state on disk only | 7.5 | 6.5 | 6 | 6 | 5 | 0.645 |
| 17 | Snapshot first: restore, stop and fork | 6.5 | 3.5 | 6.5 | 8.5 | 2 | 0.568 |
| 18 | Warm and primed pools, throwaway, git-only exit | 8.5 | 4.5 | 4.5 | 4.5 | 4 | 0.582 |
| 19 | Declared environments as signed, layered images | 8 | 4 | 5.5 | 5 | 2.5 | 0.563 |
| 20 | Fleet of owned hosts, client placement, SSH API | 7 | 6 | 5 | 7.5 | 5.5 | 0.630 |

### 6.1 Snapshot first (node 17)

Templates are memory and disk snapshots. A new session restores one, and a stop writes the session's memory to a file.

- Firecracker maps the memory file with `MAP_PRIVATE`, so pages load on demand and many VMs can share one file. A restore took 3 ms on one aarch64 setup ([snapshot support](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/snapshot-support.md), [production setup](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md)). The 2021 AWS paper reports restores from 4 ms, a best case ([arXiv 2102.12892](https://arxiv.org/abs/2102.12892)). REAP measured 232 ms for one function and 1,256 ms with 16 at once ([arXiv 2101.09355](https://arxiv.org/pdf/2101.09355)).
- A snapshot restores only on identical software and hardware. Intel to AMD never works ([versioning](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/versioning.md)). Diff snapshots are a developer preview.
- VMGenID reseeds only the kernel's random pool. IDs, cached random numbers and tokens in user space are copied, and Firecracker calls resuming one state twice insecure without de-duplication ([snapshot support](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/snapshot-support.md)).
- Shared template pages open a Flush+Reload side channel, the reason Firecracker tells users to turn KSM off ([production setup](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md)). cgroup v2 charges a shared page to whichever group touched it first ([cgroup v2](https://docs.kernel.org/admin-guide/cgroup-v2.html)).
- Lima's vz driver has no snapshots ([vz driver](https://github.com/lima-vm/lima/blob/master/pkg/driver/vz/vz_driver_darwin.go)). Cloud Hypervisor has a copy-on-write restore mode that shares one snapshot across VMs ([snapshot_restore.md](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/snapshot_restore.md)).

It lost on fit (3.5) and effort (2): a memory snapshot writes guest RAM, with the agent's tokens, to disk, so it bends "stores no secrets". Every unattended kernel upgrade also turns each snapshot into a cold fallback, so two lifecycles must ship.

**Kept.** A host fingerprint (kernel, CPU, VMM build) on every cached artefact, with a clean fallback on a mismatch. Fork that starts the agent again with its own fork command. A disk checkpoint at each finished turn, for a `rewind` command. Session-level idle stop that frees RAM.

### 6.2 Warm and primed pools (node 18)

A host keeps booted sandboxes ready, and a claimed one never goes back to the pool. A primed tier also holds a clone of a trusted repository with its setup run. Work leaves only through a per-session exit repository with `receive.fsckObjects`, `denyDeletes` and `denyNonFastForwards` ([git-config](https://git-scm.com/docs/git-config)).

The evidence cut the pitch. A microVM reaches init in about 125 ms and gVisor starts in about 0.1 to 0.3 s (**UNVERIFIED**, [gvisor#13182](https://github.com/google/gvisor/issues/13182)), so a bare pool saves little. On the 4 GiB test host only one warm member fits. It lost on convenience and scale (4.5 each).

**Kept.** A one-way state machine: a claimed sandbox is never recycled. A rescue commit through a temporary index at each harvest, so a stop never loses work. The host never checks out guest-written data, because of bugs such as CVE-2025-48384 ([NVD](https://nvd.nist.gov/vuln/detail/cve-2025-48384)).

### 6.3 Declared environments (node 19)

Sessions start from layers declared in files: a repository layer from `devcontainer.json`, a Dockerfile or a Nix flake, and a personal layer. A walled build box builds each layer. The host caches it by hash, and the laptop signs it with `ssh-keygen -Y` ([ssh-keygen](https://man.openbsd.org/ssh-keygen)).

- A devcontainer's `initializeCommand` runs on the host machine, and Features install as root ([devcontainer reference](https://containers.dev/implementors/json_reference/), [Features](https://containers.dev/implementors/features/)).
- Google archived kaniko on 3 June 2025 ([kaniko](https://github.com/GoogleContainerTools/kaniko)), and Coder's envbuilder is in maintenance mode ([envbuilder](https://github.com/coder/envbuilder)).
- Codex cloud caches a container after its setup script for up to 12 hours ([Codex environments](https://developers.openai.com/codex/cloud/environments)).

It lost on effort (2.5) and fit (4): a step engine, an OCI store, signing and three front ends. A signature also proves only that the user approved a digest, not that the build was honest.

**Kept.** Scan every new layer for the secret values that the supervisor holds. Never copy `~/.claude.json`, which holds the account, MCP servers and per-project trust ([Claude Code settings](https://code.claude.com/docs/en/settings)). Ship the helper as its own read-only layer, so an upgrade never forces a rebuild.

### 6.4 A fleet of owned hosts (node 20)

The client places each session on one of the user's hosts by labels, the pinned floor, readiness and free memory. (A later change: labels are metadata only, and a host is picked by name or by a rule. See [spec 18.2](../contributing/isolation-spec.md#182-picking-a-host-h19).) Each host admits or refuses under its own lock, so no central state is needed. Scripts get a JSON-RPC API on stdio, like MCP's stdio transport ([MCP transports](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports)). CI gets scoped, expiring SSH keys (`command=`, `expiry-time=`, `from=`) ([sshd](https://man.openbsd.org/sshd.8)), in the spirit of the hosted VM service's key presets.

Nomad packs by default, and kube-scheduler spreads by default ([Nomad](https://developer.hashicorp.com/nomad/api-docs/operator/scheduler), [kube-scheduler](https://kubernetes.io/docs/reference/scheduling/config/)). Packing lets idle stop actually stop hosts. It scored 0.630 but does not say how a sandbox is made on one host, so it lost here and joins later as the layer across hosts.

**Kept.** Host-side admission under a host-wide lock with a "full" error. Placement that filters on the floor pinned on the laptop, never on a host's live claim. `--json` on every command and a stdio API, with no listener.

### 6.5 Options under the guest-agent control (nodes 21 to 25)

Under node 12 the search found the same shapes again. Node 25, a fleet routed by the laptop with pools of hosts and one-machine cells, scored 0.671. Node 21, a cold boot from a pinned image, scored 0.667, slightly above node 16 because the holder stays outside there. Nodes 22 to 24 (snapshots with a suspend sealed to a laptop key, a one-shot warm pool, and cached per-repository environments) scored 0.540 to 0.584.

### 6.6 The winner: cold boot from a verified image (node 16)

**The design.**

1. **Images.** The helper builds a read-only root image once for each OCI digest. It pulls, flattens the layers with whiteouts, and writes EROFS with `mkfs.erofs --tar` or ext4 with the Go `tar2ext4` ([mkfs.erofs](https://man.archlinux.org/man/mkfs.erofs.1.en), [tar2ext4](https://pkg.go.dev/github.com/Microsoft/hcsshim/ext4/tar2ext4)). A dm-verity tree protects it, and the manifest pins the root hash. The kernel sets up the verity device from its command line with `dm-mod.create`, so no initramfs is needed ([dm-init](https://docs.kernel.org/admin-guide/device-mapper/dm-init.html)).
2. **One sparse disk per session** holds the repository at a stable path (`/work/<repo>`), the agent's home and the root overlay's upper layer, with a size cap (20 GiB by default). A stable path matters because agents key their transcripts by working folder.
3. **Start is a cold boot,** then the agent's resume command, the path that [restore.go](../../internal/host/restore.go) already runs after a host reboot. Secrets come in the start frame at each boot and never go on the disk. The unit's `MemoryMax` is the guest RAM plus a margin of about 64 MiB, so the guest's OOM killer picks the agent first. Spec 20.10 replaced this margin with a budget for each part.
4. **Stop** comes from the user, a per-session idle timer or a host shutdown. The guest ends the agent, writes the ring tail to the disk, syncs and powers off. A new state, `stopped`, keeps the disk and uses no RAM.
5. **After a host reboot,** only sessions that were working boot again, a few at a time. The rest wake when a client attaches.
6. **Fork and checkpoint** freeze the guest file system, copy the disk with `FICLONE` (or a sparse copy), and thaw it ([ioctl_ficlone](https://man7.org/linux/man-pages/man2/ioctl_ficlone.2.html)). The child starts with a new `Fork` row in the agent table, such as `claude --resume {id} --fork-session` ([Claude Code CLI](https://code.claude.com/docs/en/cli-reference)).
7. **Move.** A stopped session is a disk, a manifest and a digest, so it can move between hosts with no CPU or VMM lock-in.
8. **W2** uses the same EROFS image as its root through gVisor's annotations, and saves the root's upper layer with `runsc tar rootfs-upper` ([filesystem](https://github.com/google/gvisor/blob/975fb172241c7946a9f67b4f52e0349d9240589a/g3doc/user_guide/filesystem.md), [rootfs snapshot](https://gvisor.dev/docs/user_guide/rootfs_snapshot/)). An open regression aborts an EROFS root with an overlay in chroot mode ([gvisor#13529](https://github.com/google/gvisor/issues/13529)).

**Precedents.** Apple's `Containerization` builds ext4 from OCI images and boots one VM per container with a minimal init in under a second ([`containerization`](https://github.com/apple/containerization)). containerd has had an EROFS snapshotter since 2.1 ([containerd](https://containerd.io/docs/2.1/snapshotters/erofs/)). Docker Sandboxes keeps packages and configuration across stops ([usage](https://docs.docker.com/ai/sandboxes/usage/)).

**Why it won.** It is the most conservative lifecycle on the VM path. A fresh kernel, fresh entropy and a fresh machine ID at each boot remove the clone problems of snapshots. No memory image exists, so the tool writes no secret at rest. Start, wake and restore share one tested path. It scored the best fit (6.5) and the best effort (5) of the five.

**Changed after review.**

- **The guest root resets at every boot by default** ([question 8](../contributing/isolation-spec.md#15-open-questions)). Only `/work` and the agent's declared state folders persist. A dropper in `~/.bashrc` or the root overlay would otherwise run at every boot and copy into every fork.
- **Init comes from the project's own tools image** (L5), not from the user's image. So a helper upgrade never forces an image rebuild.
- **Image conversion runs in a box,** with no network and only the layer blobs and the output file. Layer digests are checked first, with caps on sizes and entry counts. A pure-Go converter inside the helper is preferred.
- **Templates** are promoted only after a diff review that marks rc files, hooks and `PATH` folders, and a scan for the session's secret values. Shared caches are read-only.
- **The host never mounts a guest-written disk.**
- **What the disk keeps** must be said in the security model: files that the agent writes, such as login tokens and `.env` copies, persist in plain text. Per-session disk encryption with a key that the secrets command fetches at each start is an option ([question 12](../contributing/isolation-spec.md#15-open-questions)).
- **On ext4 hosts,** the Debian and Ubuntu default, fork and checkpoint are full sparse copies. The man page names XFS and Btrfs for reflinks (that ext4 has none is inferred, **UNVERIFIED**).

## 7. L4: doors

| Node | Option | S | F | C | Sc | E | v |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 26 | NIC-less wall, supervisor egress proxy, sentinel keys | 8.5 | 6 | 6 | 8 | 4 | 0.687 |
| 27 | No-MITM allowlist, short-lived tokens from the laptop | 6.5 | 5 | 4 | 6 | 3 | 0.518 |
| 28 | Grant broker: typed doors that exist only while granted | 8 | 5 | 7 | 7 | 2 | 0.630 |
| 29 | Git-only workspace: bundles in, quarantined fetch out | 5.5 | 7 | 5 | 8 | 6 | 0.607 |
| 30 | Laptop key broker: signing per request | 6 | 4 | 3 | 3 | 4 | 0.435 |

### 7.1 No interception, short-lived tokens (node 27)

Each session's traffic leaves through an edge network namespace that the supervisor owns, with an nftables allowlist fed by a policy resolver, and an SNI check with no decryption. The laptop mints short-lived tokens: GitHub App installation tokens (1 hour, [GitHub](https://docs.github.com/rest/apps/apps?apiVersion=2026-03-10)), AWS STS, GCP, and Anthropic workload identity federation with the laptop as an issuer ([Anthropic WIF](https://platform.claude.com/docs/en/manage-claude/workload-identity-federation)).

It lost (0.518) on two facts. Without interception nothing can pin the tenant, so an attacker's own key still works on the allowed model API. Anthropic's tenant restriction header must be added by a proxy that inspects TLS ([tenant restrictions](https://support.claude.com/en/articles/13198485-enforce-network-level-access-control-with-tenant-restrictions)). And subscription logins cannot be short-lived: `claude setup-token` lasts a year ([authentication](https://code.claude.com/docs/en/authentication)), and OpenAI's federation rejects an arbitrary issuer ([OpenAI WIF](https://developers.openai.com/api/reference/workload-identity-federation/)).

**Kept.** A resolver that never forwards a denied name, and inserts allowed addresses before it answers. A deny set for host addresses, private ranges and the cloud metadata address in every mode. Refuse ECH and answer HTTPS and SVCB queries with no data. Revoke GitHub installation tokens at stop.

### 7.2 Grant broker (node 28)

Every door is a typed grant: a path, a domain, a secret for one host, an SSH key for one host, a port, a service or a device. The laptop resolves each grant, and the supervisor enforces it. Each live door is one Unix socket that exists only while granted. A repository can only propose grants, which the user approves once by hash.

- OpenSSH agent destination constraints bind a key to a destination through `session-bind@openssh.com`, but only for user authentication, not for commit signing ([agent restrictions](https://www.openssh.org/agent-restrict.html)).
- Cloud Hypervisor can hot-add a virtio-fs share ([hotplug](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/hotplug.md)). Firecracker cannot.
- Anthropic's sandbox-runtime asks a separate decider process about each request ([srt proxy](https://github.com/anthropics/sandbox-runtime/blob/main/docs/srt-proxy.md)).

It scored 0.630 but lost on effort (2): a policy grammar, five door protocols and per-wall file mechanics. Approval fatigue is a risk too.

**Kept.** Two secret modes, shown plainly: a bound secret (a sentinel swapped on the trusted side) and an exposed secret (the value in the environment). Repository grants as untrusted requests pinned by hash. Deny always wins. An `ssh` door built on `session-bind`. `grant freeze` closes every door at once.

### 7.3 Git-only workspace (node 29)

The repository enters as a git bundle, and work leaves only through a fetch into a quarantine mirror on the host. git's own manual says that running git in a `.git` folder from an untrusted source is unsafe, because config and hooks run ([git security](https://git-scm.com/docs/git#_security)). Hidden refs are not a boundary ([git-config](https://git-scm.com/docs/git-config)). Today `pull` runs `git status` in the session folder on the host ([pull.go](../../internal/client/pull.go)).

It covers the file door only, so its security was 5.5, and T2 over the network stays open. The design grafts it whole as the file door.

**Kept.** One guard on every host-side git call that refuses paths on a session disk or a granted mount. Seed by bundle, never by a live upload-pack over the host repository. A quarantine mirror with a reflog and no prune, so work that left the wall cannot be destroyed from inside. A git remote helper over the existing transport, so `pull` works on docker and E2B hosts too.

### 7.4 Laptop key broker (node 30)

Credentials never leave the laptop, and each credentialed request goes over the bridge to a broker there. It lost (0.435): credentialed work stops while the laptop sleeps, which breaks "the session outlives the client", and model traffic resends the whole conversation through the laptop at each turn ([Messages API](https://platform.claude.com/docs/en/build-with-claude/working-with-messages)).

**Kept.** Fetch a credential at first use, and answer 503 with a hint when its source is absent. Default deny rules for the routes that worms used: create a repository, create a gist, add a collaborator, delete a ref.

### 7.5 The winner: NIC-less wall, egress proxy, sentinel keys (node 26)

**The design.**

1. **Path of a flow.** The wall has no TAP, veth or virtio-net device. Guest init makes a dummy interface for the default route, redirects outbound TCP to a local shim, runs a DNS stub, and offers an explicit proxy at `127.0.0.1:3128`. The shim carries each flow as one stream on the link that the supervisor opened ([section 5.6](#56-the-winner-holder-inside-thin-supervisor-outside-node-14)). The link is vsock on W3, and one end of a socket pair on W1 and W2. The shim is untrusted. If guest root deletes its rules, traffic has no route out.
2. **Decisions on the trusted side.** A per-session egress process (`egress <id>`) runs with Landlock (no file access) and seccomp, and holds only this session's policy, CA and keys. It resolves each name itself and never trusts a guest address. It refuses loopback, link-local (the cloud metadata address included), private ranges and the host's own addresses unless a grant names them. DNS answers come only for allowed names, and a denied name never goes upstream. UDP and ICMP have no path.
3. **Splice or intercept.** A host rule alone means splice: read the ClientHello, check SNI, copy bytes. A host is intercepted only for a credential binding, an HTTP method or path rule, or an identity pin. Then the process ends TLS with a leaf from a per-session CA (ECDSA P-256, key in memory only, new at each boot) and opens its own verified TLS upstream through Go's `httputil.ReverseProxy`. The Host header must match SNI. Hosts that pin certificates or need mTLS go on a splice-only list.
4. **Keys.** The laptop still fetches values at start. They go to the egress process only, and the guest gets random sentinels that keep each value's prefix. On a bound host the process swaps the sentinel into the named header, never into a body. It refuses any other credential on that host (the identity pin), and strips the headers that make the vendor fetch a URL. A sentinel seen in traffic to another host, a URL or a DNS name is blocked and raises a `key_misuse` alert. After a host restart, the next attach fetches the secrets again and arms the egress process, the same rule as resume today.
5. **Policy.** Presets open, balanced (the default) and locked. Balanced allows the agent's own hosts from the agent table, registries for GET and HEAD only, and code hosts for reads plus pushes to granted repositories. Layers, lowest first: the preset, the agent table, `config.toml`, the host entry, a trusted repository file, `new --allow` and live grants. A deny always wins.
6. **Approvals and audit.** Under balanced and locked, an unmatched destination is held for up to 20 s. The session shows "needs input" with the source "network", and an `egress_blocked` alert goes out. `net allow` or one key in the launcher opens it. Each flow adds one line to `audit.jsonl`: host, port, method, path without the query, status, bytes up and down, rule and key name. It never holds header values or bodies.
7. **Other doors.** Files: the L3 clone seed in, the git-only exit out. Ports: `ssh -L port:remote_socket` to a per-session Unix socket ([ssh](https://man.openbsd.org/ssh)). Devices: none. SSH agent forwarding stays off.

**Evidence.**

- The shape follows Docker's documented proxy, presets and header-only swap ([local governance](https://docs.docker.com/ai/governance/local/), [credentials](https://docs.docker.com/ai/sandboxes/configuration/credentials/)), and Anthropic's published fix for the attacker-key path ([Anthropic](https://www.anthropic.com/research/how-we-contain-claude)).
- Anthropic's sandbox-runtime filters by domain and warns that allowing github.com lets a process push anywhere, and that domain fronting can bypass a domain filter ([sandbox-runtime](https://github.com/anthropics/sandbox-runtime)).
- Claude Code honours `HTTPS_PROXY` and `NODE_EXTRA_CA_CERTS` ([network config](https://code.claude.com/docs/en/network-config)). Codex reads `CODEX_CA_CERTIFICATE`, then `SSL_CERT_FILE` ([Codex PR 14178](https://github.com/openai/codex/pull/14178)).
- A header swap cannot carry AWS SigV4, because the client derives a signing key from the secret ([SigV4](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_sigv-create-signed-request.html)).
- An NDSS 2017 study found that nearly all HTTPS interception products weaken connection security ([NDSS](https://www.ndss-symposium.org/ndss2017/ndss-2017-programme/security-impact-https-interception/)). microsandbox keeps its CA key on disk and reuses it across sandboxes ([microsandbox](https://docs.microsandbox.dev/networking/tls)). This design keeps the key in memory, new at each boot.
- A Go ECDSA P-256 signature took about 48.7 µs in a 2015 measurement, so a leaf costs well under 1 ms ([Cloudflare](https://blog.cloudflare.com/go-crypto-bridging-the-performance-gap)). The egress process is estimated at 10 to 20 MiB per session (**UNVERIFIED**, not measured).

**Why it won.** Security 8.5 and scale 8. Real keys never enter the wall at W1 to W3, so a worm like Nx finds only sentinels. The identity pin closes the attacker-key path that a plain allowlist leaves open. No TAP, bridge, iptables rule, IP address or root step exists per session, so density depends on RAM only. Both judges named the costs. It is about 4 to 6 kLOC of Go, with a proxy that opens TLS on the trusted side. Keys stay in host memory for the whole session. Version 1 has no sentinel path for OAuth refresh rotation or SigV4.

**Changed after review.** The red team found seven gaps in this layer. Each now has a fix in the design ([section 11](#11-the-red-teams-gaps)):

- Model hosts are intercepted, and the body of each model call is parsed. Tools that make the provider fetch a caller-chosen URL (OpenAI's `mcp` tool with a `server_url`, Anthropic's web fetch and MCP connector) are refused unless granted ([OpenAI MCP](https://developers.openai.com/api/docs/guides/tools-connectors-mcp)). Unknown endpoints on model hosts fail closed.
- Endpoints that mint or rotate tokens (`npm token create`, OAuth refresh) are denied on bound hosts ([npm token](https://docs.npmjs.com/cli/v10/commands/npm-token)). A credential-shaped string in a response from a bound host becomes a new sentinel, with a `key_minted` alert.
- Pushes are limited to creates and fast-forwards under `refs/heads/agent/<session>/`, read from the receive-pack commands ([pack protocol](https://git-scm.com/docs/gitprotocol-pack)). Writes under `.github/`, runner registration, repository creation and gists are refused, and so are pushes to public repositories that the user did not name.
- Wildcards that span a private suffix in the Public Suffix List are refused. Object stores and code hosts are GET and HEAD only by default. Each host gets an upload byte budget. The open preset is a recorded downgrade.
- `LoginFiles` are never copied into a walled session. An exposed secret needs a grant for that one secret, and the manifest, the row and an alert record it.
- Approvals show the registrable domain and its punycode form, refuse IDN names unless typed, never offer "always" from the launcher or an alert, and are rate-limited.
- The Go toolchain is pinned above CVE-2026-56866 and CVE-2026-39825, rules match a parsed, canonical request, and every parser that faces the guest is fuzzed ([NVD](https://nvd.nist.gov/vuln/detail/CVE-2026-56866)).

## 8. L5: supply chain and autonomy

| Node | Option | S | F | C | Sc | E | v |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 31 | Registry firewall inside the egress process | 8.7 | 4 | 5 | 6 | 3 | 0.600 |
| 32 | Flight recorder with trust labels, tripwires, kill switch | 8.2 | 5 | 6 | 6 | 2 | 0.602 |
| 33 | Throwaway sessions, one exit: a reviewed git bundle | 8.5 | 5 | 4 | 8 | 3 | 0.617 |
| 34 | Laptop-held digests: a signed, reproducible boot chain | 8 | 6 | 7 | 7 | 4 | 0.675 |
| 35 | Agent rows as policy: model-API guard, caps, split roles | 8.6 | 4 | 3 | 5 | 2 | 0.526 |

### 8.1 Registry firewall (node 31)

A class of rules inside the egress process intercepts npm, PyPI, crates.io and the Go module proxy. It drops versions younger than a minimum age, versions in the OSV malicious-package feed, and versions with git or URL dependencies. It inspects artefacts for new install hooks, a root `binding.gyp` and `.pth` files. It refuses publish and unpublish.

- In a sample of 10 attacks, 8 had windows under a week, and a 14-day cooldown would have blocked all but xz-utils ([cooldowns](https://blog.yossarian.net/2025/11/21/We-should-all-be-using-dependency-cooldowns)). npm has `min-release-age`, and keeps a vulnerable version when the window blocks an audit fix ([npm config](https://docs.npmjs.com/cli/v11/using-npm/config/)). pnpm 11 sets `minimumReleaseAge` to 1440 minutes by default ([pnpm 11](https://pnpm.io/blog/releases/11.0)) and blocks git and tarball sub-dependencies ([pnpm](https://pnpm.io/supply-chain-security)).
- Abbreviated npm metadata has no publish times, so the firewall needs the full document. A researcher fetched both on 9 November 2026: typescript was 15.9 MB against 8.8 MB ([registry](https://registry.npmjs.org/typescript)).
- On 9 November 2026 OSV held 223,236 malicious-package records for npm, 12,017 for PyPI, 22 for crates.io and 20 for Go ([OSV data](https://storage.googleapis.com/osv-vulnerabilities/npm/modified_id.csv)). A Go `.info` time is the commit time, which an attacker sets ([go ref](https://go.dev/ref/mod)), and a typosquat stayed in the Go mirror for three years ([Socket](https://socket.dev/blog/malicious-package-exploits-go-module-proxy-caching-for-persistence)).
- GitHub's package firewall is in preview for Enterprise Cloud only ([changelog](https://github.blog/changelog/2026-10-20-github-package-firewall-in-public-preview/)). Aikido Safe Chain is AGPL-3.0 and Socket's free firewall is not open source, so neither fits ([Safe Chain](https://github.com/AikidoSec/safe-chain), [sfw-free](https://github.com/SocketDev/sfw-free/blob/main/README.md)).

It lost on fit (4): a feed sync would phone home unless the feed ships with releases, and it intercepts registries that L4 would otherwise splice.

**Kept, now.** Phase 0: guest init writes npm `min-release-age` and `ignore-scripts`, pnpm `minimumReleaseAge` and `strictDepBuilds`, uv `exclude-newer`, the cargo publish-age setting and `GOFLAGS=-mod=readonly`. Guest root can remove these, so they are defence in depth only. The registry write gate: no publish, unpublish or yank without a grant.

### 8.2 Flight recorder and tripwires (node 32)

The supervisor writes a metadata-only record outside the wall. Each record carries a trust label: "trusted" from the supervisor, the egress process or the gVisor Sentry, and "guest" from inside. Rules raise alerts and can start a three-step kill switch: cut network and keys, freeze, kill.

- Writing 1 to `cgroup.freeze` stops a whole tree, and `cgroup.kill` sends SIGKILL to it ([cgroup v2](https://docs.kernel.org/admin-guide/cgroup-v2.html)). Firecracker pauses all vCPUs with one API call ([snapshot support](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/snapshot-support.md)).
- gVisor streams trace points to a process outside the sandbox ([seccheck](https://github.com/google/gvisor/blob/master/pkg/sentry/seccheck/README.md)). Seccomp user notification must not enforce a security policy ([seccomp_unotify](https://man7.org/linux/man-pages/man2/seccomp_unotify.2.html)).
- Recording command lines conflicts with decision [0025](../contributing/decisions/0025-debug-mode.md), which forbids it in the debug mode.

It lost on effort (2): three recorder backends, a rule engine and a kill switch. It also detects and contains, but never prevents the first harmful action.

**Kept.** A trust label on every security record and alert. Sentinels and planted decoys as exact-match tripwires in the egress process, including base64 forms. Rules, kept as data, for the worms' GitHub moves: repository creation, visibility changes, workflow writes and runner registration tokens ([GitHub runners API](https://docs.github.com/en/rest/actions/self-hosted-runners)). The kill switch on the trusted side. Evidence for decoys came later: canary keys caught a real CI breach when the attacker checked them ([facts](build-facts.md#7-later-phases), [spec 21.7](../contributing/isolation-spec.md#217-decoys-p72)).

### 8.3 Throwaway sessions and a reviewed exit (node 33)

Every walled session is disposable. Work leaves only as a git bundle that the client imports into quarantine refs, then shows a review screen. The screen flags git hooks, CI files, build and install scripts, registry config, agent project files, hidden Unicode and the session's own secret values.

- Shai-Hulud 2.0 wrote its workflow through the GitHub contents API, which a local review never sees ([Sysdig](https://www.sysdig.com/blog/return-of-the-shai-hulud-worm-affects-over-25-000-github-repositories)).
- VS Code tasks can run when a folder opens ([VS Code](https://code.visualstudio.com/docs/terminal/basics)). Trojan Source showed that bidirectional controls make reviewed code differ from compiled code ([CVE-2021-42574](https://community.developer.atlassian.com/t/cve-2021-42574-unrendered-unicode-bidirectional-override-characters-may-impact-apps-with-code-visualizing-components/53314)).

It lost on convenience (4): users would lose today's direct view of the work, and many would turn auto-approval on.

**Kept.** Unbundle into a fresh repository with hooks off, `fsck` on, quarantine refs and no checkout. Pin setup-script trust to the script's hash, as direnv does with `.envrc`. A data file of flag rules, with each agent row naming its project files. Clean every untrusted string shown outside the attach stream.

### 8.4 Agent rows as policy (node 35)

Each agent row gets a guard block as data: its model hosts and wire format, its base-URL and CA variables, the tools that bring in web content, and hardening switches. The egress process runs the model API as its own door, with spend, rate and concurrency caps. Profiles keep a session from holding untrusted input, private data and an outbound channel all at high risk.

- Claude Code can be pointed at a gateway, and `CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1` strips credentials from the environments of the commands it starts ([env vars](https://code.claude.com/docs/en/env-vars), [gateway](https://code.claude.com/docs/en/llm-gateway-connect)). Codex reads admin requirements from `/etc/codex/requirements.toml` ([managed configuration](https://learn.chatgpt.com/docs/enterprise/managed-configuration)).
- Host lists change: Claude Code's telemetry hosts changed between releases ([issue](https://github.com/anthropics/claude-code/issues/1217)).

It lost on fit (4) and convenience (3). Settings fragments change an agent's permissions, which the design's non-goals forbid ([design](../explanation/design.md)). Rows also go stale between releases.

**Kept.** Generate the balanced preset from the session's agent row. Close server-side fetch on model hosts (now a red-team fix in L4). Per-session spend and rate caps in the egress process, with a budget alert. Service rows for GitHub and npm that refuse the worms' moves even through an open token door.

### 8.5 The winner: laptop-held digests (node 34)

**The design.**

1. **The laptop holds the digests, the hosts hold the bytes.** Each release adds a release manifest, `release-manifest.json`, which the client embeds. For each architecture it lists the SHA-256 of the helper, the shadow terminal, the guest kernel, a small tools image (with its verity root hash and salt), the pinned VMM and runsc. The user's images go in a second lock on the laptop, keyed by OCI digest. A host never decides what is trusted.
2. **Reproducible builds.** The release workflow builds a kernel.org longterm kernel, pinned by SHA-256, with a committed config based on Firecracker's CI configs plus `DM_VERITY`, `DM_INIT`, `EROFS`, `OVERLAY_FS` and `VSOCK` ([kernel policy](https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md)). `CONFIG_MODULES=n` removes module signing, a known cause of builds that differ, and the `KBUILD_BUILD_*` values are fixed ([reproducible builds](https://docs.kernel.org/kbuild/reproducible-builds.html)). The tools image comes from `mkfs.erofs` with a fixed time, no UUID and root owners, plus a verity tree with a fixed salt ([mkfs.erofs](https://github.com/erofs/erofs-utils/blob/dev/man/mkfs.erofs.1)). Each release also ships the kernel's source and config.
3. **Upstream pins.** A lock file pins the VMM and runsc by SHA-256. At pin time CI compares each hash with the upstream checksum and with GitHub's immutable asset digest ([asset digests](https://github.blog/changelog/2025-06-03-releases-now-expose-digests-for-release-assets/)). Firecracker publishes only `.sha256` files, with no public signature ([firecracker#6117](https://github.com/firecracker-microvm/firecracker/issues/6117)).
4. **Checks.** `dial` checks the helper with `sha256sum -c` against the client's digest before the bridge starts. The supervisor hashes the VMM binary through an open file descriptor and runs that descriptor, so nothing can swap the file between check and run. It builds the kernel command line only from the client's digests, with two verity tables and `panic_on_corruption` ([verity](https://docs.kernel.org/admin-guide/device-mapper/verity.html)). fs-verity seals artefacts where the file system has it ([fs-verity](https://docs.kernel.org/filesystems/fsverity.html)). Spec 20.5 replaced the hash of an open file: the supervisor checks fs-verity, or hashes a sealed `memfd` copy ([question 82](../contributing/isolation-spec.md#15-open-questions)).
5. **pins-watch.** A daily CI job checks every pin outside a package ecosystem (the Ghostty commit, the VMM, the kernel, runsc) against GitHub repository advisories and OSV, and opens an issue on a match. A bump waits 7 days unless it fixes a security problem. The Ghostty advisories use the "other" ecosystem, with free-text commit ranges, and are in neither OSV nor GitHub's global database. So no package scanner matches them to a Zig pin ([build facts](build-facts.md#61-ghostty-and-its-advisories)).
6. **A signed-tag release gate.** `release.yml` runs `git verify-tag` against a committed allowed-signers file before any build. The tag key is a hardware-backed `ed25519-sk` key ([Yubico](https://developers.yubico.com/SSH/Securing_git_with_SSH_and_FIDO2.html)). A stolen GitHub token alone cannot make a release, the lesson of tj-actions ([GHSA-mw4p](https://github.com/tj-actions/changed-files/security/advisories/GHSA-mw4p-6x4p-x5m5)). Immutable releases lock tags and assets ([GitHub](https://github.blog/changelog/2025-10-28-immutable-releases-are-now-generally-available/)).

It builds on what exists: the SSH release signature of decision [0017](../contributing/decisions/0017-release-signatures.md), cosign, SLSA provenance and `just repro`. Go builds with `CGO_ENABLED=0 -trimpath` are reproducible ([Go](https://go.dev/blog/rebuild)). sigstore-go verifies offline ([sigstore-go](https://docs.sigstore.dev/language_clients/go/)).

**Why it won.** The best fit (6), convenience (7), scale (7) and effort (4) of the five. A compromised helper would run outside every wall and see every key, so this closes a high-impact path. It also found a real gap: the Ghostty pin. Both judges said plainly that it adds almost nothing against T1, T2 and T3 inside a session, the owner's main threats. So the design does not take it alone. It grafts the best parts of nodes 31, 32, 33 and 35 ([section 9](#9-the-design-after-review)).

**Changed after review.** The security model must state that the dial check cannot defend a compromised login account: a planted rc file runs before the check. Only a separate identity for the trusted side closes that ([section 11](#11-the-red-teams-gaps), gap 1).

## 9. The design after review

The search picked the path. The fact check, the red team and the losers' best ideas then changed it. This is the result, in the order to build it.

### 9.1 Do now, before any wall

These fix holes that exist today. Each one stands alone.

1. Give each agent a hook-only socket that accepts only hook events, enforce `Hello.Role`, and check the peer in `Holder.serve`.
2. Move the Ghostty pin past `e257bb45` and `0af5c9e55`, and cap client sizes at about 4096 columns and rows (spec 19.1 sets 4096 columns and 2048 rows).
3. Validate a hook's session ID against a per-agent pattern before it enters the manifest, and refuse an argv with a danger flag that the user did not type.
4. Pin setup-script trust to the script's SHA-256, and ask again after any change.
5. Stop running host git in a session's repository: replace the `git status` calls in `removeWorktree` and `pull`. Copy `.worktreeinclude` files through `os.Root` or `openat2` with `RESOLVE_BENEATH` and `RESOLVE_NO_SYMLINKS` ([Go 1.24](https://go.dev/doc/go1.24)).
6. In the client, clean every string a host sends: C0, C1, DEL, bidirectional controls, zero-width and tag characters. Stop showing `Status.Reason` raw.
7. Never treat a paste as a file drop when its text matches a recent OSC 52 write from the session. Refuse paths under `~/.ssh`, `~/.aws`, `~/.config` and `~/.gnupg`, and name every uploaded file in a notice.
8. Set `hostLoopback: false` on Lima hosts, and pass `--nested-virt` where the probe finds support ([limactl create](https://lima-vm.io/docs/reference/limactl_create/)).
9. Add the pins-watch job.

### 9.2 The wall

- The levels `process`, `gvisor` and `microvm` (W1 to W3), probed at setup, with the floor pinned on the laptop. New sessions below W1 are refused by default. W4 is a placement for provider hosts.
- W3 is the default where `/dev/kvm` works: Cloud Hypervisor v52.0 or later on host kernels 6.8 and later, or Firecracker 1.17.0 or later on 5.10, 6.1 and 6.18. On EC2, W3 also comes from Nitro Sandboxes through Firecracker 1.18.0 or later ([section 10.1](#101-aws-nitro-sandboxes-on-ec2)). Disks are always raw, with backing files off, and Cloud Hypervisor starts with `--landlock`.
- **Each component that touches untrusted input gets its own identity:** the VMM, the egress process, the image converter and any host-side git. Each runs in a user namespace with a mapped subuid and an empty network namespace. Its mount namespace shows only its session folder and sockets: no `/dev/vhost-*`, no `$HOME` writes, no systemd user bus. Each gets Landlock, seccomp, `NO_NEW_PRIVS` and `PR_SET_DUMPABLE=0`.
- Kernel hardening: the W1 seccomp filter refuses `AF_VSOCK` and `io_uring_setup`. Host setup blocks the `vsock_loopback`, `vmw_vsock_virtio_transport` and `vhost_vsock` modules, which hybrid vsock does not need, and sets `kernel.io_uring_disabled=2`. The guest kernel is built without vsock loopback and io_uring. `doctor` checks the host kernel against a data list of known local root bugs.

### 9.3 Control

- The holder inside the wall, a supervisor outside, as in [section 5.6](#56-the-winner-holder-inside-thin-supervisor-outside-node-14).
- A trusted manifest next to fields that the guest claims. Alert destinations and their tokens never enter the wall.
- In the client, observe escape sequences without changing them ([question 26](../contributing/isolation-spec.md#15-open-questions)). Flag OSC 52 writes, notifications that copy the client's own title, title reports and file-transfer sequences. Report each one in a notice and the audit log. Warn when the terminal's version (queried with XTVERSION) is in a known-bad range.

### 9.4 Lifecycle

- Cold boot from a verified image, with init from the project's tools image. The root resets at every boot, and only `/work` and declared agent state persist.
- Stop, wake on attach, lazy restore after a reboot, reflink fork and checkpoint, and moves between hosts.
- Conversion in a box. Templates promoted only after review. Shared caches read-only.

### 9.5 Doors

- No network card. One egress process per session, with presets, an identity pin, sentinels, a tripwire, DNS for allowed names only and an address check on every dial.
- Model-API body checks, denied token minting, push limits, Public Suffix List checks, upload budgets and safer approvals ([section 7.5](#75-the-winner-nic-less-wall-egress-proxy-sentinel-keys-node-26)).
- Files: a clone seeded by bundle, and `pull` into quarantine refs with `transfer.fsckObjects`, no checkout, and a review summary that flags files which run code on the laptop or in CI. `.worktreeinclude` becomes a grant on the laptop. A repository file may only narrow egress or propose grants that the laptop confirms.
- Ports: one grant per port, shown on the session row. Suggest a `<id>.localhost` name so that the browser gives each session its own origin and cookies ([RFC 6265](https://datatracker.ietf.org/doc/html/rfc6265#section-8.5)).
- Secrets: never copy agent `LoginFiles` into a walled session. The supported paths in version 1 are API keys and `claude setup-token` as bound secrets, git over HTTPS through the egress process, and an `ssh` door built on `session-bind`. An exposed secret is a recorded downgrade, and refresh-token files are refused as exposed secrets by default ([question 15](../contributing/isolation-spec.md#15-open-questions)).

### 9.6 Supply chain and autonomy

- Laptop-held digests, reproducible boot pair, signed-tag gate and pins-watch.
- Phase 0 package-manager settings in guest init, and the registry write gate. The full registry firewall comes later, with the feed shipped in releases or synced by a command the user starts ([question 20](../contributing/isolation-spec.md#15-open-questions)).
- Tripwires on sentinels and decoys, GitHub worm rules as data, and the three-step kill switch.
- Per-session spend and rate caps in the egress process.

## 10. Platforms

| Platform | Best level | How | Fallback | Sources |
| --- | --- | --- | --- | --- |
| Mac on Apple silicon, as the laptop | None on macOS itself | The helper is Linux-only. Apple `container` gives a VM per container but needs macOS 26 | Run sessions on a Lima host or a remote host. A native helper on libkrun with HVF needs a darwin port of the holder ([question 37](../contributing/isolation-spec.md#15-open-questions)) | [container](https://github.com/apple/container) |
| Mac, Lima vz host, M3 or later, macOS 15 or later | W3, nested | `--nested-virt` gives `/dev/kvm` in the guest. The speed is unmeasured | W2 if the probe fails | [Apple API](https://developer.apple.com/documentation/virtualization/vzgenericplatformconfiguration/isnestedvirtualizationsupported), [container machine](https://github.com/apple/container/blob/main/docs/container-machine.md) |
| Mac, Lima vz host, M1 or M2 | W2 | gVisor arm64 with systrap. The Ubuntu template needs an `AppArmor` userns profile | W1, or W4 with one Lima VM per session | [gVisor install](https://gvisor.dev/docs/user_guide/install/), [Ubuntu](https://ubuntu.com/blog/ubuntu-23-10-restricted-unprivileged-user-namespaces) |
| Linux with KVM, host kernel 6.8 or later (Ubuntu 24.04, Debian 13) | W3 | Cloud Hypervisor as the user in the kvm group, after one root step | W2, recorded | [Cloud Hypervisor](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/README.md) |
| Linux with KVM, host kernel 5.10, 6.1 or 6.18 (Debian 12 is 6.1) | W3 | Firecracker with the MMIO transport. Clone mode only, since it has no virtio-fs | W2, recorded | [kernel policy](https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md) |
| Cloud VM without nested virtualisation: Hetzner Cloud, EC2 types without Nitro Enclaves, GCE Arm, E2 and AMD other than N4D, Azure Arm | W2 | gVisor systrap, no memory checkpoint when rootless | W1 where runsc cannot run, or W4 through E2B | [Hetzner](https://docs.hetzner.com/cloud/servers/faq/), [GCE](https://docs.cloud.google.com/compute/docs/instances/nested-virtualization/overview) |
| EC2 with nested KVM: M7i, C7i, R7i, M8i, C8i, R8i, X8i, I7i and their variants, all Intel | W3, nested | KVM only, as the hypervisor inside the instance. AWS advises bare metal for latency-sensitive work | Benchmark W2 against W3 at setup and pin the faster one that meets the floor | [EC2](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/amazon-ec2-nested-virtualization.html) |
| EC2 with Nitro Enclaves: most M, C and R families from the 5th generation on, Intel, AMD and Graviton, except their smallest sizes and most metal sizes | W3, a first-level guest | Firecracker 1.18.0 or later drives `/dev/nitro_enclaves` instead of `/dev/kvm`. The Nitro hypervisor enforces the boundary. Needs the enclave option at launch and an Amazon Linux 2023 kernel 6.18 parent | Nested KVM where the type has it, else W2 | [EC2](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/amazon-ec2-nitro-sandboxes.html), [Firecracker](https://github.com/firecracker-microvm/firecracker/blob/firecracker-v1.18/docs/nitro-sandboxes/README.md) |
| GCE with nested virtualisation | W3, nested | KVM only, with a CPU penalty of 10% or more | As for EC2 | [GCE](https://docs.cloud.google.com/compute/docs/instances/nested-virtualization/overview) |
| Azure x86 sizes such as Dsv6 | W3, nested | Arm sizes such as Dpsv6 have no nested virtualisation | W2 | [Dpsv6](https://learn.microsoft.com/en-us/azure/virtual-machines/sizes/general-purpose/dpsv6-series) |
| Existing provider: lima | As for the Mac rows | Add `--nested-virt` where supported and `hostLoopback: false` to `CreateArgs` | W2 in one Lima VM rather than one VM per session | [lima.go](../../internal/provider/lima/lima.go) |
| Existing provider: docker | W0 | The default seccomp profile blocks namespaces, and no `/dev/kvm` is passed in | Refuse by default and require a named downgrade ([question 6](../contributing/isolation-spec.md#15-open-questions)), run the container under runsc as root on the Docker host, or place walled sessions elsewhere | [Docker seccomp](https://docs.docker.com/engine/security/seccomp/) |
| Existing provider: e2b | W4 | Each sandbox is a Firecracker microVM, but nothing trusted runs outside it | Use E2B's own egress lists and `allowPublicTraffic: false`. Keys in the environment only as a recorded downgrade ([question 22](../contributing/isolation-spec.md#15-open-questions)). Whether W1 or W2 runs inside an E2B guest is untested | [E2B network](https://docs.e2b.dev/network/internet-access), [persistence](https://docs.e2b.dev/sandbox/persistence) |

**Density.** On the 4 GB test host, about 2 or 3 W3 sessions with 1 GiB guests fit, with dozens more stopped on disk. On an 8 GB host, about 5 busy Claude sessions fit today ([architecture](../explanation/architecture.md)). The research estimates 55 to 90 MiB more per W3 session for the guest kernel, the holder and the supervisor (**UNVERIFIED**, not measured). Spec 20.10 now gives a budget for each part, which P3.8 measures. Fixed guest RAM is the real limit, because Firecracker's free page reporting is still a developer preview ([ballooning](https://github.com/firecracker-microvm/firecracker/blob/main/docs/ballooning.md)).

### 10.1 AWS Nitro Sandboxes on EC2

Researched on 10 November 2026. AWS announced Nitro Sandboxes on 9 November 2026 ([announcement](https://aws.amazon.com/about-aws/whats-new/2026/11/nitro-sandboxes-eks-ecs-ec2/)). The same day, Firecracker 1.18.0 added support for them ([release](https://github.com/firecracker-microvm/firecracker/releases/tag/v1.18.0)).

**What it is.**

- A Nitro Sandbox is "a lightweight virtual machine, or child VM" that runs beside the workload on an EC2 instance. It is "a first-level, hardware-virtualized guest", not a nested guest. The Nitro hypervisor isolates it from the parent instance and from other sandboxes ([EC2 guide](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/amazon-ec2-nitro-sandboxes.html)).
- It runs on Intel, AMD and Graviton, at no extra charge, in every commercial region ([announcement](https://aws.amazon.com/about-aws/whats-new/2026/11/nitro-sandboxes-eks-ecs-ec2/)). AWS names "agentic workloads" that "generate and run untrusted code" as a use, "giving each agent session a hardware-isolated boundary" ([EC2 guide](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/amazon-ec2-nitro-sandboxes.html)).
- It needs an instance type that supports Nitro Enclaves, launched with `--enclave-options Enabled=true`. Most M, C and R families from the 5th generation on qualify, except their smallest size and most metal sizes. The burstable T families are not on the list ([EC2 guide](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/amazon-ec2-nitro-sandboxes.html)).
- EKS runs a pod in one with `runtimeClassName: isolated-sandbox`, and ECS has isolated and confidential tasks on Managed Instances. Inside, EKS starts a Firecracker microVM per pod, and a pod can boot with no network device at all ([EKS](https://docs.aws.amazon.com/eks/latest/userguide/sandboxing.html), [ECS](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/isolated-confidential-tasks.html)). The EKS guide warns that the guest's vsock channel can still reach ports bound inside the guest, so a missing network device "is not absolute isolation". The design's link already dials only into the guest ([0030](../contributing/decisions/0030-local-sockets-do-not-listen.md)).

**How Firecracker drives it.**

- Firecracker opens the Nitro Enclaves driver at `/dev/nitro_enclaves` instead of `/dev/kvm`. It keeps its API, its jailer and its virtio devices, and a new `PUT /enclave` endpoint picks the mode ([release](https://github.com/firecracker-microvm/firecracker/releases/tag/v1.18.0)).
- No `/dev/kvm` and no bare-metal instance are needed. Guest CPUs are hardware CPUs that the Nitro System schedules, so the parent's cgroups do not govern them ([comparison with KVM](https://github.com/firecracker-microvm/firecracker/blob/firecracker-v1.18/docs/nitro-sandboxes/comparison-with-kvm.md)).
- The guest boots through UEFI from an Enclave Image Format file, which Firecracker can build in memory from a kernel and boot arguments. The guest kernel needs an EFI stub, ACPI, PCIe and virtio-pci ([getting started](https://github.com/firecracker-microvm/firecracker/blob/firecracker-v1.18/docs/nitro-sandboxes/getting-started.md)). The MMIO transport is not available ([comparison with KVM](https://github.com/firecracker-microvm/firecracker/blob/firecracker-v1.18/docs/nitro-sandboxes/comparison-with-kvm.md)). So the `nitro` driver uses virtio-pci, and the KVM `firecracker` driver keeps MMIO ([spec 21.5](../contributing/isolation-spec.md#215-the-firecracker-driver-p62)).
- Memory is donated whole at start from a physically contiguous pool, and returns only at the end. There is no balloon, no huge pages, no snapshot and no pause ([comparison with KVM](https://github.com/firecracker-microvm/firecracker/blob/firecracker-v1.18/docs/nitro-sandboxes/comparison-with-kvm.md)).
- The parent kernel needs the Nitro Sandboxes support in its `nitro_enclaves` driver. The documented kernel is Amazon Linux 2023 `kernel6.18` from `6.18.51-120.162.amzn2023` ([getting started](https://github.com/firecracker-microvm/firecracker/blob/firecracker-v1.18/docs/nitro-sandboxes/getting-started.md)). `/dev/nitro_enclaves` is `root:root` with mode 0660 in its example.

**Two modes.**

| | Direct | Confidential, the default |
| --- | --- | --- |
| The parent's access to guest memory | Read and write. "Its trust model is equivalent to a standard nested virtual machine." | None |
| Attestation | None | A Nitro Security Module with measurement registers, checked by an outside verifier such as KMS |
| The guest kernel | Unpatched virtio drivers work | Needs the proposed DMB virtio patches |
| I/O | Higher throughput | Device queues in dedicated buffers |

Sources: [EC2 guide](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/amazon-ec2-nitro-sandboxes.html), [comparison with KVM](https://github.com/firecracker-microvm/firecracker/blob/firecracker-v1.18/docs/nitro-sandboxes/comparison-with-kvm.md).

**Limits.**

- The vCPUs of all sandboxes on an instance add up to at most 5 times the instance's vCPUs. Memory is not overcommitted, so memory also limits the count ([EC2 guide](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/amazon-ec2-nitro-sandboxes.html)).
- A sandbox has at most 32 devices, 64 MiB of memory or more in steps of 2 MiB, and a fixed size for its life ([comparison with KVM](https://github.com/firecracker-microvm/firecracker/blob/firecracker-v1.18/docs/nitro-sandboxes/comparison-with-kvm.md)).
- The parent's network stack is shared. One sandbox can fill the connection-tracking table or the DNS resolver for the others, so the parent should cap each one ([EC2 guide](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/amazon-ec2-nitro-sandboxes.html)). In this design a sandbox has no network device, and its egress process has budgets.
- The device back ends still run in the Firecracker process on the parent. A back-end bug lands there, as with KVM, so the VMM's jail stays.

**What it means for this design.** EC2 stops being a host that gets only W2 on Graviton and on most Intel and AMD types. A `nitro` driver gives the `microvm` level there, in direct mode, because the trusted side already runs on the parent and the user owns the instance. It fits the cold boot of L3, which never snapshots, and the link of L2, which uses vsock. It costs a second guest kernel build, with an EFI stub and virtio-pci, and admission must count donated memory, the vCPU budget and the device slots. Confidential mode with attestation could later keep a guest's memory from the owner of a `shared` EC2 host. The work disk and the network still pass through the parent, so that needs more than a mode switch ([question 54](../contributing/isolation-spec.md#15-open-questions)).

**What could not be verified.**

- Whether the enclave option can be turned on for an instance that was launched without it.
- Whether a parent kernel other than Amazon Linux 2023's `kernel6.18` has the support, and whether it is in upstream Linux.
- Whether donated memory counts against the cgroup of the process that donates it.
- The time to boot a sandbox with an agent, and its speed against nested KVM. AWS says "near-native".

## 11. The red team's gaps

The red team attacked the first draft of the path. It found 23 gaps: 1 critical, 14 high and 8 medium. Each row says what the design now does. "Risk" means the gap stays open in part and is listed in [section 14](#14-risks-that-remain).

| # | Severity | Threat | Gap | What the design does | Source |
| --- | --- | --- | --- | --- | --- |
| 1 | Critical | T4, T5, T1 | The VMM, the egress process and the converter ran as the login user. A VMM or KVM escape could read every egress process's keys, drive every session socket, write shell rc files that run before the dial check, and use `/dev/vhost-*` (Vghost) for root | A separate identity and jail for each such component ([section 9.2](#92-the-wall)). The security model states that the dial check cannot defend a compromised login account. Risk: a dedicated system user changes how SSH reaches the helper ([question 28](../contributing/isolation-spec.md#15-open-questions)) | [GHSA-f47p](https://github.com/cloud-hypervisor/cloud-hypervisor/security/advisories/GHSA-f47p-p25q-83rh) |
| 2 | High | T4, T5 | A guest-written disk with a QCOW2 header could point the VMM at a host file | Cloud Hypervisor v52.0 or later, the disk type always raw, backing files off, `--landlock`. An end-to-end test writes such a header and expects zeros | [GHSA-jmr4](https://github.com/cloud-hypervisor/cloud-hypervisor/security/advisories/GHSA-jmr4-g2hv-mjj6) |
| 3 | High | T2, T4 | libkrun with TSI lets the guest use the VMM's host network and Unix sockets, bypassing the wall. The draft also named different VMMs in L1, L3 and L4 | One VMM choice per host, used by all layers. libkrun only with TSI off, in an empty network namespace, behind a test that every guest socket fails | [libkrun](https://github.com/libkrun/libkrun/blob/main/docs/security-model.md) |
| 4 | High | T2, T5 | A session writes a laptop path to the clipboard with OSC 52. A later paste turns it into a file drop that uploads `~/.ssh/id_ed25519` | No file drop for text that matches a recent session OSC 52 write. Images and PDFs only by default in walled sessions. Sensitive paths refused. A notice names each upload | [Ghostty config](https://ghostty.org/docs/config/reference) |
| 5 | High | T4 | Escape sequences reach the laptop unchanged: clipboard writes, fake notifications, hidden link targets, parser bugs | Observe and warn in the client, without changing bytes. An opt-in guarded attach is [question 26](../contributing/isolation-spec.md#15-open-questions), because it bends the byte-exact rule. Risk | [GHSA-w9q4](https://github.com/ghostty-org/ghostty/security/advisories/GHSA-w9q4-5jvv-mcv4) |
| 6 | High | T2 | The model API stays open in every preset. A tool declared in the request body makes the vendor's servers fetch an attacker URL | Parse model request bodies and refuse fetch tools unless granted. Unknown endpoints fail closed | [OpenAI MCP](https://developers.openai.com/api/docs/guides/tools-connectors-mcp) |
| 7 | High | T5 | A bound key can mint a new real token, which reaches the guest in plain text | Deny minting endpoints, and turn credential-shaped responses into new sentinels with an alert | [npm token](https://docs.npmjs.com/cli/v10/commands/npm-token) |
| 8 | High | T1, T2, T3 | Pushes to granted repositories run in CI with the repository's secrets, can add workflows, and can publish at once on a public repository | Push only to `refs/heads/agent/<session>/`, no deletes, no `.github/` writes, no runners, no new repositories or gists, no public pushes unless named | [Wiz](https://www.wiz.io/blog/shai-hulud-2-0-ongoing-supply-chain-attack) |
| 9 | High | T4 | Host git ran in session-writable repositories, and `.worktreeinclude` followed symlinks. Docker had the same class in `sbx cp` | No host git on session data. `os.Root` or `openat2` for every trusted copy. `.worktreeinclude` as a laptop grant | [Endor Labs](https://www.endorlabs.com/learn/docker-sandboxes-vulnerability-lets-ai-agents-access-sensitive-files-outside-their-sandbox-cve-2026-103916) |
| 10 | High | T4, T1 | Work that reaches the laptop can carry agent hooks, editor tasks, `.envrc` or a hostile `.gitmodules` into a trusted folder | `pull` into quarantine refs with fsck, no checkout, a review summary with flags kept as data, `.gitmodules` values with a CR refused | [Check Point](https://research.checkpoint.com/2026/rce-and-api-token-exfiltration-through-claude-code-project-files-cve-2025-59536/) |
| 11 | High | T3, T1 | A hook's session ID can carry a flag such as one that turns approvals off, and it runs at every cold boot | Per-agent ID patterns, `--` before positional IDs, a `DangerFlags` list in each agent row | [hooks.go](../../internal/host/hooks.go) |
| 12 | High | T1 | The chosen L5 checks only the project's own artefacts, not what the agent installs | Phase 0 package-manager settings now, the registry write gate, the full firewall later | [pnpm 11](https://pnpm.io/blog/releases/11.0) |
| 13 | High | T2 | Data can leave to any tenant behind an allowed name: object stores, app hosts, code hosts, domain fronting, ECH | Public Suffix List checks, GET and HEAD only on object stores and code hosts, ECH refused, upload budgets, Open as a recorded downgrade. Risk: fronting on allowed CDNs | [sandbox-runtime](https://github.com/anthropics/sandbox-runtime) |
| 14 | High | T5 | OAuth logins, SSH keys, SigV4 and service-account JWTs have no sentinel path, so users copy login files in | `LoginFiles` never seeded. Exposed secrets recorded. Refresh-token files refused. An OAuth broker is later research ([question 15](../contributing/isolation-spec.md#15-open-questions)). Risk | [authentication](https://code.claude.com/docs/en/authentication) |
| 15 | High | T4, T1 | VsockDrop and Vghost give local root from W1, or from a VMM escape | W1 seccomp refuses `AF_VSOCK` and io_uring. Modules blocked, `io_uring_disabled=2`, `/dev/vhost-*` hidden. `doctor` warns on unpatched kernels, and never lowers a floor (spec 20.11) | [TuxCare](https://tuxcare.com/blog/cve-vsockdrop/) |
| 16 | Medium | T1, T2 | Trust was a yes or no, so an edited setup script or network file runs with no new question | Trust pinned to file hashes. Repository files only narrow egress or propose grants | [trust.go](../../internal/client/trust.go) |
| 17 | Medium | T1, T4 | A dropper on the kept disk ran at every boot and spread through forks and templates | Root reset by default, templates reviewed, caches read-only, fork rows show the parent's last flagged event | [usage](https://docs.docker.com/ai/sandboxes/usage/) |
| 18 | Medium | T2, T4, T5 | The egress process parses hostile HTTP, TLS and DNS while it holds real keys. Two 2026 Go proxy bugs apply | Pinned Go above the fixes, canonical requests, refusal of ambiguous framing, one Transport per process, fuzzing from the first release. Risk | [NVD](https://nvd.nist.gov/vuln/detail/CVE-2026-56866) |
| 19 | Medium | T2, T3 | An injected agent can wear the user down with lookalike names and requests to downgrade | Registrable domain and punycode in every approval, IDN refused unless typed, no "always" from the launcher, rate limits, downgrades only for new sessions | [local governance](https://docs.docker.com/ai/governance/local/) |
| 20 | Medium | T4 | Guest text reaches the launcher and `ls` outside the attach stream, and alert commands get it in variables | Clean all host strings, show guest text as "session says", type-check IDs and ports, separate variables for guest text | [detail.go](../../internal/tui/detail.go) |
| 21 | Medium | T4 | A forwarded port serves guest content at a localhost origin in the laptop's browser | A grant per port, a distinct loopback address where possible, a `<id>.localhost` name, a warning on the first forward | [RFC 6265](https://datatracker.ietf.org/doc/html/rfc6265#section-8.5) |
| 22 | Medium | T4, T1 | Image conversion ran C code on untrusted layers outside the wall, before any hash existed | Conversion in a box with no network, digest checks and caps, a pure-Go converter preferred | [mkfs.erofs](https://man.archlinux.org/man/mkfs.erofs.1.en) |
| 23 | Medium | T1 to T5 | Docker hosts give W0 and W4 hosts have no trusted egress, so "secure by default" depended on the first host the user picked | Below W1 refused by default. W4 maps grants to provider-native egress, and keys in the environment are a recorded downgrade. Agent forwarding off everywhere | [0013](../contributing/decisions/0013-host-providers.md) |

## 12. The fact check's corrections

A fact check re-read the claims that the design leans on. This note uses each correction.

| Claim in the research | Verdict | Correction | Source |
| --- | --- | --- | --- |
| Firecracker boots to init in 125 ms or less, with 5 MiB of overhead | Confirmed, narrowed | Only for 1 vCPU, 128 MiB, a minimal kernel and the serial console off. 5 MiB is the VMM threads, not guest RAM. Measure a real session start before quoting one | [SPECIFICATION.md](https://github.com/firecracker-microvm/firecracker/blob/main/SPECIFICATION.md) |
| A snapshot restores in as little as 4 ms, and keeps the screen across a stop | Confirmed, with a conflict | 4 ms is a best case. A restore cannot cross kernels, CPU models or VMM versions. The chosen L3 saves no memory, so on this path a stop loses the screen and the running turn | [snapshot support](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/snapshot-support.md) |
| Hybrid vsock needs no network port | Confirmed, with a caveat | True, but in the first draft the supervisor listened on two Unix sockets that a guest could reach. The spec now has the supervisor dial into the guest ([0030](../contributing/decisions/0030-local-sockets-do-not-listen.md)). Firecracker's lack of virtio-fs is current | [vsock](https://github.com/firecracker-microvm/firecracker/blob/main/docs/vsock.md) |
| Cloud Hypervisor is Apache-2.0 or BSD-3-Clause | Wrong | It is both together: Apache-2.0 and BSD-3-Clause, so the BSD notices must ship. Its 6.8 kernel minimum is right, and Firecracker tests only 5.10, 6.1 and 6.18, so no single VMM covers every common host | [CONTRIBUTING](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/CONTRIBUTING.md) |
| libkrun can be a default W3 VMM | Confirmed facts, wrong conclusion | Its VMM serves devices with its own host permissions, and it can hijack host Unix sockets. It earns W3 only inside a jail with TSI off | [libkrun](https://github.com/libkrun/libkrun/blob/main/docs/security-model.md) |
| gVisor systrap needs no virtualisation, and rootless runsc skips some features | Confirmed | Rootless has no `create`, no save and restore, and no netstack, and it ignores cgroup errors | [rootless](https://gvisor.dev/docs/user_guide/rootless/) |
| Nested virtualisation on a Mac needs macOS 15 and an M3 | Confirmed | Apple's API page states only macOS 15. The M3 requirement comes from Apple's `container` docs. M1 and M2 give no `/dev/kvm` to any guest | [container machine](https://github.com/apple/container/blob/main/docs/container-machine.md) |
| EC2 nested KVM covers C7i, M7i, R7i, C8i, M8i and R8i | Confirmed, extended | The list also has the flex, `d` and `z` variants, X8i, I7i and I7ie. All are Intel. Graviton and AMD are not on it | [EC2](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/amazon-ec2-nested-virtualization.html) |
| GCE has no nested virtualisation on Arm, nor on AMD except N4D | Confirmed, extended | E2 and memory-optimised VMs are also out. KVM only. 10% or more CPU penalty. Probe `/dev/kvm`, do not trust the machine type | [GCE](https://docs.cloud.google.com/compute/docs/instances/nested-virtualization/overview) |
| Azure supports nested virtualisation from Dv3 | Confirmed for x86 only | Dsv6 has it, the Arm Dpsv6 does not | [Dpsv6](https://learn.microsoft.com/en-us/azure/virtual-machines/sizes/general-purpose/dpsv6-series) |
| Hetzner Cloud servers have no `/dev/kvm` | Confirmed | One research claim, from a forum, said they do. It is wrong and is not used here | [Hetzner FAQ](https://docs.hetzner.com/cloud/servers/faq/) |
| Docker Sandboxes keeps real keys out of the VM | Confirmed, with four details | Swaps happen in headers only. A kit can pass a real OAuth token in. Claude Code logs in inside when no credential is stored. A workspace is optional since sbx 0.42.0 | [credentials](https://docs.docker.com/ai/sandboxes/configuration/credentials/) |
| Docker Sandboxes on Linux works for one user with UID 1000 | Wrong, out of date | The current install page asks for Ubuntu 24.04 or later, KVM and the kvm group, and names no UID limit | [install](https://docs.docker.com/ai/sandboxes/install/) |
| The hosted VM service's feature list | Confirmed, extended | $0.036 an hour, a stopped disk kept at least 14 days, a new IP at each resume, some sandboxes without their own IPv4 | Its docs |
| The Ghostty advisory forces the parser inside the wall | Confirmed facts, softer weight | GitHub rates it Moderate, and the advisory says that libghostty-vt is affected in its default configuration. Bump the pin, and rest the L2 choice on the general rule | [GHSA-w9q4](https://github.com/ghostty-org/ghostty/security/advisories/GHSA-w9q4-5jvv-mcv4) |

## 13. Rules the design bends

| Rule | What happens | Why |
| --- | --- | --- |
| Byte-exact while attached | Kept. The supervisor passes output and input frames unchanged, and the client only observes escape sequences. An opt-in guarded attach would bend it ([question 26](../contributing/isolation-spec.md#15-open-questions)) | T4 against the laptop stays open by rule |
| Nothing new listens | No TCP or UDP port opens, loopback included. A forwarded port is a Unix socket per port. The supervisor dials into the guest, so no trusted process listens where a guest can connect. The guest shim listens on loopback inside the guest only. Decision [0030](../contributing/decisions/0030-local-sockets-do-not-listen.md) says that a Unix socket in a folder that only the user can reach, and a VM socket that only one sandbox's VMM can reach, are not listening on a host ([question 23](../contributing/isolation-spec.md#15-open-questions)) | vsock and Unix sockets carry every door without a TCP listener |
| No relay, no telemetry | Kept. Downloads of artefacts are fetches the user starts. Advisory checks run in project CI, or in an opt-in `doctor --online`. A sandbox's egress process runs on the user's own host, for one session, under the user's grants. Decision [0031](../contributing/decisions/0031-the-egress-process-is-not-a-relay.md) says that it is not a relay ([question 24](../contributing/isolation-spec.md#15-open-questions)) | A relay is a machine or service that the user does not own, or that the project runs |
| The tool stores no secrets | The tool still writes no value. But real keys live in the egress process's memory for the whole session, not only until the agent starts. The laptop arms them again after a host reboot. The per-session TLS key also lives in memory only. A session's disk keeps whatever the agent writes. No memory snapshot exists on this path. Decision [0032](../contributing/decisions/0032-secret-values-in-memory-only.md) records this ([question 25](../contributing/isolation-spec.md#15-open-questions)) | Sentinels need the real value on the trusted side |
| TLS stays end to end | The egress process opens TLS for hosts with a key binding, an HTTP rule or an identity pin, with a per-session CA in memory | Needed for the identity pin and for the model-API checks. Needs a decision record of its own and every maintainer's agreement ([question 16](../contributing/isolation-spec.md#15-open-questions)) |
| Agents are data | Kept. New agent fields: `Fork`, `DangerFlags`, session ID patterns, egress hosts, credential bindings, CA variables, project files | |
| Small and boring, one binary on each side | Bent. The host gains a VMM, runsc, a guest kernel and a tools image. The client stays one binary and embeds only digests | A wall needs a runtime. Licences: the GPL-2.0 kernel ships separately with its source |
| The session outlives the client | Kept while it runs. An idle stop or a host shutdown now ends the agent process and keeps the disk. The non-goal "no promise that a process survives a host reboot" grows to cover a stopped session | Stopped sessions use no RAM |
| One-time privileged setup only for power-off | Bent. Setup also adds the kvm group, an `AppArmor` userns profile, subuid ranges, module blocks and the io_uring setting ([question 29](../contributing/isolation-spec.md#15-open-questions)) | The wall and the per-component identities need them |
| The holder stays outside the wall (the L1 draft) | Changed by L2. Decision [0029](../contributing/decisions/0029-a-sandbox-per-session.md) says why: no memory-unsafe parser of hostile bytes runs on the trusted side | |
| Decision 0024, the memory cap | Each guest needs fixed RAM below its `MemoryMax`, so the cap cannot be every session's guest RAM at once ([question 13](../contributing/isolation-spec.md#15-open-questions)) | Needs a new record next to 0024 ([0024](../contributing/decisions/0024-memory-cap.md)) |
| Decision 0025, the debug mode | Kept. The recorder logs no command lines | |

## 14. Risks that remain

- **A poisoned package still runs.** The wall contains it and the doors limit it. A payload that runs at import or test time after the age window, an old typosquat or a slow attack such as xz-utils still runs with every open door until the session ends.
- **Misuse inside the rules.** Sentinels stop theft, not use. Code in a session can push a backdoor to its own agent branch, open a pull request or spend the model budget until a rule fires or the session ends.
- **Exfiltration through allowed channels.** Data in a GET URL to an allowed host, a push to a session branch, the model vendor itself, and domain fronting on an allowed CDN all stay possible. The audit log shows the bytes after the fact.
- **A backdoor in ordinary code** passes every wall by design. The review summary flags files that run code, but it cannot judge code, and people approve large diffs.
- **Escape sequences reach the laptop.** The client observes and warns, but by rule it does not change bytes.
- **Escapes from the wall.** A zero-day in the VMM, KVM or the host kernel still works. pins-watch shortens the window for known bugs only. On W2 and W1 hosts, a gVisor or kernel bug reaches the host.
- **The trusted side is new code.** The egress process parses hostile HTTP, TLS and DNS, and the supervisor parses guest frames. Go is memory-safe and each process is jailed, but a logic bug can leak one session's keys.
- **Host root reads every key** in the egress processes' memory while sessions run.
- **Credentials without a sentinel path** (OAuth refresh tokens, SSH signing, SigV4) stay outside version 1. Users who need them will choose an exposed secret, a recorded downgrade.
- **The project's own release key.** With one maintainer there is no two-person rule. A stolen hardware key or a coerced maintainer can still sign a bad helper. Upstream VMM bytes are trusted as of the day they are pinned.
- **Upkeep.** A guest kernel, a VMM, runsc and images must be patched on a schedule by one maintainer. Every driver adds a test matrix.
- **W4 and W0 hosts** have no trusted egress. Their sessions rely on the provider's controls, and the row says so.

## 15. What we could not verify

- The end-to-end start of a W3 session (VMM, kernel, init, fsck, holder, agent) on a real host. No one ran it.
- The speed of nested W3 on Lima with an M3, on EC2 and on GCE. The cost of W2 for `npm install` and git on large repositories.
- Whether the October 2026 Ghostty bugs are reachable with a 10,000-line scrollback.
- Whether gVisor's netstack under `--network=none` takes a dummy route and an nftables redirect, or W2 must use the explicit proxy only.
- Whether each agent honours `HTTPS_PROXY` for WebSockets and streaming, and trusts the CA bundle in every code path. Which header carries `CLAUDE_CODE_OAUTH_TOKEN`.
- Whether W1 or W2 runs inside an E2B guest, and whether rootless `runsc` supports tracing outside Docker and containerd.
- Start and memory figures for Cloud Hypervisor and libkrun, vsock latency, and the real memory cost per session. The 55 to 90 MiB, 10 to 20 MiB and 5 to 25 ms figures in this note are estimates.
- Kata start times and memory (third-party figures only), and Docker's VMM (a forum comment only).
- That ext4 has no reflink (inferred from the man page), and that `limactl clone` uses APFS copy-on-write.
- The npm version that added `min-release-age`, crates.io's publish times and Rust's matching setting, and the licences of osv-scalibr and hcsshim.
- How long an agent's HTTP client waits on a held flow before it fails, and how each agent copes with a dropped streaming request.
- The PwnEscape trigger path through guest networking.
- What the Amazon Q 1.84.0 prompt asked the agent to do, from a primary source.

## Open questions

The spec's [section 15](../contributing/isolation-spec.md#15-open-questions) is the only full list of open questions. Each one there has its options and a recommended default. The numbers below are the numbers there.

- **From the main search:** questions 2, 3, 6, 7, 8, 11, 12, 15, 16, 17, 18, 19, 20, 21, 22, 26, 29, 37 and 42.
- **From the hosts search:** questions 5, 13, 14, 23, 26, 27, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40 and 41.
- **Decided since:** the first draft of this note asked whether the supervisor should dial the guest. Then no host listener would be reachable from the guest. The spec decides that it does ([question 23](../contributing/isolation-spec.md#15-open-questions), decision [0030](../contributing/decisions/0030-local-sockets-do-not-listen.md)).

## Your own hosts

This section asks how a user turns a machine of their own into a sandbox host, and how `new` then opens a sandbox there on demand. The machines are ad hoc: a desktop, a homelab server, a spare laptop, a Mac, a cloud VM, a bare-metal server, or a teammate's or a company's machine. Docker Sandboxes and the hosted VM service are the references. The hosted VM service runs the hosts itself. Docker Sandboxes run on one laptop or in Docker's cloud. Here the user brings the hosts, and everything stays on them, with no hosted control plane.

The sandbox itself is the subject of the main search, in sections 1 to 15. The hosts search assumed its outcome:

- On a Linux host with KVM, each sandbox is a microVM that the host helper starts. The main search picks Cloud Hypervisor or Firecracker, with no libkrun by default ([question 3](../contributing/isolation-spec.md#15-open-questions)).
- Where KVM is missing, a lower level (`gvisor` or `process`) is used, and the user is told.
- On a Mac, each sandbox is a VM from Apple's Virtualization framework. This is `vz`, a later level ([question 37](../contributing/isolation-spec.md#15-open-questions)).
- The helper installs the runtime (the VMM, `runsc` and the boot pair) and the images on each host.

The words in this section are those of the whole note:

| Word | Meaning |
| --- | --- |
| Session | What `new` starts: one agent in one terminal, as today. |
| Sandbox | The isolated place where one session's agent and all its processes run. |
| Wall | What separates a sandbox from the host and from other sandboxes. |
| Level | How strong a wall is: `none`, `process`, `gvisor` or `microvm`. `vz` is a later level for Mac hosts. |
| Floor | The weakest level that a host may give a new session, pinned on the laptop in `hosts.toml`. |
| Tier | How far the user trusts a host: `own`, `throwaway` or `shared`. The hosts search drafts called this a "level". |
| Root step | The one optional script that needs root on a host. |
| Runtime | The artefacts that a host needs for walls: the VMM, `runsc` and the boot pair. |
| Release manifest | The one list of digests of every trusted artefact of a release. The client embeds it. |
| Grant | An explicit permission, made on the laptop, that opens a door for one target: a secret, the SSH agent, an alert token, a repository or a place in the auto pool. A host's tier caps the grants that its sandboxes may get. |

### Summary

1. **`host add` stays the only way in.** (A later change: `host join` adds a second way in, at the owner's request. See [spec 18.3](../contributing/isolation-spec.md#183-a-machine-that-joins-h20).) Of five ways to attach a machine, the best is today's path: SSH, consent, install, doctor. A new prepare phase follows it. A sandbox probe, a runtime checked against the release manifest, one optional root step and a test boot make the host a sandbox host. It needs no new port, relay, daemon or pairing service.
2. **Most Debian and Ubuntu hosts need root once.** Debian builds systemd so that `/dev/kvm` is mode 0660 for the `kvm` group. Upstream systemd uses 0666. The probe says which level the host gives now, and which level it gives after the root step.
3. **The root step stays small when the guest has no network card.** A guest that reaches the host only over vsock needs no TAP device, no network namespace and no user namespace. The root step then holds only `/dev/kvm` access, linger, and an optional cgroup delegation. It needs no `AppArmor` profile, which would weaken Ubuntu's user namespace restriction for the whole account. The main design adds a few more lines to the root step ([section 13](#13-rules-the-design-bends), [question 4](../contributing/isolation-spec.md#15-open-questions)).
4. **The laptop writes the root script, and sudo asks for the password.** The helper must not write a script that root runs. The client builds the script from fixed templates, shows each line with its reason and its undo, and runs it through `ssh -t … sudo`. The user can also print it for the machine's owner, or skip it.
5. **The weak point is the account.** On the chosen path, a VMM escape reaches the login user's account: its `~/.ssh`, its agent logins, the helper and every other sandbox. Landlock and seccomp make an escape harder, but they do not remove it. The main design also jails each VMM under a mapped subuid ([section 9.2](#92-the-wall), [question 28](../contributing/isolation-spec.md#15-open-questions)). A root-owned launcher with one slot account for each sandbox closes this gap, at the cost of a standing sudo rule. The spec makes that a second phase ([question 27](../contributing/isolation-spec.md#15-open-questions)).
6. **Placement ships in two steps.** First comes named placement: `new <host>`, a saved default, and a fit check on the host. Then comes `new auto`, which ranks the user's hosts from live facts. In both, the host admits or refuses each sandbox under a file lock, so two laptops cannot overfill one host.
7. **A fit check counts declared memory, not live use.** Kubernetes, Proxmox and the Firecracker balloon docs all count what a guest may use. Live pressure only ranks hosts.
8. **Tiers decide what leaves the laptop.** Each grant has a lowest tier that may get it. The laptop checks it. The host never learns its tier, so it cannot raise it. Tiers won under both placement options (0.715 and 0.732), so this result does not depend on the placement choice.
9. **Today's client forwards the SSH agent when the user's config says so.** The generated SSH config includes `~/.ssh/config` first, and the client passes no `ForwardAgent=no`. A fix can ship before any sandbox work.
10. **The host key pin goes on the `ssh` command line.** Options on the command line win over every config file. A pin in the generated config would lose to a `StrictHostKeyChecking no` in the user's own config.
11. **Images are the slow part.** A runtime is about 20 MB. An agent image is 0.34 GB to 1.06 GB for each architecture. Sent from a laptop on a 20 Mbit/s uplink, 1 GB takes about 7 minutes (computed). A host that downloads by digest takes seconds. Images come only from the signed release or from the laptop, never from another host ([question 35](../contributing/isolation-spec.md#15-open-questions)).
12. **Root on a host still sees everything.** No option here protects code, prompts or the model token from the host's own root (T6). Tiers limit what goes there. Only confidential VMs would do more, and they need TEE hardware.

### Method

The hosts search was a Monte Carlo tree search over three layers. The search called them H1, H2 and H3. The plan now uses H1 to H18 as task IDs, so this section uses the layers' names:

- **Attach:** how an ad-hoc machine becomes a sandbox host: how it joins, what is installed, which privileges it needs, its identity, and how it leaves.
- **Placement:** how `new` picks a host and opens a sandbox there, and how images and templates reach each host.
- **Fleet:** how the user's hosts stay safe and healthy over time: tiers, short-lived hosts, upgrades, health, hosts behind NAT, shared and team machines.

Each iteration did these steps:

1. Select a leaf by UCT with max backup, with c = 0.1. When the remaining iterations were just enough to reach the Fleet layer, selection was pure exploitation.
2. Expand the leaf into 5 options. Five agents researched one option each, in parallel.
3. Score each option by a rollout to the last layer, from two judges. Each judge named the later choices that it assumed. One judge scored security. The other scored fit with the code, convenience, scale and effort. Each score is from 0 to 10. A high effort score means less work.
4. Compute the value: v = (0.35 security + 0.15 fit + 0.20 convenience + 0.15 scale + 0.15 effort) / 10.
5. Back up: Q is the best value in a node's subtree, and N counts the rollouts through the node.

The search ran 4 iterations and scored 20 options:

| Iteration | Leaf expanded | New children, with their values |
| --- | --- | --- |
| 1 | The root, into the Attach layer | SSH add, then prepare 0.615. Rootless only 0.513. Machine-side join 0.585. Slot accounts and a root launcher 0.570. Providers and fleets 0.515 |
| 2 | SSH add, into the Placement layer | Named placement 0.682. Client placement with host admission 0.643. Layered templates 0.573. Warm pools 0.535. Move and fork across hosts 0.403 |
| 3 | Named placement, into the Fleet layer | Tiers with one grant gate 0.715. Loaned hosts 0.485. Upgrades, health and drain 0.620. Reach without a relay 0.500. Team fleets 0.418 |
| 4 | Client placement, into the Fleet layer | Tiers as a grant ceiling that only the client enforces 0.732. Leased hosts 0.470. Upgrades, health and drain 0.630. Reach ladder 0.620. Team hosts 0.495 |

Then one critic read the best path. As a red team, it listed the path's gaps by threat. As a fact check, it tested twelve load-bearing claims against primary sources. One writer wrote the draft. The hosts search used 30 agents in all: 20 researchers, 8 judges, the critic and the writer.

The results of the review are folded into the layers below, and listed in full under [Red team](#red-team-gaps-and-answers) and [Fact check](#fact-check-corrections). [Section 3.6](#36-weight-sensitivity) tests the weights for this search too.

The threats are those of the whole note, with one more for hosts:

| Threat | Meaning |
| --- | --- |
| T1 | A malicious dependency runs code inside a session. |
| T2 | Prompt injection makes the agent exfiltrate code or secrets. |
| T3 | An autonomous agent does destructive things. |
| T4 | A compromised session attacks other sessions, the host (with the account that owns its files), or the laptop. |
| T5 | Credentials are stolen, or misused. |
| T6 | A host that the user does not fully trust sees what runs on it, so what is sent to it must be limited. |

Rules for the research:

- Read-only. No account was made. No VM or container was started, and nothing was installed.
- Facts come from public docs, specifications, man pages and source code. Each fact has a link.
- A number marked *computed* is arithmetic, not a measurement. **UNVERIFIED** marks a claim that no primary source confirmed.
- Measurements of the project's own code come from the repository: the helper sizes in `internal/client/helpers/`, and the `host add` times in [testing](../contributing/testing.md#time-to-first-agent).

Limits of the method:

- Four iterations is a small search. Four options at the Attach layer and three at the Placement layer were scored once and never expanded.
- Scores are judgements by models, not measurements. They rank options. They do not prove them.
- Two Attach options beat the chosen one on security alone: the machine-side join (7) and the slot accounts (6). They lost on fit and convenience. The red team's main gap points at the same weakness, so the spec takes the slot-account launcher as a second phase ([question 27](../contributing/isolation-spec.md#15-open-questions)).

### The tree

The columns are as in [the main tree](#34-the-tree).

| Node | Parent | Layer | Option | S | F | C | Sc | E | v | Q | N |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | root | Attach | **SSH add, then prepare:** the runtime as the login user, with one shown, reversible root step | 4 | 8 | 8 | 7 | 6 | 0.615 | 0.732 | 16 |
| 2 | root | Attach | Rootless only: a per-user runtime under Landlock, with no guest network card | 3.5 | 7 | 6 | 6 | 5 | 0.513 | 0.513 | 1 |
| 3 | root | Attach | Machine-side join: a pinned one-line command makes a dedicated account and pairs the machine | 7 | 4 | 5 | 7 | 5 | 0.585 | 0.585 | 1 |
| 4 | root | Attach | Slot accounts and a small root-owned launcher behind one sudo rule, with no daemon | 6 | 5 | 6 | 7 | 4 | 0.570 | 0.570 | 1 |
| 5 | root | Attach | Provider-made hosts and adopted fleets behind one lifecycle seam | 5 | 5 | 5 | 9 | 2 | 0.515 | 0.515 | 1 |
| 6 | 1 | Placement | Named placement: `new <host>` or a saved default, a fit check on the host, no scheduler | 6 | 9 | 7.5 | 5 | 7.5 | 0.682 | 0.715 | 6 |
| 7 | 1 | Placement | **Client-side placement with host-side admission:** `new auto` ranks hosts from live facts | 5 | 7 | 8 | 8 | 5.5 | 0.643 | 0.732 | 6 |
| 8 | 1 | Placement | Content-addressed layered templates: signed manifests over read-only EROFS layers | 6.5 | 5.5 | 6 | 6 | 3.5 | 0.573 | 0.573 | 1 |
| 9 | 1 | Placement | Warm pools: single-use warm sandboxes on each host, claimed by rename | 4.5 | 6 | 6.5 | 6.5 | 4 | 0.535 | 0.535 | 1 |
| 10 | 1 | Placement | Sessions follow the work: logical checkpoints for move and fork, and `new --near` | 3.5 | 4.5 | 5 | 5 | 2.5 | 0.403 | 0.403 | 1 |
| 11 | 6 | Fleet | Tiers (own, shared, throwaway) behind one grant gate on the laptop | 7 | 9 | 7 | 5 | 8 | 0.715 | 0.715 | 1 |
| 12 | 6 | Fleet | Loaned hosts: a set time, work saved as git bundles, then a wipe | 6 | 5 | 4 | 3 | 5 | 0.485 | 0.485 | 1 |
| 13 | 6 | Fleet | Upgrades, health and drain: prove each runtime before use, restart exposed sandboxes when idle | 5 | 8 | 8 | 7 | 4 | 0.620 | 0.620 | 1 |
| 14 | 6 | Fleet | Reach without a relay: SSH paths the user owns, with the host key pinned end to end | 4 | 5 | 6 | 5 | 6 | 0.500 | 0.500 | 1 |
| 15 | 6 | Fleet | Team fleets: one account per person, kernel quotas, a signed policy in git | 5.5 | 3 | 3 | 6 | 2 | 0.418 | 0.418 | 1 |
| 16 | 7 | Fleet | **Host tiers (own, throwaway, shared)** as a grant ceiling that only the client enforces | 7 | 8.5 | 7.5 | 6.5 | 7.5 | 0.732 | 0.732 | 1 |
| 17 | 7 | Fleet | Leased hosts: a time-boxed join that ends by itself, with a footprint ledger | 5.5 | 4.5 | 4.5 | 4 | 4 | 0.470 | 0.470 | 1 |
| 18 | 7 | Fleet | Upgrades, health and drain: versioned runtime upgrades on connect, a cheap health report | 4 | 8 | 8 | 8.5 | 5.5 | 0.630 | 0.630 | 1 |
| 19 | 7 | Fleet | Reach ladder with no project relay: direct, ProxyJump, the user's overlay, then a reverse tunnel | 5 | 7 | 6.5 | 6.5 | 7.5 | 0.620 | 0.620 | 1 |
| 20 | 7 | Fleet | Team hosts: one account per person, ceilings set by root, a host policy file, a journal audit | 6 | 4 | 4.5 | 6 | 3 | 0.495 | 0.495 | 1 |

The best path is 1, then 7, then 16, with Q 0.732. Node 6 has a higher value of its own (0.682) than node 7 (0.643). But node 7 leads to the best leaf. Node 7 keeps `new <host>` unchanged, so it builds on node 6. The spec ships node 6 first and node 7 on top of it.

Two patterns hold across both Fleet expansions:

- Tiers ranked first under both parents (0.715 and 0.732). Upgrades and health ranked second (0.620 and 0.630).
- Team fleets (0.418 and 0.495) and timed leases (0.485 and 0.470) ranked last under both parents.

### Attach

#### The options

| Node | Option | v | What it gives | What it costs |
| --- | --- | --- | --- | --- |
| 1 | SSH add, then prepare | 0.615 | Today's door and code. Root is optional, shown and undoable. A host without root still works, at a lower level that the client names. | Every VMM runs as the login user. |
| 3 | Machine-side join | 0.585 | A dedicated account with no sudo, a key that expires, a forced command, and a pairing proof with no trust on first use. | A second way in, sudo on every machine, two pastes, and a one-time code that shows in `ps`. |
| 4 | Slot accounts and a root launcher | 0.570 | A VMM escape lands in a jailed slot account with no home. Images are root-owned. | A standing NOPASSWD rule and new root code. Machines without admin rights cannot use it at all. |
| 5 | Provider-made hosts and fleets | 0.515 | Scale: Lima, GCE, EC2, Proxmox, Incus and Kubernetes behind one seam. | It does not answer the ad-hoc machine. Many adapters drift with their APIs. |
| 2 | Rootless only | 0.513 | Nothing changes outside one folder. | The root step moves to the owner. It does not go away. |

#### Why the SSH path won

- It keeps the one door that exists today: the system OpenSSH, with jump hosts and Tailscale. It needs no new port, relay, control plane or pairing service.
- It reuses `connectHost`, `upload()` with `sha256sum -c`, the versioned folders, the signed manifest of [decision 0017](../contributing/decisions/0017-release-signatures.md), the consent prompts, `host doctor --fix`, and the sessions' slice with its memory caps.
- Root is optional, needed once, and shown line by line with a reason and an undo. Lima and rootless Docker are precedents.
- The password goes to sudo through an SSH terminal, so the client never sees it.
- A host without root still works, at a level that the client names. That is what "add any machine" needs.
- It is the cheapest full path: about 4 to 6 weeks for Linux.

Its security score is 4 of 10. The sandboxes, the egress process and the runtime all run as the login user who owns the host's files.

#### Evidence

**Access to `/dev/kvm`**

- Firecracker needs read and write access to `/dev/kvm`. Its guide grants it with `sudo setfacl -m u:${USER}:rw /dev/kvm`, or with `sudo usermod -aG kvm ${USER}`, or by running Firecracker as root ([getting started](https://github.com/firecracker-microvm/firecracker/blob/main/docs/getting-started.md)).
- Upstream systemd's build option `dev-kvm-mode` defaults to 0666 ([meson_options.txt](https://github.com/systemd/systemd/blob/main/meson_options.txt)). Its udev rule is `KERNEL=="kvm", GROUP="kvm", MODE="{{DEV_KVM_MODE}}", OPTIONS+="static_node=kvm"`, with no `uaccess` tag ([50-udev-default.rules.in](https://github.com/systemd/systemd/blob/main/rules.d/50-udev-default.rules.in)).
- Debian builds systemd with `-Ddev-kvm-mode=0660` ([debian/rules](https://salsa.debian.org/systemd-team/systemd/-/blob/debian/master/debian/rules)). A user outside the `kvm` group cannot open `/dev/kvm` there. That Ubuntu does the same is **UNVERIFIED**.
- A lingering systemd user manager keeps the groups it started with. `usermod -aG` takes effect only after the user manager restarts, and the documented fix, `loginctl terminate-user`, ends all of the user's processes ([podman pull request 29285](https://github.com/podman-container-tools/podman/pull/29285)). So the root step grants an ACL for now and the group for later boots.
- That an ACL on `/dev/kvm` lasts until the next reboot, and no longer, comes from forum reports only. It follows from `/dev` being made again at each boot. **UNVERIFIED** by a test.

**Running a VMM without root**

- The Firecracker jailer runs as root. It uses `unshare`, `pivot_root`, `chroot`, `mknod` and `chown`, then drops to a uid and gid. It treats all its inputs as trusted, including `--exec-file`, the chroot base and `--netns` ([jailer](https://github.com/firecracker-microvm/firecracker/blob/main/docs/jailer.md)). A sudo rule that lets a user run it with free arguments gives that user root.
- A guest network card on Firecracker needs a host TAP device, made with `sudo ip tuntap add`, and NAT rules made with `sudo iptables-nft` ([network setup](https://github.com/firecracker-microvm/firecracker/blob/main/docs/network-setup.md)).
- Firecracker's vsock maps guest ports to Unix sockets on the host (`uds_path` and `uds_path_PORT`). It needs no TAP and no root ([vsock](https://github.com/firecracker-microvm/firecracker/blob/main/docs/vsock.md)). Cloud Hypervisor's vsock is the same hybrid design ([vsock](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/vsock.md)).
- Cloud Hypervisor's `--landlock` limits the VMM to the files it declares. It covers mostly files, is applied at VM creation, needs `--landlock-rules` for hot-plug paths, and needs Landlock in the host kernel ([landlock](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/landlock.md)). It gives no uid drop, chroot, namespaces or cgroups. So it is only a partial stand-in for the jailer.
- Cloud Hypervisor's README gives the binary `cap_net_admin` with `sudo setcap` for TAP networking ([README](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/README.md)).
- That Cloud Hypervisor runs with a user-mode network stack on a Unix socket is **UNVERIFIED**. One issue reports a VMM panic with `passt`'s vhost-user and two or more vCPUs on v51 ([issue 7766](https://github.com/cloud-hypervisor/cloud-hypervisor/issues/7766)).
- libkrun's TSI proxies guest `AF_INET`, `AF_INET6` and `AF_UNIX` sockets in the VMM's host network context ([security model](https://github.com/containers/libkrun/blob/main/docs/security-model.md)). A guest with TSI reaches what the host account reaches.
- Landlock lets unprivileged processes restrict themselves. ABI 4 adds TCP bind and connect rules by port, and ABI 6 scopes signals and abstract Unix sockets ([Landlock](https://docs.kernel.org/userspace-api/landlock.html)).
- Cloud Hypervisor's minimum host kernel is 6.8 ([repository](https://github.com/cloud-hypervisor/cloud-hypervisor)). Firecracker supports host kernels 5.10, 6.1 and 6.18 ([kernel policy](https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md)). Debian 13's 6.12 and Ubuntu 24.04's 6.8 are not on Firecracker's list. The main search picks the VMM ([question 3](../contributing/isolation-spec.md#15-open-questions)). The probe only reports the kernel.

**User namespaces and gVisor**

- Since Ubuntu 23.10, an unprivileged process can make a user namespace only if an `AppArmor` profile with a `userns` rule confines it. It is on by default from 24.04 ([Ubuntu](https://ubuntu.com/blog/ubuntu-23-10-restricted-unprivileged-user-namespaces)).
- Ubuntu warns that unconfined profiles with the `userns` rule give a trivial bypass to any unconfined process that runs the profiled program ([Ubuntu discourse](https://discourse.ubuntu.com/t/understanding-apparmor-user-namespace-restriction/58007)). Docker's example profile names `$HOME/bin/rootlesskit`, a path that the user can write ([rootless troubleshooting](https://docs.docker.com/engine/security/rootless/troubleshoot/)).
- `runsc --rootless` has no Netstack, so the sandbox uses the host's network. It maps only the caller's uid. More uids, or a network namespace, need `newuidmap`, which is setuid ([gVisor rootless](https://gvisor.dev/docs/user_guide/rootless/)).

**cgroups**

- `user@.service` has `Delegate=pids memory cpu` and `DelegateSubgroup=init.scope` ([user@.service.in](https://github.com/systemd/systemd/blob/main/units/user%40.service.in)). The `io` and `cpuset` controllers need a drop-in, which needs root. A drop-in for one user goes in `user@<uid>.service.d`. A drop-in in `user@.service.d` changes every user on the machine.
- The user manager reads its controllers only when it starts. `systemctl --user daemon-reexec` counts as a start, needs no root, and keeps running units ([build facts](build-facts.md#4-linux-walls)). UNVERIFIED: that `daemon-reload` gives the running unit the `io` controller.

**Disks**

- `FICLONE` (Linux 4.5) makes reflink copies on Btrfs and XFS ([ioctl_ficlone](https://man7.org/linux/man-pages/man2/ioctl_ficlone.2.html)). ext4 has no `remap_file_range`, so `FICLONE` fails there ([fs/ext4/file.c](https://github.com/torvalds/linux/blob/master/fs/ext4/file.c)). XFS supports reflink only when the file system was made with it. On ext4, each new sandbox disk is a full copy. The 2 s to 10 s for a 2 GB to 4 GB copy on NVMe is an estimate, not a measurement.
- `mke2fs -d` fills a new file system from a folder or a tarball, without root ([`mke2fs`](https://man7.org/linux/man-pages/man8/mke2fs.8.html)). Firecracker's guide builds its root file system with `sudo mkfs.ext4` ([getting started](https://github.com/firecracker-microvm/firecracker/blob/main/docs/getting-started.md)). Prebuilt images avoid root on the host.

**Precedents for a root step**

- `limactl sudoers` writes a narrow sudoers file that the user reviews and installs with sudo. Lima refuses a `socket_vmnet` binary that root does not own ([Lima vmnet](https://lima-vm.io/docs/config/network/vmnet/)).
- `dockerd-rootless-setuptool.sh` shows help when a prerequisite is missing. Rootless Docker uses no setuid binary except `newuidmap` and `newgidmap` ([rootless Docker](https://docs.docker.com/engine/security/rootless/)).
- DevPod's SSH provider needs the remote user to be root or in the `docker` group ([devpod-provider-ssh](https://github.com/loft-sh/devpod-provider-ssh/blob/main/README.md)).
- The k3s installer leaves an uninstall script on each agent node ([k3s uninstall](https://docs.k3s.io/installation/uninstall)).
- `authorized_keys` options: `restrict` turns off forwarding, PTY allocation and `~/.ssh/rc`. `command=` forces a command. `expiry-time` stops the key after a date ([sshd(8)](https://man.openbsd.org/sshd.8)). OpenSSH added `restrict` in 7.2 and `expiry-time` in 7.7 ([release notes](https://www.openssh.com/releasenotes.html)).
- `sudo` sets `USER` to the target user ([sudoers](https://www.sudo.ws/docs/man/sudoers.man/)). A root script that writes `$USER` names root, not the login user.

**Cloud VMs and nested KVM**

- EC2 supports nested virtualisation on virtual instances of the M7i, M7i-flex, M8i, M8id, M8i-flex, C7i, C7i-flex, C8i, C8id, C8i-flex, R7i, `R7iz`, R8i, R8id, R8i-flex, X8i, I7i and I7ie families. No Graviton type is listed ([AWS](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/amazon-ec2-nested-virtualization.html)).
- GCE nested virtualisation does not work on E2, memory-optimised, Arm or most AMD VMs. CPU-bound work may lose 10 % or more ([GCE](https://docs.cloud.google.com/compute/docs/instances/nested-virtualization/overview)).
- Hetzner Cloud servers cannot run nested virtualisation ([Hetzner FAQ](https://docs.hetzner.com/cloud/servers/faq/)).
- From v1.18, Firecracker can launch Nitro Sandboxes through `/dev/nitro_enclaves` on EC2 types that support Nitro Enclaves, on x86_64 and Graviton, with no `/dev/kvm` ([Nitro Sandboxes](https://github.com/firecracker-microvm/firecracker/blob/main/docs/nitro-sandboxes/README.md)). AWS launched it on 9 November 2026, and it gives the `microvm` level through a `nitro` driver ([section 10.1](#101-aws-nitro-sandboxes-on-ec2)). The probe reports the device.

**Mac hosts**

- A process that uses Apple's Virtualization framework needs the `com.apple.security.virtualization` entitlement ([Apple](https://developer.apple.com/documentation/bundleresources/entitlements/com.apple.security.virtualization)). Lima signs `limactl` with it and runs VZ VMs without root ([vz.entitlements](https://github.com/lima-vm/lima/blob/master/vz.entitlements)).
- From macOS 15, the Virtualization framework needs an unlocked login keychain when it runs a VM ([Tart FAQ](https://tart.run/faq/)). The check of 10 November 2026 found that this applies to macOS guests. Lima's contributors report that a Linux VM starts from a boot launch daemon with nobody logged in ([locations, section 5](locations.md#5-mac-hosts-checked-again)).
- A daemon's start for an account that has never logged in is still **UNVERIFIED**. The locations note gives a test by hand.

**Sizes**

| Item | Size | Source |
| --- | --- | --- |
| The helper today | 9.3 MB (amd64), 8.7 MB (arm64) | `internal/client/helpers/`, a local build |
| `host add` today, over a jump host on Wi-Fi | 1.8 s to 2.2 s | [testing](../contributing/testing.md#time-to-first-agent) |
| Firecracker v1.18.0 release archive | 7.6 MB (x86_64), 7.4 MB (aarch64) | [release](https://github.com/firecracker-microvm/firecracker/releases/tag/v1.18.0) |
| Cloud Hypervisor v54.0 static binary | 7.4 MB (x86_64), 5.8 MB (aarch64) | [release](https://github.com/cloud-hypervisor/cloud-hypervisor/releases/tag/v54.0) |
| Firecracker CI guest kernel 6.1.141 | 41.9 MB | [CI artefacts](https://s3.amazonaws.com/spec.ccfc.min?list-type=2&prefix=firecracker-ci/v1.13/x86_64/) |
| Ubuntu 24.04 minimal image | 132 MB squashfs (amd64), 95 MB (arm64) | [cloud images](https://cloud-images.ubuntu.com/minimal/releases/noble/release/) |
| Docker's agent sandbox templates | 342 MB to 1.06 GB compressed, each architecture | [Docker Hub](https://hub.docker.com/r/docker/sandbox-templates/tags) |
| gVisor release-20261102.0 | 58.8 MB (x86_64, zstd) | [release](https://github.com/google/gvisor/releases/tag/release-20261102.0) |

**Licences**

- Firecracker and libkrun are Apache-2.0 ([Firecracker release](https://github.com/firecracker-microvm/firecracker/releases/tag/v1.18.0), [libkrun](https://github.com/containers/libkrun/blob/main/README.md)).
- Cloud Hypervisor ships Apache-2.0 and BSD-3-Clause licence texts ([release](https://github.com/cloud-hypervisor/cloud-hypervisor/releases/tag/v54.0)). One research agent saw only the BSD-3-Clause text, so the exact split per file is **UNVERIFIED**.
- Libkrunfw bundles a GPL-2.0-only kernel in an LGPL-2.1 library. A binary release must come with the kernel's source ([libkrunfw](https://github.com/containers/libkrunfw/blob/main/README.md)). Any guest kernel that the project ships brings the same duty.
- `passt` is GPL-2.0-or-later ([`passt`](https://passt.top/builds/latest/web/passt.1.html)). gvisor-tap-vsock is Apache-2.0.

#### What the red team changed

The red team found that node 1, as researched, had six faults. Each one changes the design:

1. **The helper wrote the root script.** A compromised helper could then write what root runs. The client now builds the script from fixed templates. The probe chooses lines through enums and booleans only, never through strings.
2. **The script used `$USER`.** Under sudo, that names root. The client now writes the login name, after a strict name check.
3. **The script sat in a folder the user can write,** between its check and `sudo sh <file>`. The client now sends it inline: `ssh -t <host> /usr/bin/sudo /bin/sh -c '<script>'`. No file exists on the host before root runs it.
4. **Root step (b) wrote an `AppArmor` `userns` profile.** It weakens Ubuntu's restriction for every process that runs the program. The main design now gives the guest no network card by default, with egress over vsock. So no user namespace is needed, and step (b) is gone.
5. **The `user@.service` drop-in changed every user on the machine.** It is now scoped to `user@<uid>.service.d`.
6. **A compromised account could capture the sudo password** through its shell start files. "Run" is now offered by default only at the first add, before any sandbox has run there. Later, "print" is the default, so the machine's owner runs the script from a root console.

The main gap stays open on this path: a VMM escape lands in the login user's account. The spec says so plainly, and offers the slot-account launcher of node 4 as a second phase ([question 27](../contributing/isolation-spec.md#15-open-questions)).

#### Ideas taken from the other Attach options

| From | Idea | Where it goes |
| --- | --- | --- |
| 2 Rootless | No guest network card by default, with egress over vsock to an egress process on the trusted side | The main design ([section 7.5](#75-the-winner-nic-less-wall-egress-proxy-sentinel-keys-node-26)), and the smaller root step here |
| 2 Rootless | A "mini jailer": the helper sets `no_new_privs` and Landlock, then starts the VMM | The main design ([section 9.2](#92-the-wall)) |
| 3 Join | An `authorized_keys` line with `restrict` and `expiry-time` for a borrowed machine | A docs recipe for `shared` hosts ([question 31](../contributing/isolation-spec.md#15-open-questions)) |
| 3 Join | A leave script on the machine that its owner can run without the client | The root-owned `undo.sh` |
| 4 Slot accounts | A root-owned launcher, one sudo line, a slot uid for each sandbox and a root-owned image store | A second phase ([question 27](../contributing/isolation-spec.md#15-open-questions)) |
| 4 Slot accounts | Deny the LAN, the tailnet and cloud metadata to sandboxes by default | The main design ([section 7.5](#75-the-winner-nic-less-wall-egress-proxy-sentinel-keys-node-26)) |
| 5 Providers | Pin a made host's key out of band, from GCE guest attributes or the cloud-init console | Later, with each provider |
| 5 Providers | Refuse a machine type that cannot run nested KVM when the host is made, not later | Later, with each provider |

### Placement

#### The options

| Node | Option | v | What it gives | What it costs |
| --- | --- | --- | --- | --- |
| 6 | Named placement | 0.682 | The smallest change. The user names the host, or saves a default. The host checks the fit. | The user spreads work by eye. Nothing fails over. |
| 7 | Client-side placement with host-side admission | 0.643 | `new auto` picks a host from live facts, explains why, and retries when a host refuses. | More policy keys. Auto contacts hosts that the user did not name. Hosts report their own facts. |
| 8 | Layered templates | 0.573 | Signed, content-addressed layers. No copy for each sandbox. | It does not choose a host. Many moving parts. |
| 9 | Warm pools | 0.535 | Saves the VM boot, about 1 s to 3 s (**UNVERIFIED**). | A state machine on every host, and hot VMs that hold memory. |
| 10 | Sessions follow the work | 0.403 | Move and fork between hosts. | Live moves need identical hosts. The laptop pipes every byte. |

#### Why two steps

Node 7 keeps `new <host>` unchanged and adds `auto` beside it. So named placement is the first step of node 7, not a rival. Named placement also scored higher on its own (0.682), because it fits today's code and keeps the user in charge. Auto placement adds the most for a user with several hosts, and its judges asked for a dry run that explains every refusal.

#### Evidence

**Named defaults elsewhere**

- Docker has `docker context use` and `--context`. `DOCKER_CONTEXT` overrides the saved default ([Docker CLI](https://docs.docker.com/reference/cli/docker/)).
- Podman has `podman system connection default <name>`, and `CONTAINER_CONNECTION` overrides it ([Podman](https://docs.podman.io/en/latest/markdown/podman-system-connection-default.1.html)).
- Incus has `incus remote switch <remote>` ([Incus remotes](https://linuxcontainers.org/incus/docs/main/remotes/)). DevPod has `devpod provider use <name>` ([DevPod](https://devpod.sh/docs/managing-providers/add-provider)).
- The hosted VM service's `current` means the last sandbox made or forked in this terminal, and its quickstart shows no choice of host or region.

**How schedulers choose**

- Nix remote builders: "If there are multiple machines of the right type, Nix prefers the fastest, taking load into account". A machine with mandatory features is used only when a build asks for all of them ([nix.conf](https://nix.dev/manual/nix/latest/command-ref/conf-file.html)).
- The Nix build hook is a client-side scheduler. It counts load with local slot lock files and cannot see load from other clients ([build-remote.cc](https://raw.githubusercontent.com/NixOS/nix/master/src/nix/build-remote/build-remote.cc)).
- Nomad filters, then ranks. Its schedulers run "without locking or reservations", and the leader's plan queue rejects plans that conflict ([Nomad](https://developer.hashicorp.com/nomad/docs/concepts/scheduling/how-scheduling-works)). Its algorithm is `binpack` or `spread` ([scheduler API](https://developer.hashicorp.com/nomad/api-docs/operator/scheduler)). `nomad alloc status -verbose` shows why each node lost ([alloc status](https://developer.hashicorp.com/nomad/commands/alloc/status)). Nomad is under the Business Source License 1.1, so the project may take its ideas but no code ([LICENSE](https://github.com/hashicorp/nomad/blob/main/LICENSE)).
- An Incus cluster puts an instance on the member with the fewest instances, and breaks ties at random ([Incus clustering](https://linuxcontainers.org/incus/docs/main/explanation/clustering/)).
- Kube-scheduler checks the sum of requests against capacity, "although actual memory or CPU resource usage on nodes is very low" ([Kubernetes](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/)).
- The Proxmox static-load scheduler scores nodes by the configured CPU and memory of guests, and weights memory above CPU, because memory is "a truly limited resource" ([Proxmox HA](https://pve.proxmox.com/wiki/High_Availability)).
- E2B samples K ready nodes, picks the least busy, and retries on a full node ([E2B architecture](https://github.com/e2b-dev/infra/blob/b74dbb6b807f92e32a11d0aa989f6387c7f6e3f3/docs/ARCHITECTURE.md)). In a simulated burst of 500 starts on 10 nodes, stale metrics sent 120 sandboxes to one node and 2 to another. Shadow state for in-flight starts brought the imbalance down from a CV of 0.941 to 0.021 ([E2B pull request 2201](https://github.com/e2b-dev/runtime/pull/2201)). Whether it merged was not checked.

**Memory and pressure**

- The Firecracker balloon driver in the guest is untrusted. The host "should always ensure the host is prepared to handle a situation in which the Firecracker process uses all of the memory it was given at boot" ([ballooning](https://github.com/firecracker-microvm/firecracker/blob/main/docs/ballooning.md)). So admission counts declared memory.
- Firecracker adds at most 5 MiB of VMM memory for 1 vCPU and 128 MiB, and reaches the guest's `/sbin/init` in 125 ms or less ([specification](https://github.com/firecracker-microvm/firecracker/blob/main/SPECIFICATION.md)).
- `MemAvailable` is "an estimate of how much memory is available for starting new applications, without swapping" ([proc](https://docs.kernel.org/filesystems/proc.html)). `/proc/pressure/{cpu,memory,io}` give `some` and `full` stall shares as `avg10`, `avg60` and `avg300` ([PSI](https://docs.kernel.org/accounting/psi.html)).

**Reach and wake**

- GNU parallel's `--filter-hosts` drops hosts where SSH login fails, at the start only ([GNU parallel](https://www.gnu.org/software/parallel/parallel.html)).
- Tailscale's peer status has `Online`, `LastSeen` and `Relay` ([ipnstate.go](https://github.com/tailscale/tailscale/blob/main/ipn/ipnstate/ipnstate.go)). It can tell that a tailnet host is away without a dial.
- E2B resumes a paused sandbox in about 1 second ([E2B persistence](https://docs.e2b.dev/sandbox/persistence)).

**GPUs**

- Cloud Hypervisor passes devices through with VFIO, after the device is bound to `vfio_pci`, which needs root ([VFIO](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/vfio.md)). Firecracker's PCIe work will not add VFIO passthrough ([issue 5133](https://github.com/firecracker-microvm/firecracker/issues/5133)). GPU placement is out of this section.

**Today's code**

- `new` needs a host (`cobra.MinimumNArgs(1)` in `internal/cli/session.go`). The launcher's `newSessionHost` (`internal/tui/actions.go`) uses the selected host, or the only host, and otherwise shows "Choose a host, then press n."
- The client's SSH uses `ControlPersist=600` and `ConnectTimeout 15` (`internal/client/ssh.go`). The launcher pings every 2 s and updates its round trips every 10 s ([UX spec](../contributing/ux-spec.md#23-hosts)). `onLatencyTick` (`internal/tui/hosts_conn.go`) does no I/O, so facts need a new periodic request.
- The launcher dials with `WithoutWake`, so it never starts a stopped machine by itself (`internal/client/machine.go`).
- `lockSession` (`internal/host/lock.go`) shows the pattern for a host-wide lock: a file under `flock`, shared by every bridge.

#### Images and templates

The best path had no answer for how images reach a host. Nodes 8, 9 and 10 give the evidence.

- Incus names each image by a SHA-256 fingerprint. A request by fingerprint gets an exact version. A request through an alias follows updates ([Incus images](https://linuxcontainers.org/incus/docs/main/image-handling/)).
- An OCI descriptor's digest is a content identifier. Content from an untrusted source should be checked against the digest, and its size checked first ([OCI descriptor](https://github.com/opencontainers/image-spec/blob/main/descriptor.md)).
- `nix copy --to ssh://` with `--substitute-on-destination` lets the remote fetch missing paths itself. Paths must be signed by trusted keys ([nix copy](https://nix.dev/manual/nix/2.28/command-ref/new-cli/nix3-copy)).
- E2B keeps the template root file system read-only, with copy-on-write for each sandbox, and caches templates on each node with pins that block eviction ([E2B runtime](https://github.com/e2b-dev/runtime/blob/main/docs/ARCHITECTURE.md)).
- Docker Sandboxes has `kit.requireSignature` and `kit.trustedSigners`, and a kit set records image digests ([kits](https://docs.docker.com/ai/sandboxes/customize/kits-v2/)).
- A Firecracker snapshot does not restore across CPU models, and restoring one snapshot more than once is insecure, because random seeds and identifiers repeat ([snapshot support](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/snapshot-support.md)). Snapshots never leave their host. Only images travel.
- OpenSSH's default session channel window is 2 MiB ([channels.h](https://github.com/openssh/openssh-portable/blob/master/channels.h)). One channel's throughput is capped near window / RTT (computed).
- `zstd --patch-from` makes binary patches against reference files up to 2 GB ([zstd](https://github.com/facebook/zstd/wiki/Zstandard-as-a-patching-engine)).
- Apple's Virtualization framework save files are hardware encrypted. No other Mac or user account can restore them ([WWDC 2023](https://developer.apple.com/videos/play/wwdc2023/10007/)).

What the spec takes:

- **First version:** whole images, compressed with zstd, named by SHA-256, and cached on each host by digest. The release manifest lists the project's images, and a lock on the laptop lists the user's own ([section 8.5](#85-the-winner-laptop-held-digests-node-34)). The host downloads by digest when it can, or the laptop copies them ([question 39](../contributing/isolation-spec.md#15-open-questions)).
- **Later:** layered EROFS images (node 8), patches with `zstd --patch-from`, and warm pools of single-use sandboxes (node 9).
- **Not planned:** live moves between hosts (node 10). Firecracker needs identical hosts, Apple's save files stay on one Mac, and libkrun has no snapshot API ([Lima issue](https://github.com/lima-vm/lima/issues/5533), **UNVERIFIED** from libkrun's own docs).

#### What the red team changed

- **Facts are untrusted input.** After a VMM escape, a helper can claim room, cached images and the `microvm` level, and pull secret-bearing sessions onto itself. The client now compares each host's facts with the values it recorded at prepare time: total memory, architecture and level. A host that disagrees leaves the auto pool. A host that took most of this laptop's recent auto placements ranks lower while another host fits.
- **Templates come only from the laptop or the signed release.** A template built or copied on a host is only as clean as that host. The client records each digest at build time, and never accepts a digest that a host reports. No host gets a key to another host ([question 35](../contributing/isolation-spec.md#15-open-questions)).

### Fleet

#### The options

| Node | Option | v | What it gives | What it costs |
| --- | --- | --- | --- | --- |
| 16 | Host tiers as a grant ceiling | 0.732 | One ordered scale and one table. The host cannot see or raise its tier. It closes today's agent forwarding gap. | The user sets the label. Plain `ssh` and old clients bypass it. |
| 18 | Upgrades, health and drain | 0.630 | Versioned runtime upgrades on connect, a health report in each facts reply, and a drain mark on the host. | Health is self-reported. Hosts nobody opens stay old. |
| 19 | Reach ladder | 0.620 | Direct, ProxyJump, the user's overlay, then a reverse tunnel to a machine the user owns. | A host behind NAT still needs someone at the machine once. |
| 20 | Team hosts | 0.495 | One Unix account per person, with kernel limits and an audit trail. | Reverses today's non-goals. Large. |
| 17 | Leased hosts | 0.470 | A join that ends by itself, with a wipe. | The wipe is best effort on SSDs. Bends "the session outlives the client". |

#### Why tiers won

- It is a small change on the client. `client.Host` already holds a list of secrets for each host, so a tier and one check fit beside it.
- The host never learns its tier, so a compromised or nosy host cannot raise it. That is the core of T6.
- Proven systems use the same pattern. GitHub withholds secrets from fork builds, GitLab has protected runners, and the Kubernetes node authorizer limits a node to its own pods' secrets.
- It closes a real gap: today, a `ForwardAgent yes` in the user's SSH config reaches every host.
- It costs about 2 weeks.

#### Evidence for tiers

- `ssh` takes command-line options first, then the user's config, then the system file. The first value wins ([ssh_config](https://man.openbsd.org/ssh_config)).
- `ssh_config(5)` warns that users who can bypass file permissions on the remote host can use a forwarded agent ([ssh_config](https://man.openbsd.org/ssh_config)). OpenSSH 8.9 added destination constraints for agent keys. An attacker on an allowed host can still use the agent ([agent restrictions](https://www.openssh.com/agent-restrict.html)).
- `ClearAllForwardings` clears every local, remote and dynamic forward ([ssh_config](https://man.openbsd.org/ssh_config)).
- The generated wrapper (`writeWrapper` in `internal/client/ssh.go`) includes `~/.ssh/config` before its own `Host *` block. No file in `internal/` sets `ForwardAgent=no`. So the [security model](../explanation/security.md)'s line "SSH agent forwarding is off by default" holds only when the user's config agrees.
- GitHub passes no secrets except `GITHUB_TOKEN` to a workflow that a fork triggers ([GitHub secrets](https://docs.github.com/en/actions/security-for-github-actions/security-guides/using-secrets-in-github-actions)). GitHub warns that untrusted code can compromise a self-hosted runner for good ([secure use](https://docs.github.com/en/actions/reference/security/secure-use)).
- A GitLab protected runner runs jobs only on protected branches or tags. Users with access to the runner host can see the code it ran ([GitLab runners](https://docs.gitlab.com/ci/runners/configure_runners/)).
- The Kubernetes Node authorizer lets a kubelet read only the secrets of pods bound to its own node ([Node authorizer](https://kubernetes.io/docs/reference/access-authn-authz/node/)). A `NoSchedule` taint keeps pods off a node unless they tolerate it ([taints](https://kubernetes.io/docs/concepts/scheduling-eviction/taint-and-toleration/)).
- Each Codespaces user secret names the repositories that may get it ([Codespaces](https://docs.github.com/en/codespaces/managing-your-codespaces/managing-your-account-specific-secrets-for-github-codespaces)). A GitHub fine-grained token can be limited to some repositories and can expire ([tokens](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens)).
- A token from `claude setup-token` can only make model requests ([Claude Code IAM](https://code.claude.com/docs/en/iam)). It is the kind of low-value token that a `shared` host may get.
- Docker Sandboxes inject credentials in a host-side proxy, so the real value never enters the sandbox ([Docker credentials](https://docs.docker.com/ai/sandboxes/configuration/credentials/)).
- In Confidential Containers, the whole host outside the enclave is untrusted. It needs TEE hardware such as SEV-SNP or TDX ([CoCo trust model](https://confidentialcontainers.org/docs/architecture/trust-model/trust-model/)).
- The client's TOML decoder (`github.com/BurntSushi/toml`) ignores keys that have no struct field ([toml](https://pkg.go.dev/github.com/BurntSushi/toml)). `SaveHosts` writes the whole file from the struct (`internal/client/config.go`). So an older client that saves `hosts.toml` drops a new key. The spec keeps tiers in a file of their own, `tiers.toml`, for this reason ([question 33](../contributing/isolation-spec.md#15-open-questions)).

#### Upgrades, health and drain

From nodes 13 and 18:

- Firecracker patches each minor release for at least 6 months, and the last two minors for up to 1 year ([release policy](https://github.com/firecracker-microvm/firecracker/blob/main/docs/RELEASE_POLICY.md)). Cloud Hypervisor releases a major about every 6 weeks, and supports each for about 12 weeks ([releases](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/releases.md)).
- Firecracker snapshots do not cross major format versions, and restores across host kernels are "considered unstable" ([versioning](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/versioning.md)). So a runtime upgrade cannot move a running sandbox. New sandboxes use the new runtime, and old ones keep theirs.
- E2B pins the kernel of each template build, and only new builds roll forward ([E2B pull request 2592](https://github.com/e2b-dev/runtime/pull/2592)). Docker Sandboxes keep old sandboxes running after a CLI update ([release notes](https://docs.docker.com/ai/sandboxes/release-notes/)).
- Nomad's drain stops new placements and moves existing ones, with a deadline of 1 hour by default ([node drain](https://developer.hashicorp.com/nomad/commands/node/drain)). Incus `evacuate` marks a member so that it takes no new instances ([Incus cluster](https://linuxcontainers.org/incus/docs/main/howto/cluster_manage/)).
- kured reboots a node when `/var/run/reboot-required` exists, after a drain ([kured](https://github.com/kubereboot/kured)). Ubuntu's `unattended-upgrades` uses the same file ([Ubuntu](https://ubuntu.com/server/docs/how-to/software/automatic-updates/)).
- The kubelet's default hard eviction thresholds include `memory.available<100Mi` and `nodefs.available<10%` ([node-pressure eviction](https://kubernetes.io/docs/concepts/scheduling-eviction/node-pressure-eviction/)).
- TUF defines a rollback attack as tricking a client into installing software older than what it knew to be available ([TUF](https://theupdateframework.github.io/specification/latest/)). A signed manifest that a host serves needs a version and an expiry to resist it. The hosts search therefore proposed a signed runtime manifest with its own key, a minimum version and an expiry. The main design makes that unneeded. The client embeds the release manifest's digests ([section 8.5](#85-the-winner-laptop-held-digests-node-34)), so a host cannot offer an older runtime. No separate runtime key, minimum version or expiry is needed ([question 30](../contributing/isolation-spec.md#15-open-questions)).

#### Hosts behind NAT

From nodes 14 and 19:

- `restrict` turns off forwarding, `port-forwarding` turns it back on, and `permitlisten` limits `-R` to one listen address and port ([sshd(8)](https://man.openbsd.org/sshd.8)). OpenSSH 7.8 added `PermitListen` and `permitlisten` ([release 7.8](https://www.openssh.com/txt/release-7.8)).
- `AllowTcpForwarding remote` allows only remote forwards, which blocks `-L` and `-W` from a tunnel account. `GatewayPorts` defaults to `no`, so remote forwards bind to loopback ([sshd_config](https://man.openbsd.org/sshd_config.5)).
- `ssh -J` makes "a TCP forwarding to the ultimate destination" ([ssh(1)](https://man.openbsd.org/ssh.1)). For a jump, `ssh` passes `-F <config>` to the nested `ssh`, so the jump reads the same generated config ([ssh.c](https://github.com/openssh/openssh-portable/blob/master/ssh.c)). Options from the command line do not reach the jump.
- Tailscale quarantines a shared machine: it "can receive incoming connections … but cannot start connections" ([sharing](https://tailscale.com/kb/1084/sharing)). A new tailnet starts with an allow-all policy ([ACL examples](https://tailscale.com/docs/reference/examples/acls)).
- In userspace mode, Tailscale proxies incoming connections to the same port on `127.0.0.1` ([FAQ](https://tailscale.com/docs/reference/faq/other-vpns)). A sandbox that reaches the host's loopback can reach that proxy.
- In one Tailscale test on one route, DERP, Tailscale's own relay servers, gave about 2.2 Mbit/s. Tailscale's "peer relay" feature, which forwards through a node of the user's own, gave 27 Mbit/s to 35 Mbit/s ([peer relays](https://tailscale.com/blog/peer-relays-international-networks)). At 2.2 Mbit/s, 1 GB takes about 61 minutes (computed).
- Headscale is BSD-3-Clause ([Headscale](https://github.com/juanfont/headscale)). NetBird's `management/`, `signal/` and `relay/` are AGPLv3 ([NetBird](https://github.com/netbirdio/netbird)).
- `passt` maps the guest's gateway address to the host's loopback by default, and `--map-host-loopback none` turns that off ([`passt`](https://passt.top/builds/latest/web/passt.1.html)).

The tool sets up no tunnel. The docs give the ladder, and `host doctor` names the route ([question 36](../contributing/isolation-spec.md#15-open-questions), decision [0031](../contributing/decisions/0031-the-egress-process-is-not-a-relay.md)).

#### Leave and wipe

From nodes 12 and 17:

- Today, `host rm` only forgets a host. The helper and the sessions stay (`hostRmCmd` in `internal/cli/host.go`). Uninstall is a list of shell steps ([uninstall](../how-to/uninstall.md)).
- `shred` cannot reliably destroy files on journalling or copy-on-write file systems, and SSD wear levelling keeps old blocks ([shred](https://www.gnu.org/software/coreutils/manual/html_node/shred-invocation.html)). A wipe is best effort.
- A systemd timer with `Persistent=` keeps state, and the docs say to clean it before the timer is removed ([systemd.timer](https://www.freedesktop.org/software/systemd/man/latest/systemd.timer.html)).
- OpenSSH 7.7 added `expiry-time` ([release 7.7](https://www.openssh.org/txt/release-7.7)). UTC times need OpenSSH 9.1 ([release 9.1](https://www.openssh.com/txt/release-9.1)). That `sshd` checks the expiry only at login, so an open ControlMaster lives on, is an inference from the code, and **UNVERIFIED** by a test ([auth2-pubkeyfile.c](https://github.com/openssh/openssh-portable/blob/master/auth2-pubkeyfile.c)).
- `git bundle` packs a range into one file, and `git bundle verify` checks its prerequisites ([git bundle](https://git-scm.com/docs/git-bundle)). On this repository (237 commits, a 3.4 MB `.git`), a full bundle was 3.1 MB and the last 20 commits 123 KB (measured on a laptop).

The spec takes the footprint ledger and `host rm --purge` from node 17. It does not take timed leases now.

#### Team machines

Nodes 15 and 20 ranked last under both parents. The evidence shows why:

- Docker Sandboxes' organisation policies need a paid plan and Docker's cloud ([policies](https://docs.docker.com/ai/governance/organization/policies/)).
- Codex reads an admin-enforced `/etc/codex/requirements.toml` ([Codex](https://learn.chatgpt.com/docs/enterprise/managed-configuration)). Claude Code reads `/etc/claude-code/managed-settings.json` ([Claude Code](https://code.claude.com/docs/en/managed-settings)). The agents already have their own admin controls.
- `user-UID.slice` limits one user ([user@.service](https://www.freedesktop.org/software/systemd/man/latest/user@.service.html)). journald's fields that start with `_` cannot be changed by the client ([journal fields](https://www.freedesktop.org/software/systemd/man/latest/systemd.journal-fields.html)). nftables `meta skuid` matches the sending socket's uid ([nftables](https://www.netfilter.org/projects/nftables/manpage.html)).
- [How simstim compares](../explanation/comparison.md) sends teams that need access control and audit to Coder. Team hosts would reverse that.

A teammate's machine, used through the user's own account there, is a `shared` host. Team fleets wait for demand ([question 41](../contributing/isolation-spec.md#15-open-questions)).

### Red team: gaps and answers

| Threat | Gap | Answer in the spec |
| --- | --- | --- |
| T4, T1, T5 | Every VMM runs as the login user, who owns the helper, the runtime, the images, the admission ledger, the egress process and `~/.ssh`. One escape can rewrite the helper, which `Dial` runs by path and `Probe` checks only with `test -x`. | Said plainly in the docs and the level's label. A root-owned launcher with slot uids and a root-owned store is the second phase ([question 27](../contributing/isolation-spec.md#15-open-questions)). The main design's dial also checks the helper's digest, but a compromised account can act before that check ([section 8.5](#85-the-winner-laptop-held-digests-node-34)). |
| T4 | Root step (b), an `AppArmor` `userns` profile, weakens the whole machine. | Removed. The guest has no network card by default, so no user namespace is needed. |
| T4 | The helper wrote the root script, it sat in a writable folder, and it used `$USER`. A compromised account can also capture the sudo password. | The laptop builds the script from templates, with the literal login name. It goes inline through `sudo /bin/sh -c`. "Run" is the default only at the first add. |
| T4 | The ACL and the `kvm` group open `/dev/kvm` to every process of the user. The `user@.service` drop-in changes every user. Grants outlive `host rm`. | The ACL and group are shown as such. The drop-in is scoped to `user@<uid>.service.d`. A root-owned `undo.sh` on the host lets the owner undo without the client. |
| T6, T4 | A pin in the generated config loses to the user's `StrictHostKeyChecking no`. The same order lets `ForwardAgent yes` reach every host today. The host ID authenticates nothing. | Pin and forwarding options go on the command line. The pin is seeded from the user's own `known_hosts`. The host ID is a hint, and a change drops the tier. The forwarding fix ships first ([question 40](../contributing/isolation-spec.md#15-open-questions)). |
| T1 | A signed manifest with no version or expiry allows a rollback to an older, flawed runtime. | The hosts draft gave the manifest a version, a minimum version and an expiry. The main design replaces this: the client embeds the release manifest's digests, so a host cannot offer an older runtime ([question 30](../contributing/isolation-spec.md#15-open-questions)). |
| T1 | `ssh-keygen -Y verify` needs OpenSSH 8.1, and the documented client minimum version is 8.0. | The helper and the client check signatures in Go, with an Apache-2.0 library ([question 38](../contributing/isolation-spec.md#15-open-questions)). |
| T1 | One release key would sign the helper, the VMMs, the kernels and the images. | The hosts draft proposed a separate runtime key, held offline. The main design keeps one release key, as in decision [0017](../contributing/decisions/0017-release-signatures.md), and the client embeds the release manifest. The owner adds a hardware key for the signed-tag gate ([question 30](../contributing/isolation-spec.md#15-open-questions)). Agent CLIs in images are pinned by version and digest in `internal/agents`. |
| T4 | A template built on a host, or copied from one, can poison the fleet. | Templates come from the laptop or the signed release only. No host-to-host copy, and no host key on another host ([question 35](../contributing/isolation-spec.md#15-open-questions)). |
| T2, T4 | Rootless gVisor uses the host network, and its uid is the user's. | The `gvisor` level runs with no network and a socket to the egress process, or is labelled `process` and kept out of auto placement ([question 5](../contributing/isolation-spec.md#15-open-questions)). |
| T2, T4, T6 | TSI and walls that use the host network reach the LAN, the tailnet and the cloud metadata address. | Handed to the main design: no TSI, vsock egress only, and a default deny of loopback, link-local, RFC 1918, and Tailscale's IPv4 and IPv6 ranges ([section 7.5](#75-the-winner-nic-less-wall-egress-proxy-sentinel-keys-node-26)). |
| T5 | An injected credential works for any allowed destination. | Handed to the main design: bind each secret to its destinations and to one sandbox, as a bound secret. |
| T6 | An older client's `SaveHosts` drops the tier, and the host then counts as `own`. | Tiers live in `tiers.toml`, which older clients never write. A host with no entry after the migration is `shared` until the user answers ([question 33](../contributing/isolation-spec.md#15-open-questions)). |
| T6 | E2B and cloud provider hosts would default to `own`, though a third party has root. | `host create e2b` defaults to `shared` ([question 32](../contributing/isolation-spec.md#15-open-questions)). |
| T5, T6 | `--purge` leaves the user's key, agent logins, linger and root grants on a borrowed machine. | `--purge` removes what the footprint ledger lists, offers to delete agent logins, runs or prints the root undo, and prints the rotate list. The user's key line is reported, not removed, because the tool did not add it. |
| T4 | A compromised laptop drives every host with one key and an open ControlMaster. | Out of scope. The docs say so, and recommend FIDO keys. A forced-command key for `shared` hosts is [question 31](../contributing/isolation-spec.md#15-open-questions). |
| T4, T6 | A reverse tunnel puts a key to the rendezvous on a borrowed host, and an allow-all tailnet lets a host reach the laptop. | Docs only: a hardened tunnel recipe on a machine the user owns, and Tailscale shares for borrowed machines. The tool runs no tunnel ([question 36](../contributing/isolation-spec.md#15-open-questions)). |
| T4 | Terminal escapes from a sandbox reach the laptop. | Advice for `shared` and `throwaway` hosts. A filter would bend "byte-exact" ([question 26](../contributing/isolation-spec.md#15-open-questions)). |
| T4, T5 | A compromised host lies in its facts to attract work. | Facts are checked against prepare-time values. A host that takes most placements ranks lower. The reason for each placement is recorded. |
| Health | A Mac host after a reboot cannot start VMs until its keychain is unlocked. | Mac hosts are later ([question 37](../contributing/isolation-spec.md#15-open-questions)). The fact check found that this may affect macOS guests only. |

### Fact check: corrections

| Claim | Verdict | Correction | Source |
| --- | --- | --- | --- |
| Firecracker needs `/dev/kvm`, granted by `setfacl` or the `kvm` group. Debian uses mode 0660. | Confirmed | The guide adds a third route: run as root. Debian also sets `group-render-mode=0660`. | [getting started](https://github.com/firecracker-microvm/firecracker/blob/main/docs/getting-started.md) |
| The jailer needs root, and its operator is in the trusted computing base. | Confirmed | All its inputs are trusted. A root launcher must check every argument before it calls the jailer. | [jailer](https://github.com/firecracker-microvm/firecracker/blob/main/docs/jailer.md) |
| Firecracker networking needs a TAP device and NAT rules, made with sudo. | Confirmed | Only for a guest network card. vsock needs no TAP and no root. | [network setup](https://github.com/firecracker-microvm/firecracker/blob/main/docs/network-setup.md), [vsock](https://github.com/firecracker-microvm/firecracker/blob/main/docs/vsock.md) |
| Cloud Hypervisor's `--landlock` can stand in for the jailer. | Confirmed in part | It covers mostly files, and gives no uid drop, chroot, namespaces or cgroups. | [landlock](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/landlock.md) |
| Cloud Hypervisor and libkrun can network a guest with no root through a user-mode stack. | **UNVERIFIED** | Cloud Hypervisor documents TAP and vhost-user. One issue reports a panic with `passt`'s vhost-user. TSI uses the host's network. The safe default is no network card. | [issue 7766](https://github.com/cloud-hypervisor/cloud-hypervisor/issues/7766) |
| `user@.service` delegates `pids memory cpu`. `io` and `cpuset` need a root drop-in. | Confirmed | Scope the drop-in to `user@<uid>.service.d`. The date "cpu from systemd 252" has only secondary sources. | [user@.service.in](https://github.com/systemd/systemd/blob/main/units/user%40.service.in) |
| Ubuntu 24.04 restricts user namespaces unless an `AppArmor` profile allows the program. | Confirmed | Such profiles are a trivial bypass for unconfined processes. On from 24.04, introduced in 23.10. | [Ubuntu discourse](https://discourse.ubuntu.com/t/understanding-apparmor-user-namespace-restriction/58007) |
| Rootless gVisor uses the host network. | Confirmed | It also maps only the caller's uid, suits `runsc do`, and has no save and restore. | [gVisor rootless](https://gvisor.dev/docs/user_guide/rootless/) |
| A lingering user manager keeps its old groups after `usermod -aG`. | Confirmed (was UNVERIFIED) | The fix, `loginctl terminate-user`, ends all of the user's processes. | [podman 29285](https://github.com/podman-container-tools/podman/pull/29285) |
| Reflink works on Btrfs and XFS, not on ext4. | Confirmed | XFS only when made with reflink. The full-copy time on ext4 is an estimate. | [fs/ext4/file.c](https://github.com/torvalds/linux/blob/master/fs/ext4/file.c) |
| AWS nested KVM on some Intel families, none on Graviton. No nested virtualisation on Hetzner Cloud. | Confirmed | The AWS list is longer (see Evidence). The February 2026 date was not checked. | [AWS](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/amazon-ec2-nested-virtualization.html), [Hetzner](https://docs.hetzner.com/cloud/servers/faq/) |
| Command-line `-o` options win over the user's config. | Confirmed | The destination's options do not reach ProxyJump hops. The wrapper includes the user's config first, so its own block loses. | [ssh_config](https://man.openbsd.org/ssh_config) |

This section's own check found one more correction. One third-party source says that the keychain rule on macOS 15 applies to macOS guests only ([tart-skills](https://github.com/jonnyzzz/tart-skills)).

### Platform support

| Host | Level without root | Level after the root step | Notes |
| --- | --- | --- | --- |
| Linux with KVM, `/dev/kvm` 0666 (upstream systemd) | `microvm` | `microvm` | No root step needed for the level. |
| Linux with KVM, user already in `kvm` | `microvm` | `microvm` | |
| Debian with KVM, user not in `kvm` | `gvisor` if user namespaces work, else `process` | `microvm` | `/dev/kvm` is 0660 root:kvm. |
| Ubuntu 23.10 or later with KVM, user not in `kvm` | `process` | `microvm` | User namespaces are blocked without an `AppArmor` profile. The hosts draft installed none. The main design adds one for a root-owned launcher only ([question 4](../contributing/isolation-spec.md#15-open-questions)). |
| Cloud VM with nested KVM (the EC2 families above, GCE N2 and others) | as Linux above | `microvm` | GCE warns of 10 % or more CPU loss. |
| EC2 with Nitro Enclaves, Graviton included | `gvisor` or `process` | `microvm` | The `nitro` driver needs `/dev/nitro_enclaves` open to the user, which a root step gives ([section 10.1](#101-aws-nitro-sandboxes-on-ec2)). |
| Cloud VM without nested KVM or Nitro Enclaves (Hetzner Cloud, GCE E2) | `gvisor` or `process` | `gvisor` or `process` | |
| Bare metal | as Linux above | `microvm` | |
| Mac, Apple silicon | not in the first version | `vz`, later | Needs a darwin helper, launchd and an entitled binary ([question 37](../contributing/isolation-spec.md#15-open-questions)). |
| Windows | no | no | |

The `microvm` level needs Linux on amd64 or arm64, systemd with cgroup v2, and a kernel that the chosen VMM supports.

### Performance

| What | Number | Kind |
| --- | --- | --- |
| `host add` today | 1.8 s to 2.2 s, over a jump host on Wi-Fi | Measured ([testing](../contributing/testing.md#time-to-first-agent)) |
| Runtime download | About 8 MB of VMM and 10 MB to 42 MB of kernel | From release sizes. "10 MB compressed" is an estimate. |
| An image of 1.0 GB | About 8 s at 1 Gbit/s, 80 s at 100 Mbit/s, 400 s at 20 Mbit/s, 61 minutes over DERP at 2.2 Mbit/s | Computed |
| Firecracker to guest `/sbin/init` | 125 ms or less, for a minimal guest | Specification |
| A guest with an agent, to ready | About 1 s | **UNVERIFIED**. Depot measured 0.6 s P50 and 1.2 s P90 for tuned Cloud Hypervisor guests ([Depot](https://depot.dev/blog/optimizing-microvm-boot-times)). |
| A new sandbox disk | Milliseconds with reflink. 2 s to 10 s as a full copy on ext4 | Estimate |
| The facts request | About 1 KB, from about six small files | Estimate |
| A dial through a live ControlMaster | About 30 ms. A fresh `ssh`, about 0.66 s | Anecdotal ([OpenSSH cookbook](https://en.wikibooks.org/wiki/OpenSSH/Cookbook/Multiplexing)) |
| `ssh -G` | Under 10 ms | Measured on a macOS laptop |
| `tailscale status --json` | 89 ms warm, 561 ms cold | Measured on a macOS laptop |
| Room for 4 GiB sandboxes, with [0024](../contributing/decisions/0024-memory-cap.md)'s reserve | 16 GiB host: 3. 32 GiB: 7. 62.8 GiB: 14 | Computed |
| The root step | About 10 s to 30 s of typing | Estimate |

### Rules

| Rule | Kept or bent | How |
| --- | --- | --- |
| Byte-exact while attached | Kept | Placement and prepare print before attach, never during it. A filter for `shared` hosts would bend it, and is not proposed ([question 26](../contributing/isolation-spec.md#15-open-questions)). |
| Nothing new listens on a host | Kept | The egress process and the VM consoles use Unix sockets and vsock, never a network port. Decision [0030](../contributing/decisions/0030-local-sockets-do-not-listen.md) says that these do not count as listening ([question 23](../contributing/isolation-spec.md#15-open-questions)). |
| No relay | Kept | The laptop copies its own images to its own hosts. No bytes go from one host to another. Jump hosts, tunnels and DERP are the user's own setup, documented as such. Decision [0031](../contributing/decisions/0031-the-egress-process-is-not-a-relay.md) says that recipes for hosts behind NAT use only machines that the user owns, and that the tool runs no tunnel. |
| No telemetry | Kept | A host may download images from the release URL. That is new outbound traffic, but it reports nothing. It is a setting ([question 39](../contributing/isolation-spec.md#15-open-questions)). |
| The tool stores no secrets | Kept | `tiers.toml` and the grant ledger hold names, times and host IDs, never values. The user makes any per-host key. Decision [0032](../contributing/decisions/0032-secret-values-in-memory-only.md) records where secret values live. |
| Agents are data | Kept | Low-value token hints and agent CLI pins for images go in `internal/agents`. |
| Clean-room, licences | Kept | Nomad (BUSL-1.1) and Nix (LGPL) give ideas only. The guest kernel (GPL-2.0) ships with its source. `passt` is avoided. The Go `sshsig` library is Apache-2.0 ([sshsig](https://github.com/hiddeco/sshsig)). |
| `fix.go`: "Nothing here uses sudo" | Kept | The helper never runs sudo. The client runs `ssh -t … sudo`, and the user types the password into sudo. |
| [0024](../contributing/decisions/0024-memory-cap.md): a host lowers a memory request to its limit | Changed for sandboxes | A sandbox that does not fit is refused, not shrunk ([question 13](../contributing/isolation-spec.md#15-open-questions)). Needs a new record. |
| [0013](../contributing/decisions/0013-host-providers.md): created hosts | Changed | `host create e2b` defaults to the `shared` tier ([question 32](../contributing/isolation-spec.md#15-open-questions)). Needs a new record. |
| The user's SSH settings win | Changed for saved hosts | The host key pin and the forwarding options override the user's config. Needs a new record. |

### What this design does not solve

- **Root on a host.** It sees the memory and disk of every sandbox there, and every secret sent. Tiers limit what goes there. Nothing hides what does.
- **A VMM escape in the first phase.** It reaches the login user's account and, through the helper, every later sandbox on that host. The second phase moves VMMs to slot accounts ([question 27](../contributing/isolation-spec.md#15-open-questions)).
- **A compromised laptop.** It holds the keys and all the policy. It can drive every host.
- **Plain `ssh` and other tools.** They ignore tiers. Only this client enforces them.
- **Recall.** A value sent to a host cannot be taken back. The rotate list says what to change.

### What this section could not verify

- That Ubuntu builds systemd with `/dev/kvm` mode 0660, as Debian does.
- That an ACL on `/dev/kvm` lasts until the next reboot and survives udev change events.
- That `daemon-reload` gives a running user manager the `io` controller. A re-execution of the user manager follows it ([build facts](build-facts.md#4-linux-walls)).
- Settled since: default Ubuntu and Debian cloud images include neither `acl` nor `uidmap` ([build facts](build-facts.md#4-linux-walls)).
- That Cloud Hypervisor runs with a user-mode network stack on a Unix socket.
- That a guest with an agent boots to ready in about 1 s.
- That the Virtualization framework can start a Linux VM at boot for an account that has never logged in ([locations, section 5](locations.md#5-mac-hosts-checked-again)).
- That libkrun has no snapshot API (from a Lima issue, not libkrun's docs).
- That `sshd` checks `expiry-time` only at login.
- That Firecracker GPU passthrough exists in any form.
- That E2B pull request 2201 merged.
- The exact licence of each Cloud Hypervisor source file.
- That the facts request takes under 10 ms on a host with many sessions.

## Policy for parallel work

Researched on 10 November 2026.

On 10 November 2026 the owner asked for more control over the design above:

- One config for all sessions, which a narrower place can override when it needs to.
- Network rules for all sessions, or one-off exceptions.
- Not the strictest defaults for everyone: full control, so that many sessions can run at once with confidence.
- A terminal beside the agent, in the same files, for commands that a person runs.
- Research on automatic classifiers.

This section did not run a tree search. Three researchers worked in parallel: one on shipping products, one on published research, and one on egress tools and on the services that real attacks used. Their reports were checked against the primary sources for the numbers that the design leans on. The design that follows is in [the spec](../contributing/isolation-spec.md), sections 6.11, 7.2, 7.8, 7.9 and 8, and in decision [0029](../contributing/decisions/0029-a-sandbox-per-session.md).

### Summary

1. **Every product that decides on its own puts rules first and a model second.** Claude Code checks a fixed allowlist and the user's rules before its classifier ([auto mode](https://www.anthropic.com/engineering/claude-code-auto-mode)). Codex reviews only requests that its allowlist does not cover ([auto-review](https://learn.chatgpt.com/docs/sandboxing/auto-review)). Anthropic's sandbox runtime checks its deny and allow lists before it asks a callback ([sandbox-runtime](https://github.com/anthropics/sandbox-runtime)).
2. **Published error rates are useful but uneven.** Claude Code's classifier reports a 0.4% false-positive rate on 10,000 real calls, and misses of 17% on 52 real overeager actions and 5.7% on 1,000 synthetic exfiltration attempts. An independent stress test of the same system measured 81.0% misses end to end, and 70.3% misses with 31.9% false positives on the actions that the classifier sees ([stress test](https://arxiv.org/abs/2604.04978)). Codex's reviewer approves about 99% of the requests it reviews, and catches 90.3% of synthetic overeager actions and 99.3% of synthetic prompt injections ([OpenAI](https://alignment.openai.com/auto-review/)).
3. **Research finds every guard breakable.** Adaptive attacks broke 12 published defences, most of them at over 90% attack success, with a median of 13 to 110 queries ([paper](https://arxiv.org/abs/2510.09023)). Defences that build a policy from the user's trusted request, and enforce it in code, held better in their own tests ([Progent](https://arxiv.org/abs/2504.11703), [Conseca](https://arxiv.org/abs/2501.17070), [CaMeL](https://arxiv.org/abs/2503.18813)).
4. **Data leaves through channels that are already allowed.** Examples are an allowed model API used with the attacker's own key ([write-up](https://embracethered.com/blog/posts/2025/claude-abusing-network-access-and-anthropic-api-for-data-exfiltration/)), DNS lookups by allowed tools ([CVE-2025-55284](https://embracethered.com/blog/posts/2025/claude-code-exfiltration-via-dns-requests/)), and an allowed proxy ([EchoLeak](https://arxiv.org/abs/2509.10540)). A judge that sees only destinations cannot see these. The design's identity pin, closed DNS and upload budget cover them.
5. **Real attacks used a short list of services.** These are `webhook.site`, `requestcatcher.com`, `lhr.life`, `trycloudflare.com`, out-of-band test hosts, Discord and Telegram webhooks, bare IP addresses and lookalike domains. Fixed rules catch them with no model (section [Fixed checks](#fixed-checks-before-any-model)).
6. **Configurable policy elsewhere is simpler than the owner asks for.** Docker Sandboxes lets a deny in any source win. GitHub's firewall has an organisation and a repository level. Codex Cloud offers three presets. None of them lets one session lift a broad deny for an exception, which the owner asked for.
7. **A terminal beside the agent is common.** Docker Sandboxes, the hosted VM service, GitHub Codespaces and Modal each give a shell in the same sandbox.
8. **The choice.** Six layers with one shape, where the narrowest matching layer decides and locks hold what must never change. An unknown action that each layer picks: ask, deny, allow with a log, or classify. A classifier with fixed checks first and an optional judge that sees structured fields only. Shells inside the sandbox, sharing its policy. SSH keys through the sandbox's own agent.

### Configurable policy in other tools

| Tool | Layers | Precedence | One-off exceptions |
| --- | --- | --- | --- |
| Docker Sandboxes | A preset, global rules, rules for one sandbox, and rules that kits bring | A deny from any source beats an allow ([local policy](https://docs.docker.com/ai/governance/local/)) | An approval makes a rule for one host and port in one sandbox ([approvals](https://docs.docker.com/ai/governance/local/approvals/)) |
| GitHub Copilot cloud agent | The organisation, then the repository, which the organisation may allow ([firewall](https://docs.github.com/en/copilot/how-tos/copilot-on-github/customize-copilot/customize-the-firewall)) | Allowlists add up | None. A blocked request appears in the pull request. |
| Codex Cloud | Presets: none, common dependencies or all, plus the user's own domains, and an option for `GET`, `HEAD` and `OPTIONS` only ([internet access](https://learn.chatgpt.com/docs/cloud/internet-access)) | The preset, then the list | None found |
| Codex CLI | `domains = {host = "allow" \| "deny"}` in its config ([approvals](https://learn.chatgpt.com/docs/agent-approvals-security)) | A deny wins | The reviewer, or the user, approves one request |
| Gemini CLI | TOML rules with a priority ([policy engine](https://geminicli.com/docs/reference/policy-engine/)) | The highest priority wins. Without a person, "ask" means deny. | A rule with a higher priority |
| Claude Code | Settings at several levels. It never reads its classifier's rules from a repository's settings ([auto mode config](https://code.claude.com/docs/en/auto-mode-config)). | A hard deny, then a soft deny, then an allow, then the user's clear intent | The user's own words in the session |
| Harden-Runner | Audit mode, then block mode. A baseline from past runs becomes a policy ([baseline](https://docs.stepsecurity.io/github-actions/harden-runner/baseline)). | Its global block list applies even in audit mode ([repository](https://github.com/step-security/harden-runner)) | A change to the baseline |

Desktop firewalls answer the question of scope well. OpenSnitch offers once, a time from 30 seconds to 12 hours, until reboot and forever, and it denies after 30 seconds by default ([prompt durations](https://github.com/evilsocket/opensnitch/blob/master/ui/opensnitch/dialogs/prompt/utils.py), [defaults](https://github.com/evilsocket/opensnitch/blob/master/ui/opensnitch/config.py)). Little Snitch turns each answer into a rule with a lifetime ([Little Snitch](https://help.obdev.at/littlesnitch7/alert-overview)). The design's `--once`, `--for` and layers follow the same idea.

### Automatic classifiers in products

| Product | What it judges | How | What it never sees | Measured |
| --- | --- | --- | --- | --- |
| Claude Code auto mode | Tool calls that its rules do not settle | Two stages: a fast filter tuned to block, then reasoning on flagged calls only. The docs name Sonnet 5 as the default model ([permission modes](https://code.claude.com/docs/en/permission-modes)). | The agent's prose and every tool result. "Assistant prose, tool-call descriptions, and tool outputs are stripped" ([auto mode](https://www.anthropic.com/engineering/claude-code-auto-mode)). | Above, in the summary. Users approved 93% of manual prompts. |
| Codex auto-review | Escalations, and network requests that need approval | A separate agent on GPT-5.4 Thinking at low reasoning ([OpenAI](https://alignment.openai.com/auto-review/)) | It sees tool output, labelled as untrusted evidence ([policy template](https://github.com/openai/codex/blob/rust-v0.167.0/codex-rs/prompts/templates/guardian/policy_template.md)) | About 200 times fewer interruptions than manual approval |
| Cursor | Commands that cannot run in its sandbox | A small model in the agent's own request stream ([run modes](https://cursor.com/docs/agent/security/run-modes)) | Not stated | Blocks about 4% of the calls it reviews. Cursor says it "is not a security boundary" ([blog](https://cursor.com/blog/agent-autonomy-auto-review)). |
| Gemini CLI with Conseca | Tool calls | A model writes a policy from the user's prompt, and a small model checks each call against it ([pull request](https://github.com/google-gemini/gemini-cli/pull/13193)) | Not stated | None published |
| Anthropic's sandbox runtime | Network requests | Deny list, allow list, then a decider program. Nothing is dialled before an allow, and a decider that takes more than 5 seconds gives 503 ([srt-proxy](https://github.com/anthropics/sandbox-runtime/blob/main/docs/srt-proxy.md)). | The decider gets method, host, port, server name, path, query and headers | None |
| Amp | Tool calls | Rules, or a program of the user's that answers by exit code ([permissions](https://ampcode.com/notes/permissions)) | Only the tool's parameters | None |
| Docker Sandboxes, GitHub Copilot | Network requests | Rules only. Nothing decides on its own. | | |

What the products teach:

- **Keep text that an attacker wrote away from the judge.** Claude Code removes tool results so that injected content never reaches its classifier. It also removes the agent's own text, so that the agent cannot argue its case.
- **Judge a destination once, then enforce it in code.** In Claude Code's sandbox, the classifier judges a command's list of hosts once, and the proxy enforces it for each connection ([sandboxing](https://code.claude.com/docs/en/sandboxing)).
- **Fail closed, and give the same reason each time.** Claude Code asks a person after 3 denials in a row or 20 in all ([permission modes](https://code.claude.com/docs/en/permission-modes)). Codex stops a turn after 3 in a row or 10 in the last 50 ([auto-review](https://learn.chatgpt.com/docs/sandboxing/auto-review)).
- **Never take the judge's rules from the repository.** Claude Code refuses its classifier's settings from a repository, "so a checked-in repo… could otherwise inject its own allow rules" ([auto mode config](https://code.claude.com/docs/en/auto-mode-config)).
- **For a push, the payload matters as much as the target.** Codex's default policy says that authorisation for sensitive egress "must specify the payload to be sent as well as the destination" ([policy](https://github.com/openai/codex/blob/rust-v0.167.0/codex-rs/prompts/templates/guardian/policy.md)). The design keeps pushes on the push door, where the egress process reads the refs (spec 7.2).

### What the research says

- **Guards built on a model.** LlamaFirewall's two guards brought attack success on AgentDojo from 17.6% to 1.75%, at a utility of 42.7% against 47.7% ([LlamaFirewall](https://arxiv.org/html/2505.03574v1)). MELON reached 0.32% ([MELON](https://arxiv.org/html/2502.05174)). Task Shield reached 2.07% ([Task Shield](https://arxiv.org/abs/2412.16682)).
- **The same guards under adaptive attack.** A 2025 study broke 12 defences, most of them at over 90% attack success. MELON fell to 71% to 95%, and an adaptive search beat PromptGuard at 94% to 99% ([paper](https://arxiv.org/abs/2510.09023)). An earlier study broke all eight that it tried, at over 50% ([paper](https://arxiv.org/abs/2503.00061)). An agent that writes injections into its own output evades the monitors that read it ([paper](https://arxiv.org/abs/2510.09462)).
- **Policies from trusted intent.** Progent cut attack success on AgentDojo from 39.9% to 1.0% with the same utility, with rules over tool arguments ([Progent](https://arxiv.org/html/2504.11703v3)). Conseca writes a policy for each task from the user's request, never from tool output, and enforces it in code ([Conseca](https://arxiv.org/html/2501.17070v3)). CaMeL solved 77% of AgentDojo's tasks with provable security, against 84% with no defence ([CaMeL](https://arxiv.org/html/2503.18813v2)). Their evaluations are small or their own.
- **Monitoring over time.** An ensemble that reviews actions after the fact reached 6% misses at 1% false positives on held-out work ([Async Control](https://arxiv.org/abs/2512.13526)). Splitting a harmful goal into harmless steps beat single-step checks 87% of the time, and a monitor that read the sequence stopped 93% ([paper](https://arxiv.org/abs/2506.10949)).
- **Where the evidence is weak.** No paper tests a judge that sees only destinations and push targets. No study measures leakage from a sandbox that holds no real secrets. Most defence numbers come from fixed attacks, which adaptive attacks overturn.

### Exfiltration through allowed channels

- A model API that the sandbox must reach takes uploads with the attacker's own key, up to 30 MB a file ([write-up](https://embracethered.com/blog/posts/2025/claude-abusing-network-access-and-anthropic-api-for-data-exfiltration/)). The design's identity pin refuses any credential that the egress process did not put in (spec 7.3).
- Allowed tools such as `ping`, `nslookup` and `dig` carried data in DNS names ([CVE-2025-55284](https://embracethered.com/blog/posts/2025/claude-code-exfiltration-via-dns-requests/)). A resolver that answers only allowed names stopped DNS exfiltration behind a firewall ([paper](https://arxiv.org/abs/2610.22578)). The design's DNS stub does the same (spec 7.2).
- A fetch URL that looks like part of the task carried secrets out at 79.7% success across 11 models ([LLMLeak](https://arxiv.org/abs/2610.01768)). A destination judge cannot see the data in a path or a query. The upload budget and the method rules limit it.
- A Microsoft 365 attack evaded a classifier and then used a proxy that the content security policy allowed ([EchoLeak](https://arxiv.org/abs/2509.10540)).

### Fixed checks before any model

| Check | Why | Source |
| --- | --- | --- |
| Normalise the name, refuse a null byte and an empty label, and never read an empty list as "allow all" | Both bypassed another sandbox's allowlist in 2025 | [write-up](https://oddguan.com/blog/second-time-same-sandbox-anthropic-claude-code-network-allowlist-bypass-data-exfiltration/) |
| Refuse an IP address that no rule names | A February 2026 npm campaign sent data to a bare address and port | [Socket](https://socket.dev/blog/stegabin-26-malicious-npm-packages-use-pastebin-steganography) |
| Refuse `webhook.site` | Shai-Hulud sent stolen secrets there in September 2025 | [Wiz](https://www.wiz.io/blog/shai-hulud-npm-supply-chain-attack) |
| Refuse `*.requestcatcher.com` and `*.lhr.life`, and the out-of-band test hosts in the same detection rules | A 38-package npm campaign in May 2026 used them | [Panther](https://panther.com/blog/frankly-malicious-inside-a-38-package-npm-supply-chain-campaign-targeting-tech-giants) |
| Refuse `*.trycloudflare.com`, and lookalikes such as `scan.aquasecurtiy.org` | The TeamPCP campaign of March 2026 used both | [Unit 42](https://unit42.paloaltonetworks.com/teampcp-supply-chain-attacks/) |
| Refuse `oast.fun` | An August 2026 dependency-confusion advisory | [advisory](https://github.com/advisories/GHSA-j8vq-ph59-27pv) |
| Refuse Discord webhooks and the Telegram bot API by path | npm and PyPI campaigns sent data to both | [Datadog](https://securitylabs.datadoghq.com/articles/mut-8964-an-npm-and-pypi-malicious-campaign-targeting-windows-users/), [Socket](https://socket.dev/blog/weaponizing-discord-for-command-and-control) |
| Refuse a name whose confusable skeleton equals an allowed name's | Lookalike domains | [UTS #39](https://www.unicode.org/reports/tr39/) |
| Refuse long random labels | DNS tunnels put encoded data in labels | [Unit 42](https://unit42.paloaltonetworks.com/detecting-dns-tunneling-campaigns/) |
| Tag a domain 32 days old or less, when the user turns RDAP on | Over 70% of newly registered domains were malicious, suspicious or not safe for work | [Unit 42](https://unit42.paloaltonetworks.com/newly-registered-domains-malicious-abuse-by-bad-actors/), [RFC 9083](https://www.rfc-editor.org/rfc/rfc9083) |
| Never treat popularity as safety | Discord, GitHub and Pastebin are popular, and attacks used all three. tj-actions was caught by a call to `gist.githubusercontent.com`. | [StepSecurity](https://www.stepsecurity.io/blog/harden-runner-detection-tj-actions-changed-files-action-is-compromised), [Datadog](https://securitylabs.datadoghq.com/articles/shai-hulud-2.0-npm-worm/) |

The tool ships no popularity list. The licence of the combined Tranco list is not stated, and one of its sources is CC BY-NC 4.0 ([Tranco](https://tranco-list.eu/)).

### Terminals beside the agent

Docker Sandboxes gives `sbx exec -it <name> bash` ([usage](https://docs.docker.com/ai/sandboxes/usage/)). The hosted VM service gives `ssh <id>`. GitHub Codespaces gives `gh codespace ssh` ([gh codespace](https://cli.github.com/manual/gh_codespace)), and Modal gives `modal shell` ([Modal](https://modal.com/docs/reference/cli/shell)). In each, the shell shares the sandbox's files and limits. None gives a terminal its own network policy inside one sandbox.

### The choices

**Layers, and who decides.** Three options:

1. A deny in any layer wins, as in Docker Sandboxes. It is simple and safe, but a session can never lift a broad deny, which the owner asked for.
2. Six layers with one shape, where the narrowest layer that matches decides, and `[lock]` holds what must never change. It gives one-off exceptions and keeps hard denies possible.
3. One policy for each session, with no shared config. It is easy to reason about, but every parallel session repeats the same rules.

Option 2. It follows the order of clig.dev, where flags beat project files and project files beat user files ([clig.dev](https://clig.dev/#configuration)). The repository's own file only narrows, for the reason that Claude Code gives.

**Sites that no rule covers.** Three options:

1. Always ask a person. It does not scale: users approve 93% of prompts, and parallel sessions stall.
2. An unknown action that each layer picks: `ask`, `deny`, `allow` with a log, or `classify`.
3. A model judge for every unknown site. It sends names to a third party by default, and research shows that judges can be fooled.

Option 2, with `ask` in the built-in layer. For `classify`, the fixed checks above run first, and the judge is optional. The judge sees structured fields, as Claude Code's classifier does, and the user's own task line, as Conseca does. Its allow never reaches past the session. `allow` with a log, then `policy learn`, follows Harden-Runner's path from audit to a baseline.

**Shells.** Inside the sandbox, sharing its policy, because processes in one sandbox can reach each other. A shell on the host would need the host to run code in a folder that the sandbox can write.

**SSH keys.** The original request named the SSH agent as a door that a grant opens, but the first spec said "never". A sandbox now gets an agent that the supervisor serves, which holds only the keys that a layer names. The laptop's own agent still never enters a sandbox, because it holds every key and its forward ends when the laptop sleeps.

### What this section could not verify

- The exact host list of Docker Sandboxes' Balanced preset.
- The latency and the cost of Claude Code's classifier. Anthropic publishes neither.
- Whether a judge that sees only destinations makes errors at the rates that judges of whole actions make. No study tests one.
- That ngrok and transfer.sh appear in 2024 to 2026 supply-chain incidents. They appear in detection rules, not in incident reports.
- The licence of the combined Tranco list.
- Which OpenSSH release first sends the `session-bind@openssh.com` extension.
- The 2026 DNS incidents at OpenAI and Check Point that one paper cites.

## Later changes in the spec

The spec and the plan came after this note, and changed some of its choices. This list names each change, so that a reader of this note knows which text wins. The spec wins.

| This note says | The spec now says | Where |
| --- | --- | --- |
| Policy layers: "A deny always wins" (L4, item 5). | The narrowest layer with a matching rule decides. In that layer, a deny wins. A session can allow a host that all sessions deny. | [Spec 8.2](../contributing/isolation-spec.md#82-layers-and-precedence), rule 2 |
| A trusted repository file is a layer. | A repository's own file is untrusted. It only narrows, and its allows are proposals. | [Spec 8.5](../contributing/isolation-spec.md#85-a-repositorys-own-file) |
| The egress process strips the headers that make the vendor fetch a URL. | The model API guard parses the request body, and refuses a request with a server-side fetch tool. | [Spec 7.2](../contributing/isolation-spec.md#72-network) |
| Alerts `key_misuse` and `key_minted`. | `secret_misuse` and `secret_minted`. | [Spec 10.8](../contributing/isolation-spec.md#108-alerts) |
| Pushes go only under `refs/heads/agent/<session>/`. | The push door's prefix is `refs/heads/<name>/<id>/`. | [Spec 7.2](../contributing/isolation-spec.md#72-network) |
| Approvals never offer "always". | The policy sheet offers a lasting rule for the repository or the profile, and never for all sessions. | [Spec 9.4](../contributing/isolation-spec.md#94-the-launcher) |
| Per-session spend and rate caps. | A token budget, `network.model_budget`. The tool shows no prices. | [Spec 17.8](../contributing/isolation-spec.md#178-model-usage-and-budgets-p216) |
| Package manager settings in phase 0. | Task P2.8, in phase 2. | [Spec 7.10](../contributing/isolation-spec.md#710-package-managers-in-the-guest-p28) |
| The guest sets `GOFLAGS=-mod=readonly` (8.1). | Go has acted as if `-mod=readonly` were set since 1.16. The guest sets `GOTOOLCHAIN=local`, `GOPROXY` without `direct`, and `GOVCS=*:off`. | [Spec 7.10](../contributing/isolation-spec.md#710-package-managers-in-the-guest-p28), [build facts 3](build-facts.md#3-package-managers-and-registries) |
| The guest writes the cargo publish-age setting (8.1). | cargo has no stable setting. Only the registry gate covers crates. | [Spec 7.11](../contributing/isolation-spec.md#711-registry-gates-p71) |
| The firewall drops versions with git or URL dependencies (8.1). | The gate refuses a tarball that a package manager fetches from a host that is not a registry. A git dependency looks like any clone, so the gate cannot see it. | [Spec 7.11](../contributing/isolation-spec.md#711-registry-gates-p71) |
| Shared caches, read-only. | Kept caches belong to one sandbox and are never shared. | [Spec 17.5](../contributing/isolation-spec.md#175-kept-caches-p115) |
| The guest writes the ring tail to disk at a stop. | No screen is kept across a stop. | [Question 10](../contributing/isolation-spec.md#15-open-questions) |
| The holder runs as a guest user apart from the agent. | On a process wall, both run as uid 1000. On a microvm, the holder is uid 900 and the agent uid 1000. | [Spec 17.10](../contributing/isolation-spec.md#1710-root-inside-a-microvm-p34) |
| The floor is `wall.floor`. | The key is `wall_floor` in `hosts.toml`. | [Spec 8.4](../contributing/isolation-spec.md#84-hoststoml) |
| Root step (b), the `AppArmor` profile, is gone. | A root-owned launcher gets the profile, and starts the guest init in a child profile with no capabilities, as Ubuntu's own bubblewrap profile does. | [Spec 14.4](../contributing/isolation-spec.md#144-privileges-the-root-step), [build facts 4](build-facts.md#4-linux-walls) |
| `doctor` lowers the level or warns on a kernel with a known local root bug. | `doctor` warns, and the user decides. | [Question 7](../contributing/isolation-spec.md#15-open-questions) |
| An E2B host runs at level `none`. | An E2B host gets the probe's answer: `gvisor` or `process` where it passes, else `none`. Step 1 of P7.4 runs the probe there, and the level stays UNVERIFIED until then. | [Spec 11](../contributing/isolation-spec.md#11-platform-matrix-and-fallbacks), [spec 21.8](../contributing/isolation-spec.md#218-a-machine-for-each-session-p74) |
| CVE-2026-39825, with no mark. | The spec marks it UNVERIFIED. | [Spec 7.2](../contributing/isolation-spec.md#72-network) |
| Stop running host git in a session's repository (9.1, item 5). | The host runs git in an unwalled session's repository with hooks and fsmonitor off, in a scope under the sessions' slice. It runs no git where a sandbox can write. | [Spec 19.3](../contributing/isolation-spec.md#193-safe-copies-and-host-git-p07), [question 77](../contributing/isolation-spec.md#15-open-questions) |
| A microvm's guest memory equals the session's cap, or the cap less about 64 MiB. | The spec gives a memory budget with each part. | [Spec 20.10](../contributing/isolation-spec.md#2010-the-memory-of-a-microvm-p33-p38) |
| Images are EROFS from `mkfs.erofs --tar`, or ext4 from `tar2ext4`. | User images and the agents disk are ext4 from `tar2ext4`. Only the tools image is EROFS, and CI builds it. | [Spec 20.2](../contributing/isolation-spec.md#202-images-p32) |
| Cloud Hypervisor v52.0 or later. | The pin is v54.0, and v52.0 is the advisory floor. | [Spec 20.3](../contributing/isolation-spec.md#203-the-cloud-hypervisor-driver-p33) |
| The client places each session by labels, among other facts (Placement). | Labels are metadata only. The invoker picks a host by name, and a rule fills in when the invoker names none. | [Spec 18.2](../contributing/isolation-spec.md#182-picking-a-host-h19) |
| `host add` stays the only way in (Attach, item 1). | `host invite` and `host join` add a second way in. The laptop still starts every connection. | [Spec 18.3](../contributing/isolation-spec.md#183-a-machine-that-joins-h20) |

## Sources and method

The main search ran seven expansions on 9 November 2026. Each sent five researchers in parallel, one per option, and two judges scored the five. Researchers read official docs, specifications, security advisories, release notes, papers and public repositories, and read this repository's code. A red team and a fact check then read the best path, its research and its scores. The fact check re-read the claims that the design leans on, and checked feasibility on each platform. Two writers then wrote the drafts, and the main search used 53 agents in all.

The hosts search ran four expansions in the same way. One critic then served as its red team and its fact check, and one writer wrote its draft. It used 30 agents in all ([Your own hosts](#your-own-hosts)).

The second pass, [Policy for parallel work](#policy-for-parallel-work), ran on 10 November 2026. Three researchers read product docs, papers, egress tools and incident reports in parallel. The numbers that the design leans on were checked against their sources: Claude Code's auto mode, the independent stress test of it, Codex's auto-review and the sandbox runtime's decider.

The weight sensitivity in [section 3.6](#36-weight-sensitivity) was computed on 10 November 2026, from the judges' scores of both searches. It used no new research.

For this note, some incidents and escape bugs were checked again on 9 November 2026, against their advisories and write-ups. They are Amazon Q (AWS-2025-015), chalk and debug, VsockDrop, PwnEscape, Vghost, the two Cloud Hypervisor advisories and the Docker Sandboxes `sbx cp` bug. Code facts about this repository were checked against the files they name.

No account was made, no API key was used and no live authenticated API call was made. No VM, container or sandbox was started, and nothing was installed or built. The figures for npm metadata sizes and OSV record counts come from public endpoints that a researcher fetched on 9 November 2026. Every other figure comes from the cited source. Links to code in this repository point to files, not lines, because lines drift.
