# Parity with Docker Sandboxes and a hosted VM service

Researched on 10 November 2026.

This note lists every user-facing feature of Docker Sandboxes (`sbx`, v0.51.0) and of a hosted VM service, and says, for each one, what this project does: today, in [the isolation plan](../contributing/isolation-plan.md), in a task that this note adds, or not at all, and why. It also compares the three on performance and on safety, because the owner asked that performance, function and safety all count. The design that follows is in [the spec](../contributing/isolation-spec.md), section 17, and in the plan's tasks P1.14 to P1.16 and P2.15 to P2.17, with new parts of P1.9, P1.11 and P3.4. An independent reviewer then checked the catalogues against this note, and its corrections are in.

The rules for the note:

- Facts about Docker Sandboxes come from its docs, its CLI reference and its release notes. Facts about the hosted VM service come from its public docs, read on 10 November 2026. This note does not name that service or link to it. Every other row links its source. A researcher read all 112 pages of the `sbx` CLI reference and the whole of the hosted VM service's docs.
- **UNVERIFIED** marks a claim that no primary source confirmed. User reports in a project's issue tracker are marked as such.
- The status words:
  - **Built:** in today's tool.
  - **Planned:** in a task of the isolation plan or the CLI plan before this note.
  - **New:** in a task that this note adds.
  - **Differs:** the same need, met another way, and the row says how.
  - **No:** not planned, with the reason. Each one is an open question with a default in the spec.
  - **Hosted only:** it makes sense only for a service that hosts machines and bills for them.

## Summary

1. **The tables list 135 features of the two products.** Counted by the first status in each row: 14 are built today, 39 are planned already, 32 are new in this note, 34 meet the same need another way, 10 are not planned, and 6 make sense only for a hosted service. The new ones go into 6 new tasks, new parts of 3 existing tasks, and one task of the CLI plan.
2. **The new tasks.** Sizes, variables, files copied in, seeds from the laptop and git identity (P1.14). Package caches that survive a restart (P1.15). Throwaway and counted runs, runs in CI, `prune`, an event stream, an `otlp` alert format and a run time limit (P1.16). Editors over SSH (P2.15). Model usage and budgets (P2.16). More agent rows (P2.17). New parts of existing tasks: a reverse forward of one laptop port (P1.9), setup and stop scripts inside the sandbox (P1.11), and root inside a microvm as a setting (P3.4). Docs for agents (C12 in [the CLI plan](../contributing/cli-plan.md)).
3. **What stays out, and why.** A public URL needs a relay or a listener, which the project's rules forbid, so the docs show the user's own tunnel instead. A GPU or `/dev/kvm` inside a sandbox widens the wall. An SDK or a REST API would need a listener, and the CLI's `--json`, exit codes and the event stream serve scripts instead. A host-side MCP gateway would run stdio servers outside the wall. OAuth refresh and cloud keys that need signing wait for later research. Copying images between hosts stays forbidden. Each is an open question with a default ([questions 15, 35 and 55 to 65](../contributing/isolation-spec.md#15-open-questions)).
4. **Performance.** No product publishes the time to a ready agent. Docker says "seconds". The hosted VM service gets a machine in under 1 s, and says "a few seconds" for resume and fork. Published research shows what is possible: a Cloud Hypervisor guest with a full userspace booted at 0.6 s P50 after direct kernel boot and a small init ([Depot](https://depot.dev/blog/optimizing-microvm-boot-times)). This design already boots its own init, with no firmware, cloud-init or systemd. Two gaps matter more: caches lost at every start, and HTTP/1.1 on intercepted hosts. Both are fixed below.
5. **Safety.** This design closes every door by default but one: text that a sandbox writes to the clipboard with OSC 52 reaches the laptop, because the bytes pass unchanged, and Docker made that opt-in in v0.51.0. Docker Sandboxes forwards the SSH agent, mounts the working folder read and write and sends telemetry, all by default, and runs on the host the stdio MCP servers that the user registers. The hosted VM service leaves outbound traffic open, and puts secrets in the VM when an environment passes them, though its onboarding withholds them by default. Docker's own 2026 advisories show where walls leak: copies that follow planted symlinks, a file server's symlink escape, a socket relay race and DNS exfiltration ([section 6](#6-safety)). The design takes a lesson from each.

## 1. Method

- Three researchers worked in parallel on 10 November 2026: one on Docker Sandboxes, one on the hosted VM service, and one on editor access. A fourth gathered performance facts from papers, maintainers and benchmarks.
- The Docker researcher read every page under the Sandboxes and Governance docs, all 112 CLI reference pages and the release notes back to v0.21.0. The other read the hosted VM service's whole docs and index.
- Key claims were checked again against the saved pages before this note used them.
- Each row maps a feature to this design by reading [the spec](../contributing/isolation-spec.md) and today's code.

## 2. Docker Sandboxes, feature by feature

Sources: `R/` is the [CLI reference](https://docs.docker.com/reference/cli/sbx/), `S/` is the [Sandboxes docs](https://docs.docker.com/ai/sandboxes/), and `G/` is the [Governance docs](https://docs.docker.com/ai/governance/). The full paths are in each row.

### 2.1 Lifecycle and the command line

| Feature | In Docker Sandboxes | Here | Status |
| --- | --- | --- | --- |
| Create and attach | `sbx run AGENT [PATH]`, with `--name`, `-d`, `-e`, `--cpus`, `-m`, `-p`, `-t` ([run](https://docs.docker.com/reference/cli/sbx/run/)) | `new`, with `--agent`, `--dir`, `--name`, `--detach` | Built. Sizes and variables are New (P1.14). |
| Create without attaching | `sbx create` | `new --detach` | Built |
| A throwaway sandbox | `sbx run --rm` | `new --rm` | New (P1.16) |
| List | `sbx ls --format json` | `ls --json` | Built |
| Stop, delete | `sbx stop`, `sbx rm` with a prompt ([rm](https://docs.docker.com/reference/cli/sbx/rm/)) | `stop`, `rm` with a prompt | Planned (P1.11, C5) |
| Remove stopped sandboxes | `sbx prune --filter until=168h` ([prune](https://docs.docker.com/reference/cli/sbx/prune/)) | `prune --stopped-before 7d` | New (P1.16) |
| Reset everything | `sbx reset` signs out and clears all state ([reset](https://docs.docker.com/reference/cli/sbx/reset/)) | `host rm --purge` for one host. No reset of the laptop's config. | Differs |
| Run one command | `sbx exec`, exit 125 and 127 for the tool's own failures ([exec](https://docs.docker.com/reference/cli/sbx/exec/)) | `exec`, which returns the command's code | Planned (P1.13) |
| Copy files | `sbx cp`, absolute sandbox paths, no escaping symlinks ([cp](https://docs.docker.com/reference/cli/sbx/cp/)) | `cp` | Planned (P5.2). Its acceptance now names Docker's symlink bug. |
| Publish ports | `sbx ports NAME -p 8080:3000`, on loopback ([ports](https://docs.docker.com/reference/cli/sbx/ports/)) | `forward`, to the laptop's loopback | Built |
| Sign in | `sbx login`, required ([faq](https://docs.docker.com/ai/sandboxes/faq/)) | No account | Hosted only |
| Diagnose | `sbx diagnose [--upload]` | `debug bundle`, which writes a local archive and never uploads | Built |
| Update the CLI | Through Docker Desktop or the package manager | The signed installer or the package manager. No background update check. | Differs |
| Daemon control | `sbx daemon start\|stop\|status` | No daemon on the laptop | Differs |
| Completion | `sbx completion` | `completion`, with values from C10 | Built, then Planned (C10) |
| Dashboard | `sbx` or `sbx tui`, with CPU, memory and a network panel ([usage](https://docs.docker.com/ai/sandboxes/usage/)) | The launcher, with the policy sheet and `policy log` | Built, then Planned (P1.8) |
| No prompts in scripts | `--no-input` | `--no-input` | Planned (C4) |
| Machine output | `--format json\|yaml`, `-q`, data on stdout ([automation](https://docs.docker.com/ai/sandboxes/workflows/automation/)) | `--json` on every command that prints data | Planned (C4) |
| An event stream | `sbx events --json`, mentioned in the docs but not in the reference | `events --follow --json` | New (P1.16) |

### 2.2 Workspaces

| Feature | In Docker Sandboxes | Here | Status |
| --- | --- | --- | --- |
| Direct mode: the working folder, read and write, live | The default, over virtiofs ([architecture](https://docs.docker.com/ai/sandboxes/architecture/)) | `--mount <path>:rw` on a process wall, for a folder that is not in a git repository. A microvm has no folder sharing in phase 3. | Differs. The default is a clone. A writable repository is refused, because git follows `.git/commondir` to hooks and config that the guest could plant, and the host's next `git` command would run them. Docker lost a nested read-only mount after host edits ([issue 388](https://github.com/docker/sbx-releases/issues/388)). |
| Clone mode | `--clone`, with the repository read-only at `/run/sandbox/source` | The default: a clone from a git bundle that the trusted side makes | Planned (P1.6) |
| No workspace | `sbx create AGENT` | A folder outside git gives an empty workspace | Planned (P1.6, question 51) |
| More folders, read-only | `PATH:ro`, `PATH:TARGET` ([usage](https://docs.docker.com/ai/sandboxes/usage/)) | `--mount path[:guest][:ro\|:rw]` | Planned (P1.6) |
| Start in a folder | `-w` | `--dir` | Built |
| A folder from the laptop | The laptop is the host, so every mount is local | `--seed <laptop folder>` sends a git bundle from the laptop | New (P1.14) |
| More than one repository | Several direct workspaces | `--seed` repeats, and `pull --repo` brings back one repository | New (P1.14) |

### 2.3 Kits, templates and environment files

| Feature | In Docker Sandboxes | Here | Status |
| --- | --- | --- | --- |
| Kits: tools, rules, credentials, startup, files, ports, variables, as an OCI image ([customise](https://docs.docker.com/ai/sandboxes/customize/)) | `sbx run --kit REF`, `sbx kit add`, `kit-arg`, kit sets | Profiles for rules and settings, images for tools, the agent table for agents, `copy_in` for files | Differs. Profiles are plain TOML in the user's config, and images are pinned by digest. |
| Kit signatures | `sbx kit sign\|verify`, off by default ([sign](https://docs.docker.com/reference/cli/sbx/kit/sign/)) | Image signatures under `[sandbox.image_policy]` | Planned (P4.5) |
| Persistent kit volumes that survive `rm` | Since 0.50.0 ([use kits](https://docs.docker.com/ai/sandboxes/customize/use-kits/)) | Kept caches survive a stop, not `rm`. A named host folder is a mount. | Differs |
| Environment files with host lifecycle commands | `sbx env plan\|run`, experimental ([environment files](https://docs.docker.com/ai/sandboxes/configuration/environment-files/)) | Profiles, and setup, resume and stop scripts that run inside the sandbox | New (P1.11). Nothing that a repository sets runs on the host. |
| Save a sandbox as a template | `sbx template save NAME TAG [-o file.tar]`, which keeps secrets written to files ([save](https://docs.docker.com/reference/cli/sbx/template/save/)) | `image save`, after a review that scans for secret values | Planned (P5.5) |
| Load and share templates | `sbx template load`, `-o tar` | None. A saved image stays on the host that made it, and registry images come by a digest that the laptop pins. | No ([question 35](../contributing/isolation-spec.md#15-open-questions)). An image from one host could carry that host's poison to the others. |
| Registry templates, cache, mirror | `sbx run -t REF`, `platform.images.registryMirror` ([settings](https://docs.docker.com/ai/sandboxes/configuration/settings/)) | `image pull`, pinned in `images.lock`, `[images] fetch` | Planned (P3.2, H9) |
| A private registry image | `sbx secret set --registry` | The laptop pulls it with its own login and copies it to the host | New ([question 65](../contributing/isolation-spec.md#15-open-questions)) |

### 2.4 Network

| Feature | In Docker Sandboxes | Here | Status |
| --- | --- | --- | --- |
| Presets | Open, Balanced, Locked Down, chosen at the first run ([local policy](https://docs.docker.com/ai/governance/local/)) | `open`, `balanced`, `locked`, with `balanced` built in, and the unknown action chosen at setup | Planned (P1.7, P2.11) |
| Rules for all sandboxes or one | `sbx policy allow\|deny network [--sandbox N]`, and a deny always wins ([allow](https://docs.docker.com/reference/cli/sbx/policy/allow/network/)) | `policy allow\|deny` with `--in`, `--profile`, `--repo`, `--host` or `--all`. The narrowest layer wins, so a session can lift a broad deny unless a lock holds it. | Planned (P1.12). Weaker than Docker's rule on denies, by the owner's choice. Locks give back "deny always wins" where the user wants it. |
| HTTP rules by method, path and query | `--method`, `--path`, `--query` ([rules](https://docs.docker.com/ai/governance/reference/network-rules/)) | Method and path rules on intercepted hosts | Planned (P2.2). Query rules are left out: the egress process refuses long queries instead. |
| Check a target | `sbx policy check network\|http` | `policy check` | Planned (P1.12) |
| Log | `sbx policy log` | `policy log` | Planned (P1.8) |
| Approvals | `sbx policy approval respond ID --option allow` ([approvals](https://docs.docker.com/ai/governance/local/approvals/)) | A hold, answered in the policy sheet, or by `policy allow` during the hold: once, the session, the repository or the profile | Planned (P2.6) |
| Automatic decisions | None | The classifier | Planned (P2.11, P2.12) |
| UDP | Experimental | DNS only, through the egress process | No. UDP gives a second path out that the proxy cannot read. |
| DNS that follows the policy | Since 0.33.0, after CVE-2026-12039 ([CVE](https://www.cve.org/CVERecord?id=CVE-2026-12039)) | Unknown names get `NXDOMAIN` and are never sent upstream | Planned (P2.1) |
| Reach a service on the laptop | `host.docker.internal` with a rule ([development](https://docs.docker.com/ai/sandboxes/workflows/development/)) | `forward --reverse <laptop port>`, on `own` hosts, through the bridge, with no listener on the host | New (P1.9, [question 58](../contributing/isolation-spec.md#15-open-questions)) |
| An upstream proxy, PAC, NTLM or Kerberos | Experimental ([upstream proxy](https://docs.docker.com/ai/sandboxes/configuration/upstream-proxy/)) | The egress process uses the host's own `HTTPS_PROXY` | Differs. PAC and NTLM are not planned. |
| Open the host's browser from the sandbox | `browser.enabled`, on by default | A `BROWSER` helper that prints the URL in the session, and `forward` for a callback port | New (P1.14). A sandbox never drives the laptop. |

### 2.5 Credentials and SSH

| Feature | In Docker Sandboxes | Here | Status |
| --- | --- | --- | --- |
| Service secrets swapped by a proxy | `sbx secret set anthropic\|openai\|github…`, and the sandbox sees `proxy-managed` ([credentials](https://docs.docker.com/ai/sandboxes/configuration/credentials/)) | Bound secrets with sentinels of the same shape, and an identity pin | Planned (P2.3) |
| Custom secrets for named hosts | `sbx secret set-custom --host`, headers only ([set-custom](https://docs.docker.com/reference/cli/sbx/secret/set-custom/)) | `[secrets.NAME] hosts = […]` | Planned (P2.3) |
| Dynamic sources | `op://`, an AWS ARN or a command, cached 55 minutes | A command that the laptop runs at each start and arm | Built for the command form. Refresh during a session is a question already ([question 15](../contributing/isolation-spec.md#15-open-questions)). |
| OAuth sign-in on the host, refreshed by the proxy | `sbx secret set openai --oauth` | Long-lived tokens only, such as `claude setup-token`. A ChatGPT login has none. | No ([question 15](../contributing/isolation-spec.md#15-open-questions)). A broker that refreshes tokens is later research. |
| Cloud provider keys: AWS SigV4, GCP service accounts, Bedrock and Vertex | Docker: dynamic sources from AWS Secrets Manager. The hosted VM service: Bedrock keys | None: a header swap cannot sign a request | No ([question 15](../contributing/isolation-spec.md#15-open-questions)). They need a re-signing door. |
| Registry credentials | `sbx secret set --registry` | Bound secrets for registry hosts, and package manager settings | Planned (P2.3, P2.8) |
| Approval of each credential binding | `credentials.yaml`, withheld without a terminal | The grant gate and the tier of each host | Planned (H5) |
| SSH agent forwarding | On by default ([credentials](https://docs.docker.com/ai/sandboxes/configuration/credentials/#ssh-agent)) | Off. Named keys through the sandbox's own agent. | Planned (P2.13) |
| SSH into a sandbox for editors | `sbx setup ssh`, then `ssh NAME.sbx`, and SSH ends in Docker's daemon ([integrations](https://docs.docker.com/ai/sandboxes/integrations/)) | `ssh-config`, then `ssh <session>.<host>.<name>`, and SSH ends in the client | New (P2.15) |
| Desktop apps over SSH: Claude Desktop, ChatGPT, T3 Code | `claude.sbx`, `codex.sbx` ([integrations](https://docs.docker.com/ai/sandboxes/integrations/)) | Any SSH client that honours `ProxyCommand` | New (P2.15) |

### 2.6 Inside the sandbox

| Feature | In Docker Sandboxes | Here | Status |
| --- | --- | --- | --- |
| Variables | `-e K=V`, `--env-file`, `/etc/sandbox-persistent.sh` ([usage](https://docs.docker.com/ai/sandboxes/usage/)) | `--env`, `--env-file`, `env` in any layer | New (P1.14) |
| Identity variables | `SANDBOX_NAME`, `SANDBOX_ID`, `SBX_AGENT` | The session, the host and the agent, from `brand.Env` | New (P1.14) |
| Sizes | `--cpus`, `-m`. The default is all CPUs and half the host's memory ([run](https://docs.docker.com/reference/cli/sbx/run/)) | `cpus` and `memory` in any layer, and admission | New (P1.14) |
| Disk size | 20 GB for the root and 10 GB for Docker ([troubleshooting](https://docs.docker.com/ai/sandboxes/troubleshooting/)) | `disk_max`: a hard cap on a microvm, and a soft cap that the supervisor checks on a process wall | Planned and New (P3.3, P1.14) |
| Root inside | The agent has sudo in the VM ([isolation](https://docs.docker.com/ai/sandboxes/security/isolation/)) | The user `agent`, not root. `sandbox.sudo` on a microvm only. | New (P3.4, [question 60](../contributing/isolation-spec.md#15-open-questions)) |
| Docker inside | A private Docker Engine in each sandbox | `--docker` on a microvm | Planned (P3) |
| FUSE | On by default | None on a process wall. In a microvm guest with `sandbox.sudo` or `--docker`, where the guest kernel is inside the wall. | Differs ([question 57](../contributing/isolation-spec.md#15-open-questions)) |
| GPU | NVIDIA through VFIO, experimental ([GPU](https://docs.docker.com/ai/sandboxes/configuration/gpu-passthrough/)) | None | No ([question 57](../contributing/isolation-spec.md#15-open-questions)) |
| Local models | `llmman` or Ollama, experimental ([models](https://docs.docker.com/ai/sandboxes/configuration/models/)) | A model on the host or the LAN, through a `net` rule on an `own` host | Differs |
| Display and USB | Environment files, off by default | An image with a virtual display, reached with `forward` | Differs ([question 55](../contributing/isolation-spec.md#15-open-questions)) |

### 2.7 MCP, skills and agents

| Feature | In Docker Sandboxes | Here | Status |
| --- | --- | --- | --- |
| An MCP gateway on the host for each sandbox ([MCP gateway](https://docs.docker.com/ai/sandboxes/mcp-gateway/)) | `sbx mcp add --url\|--command`, dynamic discovery, OAuth | Stdio MCP servers run inside the sandbox. URL servers work through a `net` rule and a bound secret. | Differs ([question 64](../contributing/isolation-spec.md#15-open-questions)). Docker runs the stdio servers that the user registers on the host, outside the wall. |
| A shared skills store | `sbx skills add`, mounted read-only by default ([skills](https://docs.docker.com/ai/sandboxes/workflows/agent-skills/)) | `copy_in` of the laptop's skills folder at each start | New (P1.14) |
| Agents | claude, codex, copilot, cursor, devin, docker-agent, droid, gemini, kiro, opencode, shell ([run](https://docs.docker.com/reference/cli/sbx/run/)) | The agent table has claude, codex, opencode, antigravity, gemini, amp, cursor, aider, goose and shell | Built. GitHub Copilot CLI, Kiro and Droid are New rows (P2.17). |
| Agents in full-autonomy mode | By default ([faq](https://docs.docker.com/ai/sandboxes/faq/)) | The tool never changes an agent's permissions. A user may pass the agent's own flags. | Differs |
| Agent context from a kit | The `agent-context` capability ([capabilities](https://docs.docker.com/ai/sandboxes/customize/author/capabilities/)) | The repository's own `AGENTS.md` and `CLAUDE.md`, and `copy_in` for the user's own | Differs |
| Git identity | The `git-identity` capability | The laptop's `user.name` and `user.email` in every clone's config | New (P1.14) |

### 2.8 Cloud, governance and platforms

| Feature | In Docker Sandboxes | Here | Status |
| --- | --- | --- | --- |
| Cloud sandboxes, sizes, TTL, volumes, `move` | `--cloud`, with a paid plan ([cloud](https://docs.docker.com/ai/sandboxes/cloud/)) | Hosts that the user owns, and providers through `host create` | Hosted only |
| Organisation policies in Cedar, sign-in enforcement, SIEM audit | A paid plan ([organisation](https://docs.docker.com/ai/governance/organization/)) | Locks in the user's config, and the audit log on each host | Hosted only. Locks cover one person's need. |
| Platforms | macOS 14 on Apple silicon, Windows 11, Ubuntu 24.04 with KVM ([install](https://docs.docker.com/ai/sandboxes/install/)) | Linux hosts at the level they can run, with the client on macOS and Linux | Differs |
| Telemetry | On, with an opt-out ([faq](https://docs.docker.com/ai/sandboxes/faq/)) | None | No, by rule |

## 3. The hosted VM service, feature by feature

The facts come from the service's public docs, read on 10 November 2026. Commands are shown without the service's own program name.

### 3.1 Machines and lifecycle

| Feature | In the hosted VM service | Here | Status |
| --- | --- | --- | --- |
| Sizes | `--type small\|default\|large\|xlarge`, from 2 vCPU and 4 GB to 16 vCPU and 32 GB | `cpus` and `memory` in any layer, so a profile can name a size | New (P1.14) |
| Change size | Stop, then `resume --type` | A new size applies at the next start | New (P1.14) |
| Root, systemd, Docker | Passwordless `sudo`, real systemd, Docker 29.7 | `sandbox.sudo` and `--docker` on a microvm. The guest init is ours, not systemd. | New (P3.4) and Planned (P3) |
| `/dev/kvm` in every sandbox | QEMU, Firecracker, Android | None | No ([question 57](../contributing/isolation-spec.md#15-open-questions)) |
| One image, pre-installed tools and agents | Ubuntu 24.04, Node, Python, Go, Rust and 10 agent CLIs | Any registry image, pinned by digest. The default image is small, and the agents disk brings the host's agents. | Differs (P3.2, P3.5) |
| A public address for each sandbox | IPv6, and IPv4 for outbound | No network card | No, by design |
| Create with options | `new` with `--ttl`, `-e`, `--environment`, `--setup-file`, `--from`, `--count`, `--file` | `new` with `--env`, `--profile`, `--image`, `--count`, `--copy` | New (P1.14, P1.16) |
| Auto-stop after a fixed time | 1 hour by default, not an idle timer | `idle_stop` (P3.6), and `max_run` as a safety limit for unattended runs | Planned and New (P1.16) |
| Extend | `extend --hours` | `max_run`, set for each start | Differs |
| Stop with a pre-stop hook | A pre-stop script in the home folder, 60 s | `.<name>/stop` inside the sandbox, within `stop_grace` | New (P1.11) |
| A warm window of 10 minutes after a stop | Memory and processes kept, not guaranteed | A sandbox keeps running while detached. A stop is explicit, or after `idle_stop`. | Differs. No memory image ever exists, so no secret is ever written. |
| Resume | `resume`, like a reboot, with a new IP and host key | `attach` or `resume` wakes a stopped sandbox, and its sessions start again | Planned (P1.11) |
| Services that come back | Enabled systemd units start again after resume and fork | Sessions in the sandbox started with a command come back with it | New, in spec 6.11 |
| Fork | `fork`, from the last minutely snapshot | `fork --count`, with a reflink copy | Planned (P5.1) |
| Delete | `delete --yes`, asynchronous | `rm` | Planned (P1.11) |
| Readiness states | provisioning, cloning, ready, hydrated | `new` answers when the holder is ready, and `events` shows each state | Planned and New (P1.16) |
| Fail fast | `--fail-fast` gives `503 no_ready_machine` in about 1.5 s | Admission refuses at once with `no_room` | Planned (H11) |
| One-shot run | `run --push DIR -- CMD`, which deletes the sandbox and returns the exit code | `new --rm --seed . -- CMD`, which runs through `exec` when no terminal is present | New (P1.14, P1.16) |
| Push a folder | `push ID DIR` | `--seed` at the start, `cp` later | New and Planned (P1.14, P5.2) |
| The `current` alias | The last sandbox in this shell | `-` means the last session | Built |
| Idempotent create | `Idempotency-Key` header | `new` refuses a name that exists | Differs |

### 3.2 Commands, files and editors

| Feature | In the hosted VM service | Here | Status |
| --- | --- | --- | --- |
| Run a command | `exec --timeout`, exit code passed through | `exec --timeout` | Planned (P1.13) |
| Background commands | `exec --detach`, `--attach`, `--kill`, lost at a stop | A session in the same sandbox: `new --in <s> --detach -- npm run dev`, with replay, `peek` and `send` | Differs, and it survives a detach |
| Command history, even when stopped | `history` | Each session's ring of 8 MiB replays while it runs | Differs. The screen does not survive a stop ([question 10](../contributing/isolation-spec.md#15-open-questions)). |
| Processes | `ps` | `exec -- ps` | Differs |
| A files API | Read, write, list, search, replace, signed URLs | `cp` and `exec` | Differs |
| SSH | `ssh`, with a host key that changes at each resume | `shell`, and SSH for editors | Planned and New (P1.13, P2.15) |
| VS Code and Cursor | Remote-SSH, with a config script to run again after each resume | `ssh-config` writes one managed block once | New (P2.15) |
| Forward a port to the laptop | `forward --remote P` | `forward` | Built |
| Forward a laptop port into the sandbox | `forward --reverse --local P` | `forward --reverse`, on `own` hosts | New (P1.9) |

### 3.3 Sharing, desktops and snapshots

| Feature | In the hosted VM service | Here | Status |
| --- | --- | --- | --- |
| Public URLs | `host ID PORT`, token-gated, or `--public` | `forward`, and a docs recipe for the user's own tunnel on a machine that the user owns | Differs ([question 56](../contributing/isolation-spec.md#15-open-questions)). The tool runs no relay and no listener. |
| Raw TCP and UDP ports | `ufw allow` on the sandbox's own address | None | No, by design |
| Desktop streaming, VNC, browser-only, recording | Moonlight at 1080p60, noVNC | An image with a desktop and a VNC server, reached with `forward` | Differs ([question 55](../contributing/isolation-spec.md#15-open-questions)) |
| Snapshots every minute | Incremental and free | The disk lives on the user's host. Checkpoints with reflink. | Differs and Planned (P5.4) |
| Saved snapshots as templates | `snapshot ID NAME`, `new --from NAME`, which save `/home`, Docker volumes and images | `image save` keeps the root's changes. `fork` copies the whole session disk. | Planned (P5.5, P5.1) |
| Share a snapshot | A code that strips logins and keys | None | No ([question 35](../contributing/isolation-spec.md#15-open-questions)). A saved image stays on its host. |
| Download files from a stopped sandbox | `snapshot pull` | `cp` wakes the sandbox first | Differs. The host never parses a sandbox's disk. |

### 3.4 Environments, agents and credentials

| Feature | In the hosted VM service | Here | Status |
| --- | --- | --- | --- |
| Environments: repositories, variables, secret files, setup scripts | `env …` | Profiles and repository sections, a repeatable `--seed`, `copy_in`, and setup scripts inside the sandbox | Planned and New (P1.12, P1.14, P1.11) |
| Versions and upgrades | An environment version moves only on `env upgrade` | Config files that the user can keep in git, and `policy apply` | Differs |
| Safe for third parties | Withholds every secret | The `shared` tier, `--untrusted`, and `!NAME` in a layer | Planned (H4, P1.12) |
| Private registries | npm, pip, uv, Docker | Bound secrets for registry hosts, and package manager settings | Planned (P2.3, P2.8) |
| Integrated agents and models | `prompt --provider`, `--model`, `--fast` | The agent's own screen, its own flags, and `new -- <agent> -p "task"` for a headless run | Differs |
| Steer, interrupt | `steer`, `interrupt` | `send`, and `send --no-enter` with a control key | Built |
| Events | `events -f`, by polling | `events --follow` | New (P1.16) |
| More agents | pi, Kimi, Mistral Vibe, Prime, plus pre-installed Hermes and OpenClaw | Rows for pi, Kimi Code and Mistral Vibe | New (P2.17) |
| Computer use | An MCP server named `computer` | An image with a virtual display and a computer-use MCP server | Differs ([question 55](../contributing/isolation-spec.md#15-open-questions)) |
| An AI key proxy | Off by default, $1 a day | Bound secrets and the identity pin, on by default once phase 2 ships | Planned (P2.3) |
| AI usage | Only for proxied sandboxes | Tokens counted from the model API's own answers, with budgets | New (P2.16) |
| Switch accounts when credits run out | `ai switch` | Change what the secret's command prints, then attach, which arms the session again with the new value | Differs |

### 3.5 Integration, teams and data

| Feature | In the hosted VM service | Here | Status |
| --- | --- | --- | --- |
| A REST API and SDKs | An HTTP API, and SDKs for Python and TypeScript | The CLI with `--json`, exit codes and `events`. A library can drive it. | Differs ([question 59](../contributing/isolation-spec.md#15-open-questions)). A server would listen. |
| Scoped API keys | Presets, deny by default | No service. Tiers and policy limit each host and session. | Hosted only |
| Webhooks | ready, error, archived, degraded, recovered | Alerts to ntfy, a webhook or a command, with new sandbox events | Built, and New events (P1.16) |
| OpenTelemetry | OTLP metrics and lifecycle logs | An `otlp` alert format that sends events to a collector that the user names | New (P1.16) |
| Teams, seats, sharing | Organisations | A `shared` host for a borrowed machine. Team hosts are question 41. | Hosted only |
| Billing, plans, limits | Per second, from $20 a month | The user's own machines | Hosted only |
| Retention, zero retention, erasure | 14 days for a stopped disk | Data stays on the user's hosts. `rm` deletes, and `host rm --purge` wipes. | Differs |
| CI runners, evals | A runner label for CI, and `run` | `new --rm`, `--count` and `--json` in any CI | Differs |
| Windows apps with Wine | Recipes | Works in an image that has Wine | Differs |
| Docs for agents | `llms.txt`, Markdown pages, an MCP docs server | `llms.txt` and Markdown pages on the docs site | New (C12) |

## 4. The features this note adds

| Task | What | Why |
| --- | --- | --- |
| P1.14 | `cpus`, `memory`, `disk_max` on a process wall, `env`, `copy_in`, a repeatable `--seed` from the laptop, git identity, the `BROWSER` helper, and identity variables | Both products size sandboxes and set variables. A remote host needs a way to start from the repository on the laptop, as `run --push` does. |
| P1.15 | Package caches that survive a restart, inside each sandbox only | The home resets at each start, so caches went with it. A warm pnpm install takes 744 ms against 3.30 s ([pnpm](https://pnpm.io/benchmarks)). Caches are never shared between sandboxes, because some tools do not check hashes on read. |
| P1.16 | `new --rm`, `new --count`, runs without a terminal, `prune`, `events --follow`, sandbox alert events, the `otlp` alert format and `max_run` | Throwaway runs, CI, many runs at once, clean-up, and automation without polling. `max_run` caps an unattended run. |
| P2.15 | Editors over SSH, with SSH ending in the client | Docker and the hosted VM service both support VS Code and Cursor. Docker's model keeps the server out of the sandbox, and this design follows it. |
| P2.16 | Model usage and budgets | The hosted VM service counts usage only through its paid proxy. The egress process already reads model API answers. |
| P2.17 | More agent rows | Both products list agents that the table lacks. Agents are data. |
| P3.4 (changed) | `sandbox.sudo` on a microvm | Both give root inside. A microvm's wall does not rely on the guest's users. A process wall does, so it never gets root. |
| P1.9 (changed) | `forward --reverse`, and `forward` with no ports that asks | The hosted VM service forwards a laptop port in, and Docker lets a sandbox reach `localhost`. A sandbox once took another's OAuth callback port in Docker. |
| P1.11 (changed) | Setup, resume and stop scripts inside the sandbox | The hosted VM service has a pre-stop hook, and Docker runs kit lifecycle steps. Nothing a repository chooses runs on the host. |
| C12 | `llms.txt` and Markdown pages | The hosted VM service publishes docs for agents. Agents read this project's docs too. |

## 5. Performance

| Measure | Docker Sandboxes | Hosted VM service | This design |
| --- | --- | --- | --- |
| A new sandbox | "Get started in seconds", no figure ([product page](https://www.docker.com/products/docker-sandboxes/)) | A machine in under 1 s. Resume and fork in "a few seconds" | Targets: 700 ms above an unwalled session for a process wall, 1.5 s on bare metal and 4 s nested for a microvm (spec 12). A tuned Cloud Hypervisor guest reached 0.6 s P50 ([Depot](https://depot.dev/blog/optimizing-microvm-boot-times)). |
| Resume after a stop | State kept on the laptop | Within 10 minutes, the same machine. A 16 GB fork took about 2 minutes, and a resume 20 s to 32 s | A cold boot, the same as a start, then the agent's resume command |
| Fork | A template save, then a run | From the last minutely snapshot | A microvm forks with a reflink copy of one disk image file. A process wall copies its session folder, a tree: a reflink of 116,000 files took 8.5 s on XFS ([reflink](https://www.tunbury.org/2025/07/15/reflink-copy/)). |
| Keys to the screen | Local, with no network | SSH. 100 ms to 200 ms between the Americas and Europe | One SSH hop to the user's own host, plus at most 0.3 ms p50 from the supervisor (spec 12). The bridge uses no TTY channel, so OpenSSH's keystroke timing obfuscation does not apply ([OpenSSH 9.6](https://www.openssh.com/releasenotes.html), `internal/client/ssh.go`). |
| File I/O | Virtiofs by default. A user measured a Maven build at 13:12 on virtiofs, 2:39 on the VM's disk and 2:30 natively ([issue 31](https://github.com/docker/sbx-releases/issues/31), a user report) | The VM's own disk | The sandbox's own disk, with no folder sharing by default |
| Package installs after a restart | Persistent | Persistent disk | Kept caches (P1.15) |
| Proxy cost | All TCP through a host proxy, no figure | No proxy | Targets of 3 ms for a spliced flow and 10 ms for an intercepted request (spec 12). A proxy that spoke only HTTP/1.1 turned a 491 ms page into 7,374 ms, and HTTP/2 brought it to 843 ms ([write-up](https://ronaldbradford.com/blog/2026-10-12-six-connections-and-a-long-queue/)). Spec 7.2 now requires HTTP/2 towards the guest. |
| Many at once | Three parallel sandboxes were 1.47 times faster than serial in a user report ([issue 775](https://github.com/docker/sbx-releases/issues/775)) | 100 at once and 12 starts a minute on the $20 plan | As many as each host's memory admits, across every host the user adds |
| Memory | Half the host by default, per sandbox ([run](https://docs.docker.com/reference/cli/sbx/run/)) | Fixed sizes, all but 512 MiB usable | The session's cap. The spec's target for a microvm is 160 MiB or less above the agent's own use, not yet measured. KSM stays off, because page sharing leaks across guests ([CVE-2024-0564](https://access.redhat.com/security/cve/cve-2024-0564)). |

Further facts that shaped the tasks:

- Node's `fetch` reads the proxy variables only with `NODE_USE_ENV_PROXY=1`, from Node 24.0.0 and 22.21.0. Its `http` and `https` modules need 24.5.0 ([Node CLI](https://nodejs.org/api/cli.html), [24.5.0](https://nodejs.org/en/blog/release/v24.5.0)). The base environment of every sandbox now sets it.
- curl cannot do HTTP/3 through a proxy, and `--http3` falls back ([curl](https://curl.se/docs/manpage.html)). A sandbox has no route for UDP, so QUIC fails at once and tools fall back to TCP without a wait.
- gVisor's rootfs overlay cut an fsstress run from 262.79 s to 3.18 s, and its default now backs the overlay with a file ([gVisor](https://gvisor.dev/blog/2023/05/08/rootfs-overlay/)). Builds under gVisor still took 48% to 69% longer in one paper ([paper](https://arxiv.org/html/2505.02521v1)). `microvm` stays the preferred level where it can run.
- npm, pnpm and Yarn check what they read from their caches. pip, uv and Cargo state no check on read, and Cargo had an extraction bug that wrote outside its folder ([npm](https://docs.npmjs.com/cli/v11/commands/npm-cache), [Yarn](https://yarnpkg.com/configuration/yarnrc), [Cargo](https://blog.rust-lang.org/2022/09/14/cargo-cves/)). So caches stay inside one sandbox.

## 6. Safety

| Default | Docker Sandboxes | Hosted VM service | This design |
| --- | --- | --- | --- |
| The wall | One microVM each, and the agent has sudo ([isolation](https://docs.docker.com/ai/sandboxes/security/isolation/)) | One VM each, and the user is root | One wall each, at the host's level. The agent is not root. |
| Files | The working folder, read and write, `.git/hooks` included ([isolation](https://docs.docker.com/ai/sandboxes/security/isolation/)) | Nothing from the laptop | A clone only |
| Network out | Denied unless a rule allows it ([defaults](https://docs.docker.com/ai/sandboxes/security/defaults/)) | Open | No network card, and the unknown action |
| Network in | Ports that the user publishes, on loopback | SSH and published ports, public URLs | Nothing. Ports reach only the laptop. |
| Secrets | Values stay on the host, but variables are readable inside | Variables and files inside the VM. AI keys inside unless the proxy is on. | Values stay outside, and the guest sees sentinels |
| SSH agent | Forwarded by default | Your GitHub login when the environment passes it | Never. Named keys only. |
| On the host, outside the wall | Stdio MCP servers ([architecture](https://docs.docker.com/ai/sandboxes/architecture/)), lifecycle commands from environment files ([environment files](https://docs.docker.com/ai/sandboxes/configuration/environment-files/)) and secret helpers | Not applicable | Nothing that a sandbox or a repository chooses |
| Telemetry | On, with an opt-out | Monitoring, with an opt-out | None |
| Clipboard from the sandbox | Text copy opt-in since v0.51.0 ([release](https://github.com/docker/sbx-releases/releases/tag/v0.51.0)) | Not applicable: the agent is reached over SSH or a desktop | OSC 52 passes unchanged, the client counts the writes, and the docs show each terminal's setting that asks first |

Docker Sandboxes' own 2026 advisories, and the lesson each gives this design:

| Advisory | What happened | The lesson here |
| --- | --- | --- |
| [CVE-2026-103916](https://www.cve.org/CVERecord?id=CVE-2026-103916) | `sbx cp` followed symlinks that the sandbox planted | `cp` (P5.2) opens every path with `os.Root` and never follows a link that the guest wrote |
| [CVE-2026-17106](https://www.cve.org/CVERecord?id=CVE-2026-17106) | A tar extraction in `cp` wrote outside its target | `cp` writes on the laptop through `os.Root` too, never through tar |
| [CVE-2026-77179](https://www.cve.org/CVERecord?id=CVE-2026-77179) | A symlink escape in the macOS virtio-fs server | No folder sharing on a microvm in this plan |
| [CVE-2026-79994](https://www.cve.org/CVERecord?id=CVE-2026-79994) | A symlink race in a Unix socket relay | Sockets live in 0700 folders that the guest cannot write ([0030](../contributing/decisions/0030-local-sockets-do-not-listen.md)) |
| [CVE-2026-12039](https://www.cve.org/CVERecord?id=CVE-2026-12039) | Data left through DNS, past the allowlist | Unknown names never leave the egress process (spec 7.2) |
| [CVE-2026-12539](https://www.cve.org/CVERecord?id=CVE-2026-12539) | The ICMP block was lost after a daemon restart | No network card means nothing to lose at a restart |
| [v0.42.0](https://github.com/docker/sbx-releases/releases/tag/v0.42.0) | A sandbox took another sandbox's OAuth callback port | `forward` with no ports asks which claimed ports to forward, so a guest never picks a laptop port alone (P1.9) |
| [v0.42.0](https://github.com/docker/sbx-releases/releases/tag/v0.42.0) | Commands ran on the host through D-Bus | No sandbox sees the user's systemd bus or `$XDG_RUNTIME_DIR` (spec 7.1) |
| [Issue 261](https://github.com/docker/sbx-releases/issues/261) | Rules that a kit brought widened Locked Down | Here a narrower layer can lift a broad deny on purpose, and a lock stops it. A repository's own file can only narrow. |
| [Issue 612](https://github.com/docker/sbx-releases/issues/612) | Claude.ai MCP connectors were active after a login inside the sandbox | The model API guard cannot see connectors that the account holds. The docs say to use an API key, or an account without connectors (spec 16). |

## 7. What could not be verified

- The time from a request to a ready agent, for either product. Neither publishes one.
- The limit on local Docker sandboxes. None is documented.
- Where Docker's VMM code comes from. Docker says it wrote its own VMM, and its release notes fix an error from `krun_start_enter`, a libkrun function ([v0.30.0](https://github.com/docker/sbx-releases/releases/tag/v0.30.0)).
- Whether JetBrains Gateway honours an SSH `ProxyCommand`.
- The boot time of a Nitro Sandbox with an agent.
- The gVisor overhead of `npm install`, `git status` and Go or Rust builds on systrap.

## Sources and method

Four researchers worked on 10 November 2026: one each on Docker Sandboxes, the hosted VM service and editor access, and one on performance. Every claim above links its source, except the claims about the hosted VM service. The pages that the Docker researcher downloaded were kept outside the repository, so that claims could be checked again. No account was made, no sandbox was started and nothing was installed.
