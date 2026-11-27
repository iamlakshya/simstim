# Reuse: the open-source parts we can use instead of our own code

**Status:** research, 27 November 2026. **Rule (the owner, 27 November 2026):** if a well-known, maintained open-source library or tool meets a requirement, we use it. We build from scratch only when no available tool can meet the requirement. A working product comes first; custom rebuilds come later, one at a time.

This note checks every area of the [isolation spec](../contributing/isolation-spec.md), the [isolation plan](../contributing/isolation-plan.md), the [CLI plan](../contributing/cli-plan.md) and the earlier research against the code on the feature branch `docs/isolated-sessions` and against the open-source projects that exist today. Four parts follow, one per area. Each feature area gives the real requirement, the code today, the options (licence, maintenance, form and fit) and a recommendation. Licences must be compatible with Apache-2.0. A GPL tool is acceptable only as a separate program.

## Summary

**The size problem.** `main` is the working product: about 44,600 lines of Go. The feature branch is about 448,000 lines of Go, plus about 283,000 lines of tests. Most of the growth is not isolation. It is lifecycle proof code: custody, purge, native managers, descriptor retirement and hand-offs. There are also hand-written versions of things that libraries already do, such as an HTTP/2 layer and a TOML parser. The daily-use path needs a small part of this:

| Area | Today (non-test lines) | With reuse (estimate) |
| --- | --- | --- |
| `internal/host` and `internal/client` | about 200,000 | about 55,000 |
| Egress (`internal/egress` and the secret packages) | about 37,000 | about 3,000 to 4,000 for v1 |
| Process wall and jails (`wall/process`, `jail`, `cmd/userns`) | about 22,000 | a thin layer on bubblewrap |
| MicroVM driver | about 19,000 | about 1,500 |
| Supply chain and release scripts | tens of thousands, with a 7,000-line gVisor patch | GoReleaser, SSH signatures and GitHub attestations |

**A threat that the spec rules out.** About 30,000 lines of Go and the gVisor patch defend against a compromised login account. Spec 3.4 says the design does not defend against that threat (N5). This code should be parked, not fixed.

### The main swaps

| # | Need | Adopt | Instead of | Removes (about) |
| --- | --- | --- | --- | --- |
| 1 | Process wall and jails | [bubblewrap](https://github.com/containers/bubblewrap) as a separate program, with go-landlock and go-seccomp-bpf (already used) | Our own C launcher, `wall/process` and `jail` | 20,000 lines |
| 2 | Stop, destroy and wipe | One `systemd-run --user` unit per session, as on `main`, with [coreos/go-systemd](https://github.com/coreos/go-systemd) where a job result is needed | Custody, purge and native-manager layers, and hand-written D-Bus | 48,000 lines |
| 3 | Model API access with the key kept out of the sandbox | A local gateway route in the egress process. The agent gets a plain base URL (`ANTHROPIC_BASE_URL`; a Codex provider block), and the egress process adds the real key over verified TLS. | TLS interception, a custom HTTP/2 layer and a WebSocket parser on the daily path | 14,000 lines, and the exact Go version pin |
| 4 | Egress front, for interception later | [elazarl/goproxy](https://github.com/elazarl/goproxy), Go's `crypto/tls` to read the SNI, and [smokescreen](https://github.com/stripe/smokescreen)'s test cases for the address checks | A hand-written proxy front and SNI parser | a few thousand lines |
| 5 | Policy and approvals | Plain TOML in which any deny wins, and refuse-then-retry instead of hold timers. Defer the judge and the classifier. | Layered policy code, timers and a judge | 7,000 lines |
| 6 | Host trust | The system `ssh`, a private known_hosts file and `--host-key`. One trusted flag per host. `ssh-copy-id` in place of join and invite. | Pin staging, tiers, grants and a ledger, and `host join`/`invite` | 22,000 lines |
| 7 | Config check | [pelletier/go-toml/v2](https://github.com/pelletier/go-toml) strict decoding (it gives the line and column of each unknown key) | A 10,000-line TOML fork | 10,000 lines |
| 8 | Files in and out, and editors | [pkg/sftp](https://github.com/pkg/sftp) (already used) behind an SSH proxy, plus git bundles | A custom files door | several thousand lines |
| 9 | A Mac as a host | [Lima](https://github.com/lima-vm/lima) under the user's own account, with `limactl autostart` | A hidden account and hand-written launch daemons | several thousand lines |
| 10 | Release | GoReleaser, the SSH signature and GitHub artifact attestations. Turn on immutable releases. | A release gated on microVM conformance, patched gVisor builds and an unmaintained SLSA generator | the release blocker |
| 11 | The default image | The official `buildpack-deps:trixie-scm` image, pinned by digest | Our own image build | 1,300 lines and three CI jobs |
| 12 | Pin watching | Renovate plus an OSV query, with a notifier | A 1,800-line Python watcher | 1,650 lines |
| 13 | Later walls | Upstream [runsc](https://github.com/google/gvisor) releases. A thin [Cloud Hypervisor](https://github.com/cloud-hypervisor/cloud-hypervisor) driver using `ch-remote`. [go-containerregistry](https://github.com/google/go-containerregistry) for images. Firecracker's or Kata's published kernel configs. | A patched gVisor built with Bazel, and a large VM driver | 8,500 lines of patch and build, and 17,000 lines of driver |

**What we keep, because no tool meets the requirement:**

- **The session holder.** tmux, zellij, dtach, abduco, shpool and Eternal Terminal all fail byte-exact attach with replay by offset, or they listen on a port. shpool is the nearest and worth reading.
- The SSH key agent filter, on `x/crypto/ssh/agent`.
- `secretmem` and `nettarget`.
- `install.sh` and the signed-tag gate.
- The decoys. canarytokens is GPL-3 and phones home.

**What no whole product gives us.** DevPod has been stale since March 2025, Coder is AGPL and needs a server and relays, Daytona was archived in June 2026, and container-use has had no release since August 2025. Anthropic's [sandbox-runtime](https://github.com/anthropic-experimental/sandbox-runtime) (Apache-2.0) is the best model to copy for the process wall and masked credentials, but it is a beta that needs Node or Bun on the host. We borrow designs from all of them, not code.

**What to defer, off the daily path:**

- separate accounts (H17, H18), rewind (P5.4, which Claude Code already has as `/rewind`) and image save (P5.5)
- Firecracker (P6.2) and Nitro (P6.4), and a machine per session (P7.4)
- placement, `new auto`, facts, health, drain and host rules
- the GitHub and GraphQL rules, and the registry age gate (keep the OSV malicious-package list)
- the runtime cache, the sealed copies and the separate image verifier

### The choice about the branch

**Option A: slim the feature branch.** Keep the merged stack, then swap and delete area by area. History stays in one line. But the stack's code is woven through the client and host: even unwalled sessions go through a supervisor, a link and a guest init. And about 283,000 lines of tests are tied to the custom machinery. Each deletion means untangling first.

**Option B: rebuild on the published base (recommended).** Go back to the feature branch's last published state, before the stack landed (`6862194f`): `main` plus the 17 finished tasks. It works and is small. Add each feature with the parts above, and port from the stack only the pieces that pass review: the agent rows, CLI verbs, docs and some fixes. Do it on the feature branch with one commit that restores that tree, not with a force-push. The stack stays in the history, and on `wip/implementation-stack`, for reference. This reaches a working product soonest.

### The order of work, daily use first

1. **Decide A or B**, and write a decision record for the reuse rule, which parks the N5 code.
2. **The process wall** on bubblewrap and Landlock, for Linux hosts.
3. **The session lifecycle** on `systemd-run --user` units: new, stop and destroy.
4. **Egress v1:** an allowlisting `CONNECT` proxy, DNS policy and the model gateway route.
5. **Files** over SFTP and git bundles. **Host trust** with a pinned host key.
6. **A Mac as a host** through Lima.
7. **The release pipeline,** cut down for a first release.
8. **Then the later walls:** the thin Cloud Hypervisor driver, upstream runsc, then the deferred features, one at a time.

### The decision records this needs

Each new record replaces or settles an old one. None edits an accepted record.

- **The reuse rule and the threat model:** park the code that defends against N5.
- **The process wall on bubblewrap.**
- **The session lifecycle on systemd units:** reject the proposed records 0050 and 0051.
- **The model gateway route,** with no TLS interception on the daily path. This retires the HTTP/2 layer and the Go version pin in record 0052.
- **Simpler host trust:** drop tiers, grants, join and invite.
- **Release signing:** replace 0017. Keep the SSH signature and the attestations.
- **The gVisor and microVM paths:** upstream runsc and a thin driver.

The four parts below give the details, links and evidence for every row.

## Part A: isolation and runtimes

Checked on 27 November 2026. Release dates and licences come from each project's GitHub API or package index on that day. Line counts are non-test Go lines from `git ls-files`, on the feature branch `docs/isolated-sessions`. "Lines removed" are estimates. Research notes are in `docs/research/` (build facts means `build-facts.md`); the spec is `docs/contributing/isolation-spec.md`.

### Summary

1. **bubblewrap was never evaluated as the wall's launcher.** The spec copied the pattern of Ubuntu's bubblewrap `AppArmor` profile into its own C launcher (`cmd/userns`) and its own profile. bubblewrap is maintained (v0.13.1, 14 October 2026), packaged everywhere, and Ubuntu ships and loads its profile from 25.04. It can build the process wall and every jail.
2. **Much of the code defends threats the spec itself disclaims.** N5 (a compromised login account) and N10 (root on the host) are "not guaranteed" (spec 3.4). Yet the gVisor patch, the VMM "exec proof" and custody layers, the sealed copies and the slot accounts all defend against N5. Roughly 30k Go lines and a 7,000-line gVisor patch serve that one non-goal.
3. **Keep the VMMs, drop the scaffolding.** Cloud Hypervisor and Firecracker are the right tools. No maintained tool gives a rootless, NIC-less microVM with a host-dialled link and a jailed VMM, so a thin driver stays ours. But it should be about 1.5k lines, not about 19k.
4. **Use upstream runsc.** Spec 21.4 asks only for upstream flags. The code builds a patched runsc with Bazel into five binaries.
5. **Images:** go-containerregistry for pull and flatten, an official upstream image pinned by digest, `mkfs.erofs` for the agents disk. **Kernel:** start from Firecracker's published 6.18 guest configs, or take Kata's prebuilt 6.18 kernel. This resolves an UNVERIFIED item in spec 20.1.
6. **Park what is off the daily path:** separate accounts (H17, H18), checkpoints and rewind (P5.4, Claude Code already has `/rewind`), image save (P5.5), Firecracker and Nitro (P6.2, P6.4), a machine per session (P7.4) and the Mac host polish (H21).

### Code in scope today

| Code | Lines | What it does |
| --- | --- | --- |
| `internal/wall` | 17.3k | Interface 0.3k. `process` 4.1k (freezer 0.65k, copies 0.85k). `gvisor` 2.8k. `microvm` 8.7k (slots, runner, read-only entries, launch origin, received VMM, snapshots, checkpoints). `none` 1.0k. `chv`, `firecracker`, `nitro` 0.4k together. |
| `internal/jail` | 17.5k | Generic jail and its re-exec init (about 3k). gVisor handoff, controller, freezer and nested creation (4.9k). VMM exec owner, proof, stage and read-only entry (4.8k). File and publication helpers. |
| `internal/guest` | 15.4k | The guest init: link, holder start, DNS stub and proxy (1.0k), bundles, purge (1.1k), microvm mounts, memory and PTYs (`vm_*`, 4.1k). |
| `internal/supervisor` | 21.5k | Trusted side of a session. Egress, secrets, policy, SSH agent and ports are other audits. In scope: lifecycle, spawn, fork, checkpoint, image save (1.7k), `native_*` start-up (1.4k), `none_*` (0.7k). |
| `internal/image` | 11.0k | Registry, transfer, flatten and convert (1.4k). Agents disk (2.3k). Image save (2.4k). Ownership and signatures (1.6k). Default image (0.7k). |
| `internal/checkpoint`, `internal/slotlaunch` | 2.0k, 8.9k | Reflink checkpoints. The root-owned slot launcher, its store and its checkpoint ops (2.4k). |
| `internal/provider` | 5.4k | E2B (2.4k, a hand-written REST and envd client), Lima (1.5k), Docker (0.8k). |
| Neighbours | about 7k | `runtimecache` 2.5k, `filecopy` 1.4k, `vmneed`, `vmconfig`, `nitrodev` 1.0k, `slotlease` 0.4k, `macfacts` 0.4k. |
| `cmd/userns` | 1.3k of C | The namespace launcher: JSON on fd 3, two nested user namespaces, the mount plan, `pivot_root`, the DNS socket, confinement, a gVisor outer step. |
| `third_party/gvisor` | 7.0k-line patch | `hostguard.patch` adds about 6.7k lines to runsc release-20261102.0: a raw ELF entry before Go starts, a fixed controller, five binaries built with Bazel. Eleven `gvisor-*.md` contributor notes describe it. |
| `guest/` | 1.1k of shell, 2 full kernel configs | Kernel and tools image build, boot test, default image build with Docker and skopeo, `verity` tool. |

About 100k of the 450k Go lines sit in this area.

### What the earlier research chose or rejected, and whether it holds

| Item | Earlier position | Holds today? |
| --- | --- | --- |
| bubblewrap as the wall | Not evaluated. Only its `AppArmor` pattern was taken (build facts 4.1). | **Reopen.** It meets 4.1's steps 1 to 7 except Landlock and the port-53 bind. |
| go-landlock, go-seccomp-bpf | Chosen | Yes. go-landlock v0.11.2 (10 November 2026); the repo pins v0.11.0. go-seccomp-bpf's last release is v1.6.0 (June 2025), past 12 months, but commits continue (August 2026) and the library is small. libseccomp-golang needs cgo. Keep. |
| gVisor as W2, runsc as uid 0 in helper-made namespaces | Chosen (spec 21.4) | Yes. The spec never asked for a patched runsc. The patch is the drift. |
| Cloud Hypervisor as W3 | Chosen | Yes. v55.0 came out on 19 November 2026; the pin is v54.1, supported to about 6 January 2027. |
| Firecracker with no SDK and no jailer | Chosen | Yes, more so: firecracker-go-sdk and firecracker-containerd were archived on 10 November 2026. |
| libkrun not a driver | VMM serves devices with its own permissions; TSI uses the VMM's network | Mostly. The reason weakens when the VMM sits in a jail, which the spec does anyway. Three libkrun tools appeared (smolvm and BoxLite in December 2025, microsandbox in beta). Too young for the boundary now. |
| Kata not a driver | Root containerd, shim and virtiofsd | Yes. Its kernel artefacts are reusable, though. |
| hcsshim `tar2ext4` and `dmverity` | Chosen | Yes (v0.15.0, MIT). |
| Daytona | Server went closed in June 2026 | Yes: the repository was archived on 24 July 2026. |
| E2B Embed | Opens ports on its node | Yes. E2B has no Go SDK, but publishes its OpenAPI and envd protos. |
| systemd user units | Cannot use `User=` or `DynamicUser=` | Yes. |
| A published 6.18 guest config (UNVERIFIED in spec 20.1) | Unknown | **Resolved:** Firecracker ships `microvm-kernel-ci-x86_64-6.18.config` and `-aarch64-6.18.config`. Kata 4.4.0 pins kernel 6.18.62. |

### Over-specification that drives code

Most items below defend N5 or N10, which spec 3.4 says the design does not guarantee. The last ones exist only because of our own design, or duplicate what the agents already do. A decision record could say: "Defences against the login account and host root are out of scope until separate accounts land." Then park the code.

- **The gVisor patch.** It clears dumpability before runsc reads its config, so that another process of the same user cannot trace it. That is N5.
- **VMM exec proof, custody and sealed copies** (`jail/vmm_*`, `microvm/received_*`, `current_launch_origin*`, P4.2's sealed copies). They guard against the login user swapping bytes after the check. That is N5.
- **A static launcher so that `LD_PRELOAD` cannot reach it** (4.1). The risk exists only because the project ships its own profiled binary. With Ubuntu's own bubblewrap profile, the distribution owns that question, as it does for Flatpak.
- **Slot accounts and a root-owned store** (H17, H18). Spec 14.4.6 calls them an N5 measure, off by default.
- **dm-verity on the tools and image disks.** The disks are read-only to the guest, so verity guards against host-side change after the digest check: N5 or N10. It is cheap with hcsshim, so keep it as optional, not a v1 gate.
- **The agents disk's source digest** over inode, change time and modification time. A rebuild on `agents install` does the same job.
- **Checkpoints and `rewind`** (P5.4). Claude Code already checkpoints its own edits and conversation, with `/rewind` and `/branch` ([docs](https://code.claude.com/docs/en/checkpointing)). Git covers the rest.

---

#### 1. The process wall (plan tasks P1.5, part of H7)

- **Requirement (spec 4.1, 5.1, 11; G1, G2, G5, G7):** start the guest init in new user, mount, PID, network, IPC, UTS and cgroup namespaces. Show only granted paths, a fresh home and read-only system folders. Give an empty network with `lo`, no capability, `no_new_privs`, the seccomp profile and Landlock. Pass the link as an inherited socket. Work under Ubuntu's user-namespace restriction.
- **Today:** `cmd/userns` (1.3k lines of C) and `internal/wall/process` (4.1k). The root step installs a root-owned copy of the launcher with a custom `AppArmor` profile and a child profile with no capabilities.
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [bubblewrap](https://github.com/containers/bubblewrap) | `--unshare-all`, `--uid`, `--gid`, binds, tmpfs, new `/proc` and `/dev`, `lo` up, `--seccomp FD`, `--disable-userns`, `--die-with-parent`, `--new-session`, `--json-status-fd`, `--sync-fd` | LGPL-2.1+ (run as a separate program) | Yes (v0.13.1, 14 Oct 2026) | CLI, in Debian and Ubuntu | **Best.** Ubuntu ships [`bwrap-userns-restrict`](https://gitlab.com/apparmor/apparmor/-/raw/master/profiles/apparmor/profiles/extras/bwrap-userns-restrict), loaded by default from 25.04. On 24.04 the root step installs `apparmor-profiles` and loads it ([steps](https://etbe.coker.com.au/2024/04/24/ubuntu-24-04-bubblewrap/)). Gaps: no Landlock, and children have no capability, so nothing inside can bind port 53. |
| [nsjail](https://github.com/google/nsjail) | Namespaces, cgroups, rlimits, kafel seccomp, protobuf config | Apache-2.0 | Yes (3.6, Mar 2026) | CLI (C++) | Not packaged in Debian or Ubuntu. No distribution `AppArmor` profile, and Ubuntu's restriction blocks it ([Launchpad 2063976](https://bugs.launchpad.net/bugs/2063976)). We would build and ship it. |
| [minijail](https://github.com/google/minijail) | ChromeOS jail | BSD-3-Clause | Commits Nov 2026, last tag May 2023 | CLI and library | No recent release, little packaging. |
| [firejail](https://github.com/netblue30/firejail) | Desktop app sandbox | GPL-2.0 | Yes (0.9.82.2, Nov 2026) | setuid-root CLI | A setuid-root parser of user input. Reject. |
| [sandbox-runtime](https://github.com/anthropic-experimental/sandbox-runtime) (`srt`) | bubblewrap, seccomp and HTTP and SOCKS proxies on Unix sockets on Linux; Seatbelt on macOS | Apache-2.0 | Yes (v0.0.83, 13 Nov 2026) | npm CLI and library | Needs Node on the host. Reads the whole file system by default, where G1 wants an empty home. Its proxy duplicates the egress process. A good reference argv, not a dependency. |
| [systemd sandboxing](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html) in user units | `PrivateUsers=`, `PrivateNetwork=`, `ProtectSystem=`, `TemporaryFileSystem=`, `BindPaths=`, `SystemCallFilter=` | GPL-2.0+ (system tool) | Yes (v262.2, Nov 2026) | Unit properties | No uid 1000 inside. `PrivatePIDs=` needs v257, and Ubuntu 24.04 has v255. Under the `AppArmor` restriction: UNVERIFIED. Good for memory scopes only. |
| [go-landlock](https://github.com/landlock-lsm/go-landlock), [go-seccomp-bpf](https://github.com/elastic/go-seccomp-bpf) | Landlock rules; seccomp BPF | MIT; Apache-2.0 | v0.11.2, Nov 2026; v1.6.0, Jun 2025 | Go libraries | Keep. Build the BPF with go-seccomp-bpf and hand it to bubblewrap on an fd. |

- **Recommendation: wrap bubblewrap.** The supervisor turns the mount plan of 7.1 into a bubblewrap argv, and passes the seccomp program and the link on fds. `wall-init` applies Landlock with go-landlock as its first act, before it reads the link. Map the host user straight to uid 1000 with `--uid 1000`, so the two nested user namespaces go. For DNS on this wall, rely on the proxy variables, as `srt` does, and drop the stub on `127.0.0.53:53` (it needs a capability). Freeze with `systemctl --user freeze` instead of our own cgroup freezer. **Effort M.** Removes `cmd/userns` (1.3k C) and about 3k of `wall/process`, and the launcher-copy and profile parts of H7. **Risk:** low to medium. Ubuntu 24.04 needs the root step; minimal servers lack the `bubblewrap` package (install it, or ship a pinned static build on Debian hosts without sudo). Check first that bubblewrap leaves the inherited link fd open, and that the escape suite of P1.5 passes unchanged.
- **Spec impact:** 4.1 (steps 1 to 7 become bubblewrap flags), 5.6, 11 (the Ubuntu row), 14.4.2 (`userns-apparmor`), question 4. A new decision record: "bubblewrap builds the process wall and the jails".

#### 2. Jails for trusted-side processes (plan task P1.3)

- **Requirement (spec 5.6):** confine the VMM, the image converter, host-side git and the egress process: no network (except egress), only named paths, no capability, `no_new_privs`, seccomp, Landlock and a memory cap.
- **Today:** `internal/jail`, 17.5k lines. About 3k is a general jail. The rest is a gVisor controller (4.9k) and a VMM exec-proof, custody and read-only-entry machine (4.8k), plus publication helpers.
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [bubblewrap](https://github.com/containers/bubblewrap) | As in 1 | LGPL-2.1+ | Yes (v0.13.1) | CLI | Fits all four jails; one tool for walls and jails. No subuid mapping, but server images lack `uidmap` anyway (build facts 4.4). |
| `systemd-run --user --scope -p MemoryMax=` | A cgroup cap for one process | GPL-2.0+ | Yes (v262.2) | CLI | Already how units start. Use it for the converter's 1 GiB cap. |
| Cloud Hypervisor `--seccomp true --landlock`, Firecracker's own filters | VMM self-confinement | Apache-2.0 | Yes | Built in | Already in the spec. Keep. |
| [Firecracker jailer](https://github.com/firecracker-microvm/firecracker/blob/main/docs/jailer.md) | chroot, cgroups, netns, uid drop | Apache-2.0 | Yes (v1.18.0) | CLI, runs as root | The rejection holds: it needs root. |
| nsjail | As in 1 | Apache-2.0 | Yes | CLI | As in 1. |

- **Recommendation: wrap bubblewrap** with an argv builder of 300 to 500 lines. Drop the VMM proof and custody layers and the sealed copies (N5). The gVisor controller goes with the patch (section 5). **Effort M.** Removes about 14k to 15k of 17.5k lines. **Risk:** low. The `Subuid` option goes, which few hosts could use. The egress jail needs `--share-net`; the egress audit owns it.
- **Spec impact:** 5.6 (`Spec` keeps `Argv`, `Env`, `Binds`, `Net`, `Seccomp`, `Files`), 20.5 (P4.2's sealed copies optional), plan P1.3 and P4.2.

#### 3. Levels, the probe and the root step (plan tasks P1.1, H7)

- **Requirement (spec 5.1, 11, 14.3, 14.4):** learn the strongest level that a host runs, pin it as the floor, and give one root step that opens what the level needs.
- **Today:** `internal/wall/wall.go` (0.3k), the probe cache, `internal/host` probe and kernel files (about 2.9k), and a root step with `userns-apparmor`, `kvm-acl`, `kvm-group`, `vsock-modules`, `slots` and `nitro-udev`.
- **Options:** no library holds a level and floor model; it is product logic. The probes can run the real tools: `bwrap --unshare-all --uid 1000 true`, `runsc do true`, and `/dev/kvm` plus `cloud-hypervisor --version`. The root step can use distribution packages: `apt-get install bubblewrap` (with `apparmor-profiles` on Ubuntu 24.04), `usermod -aG kvm`, and gVisor's signed apt repository.
- **Recommendation: keep ours, simplify.** Probes become "run the tool once and read the exit code". The root step shrinks to packages and group membership; `slots` and `nitro-udev` wait for their tasks. **Effort S.** Removes the custom profile, the launcher install and its upgrade path. **Risk:** low.
- **Spec impact:** 5.1 (probe table), 14.3, 14.4.2.

#### 4. The supervisor and the guest init (plan tasks P1.4, P3.4)

- **Requirement (spec 4, 5.3, 20.3):** a trusted process per sandbox serves the session socket and passes holder frames over the link. A PID 1 inside serves the link, starts holders, reaps children and shuts down. On a microvm it also mounts the disks and the overlay root.
- **Today:** `internal/supervisor` (21.5k, much of it egress and secrets) and `internal/guest` (15.4k, with 4.1k of `vm_*`).
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [tini](https://github.com/krallin/tini) | PID 1 reaping and signals | MIT | No (v0.19.0, Apr 2020) | Binary | Unmaintained; a few lines of Go do the same. |
| [catatonit](https://github.com/openSUSE/catatonit) | The same | GPL-2.0 | Weak (v0.2.1, Dec 2024) | Binary | Adds nothing over Go. |
| [s6](https://github.com/skarnet/s6) | Supervision suite | ISC | Yes (v2.15.1.1) | Binaries | Far more than needed. |
| kata-agent | Guest agent over ttrpc and vsock | Apache-2.0 | Yes (Kata 4.4.0) | Rust binary | Only inside Kata. Its protocol does not carry our holder frames. |

- **Recommendation: keep ours.** No tool carries the holder's frames byte for byte over our link. On the process wall, bubblewrap does the mounts, so `wall-init` needs no mount code there. Trim the `native_*` start-up and the purge custody code with the N5 decision. **Effort M** (trimming). **Risk:** low; the attach fixtures guard it.
- **Spec impact:** 4.1 steps 3 and 7 only.

#### 5. The gvisor wall (plan task P6.1; `third_party/gvisor`)

- **Requirement (spec 21.4):** gVisor's user-space kernel for hosts without KVM, with `--network=none`, the link on fd 3, inside the session's unit.
- **Today:** a 7.0k-line patch on runsc, a Bazel build of five binaries, `wall/gvisor` (2.8k), `jail/gvisor_*` (4.9k) and `cmd/userns/gvisor_outer.c`.
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [Upstream runsc](https://gvisor.dev/docs/user_guide/install/) | `run`, `do`, `--network=none`, `--host-uds=none`, `--directfs`, `--ignore-cgroups`, `--pass-fd`, `checkpoint` | Apache-2.0 | Yes (release-20261123.1, 26 Nov 2026) | CLI; signed apt repository and SHA-512 tarballs | Meets 21.4's flags as written. On Ubuntu, runsc needs capabilities inside its user namespace, which Ubuntu's bwrap profile denies to children, so the root step adds a small profile for `/usr/bin/runsc` (UNVERIFIED). |
| Rootless Podman with runsc | gVisor's "method 2" | Apache-2.0 | Yes (Podman v6.2.1) | Daemonless CLI | Adds Podman, its storage and `uidmap`. Heavier. |
| `runsc --rootless` | Self-made user namespace | Apache-2.0 | Yes | CLI | `create` unsupported, and it skips defences that only root sets up ([rootless](https://gvisor.dev/docs/user_guide/rootless/)). A weaker level. |

- **Recommendation: adopt upstream runsc.** Drop the patch, the Bazel build, the five-binary closure, the controller and its notes. Start runsc under bubblewrap (Debian) or with its own profile (Ubuntu). Build this after the process and microvm paths work; it is not on the daily path. **Effort M.** Removes the 7.0k-line patch and about 6k to 7k Go lines. **Risk:** the Ubuntu profile step needs a test on 24.04 and 25.10. What the patch added is N5.
- **Spec impact:** 21.4 (add "upstream binary, no patch"), 20.9 (one `gvisor` item), the eleven `gvisor-*.md` notes retire, and a decision record.

#### 6. The microvm drivers (plan tasks P3.3, P3.6, P3.8, P6.2, P6.4)

- **Requirement (spec 20.3, 20.10, 21.5):** a KVM guest with no network card, read-only tools and image disks, a session disk, a vsock link that the host dials, a confined VMM inside the unit's `MemoryMax`, and a power-button stop. Firecracker covers older host kernels; Nitro covers EC2.
- **Today:** about 19k lines: `wall/microvm` 8.7k, `jail/vmm_*` 4.8k, `guest/vm_*` 4.1k, `vmneed`, `vmconfig` and `nitrodev` 1.0k, and parts of `runtimecache` (2.5k).
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [Cloud Hypervisor](https://github.com/cloud-hypervisor/cloud-hypervisor) with `ch-remote` | The VMM; `ch-remote` for `power-button` and `info` | Apache-2.0 and BSD-3-Clause | Yes (v55.0, 19 Nov 2026) | Static CLIs | **Keep.** Use the shipped `ch-remote` instead of an HTTP client. Move the pin to v55.0. |
| [Firecracker](https://github.com/firecracker-microvm/firecracker) with `--config-file` | The VMM | Apache-2.0 | Yes (v1.18.0, 9 Nov 2026) | CLI | Keep for P6.2 and P6.4. Its [Go SDK](https://github.com/firecracker-microvm/firecracker-go-sdk) was archived on 10 Nov 2026, so the config file is the interface. |
| [Kata Containers](https://github.com/kata-containers/kata-containers) | Kernel, agent, CH, Firecracker or QEMU, virtio-fs | Apache-2.0 | Yes (4.4.0, 17 Nov 2026) | containerd shim, as root | Root containerd and shim: the rejection holds. Reuse its kernel (section 7). |
| [libkrun](https://github.com/containers/libkrun), [krunvm](https://github.com/containers/krunvm), crun's krun handler | An in-process VMM | Apache-2.0 (crun GPL-2.0, a separate program) | Yes (v1.19.8, Nov 2026; krunvm v0.2.7, Aug 2026) | C library, CLIs | The VMM serves virtio-fs with its own rights, so it needs a jail. Debian's crun lacks krun (UNVERIFIED). |
| [smolvm](https://github.com/smol-machines/smolvm) | libkrun VMs from OCI images, no network by default, `--expose-socket`, checkpoints and branches, macOS too | Apache-2.0 | Yes (v1.37.7, 26 Nov 2026), but created Dec 2025 | CLI, Go SDK | Young. Its VMM runs with the user's rights; releases are unsigned ([security model](https://github.com/smol-machines/smolvm/blob/main/docs/security-model.md)). A spike, not a dependency. |
| [microsandbox](https://github.com/microsandbox/microsandbox) | The same class | Apache-2.0 | Yes (v0.8.3, 26 Nov 2026), "beta" | CLI, SDKs | Breaking changes expected. Watch. |
| [BoxLite](https://github.com/boxlite-ai/boxlite) | The same class; optional REST server | Apache-2.0 | Yes (v0.15.0, Nov 2026), created Dec 2025 | Library (cgo Go SDK), CLI | Young; cgo. Watch. |
| [E2B infra](https://github.com/e2b-dev/infra), [flintlock](https://github.com/liquidmetal-dev/flintlock), firecracker-containerd | Orchestrators | Apache-2.0; MPL-2.0; archived | Mixed | Daemons with ports | Reject. |

- **Recommendation: keep Cloud Hypervisor, shrink the driver.** The driver builds the argv from a template (a golden test), formats the session disk with `mkfs.ext4`, starts the VMM under bubblewrap, dials `CONNECT 1024`, presses the power button with `ch-remote` and waits. That is about 1.5k lines with the guest's mounts. Defer Firecracker and Nitro until the CH path ships. In phase 6, spike smolvm inside a bubblewrap jail as a possible swap for the whole microvm, image and kernel stack, mainly for native Mac hosts. **Effort M.** Removes about 15k to 17k lines. **Risk:** medium. The escape suite, the disk-order test and the memory test of 20.10 must still pass; what goes is N5 custody.
- **Spec impact:** 20.3 (`ch-remote`, v55.0), 20.5 (sealed copies optional), 21.5 (note the archived SDK), 11 (the CH row).

#### 7. The guest kernel and the tools image (plan tasks P3.1, P3.7, kernel half of P4.3)

- **Requirement (spec 20.1, 20.6, 20.11):** amd64 and arm64 kernels with virtio, vsock, dm-init and dm-verity, EROFS, ext4, overlayfs, nftables redirect, memcg and Landlock, with no modules. A tools image with the helper and the shadow terminal. A list of host kernel bugs for `host doctor`.
- **Today:** two full committed `.config` files, `check-config.sh`, `guest/build.sh`, a boot test, `guest/verity`, and source pins for the kernel and erofs-utils, built from source. A hand-curated `kernel_bugs.toml`.
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [Firecracker guest configs](https://github.com/firecracker-microvm/firecracker/tree/main/resources/guest_configs) | `microvm-kernel-ci-{x86_64,aarch64}-6.18.config` | Apache-2.0 repository | Yes (v1.18.0) | Config files | Start from these plus a small fragment, merged with the kernel's own `merge_config.sh`. |
| [Kata kernel](https://github.com/kata-containers/kata-containers/releases) | A prebuilt `vmlinux` in `kata-static-4.4.0-*.tar.zst`, kernel 6.18.62 | GPL-2.0 kernel, Apache-2.0 tools | Yes (4.4.0) | Binary artefact | Could replace our kernel build and its reproducibility work. UNVERIFIED that it passes `check-config.sh` (`DM_INIT`, `EROFS_FS`, `NFT_REDIR`, no modules). |
| [erofs-utils](https://github.com/erofs/erofs-utils) | `mkfs.erofs` | MIT since 1.9.2 | Yes (1.9.4, Aug 2026) | CLI | Use the build image's package, not a source build. |
| An initramfs | Kernel built in | GPL-2.0 | | Format | Simpler than an EROFS disk and its verity table; costs some guest RAM. |
| [OSV](https://osv.dev) Debian and Ubuntu kernel records | Kernel advisories with fixed package versions | Public data | Yes | Data | Generate `kernel_bugs.toml` in the release job; hosts never query it. |

- **Recommendation: simplify.** Try Kata's prebuilt kernel first, gated by `check-config.sh`. If it fails, build from Firecracker's 6.18 configs and a committed fragment. Use the distribution's erofs-utils. Generate the P3.7 list from OSV in CI. **Effort S to M.** Removes the full `.config` upkeep and perhaps the kernel build and its reproducibility task. **Risk:** low; every artefact stays pinned by digest.
- **Spec impact:** 20.1, 20.6, 20.11 and the `supply.lock` tables.

#### 8. Images, the agents disk and image save (plan tasks P3.2, P3.5, P5.5)

- **Requirement (spec 20.2, 20.4, 21.3):** pull an image by the client's digest, check every blob, flatten layers with whiteouts safely, cap decompression bombs, and write a read-only disk. Give the guest the host's installed agents read-only. Later, save a sandbox's changes as an image.
- **Today:** `internal/image` (11.0k) with hcsshim, plus `guest/base-image` scripts that build and publish the project's own default image with Docker and skopeo.
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [go-containerregistry](https://github.com/google/go-containerregistry) | Fetch by digest with checks, keychain logins, `mutate.Extract` (flatten with whiteouts), `mutate.Append`, OCI layouts | Apache-2.0 | Yes (v0.22.1, 4 Sep 2026) | Go library | **Best.** Replaces registry, transfer and flatten. Caps go in a counting reader. |
| [hcsshim](https://github.com/microsoft/hcsshim) `tar2ext4`, `dmverity` | ext4 from tar, verity tree | MIT | Yes (v0.15.0) | Go library | Keep. |
| `mkfs.erofs --tar`, `--oci`, `--force-uid` | EROFS from a tar stream or a registry | MIT | Yes (1.9.4; `--oci` since 1.9) | CLI | Good for the agents disk. Keep fetches in go-containerregistry: `--oci` digest checks are UNVERIFIED, and the converter has no network. |
| [umoci](https://github.com/opencontainers/umoci) | Rootless unpack of OCI layouts | Apache-2.0 | Yes (v0.6.0, Oct 2025) | CLI, library | Unpacks to a folder; we need a disk. |
| [skopeo](https://github.com/containers/skopeo) | Copy, inspect, mirror | Apache-2.0 | Yes (v1.25.2, Nov 2026) | CLI | Keep for mirroring in CI. |
| containerd, BuildKit | Image store, builds | Apache-2.0 | Yes (v2.4.2, v0.35.1) | Root daemons | Not on hosts. |
| [mmdebstrap](https://gitlab.mister-muffin.de/josch/mmdebstrap) | Reproducible Debian roots | MIT | Yes (1.7.0, 3 Nov 2026) | CLI | Only if we keep building an image. |
| [`buildpack-deps:trixie-scm`](https://hub.docker.com/_/buildpack-deps) | Official Debian image with curl, git and CA certificates | Debian package licences | Yes (pulled and updated Nov 2026) | OCI image | Pin by digest instead of our own default image. About 143 MB compressed on amd64. |

- **Recommendation: adopt go-containerregistry; simplify the rest.** Pull and flatten with it, inside the converter jail and our caps. Build the agents disk as one tar stream into `mkfs.erofs --tar --force-uid=1000 --force-gid=1000` on `agents install`, without the inode and change-time digest. Pin an official upstream image and mirror it with `skopeo copy --preserve-digests`. Defer image save; build it on `mutate.Append` later. **Effort M.** Removes about 5k to 6k lines and the default-image pipeline. **Risk:** medium. `mutate.Extract` must match the flatten rules of 20.2 (hard links whose target a later layer removes, device nodes); the existing tests decide.
- **Spec impact:** 20.2 (steps 2 to 4 name the library), 20.4, 21.3, and the release manifest's default image entry.

#### 9. Copy files in and out (plan task P5.2)

- **Requirement (spec 21.1):** copy files and folders between the laptop and a running sandbox under a size cap. Never follow a planted symlink on the laptop, and never write outside the target.
- **Today:** `guest` copy and put code (0.9k), `supervisor/put*` (0.4k) and `internal/filecopy` (1.4k), over custom `get` and `put` stream kinds.
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [pkg/sftp](https://github.com/pkg/sftp) (already in `go.mod`) | SFTP server and client | BSD-2-Clause | Yes (v1.13.13, 24 Nov 2026) | Go library | P2.15 already puts an SFTP server in the guest for editors. `cp` can ride the same stream. The laptop side writes through Go's `os.Root`. |
| Tar over exec (`docker cp`, `kubectl cp` style) | Archive copy | | | | The class of the Docker CVEs that spec 21.1 cites. Avoid. |
| rsync | Delta copy | GPL-3.0 | Yes | CLI | Needs rsync in every image; deltas are not needed. |

- **Recommendation: wrap pkg/sftp,** shared with P2.15. **Effort S to M.** Removes most of `filecopy` and the custom stream kinds. **Risk:** low.
- **Spec impact:** 21.1, 10.2 (stream kinds `get` and `put` become `sftp`).

#### 10. Fork and checkpoints (plan tasks P5.1, P5.4)

- **Requirement (spec 6.5, 21.2):** copy a sandbox's work cheaply for a fork. Keep disk checkpoints at turn ends for `rewind`.
- **Today:** about 8k lines: `internal/checkpoint` (2.0k), `slotlaunch` checkpoint ops (2.4k), `microvm` checkpoints and snapshots (1.4k), `supervisor` checkpoint, fork and image (1.7k), the process freezer (0.65k).
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| `FICLONE`, `cp --reflink=always` | Block-sharing copies | Kernel; GPL-3.0 coreutils | Yes | Syscall, CLI | XFS and Btrfs; not ext4 (build facts 5.8). |
| Btrfs or ZFS snapshots | Subvolume snapshots | GPL-2.0; CDDL | Yes | CLIs | Need a set-up file system on the host. |
| [CRIU](https://github.com/checkpoint-restore/criu) | Process checkpoint and restore | GPL-2.0 | Yes (v4.3.1) | CLI | Memory images, which question 11 rejects. |
| CH, Firecracker and runsc snapshots | VM memory snapshots | Apache-2.0 | Yes | Built in | Rejected by question 11 (secrets in memory images). Still holds. |
| Claude Code `/rewind` and `/branch` | The agent's own checkpoints of edits and conversation | | Current | Built in | Covers the common case for Claude Code, but not Bash edits ([docs](https://code.claude.com/docs/en/checkpointing)). |
| `systemctl --user freeze` and `thaw` | cgroup freeze of a unit | GPL-2.0+ | Yes (since v246) | CLI | Replaces our freezer. |

- **Recommendation: simplify and defer.** Fork is freeze, then a reflink copy (or a plain copy) of `/work` or the session disk. Drop `rewind` from v1. **Effort S** for fork. Parks about 6k lines. **Risk:** users of agents without their own checkpoints lose `rewind` until later.
- **Spec impact:** 21.2 (defer), 6.5, question 87, plan P5.4.

#### 11. A Mac as a host (plan task H21)

- **Requirement (spec 18.4, 11):** run sessions on a Mac through a Lima VM that starts at boot and is reachable only through a locked-down key.
- **Today:** `provider/lima` (1.5k), `join` (5.4k, shared with H20), `limaprofile` and `macfacts` (0.5k).
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [Lima](https://github.com/lima-vm/lima) | `vz` VMs, plain mode, `hostLoopback: false`, nested virtualisation on M3 or later, `limactl autostart` | Apache-2.0 | Yes (v2.3.0, 6 Nov 2026) | CLI | Keep, as chosen. |
| [Apple container](https://github.com/apple/container) | A VM for each container | Apache-2.0 | Yes (1.7.1, 6 Nov 2026) | CLI, macOS 26 | Later: one VM per session on a Mac without our own Virtualization code. |
| [Code-Hex/vz](https://github.com/Code-Hex/vz) | Go bindings for Virtualization.framework | MIT | Yes (v3.8.0, Sep 2026) | Go library, cgo, entitlement | Only for a native darwin helper (question 37). Later. |
| Seatbelt (`sandbox-exec`) | Process sandbox on macOS | Apple, deprecated | | CLI | Only for a native process wall; `srt` and Codex use it. Later. |

- **Recommendation: keep Lima, simplify H21.** Run the VM as the Mac's own user with `limactl autostart`. Leave the hidden account, our own launch daemon file and the sshd drop-in for later. It is off the daily path: the owner's Mac is the client. **Effort S** if simplified. **Risk:** the Mac's user must stay logged in, which the spec already assumes.
- **Spec impact:** 18.4 steps 2, 6 and 8; questions 68 and 91.

#### 12. Separate accounts (plan tasks H17, H18)

- **Requirement (spec 14.4.6, 21.9):** run each VMM as its own account, from a root-owned store, so that a VMM escape does not land as the login user.
- **Today:** `slotlaunch` (8.9k), `slotlease` (0.4k) and `microvm/slot*` (0.6k). Off by default. It defends N5.
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| A systemd template system unit with `DynamicUser=yes`, plus a [polkit](https://github.com/polkit-org/polkit) rule that allows only start and stop of that template | A dynamic uid per VMM, cgroup kill, state folders | GPL-2.0+, LGPL-2.0+ | Yes (systemd v262.2; polkit 128, Oct 2026) | Units and a rule file | Replaces slot accounts, `flock` slots and kill loops. The VMM leaves the user's unit, so the template sets `MemoryMax`. |
| [systemd-nsresourced](https://www.freedesktop.org/software/systemd/man/latest/systemd-nsresourced.html) | Transient 64K uid ranges for unprivileged user namespaces | GPL-2.0+ | Since v256 (Debian 13 has 257; Ubuntu 24.04 has 255) | Varlink service | Later: subuid-like isolation without `newuidmap` or a root launcher. |
| `newuidmap` and subuids | Subordinate ids | BSD-3-Clause (shadow) | Yes | Setuid CLI | Server images lack `uidmap`. |

- **Recommendation: defer.** Park the 9.9k lines behind a build tag. If it comes back, use a systemd template unit with `DynamicUser` and a narrow polkit rule, not a custom root launcher. **Effort:** none now, M later. **Risk:** none for v1, because N5 is already disclaimed.
- **Spec impact:** 14.4.6, 21.9, questions 27 and 89. Replace decision 0043 with a new record when it reopens.

#### 13. A machine for each session, and Nitro (plan tasks P7.4, P6.4)

- **Requirement (spec 21.8, 11):** give a session its own provider machine when the user owns no host that runs walls. Drive Nitro Sandboxes on EC2.
- **Today:** `provider/e2b` (2.4k, a hand-written REST and envd Connect client), `nitrodev` (0.25k).
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| E2B's [OpenAPI](https://github.com/e2b-dev/infra/tree/main/spec) and [envd protos](https://github.com/e2b-dev/infra/tree/main/packages/envd/spec) | Specs to generate REST and Connect clients | Apache-2.0 | Yes (infra 2026.30, Sep 2026; SDKs Nov 2026, JS and Python only) | Specs, with oapi-codegen and connect-go | Replace hand-written framing with generated code. |
| Community E2B Go SDKs | Small wrappers | Mixed | Few stars | Go | Not well known. Avoid. |
| Daytona; Modal's libmodal | Sandboxes; a Go SDK | | Archived (last pushes Jul 2026) | | Out. |
| [fly-go](https://github.com/superfly/fly-go), [hcloud-go](https://github.com/hetznercloud/hcloud-go), aws-sdk-go-v2 | Provider SDKs | Apache-2.0, MIT, Apache-2.0 | Yes (Nov 2026, Oct 2026) | Go libraries | Use them if a provider is added. |
| Firecracker Nitro Sandboxes | `/dev/nitro_enclaves` driver in the VMM | Apache-2.0 | Yes (v1.18.0) | CLI | P6.4 runs through the same config file. No SDK exists or is needed. |

- **Recommendation: defer P7.4 and P6.4.** When they resume, generate the E2B clients from E2B's published specs. **Effort S.** Removes about 1.5k lines of protocol code. **Risk:** the generated envd stream must stay binary-safe; the attach fixtures check it.
- **Spec impact:** 21.8 only.

### Platform matrix (spec 11) after these swaps

- **Ubuntu 25.04 and later:** the process wall needs only the `bubblewrap` package; Ubuntu loads its profile. **Ubuntu 24.04:** the root step installs `apparmor-profiles` and loads `bwrap-userns-restrict`.
- **Debian 12 and 13:** bubblewrap runs unprivileged once installed, or from a pinned static build where the user has no sudo.
- **gvisor rows:** upstream runsc. Ubuntu needs a profile for `/usr/bin/runsc` (UNVERIFIED).
- **KVM rows:** Cloud Hypervisor v55.0. The 6.8 kernel floor is unchanged.
- **Mac rows:** unchanged, through Lima.

### Checks to run first

1. On Ubuntu 24.04 and 25.10 and on Debian 13: bubblewrap leaves the link fd open, and the P1.5 escape suite passes with Landlock applied by `wall-init`.
2. runsc with the 21.4 flags under bubblewrap on Debian, and with its own profile on Ubuntu.
3. Kata 4.4.0's kernel against `check-config.sh`, and a boot of the guest init under CH v55.0.
4. go-containerregistry's `mutate.Extract` against the flatten tests of 20.2.

### Top swaps by impact on the daily-use path

The path: the owner's Mac runs the client, and a Linux server is the host. A session starts with Claude Code or Codex, the user attaches and detaches, moves files in and out, then stops and destroys it.

1. **bubblewrap builds the process wall** (section 1). This is the shortest way to a walled session on any Linux host, KVM or not. It removes `cmd/userns` and most of `wall/process`, and the custom `AppArmor` launcher in the root step. Effort M.
2. **bubblewrap builds the jails, and the N5 custody code goes** (sections 2 and "Over-specification"). About 15k lines of `jail` and the VMM proof layers stop blocking the path. Effort M.
3. **Park what the path never touches:** H17 and H18, P5.4, P5.5, P6.2, P6.4, P7.4, the gVisor patch and H21's polish. About 25k to 30k lines move behind build tags, and nobody maintains them for v1. Effort S.
4. **Files in and out over SFTP** with pkg/sftp, shared with editors, and `os.Root` on the laptop (section 9). `pull` through a bundle is another audit's. Effort S to M.
5. **Stop and destroy through systemd:** `systemctl --user stop` ends the unit and the whole bubblewrap tree, `freeze` replaces our freezer, and destroy removes the session folder. Effort S.
6. **If the server has KVM and microvm is its floor:** keep Cloud Hypervisor v55.0 with `ch-remote`, pull with go-containerregistry, pin an official base image, take Kata's kernel or Firecracker's configs, and build the agents disk with `mkfs.erofs` (sections 6 to 8). Effort M.
7. **Upstream runsc for VPS hosts without KVM** (section 5), after the above. Effort M.

Attach and detach do not change. The link stays a socket pair that the guest init inherits through bubblewrap, or a vsock that the host dials. Nothing new listens, and the supervisor still never reads output payloads, so the bytes stay exact.

## Part B: network egress, policy and secrets

Scope: spec §3 (network parts), §5.4, §7.2, §7.3, §7.8 to §7.11, §8, §12 (egress rows), §17.8 and §17.9. Plan tasks P1.7, P2.1 to P2.17, P7.1 and P7.2. Decisions 0031, 0032 and 0052. Release dates and licences were read from each project's GitHub page on 27 November 2026.

### Summary

The egress process is far larger than the design expected. The research note estimated "about 4 to 6 kLOC of Go" for the winning door design (isolation.md §7.5). Today `internal/egress` has 36,787 lines of source and 35,007 lines of tests in 266 files, written between 14 and 27 November 2026.

| Part of `internal/egress` | Source lines |
| --- | --- |
| Bound secrets: sentinels and their shape grammar, request and response secret streams, mint, member registry | 11,173 |
| HTTP interception, of which 2,392 lines are a custom HTTP/2 layer on pinned `x/net` scheduler hooks | 5,463 |
| Model API guard, usage counting and the WebSocket frame reader | 3,839 |
| Classifier and judge (with `classify/`) | 3,832 |
| Policy layers, rule IDs, merges, expiry, lockdown, private grants | 3,213 |
| Service rules: GitHub REST and GraphQL (hand-written lexer and parser), receive-pack, push door | 2,816 |
| Core: run, splice, dial, the ClientHello reader, the CA | 1,945 |
| Holds, once rules and the unknown action | 1,224 |
| DNS on `x/net/dns/dnsmessage` | 1,084 |
| Audit 840, trusted endpoints 742, upload budget 316, decoys 306 | 2,204 |

Next to it: `secretbind` 596, `secretmem` 360, `secretmode` 16, `nettarget` 693, `configdata` 383, `repopolicy` 243, `gitpolicy` 151 and `grant` 85. Code in other packages: the guest proxy (349) and DNS stub (about 690), the supervisor's SSH agent (about 640) and decoys (about 520).

**The main finding.** The daily-use path needs no TLS interception. Three steps remove the need:

- The model API is reached through a base URL that the trusted side serves. Claude Code documents this for gateways, and so does Codex.
- Registries and code hosts are spliced. The sandbox holds no token that could publish or push.
- The model host is never allowed for `CONNECT`.

The trusted side then adds the real key over its own verified TLS connection, and it strips every credential that the guest sent. So nothing needs a session CA in the guest, a sentinel shape, a custom HTTP/2 layer or a WebSocket parser. With these changes, v1 of the egress process fits in about 3,000 to 4,000 lines, which is close to the research estimate.

Earlier choices, and whether their reasons still hold:

- **Node 27 (an allowlist with no interception) lost** because "nothing can pin the tenant, so an attacker's own key still works on the allowed model API". That reason no longer holds for model APIs. When the model host is reachable only through the trusted gateway, and the gateway always replaces the credential, an attacker's key cannot be used. The gateway also sees plain bodies, so it can add a tenant header.
- **Node 30 (a broker on the laptop) lost** because credentialed work stops while the laptop sleeps. That still holds. The gateway runs on the host.
- **Node 31 (a registry firewall) lost on fit,** because a feed sync phones home. That still holds. Data that ships with a release avoids it.
- **The research rejected Aikido Safe Chain (AGPL) and Socket's free firewall (closed).** That still holds. The new open options, safedep/pmg and DataDog's supply-chain-firewall, call cloud services for each package (below).
- **Decision 0038, on TLS interception, is missing.** The plan says the owner accepts it before P2.2 merges, but the decisions folder has no 0038. The interception code was merged anyway. Moving it off the daily path avoids the gap.

---

#### 1. Forward proxy, allowlist and address checks (plan tasks P1.7, P2.7)

- **Requirement (spec §3.3 G2 and G7, §5.4, §7.2 "Path of a flow", "Decisions" and presets, §12):** every flow goes through one trusted process, which allows by host and port. It refuses private, loopback, link-local, CGNAT and metadata addresses and the host's own addresses, and it dials only the address that it checked. On port 443 it checks that the SNI equals the host and refuses ECH. It writes one audit line for each flow and listens on no network port. Targets: 3 ms or less to set up a spliced flow, and 200 MiB/s or more of throughput.
- **Today:** the core is 1,945 lines: `run.go`, `splice.go`, `dial.go`, a hand-written ClientHello parser in `tls.go` (267 lines) and `ca.go` (134 lines). The data is `rules/ranges.toml` and `presets.toml`. The guest's `proxy.go` (349 lines) serves `127.0.0.1:3128` and sends each flow over the link. The code is built. P2.7's upload budget adds 316 lines.
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [stripe/smokescreen](https://github.com/stripe/smokescreen) | A `CONNECT` and HTTP proxy with an ACL for each role. It refuses private ranges, CGNAT and addresses embedded in NAT64, 6to4 and Teredo. It resolves a name, then dials the address that it checked. `BuildProxy()` returns a handler. | MIT | v1.0.1, 25 Nov 2026. v2 is on master with no tag. | Go library and daemon | High for ranges and the allowlist. It has no SNI check or ECH refusal, and `handleConnect` cannot be swapped out. It uses Stripe's goproxy fork, logrus and Prometheus (a build tag drops Prometheus). v1 gets fixes only. |
| [elazarl/goproxy](https://github.com/elazarl/goproxy) | A proxy core: `CONNECT`, absolute-form HTTP, hijack, MITM with a certificate cache, HTTP/2 MITM since [v1.9.0](https://github.com/elazarl/goproxy/releases/tag/v1.9.0) and WebSocket relay | BSD-3-Clause | v1.9.4, 12 Nov 2026. Releases most months. | Go library | High as the front of the proxy. `ConnectHijack` lets our splice code check the SNI. Range checks are ours. |
| [sandbox-runtime `srt-proxy`](https://github.com/anthropic-experimental/sandbox-runtime/blob/main/docs/srt-proxy.md) | A proxy that takes a listening socket from the host, and a separate decider process on an inherited fd. It refuses resolved addresses that it may not dial, and refuses a mismatched Host or SNI. | Apache-2.0 | v0.0.83, 13 Nov 2026. A beta preview. | TypeScript on Node 22.12+ or Bun 1.4+, a separate executable | Medium. It matches our split of a proxy and a decider closely. But in proxy-only mode it refuses every opaque tunnel, so it has no splice. It needs a JS runtime on each host. A Node process per session would exceed the 40 MiB memory target for each session. |
| [coder/httpjail](https://github.com/coder/httpjail) | Per-process HTTP and HTTPS filtering, with rules in JS, shell or a stdin program | CC0-1.0 | v0.6.2, 24 Sep 2026 | Rust CLI | Low. Its strong mode needs sudo and its own network namespace, its CA is kept on disk, and it injects no secrets. A reference only. |
| [Squid](https://github.com/squid-cache/squid), [Tinyproxy](https://github.com/tinyproxy/tinyproxy), [Envoy](https://github.com/envoyproxy/envoy) | Mature proxies | GPL-2.0, GPL-2.0, Apache-2.0 | 7.8 on 30 Oct 2026, 1.11.3 on 7 Mar 2026, v1.40.2 on 27 Oct 2026 | C or C++ daemons | Low. Each puts a memory-unsafe parser of hostile bytes on the trusted side, which the design rules out (spec preamble). Tinyproxy had a use-after-free CVE in 2023. |
| Go's `crypto/tls`, and [inetaf/tcpproxy](https://github.com/inetaf/tcpproxy) | Read the SNI with `GetConfigForClient` on a recorded conn. `ClientHelloInfo.Extensions` shows ECH. | BSD-3-Clause, Apache-2.0 | Go 1.27. tcpproxy has no tags (last commit May 2026). | Standard library | High. It replaces the 267-line hand-written parser. |

- **Recommendation: wrap goproxy, and keep the small checks.** Use goproxy as the proxy front, with a listener built from link streams, so that nothing new listens. Splice through `ConnectHijack`. Peek at the SNI with `crypto/tls`. Keep the range check on `netip` (about 150 lines), and port smokescreen's address test cases into it. Those cases show one gap: the spec checks addresses embedded in NAT64, but not in 6to4 (`2002::/16`) or Teredo (`2001::/32`). Name the requirement that no tool meets: an SNI-equals-host check, with ECH refused, on spliced flows that arrive as link streams, under a policy that changes while the session runs. Smokescreen meets everything else, but its `CONNECT` handler cannot be extended. Defer P2.7's upload budget. Effort M. It shrinks the 1,945 core lines to about 700, and removes the hand-written parser and its fuzz targets. Risk: low. goproxy is widely used, and Fly's tokenizer and smokescreen build on it.
- **Spec impact:** §7.2 "Decisions" (add the 6to4 and Teredo checks to `ranges.toml`), §5.4 (`Run` stays as it is), P2.7 moves after the first product.

#### 2. DNS policy (plan task P2.1)

- **Requirement (spec §3.3 G3, §4.1 step 4, §7.2 "DNS"):** no name leaves the host before the policy allows it. Granted names resolve, `HTTPS` and `SVCB` queries get empty answers, and a microvm gets synthetic addresses for transparent mode.
- **Today:** `internal/egress/dns*.go` has 1,084 lines on `x/net/dns/dnsmessage`. The guest stub (`internal/guest/dns.go` and `dns_fds*.go`) is about 690 lines. The namespace launcher binds `127.0.0.53:53`.
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [miekg/dns](https://github.com/miekg/dns) | A full DNS library. v1 gets fixes only, and v2 moved to [Codeberg](https://codeberg.org/miekg/dns). | BSD-3-Clause | v1.1.73 tag, commit 3 Nov 2026 | Go library | Medium. It does no more than `dnsmessage` does for one small responder. It would add a dependency in transition. |
| [CoreDNS](https://github.com/coredns/coredns) | A DNS server built from plugins | Apache-2.0 | v1.15.0, 21 Oct 2026 | Go daemon, or a library with effort | Low. It is heavy for "NXDOMAIN unless allowed". |
| [blocky](https://github.com/0xERR0R/blocky), [AdGuard dnsproxy](https://github.com/AdguardTeam/dnsproxy) | DNS proxies with allow and deny lists | Apache-2.0 | v0.37.0 on 21 Nov 2026, v0.86.0 on 1 Oct 2026 | Go daemons | Low. Each listens on UDP port 53 and is built as a service. |
| Pi-hole | The same approach as an appliance: a daemon and a web UI | not checked | active | Daemon | Low. It is not a library. |

- **Recommendation: keep ours, and simplify or defer.** On the process wall the explicit proxy needs no DNS in the guest. The agents, npm, pip, Node's `fetch` and Go put the name in `CONNECT`, and the egress process resolves it on the trusted side. With no stub, a lookup in the empty namespace fails, so G3 already holds: no name leaves. Make P2.1 a microvm task, for synthetic addresses. When it is needed, cut the code to a responder of about 250 lines on `dnsmessage`. Effort S. It removes about 1,800 lines from the v1 path. Risk: tools that resolve before they connect, such as `ssh` and some health checks, fail closed. That is the intended result.
- **Spec impact:** G3 becomes "no DNS on the process wall, the stub on microvm". §4.1 step 4 becomes optional. P2.1 moves to phase 3.

#### 3. TLS interception, HTTP/2 and WebSocket (plan task P2.2)

- **Requirement (spec §7.2 "Interception", decisions 0032 and 0052):** end TLS for hosts that need request rules or a credential, with a CA for each session kept in memory. Verify upstream in full. Forward a canonical request and refuse smuggling, decode bodies, offer HTTP/2 to the guest, and read WebSocket frames on model hosts.
- **Today:** `ca.go` is sound, at 134 lines on the standard library. `http_*` has 5,463 lines, and 2,392 of them in `http_physical_h2_*` own the HTTP/2 writes through the pinned `x/net` scheduler hooks. That layer forces decision 0052: exactly Go 1.27.4, `x/net` 0.62.0 with the `http2legacy` tag, and a refusal when the build lacks them. `websocket*.go` adds a 1,021-line frame reader.
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [elazarl/goproxy](https://github.com/elazarl/goproxy) | MITM with `TLSConfigFromCA`, a certificate cache, request and response handlers, `AllowHTTP2` (v1.9.0) and WebSocket relay | BSD-3-Clause | v1.9.4, 12 Nov 2026 | Go library | High. It takes our `ca.go` as its signer. Its HTTP/2 MITM is 4 months old, and v1.9.1 to v1.9.4 fixed MITM edge cases, so pin it and fuzz it. |
| [lqqyt2423/go-mitmproxy](https://github.com/lqqyt2423/go-mitmproxy) | A Go port of mitmproxy with HTTP/2, WebSocket and addon hooks, including one for each WebSocket message | MIT | v1.9.5, 12 Nov 2026 | Go library and CLI | Medium. Its hook for each message suits a guard on Codex's WebSocket. The project is smaller, with one main maintainer. |
| [mitmproxy](https://github.com/mitmproxy/mitmproxy) | The best interceptor: HTTP/2, HTTP/3, WebSocket and Python addons | MIT | v12.3.1, 25 Oct 2026 | Python daemon | Low. It needs a Python runtime on each host, it is heavy for each session, and it listens on TCP. |
| [google/martian](https://github.com/google/martian) | A MITM library | Apache-2.0 | archived, last release v3.3.3 in Apr 2024 | Go library | Rejected: unmaintained. |
| [smallstep/crypto](https://github.com/smallstep/crypto) | `x509util` and `keyutil` | Apache-2.0 | v0.92.1, 21 Nov 2026 | Go library | Not needed: `ca.go` is 134 lines on the standard library. |
| [mkcert](https://github.com/FiloSottile/mkcert) | Puts a local CA into the system stores | BSD-3-Clause | v1.4.4, Apr 2022 | CLI | Rejected: stale, and we do not want the CA in the host's stores. |
| [gobwas/ws](https://github.com/gobwas/ws), [coder/websocket](https://github.com/coder/websocket) | Reading WebSocket frames | MIT, ISC | v1.5.0 on 4 Nov 2026, v1.8.15 on 15 Jun 2026 | Go libraries | High, if frames must be read later. |

- **Over-specification.** The demand for HTTP/2 to the guest comes from a write-up about a browser page (491 ms over HTTP/1.1, against 7,374 ms through an HTTP/1.1-only proxy; parity.md). An agent's model calls are a few long streams. npm and pip speak HTTP/1.1 anyway, and spliced hosts keep HTTP/2 end to end. The layer that owns each physical write exists to count usage exactly and to cancel a stream when policy changes mid-stream. A first product needs neither.
- **Recommendation: drop it from v1, and adopt goproxy later.** With sections 4 and 6, the daily path needs no interception. When interception returns (a token for `gh`, path rules, the feed gate), use goproxy's MITM with `AllowHTTP2`, our `ca.go` and about 300 lines of canonical checks. Go's `net/http` already refuses many smuggling forms. Delete the HTTP/2 physical layer, and with it decision 0052's exact-toolchain and build-tag pins. Turn off Codex's WebSocket transport (`supports_websockets = false`, below) instead of parsing frames. Effort: S now (take it off the path), M later. It removes about 2,400 lines of the HTTP/2 layer, 1,021 lines of WebSocket code and most of the remaining 3,000 HTTP lines. Risk: medium, later. goproxy's HTTP/2 MITM is new.
- **Spec impact:** §7.2 "Interception" drops "offers HTTP/2 … on every intercepted host" and the WebSocket frame paragraph. 0052 is replaced by a new record. 0038 is written before interception returns. The §12 target for an intercepted request stays.

#### 4. Secrets at the network edge (plan tasks P2.3, and the mint part of P2.5)

- **Requirement (spec §3.3 G4, §7.3, decision 0032):** a bound key's value never enters the sandbox, yet the agent still signs in. On a bound host, a request with any other credential is refused (the identity pin). Values live in memory only, and the laptop arms the session again after a host reboot.
- **Today:** 11,173 lines in `internal/egress` (`bound_*` 6,790, `sentinel*` 930, request and response secret streams 2,182, the member registry 788, mint), plus `secretbind` 596, `secretmem` 360 and `secretmode` 16. Sentinels keep each value's prefix, length and alphabet through a shape grammar, so that agents' format checks pass. Credentials that a response mints get new sentinels. Many agents in one sandbox share a member registry.
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [sandbox-runtime credentials](https://github.com/anthropic-experimental/sandbox-runtime) | `mode: "mask"`: a sentinel in the sandbox that the proxy swaps back on its way to `injectHosts`. It needs TLS termination, and it re-signs SigV4 for masked AWS keys. | Apache-2.0 | v0.0.83, 13 Nov 2026 | TypeScript library or CLI | Medium. It is the same design, and proves it. It brings the JS runtime cost of section 1, and it is a beta. Its SigV4 re-signing answers the spec's question 15. |
| [superfly/tokenizer](https://github.com/superfly/tokenizer) | An HTTP proxy on goproxy that injects a credential into a named header. The client speaks plain HTTP to it, and it speaks HTTPS upstream. | Apache-2.0 | no tags, commit 18 Nov 2026 | Go library and daemon | Medium. The pattern fits (no CA needed). Its sealed secrets and client authentication are more than we need. |
| [cyberark/secretless-broker](https://github.com/cyberark/secretless-broker) | Plain HTTP to a broker that adds credentials from Conjur or other stores | Apache-2.0 | v2.0.0, 16 Sep 2026 | Go daemon | Low. It refuses `CONNECT` ("CONNECT is not supported") and is built around credential providers. |
| [Envoy credential injector](https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/credential_injector_filter) | Puts a secret into `Authorization` or a named header | Apache-2.0 | v1.40.2 | C++ filter | Low. See section 1. |
| [dagger/container-use](https://github.com/dagger/container-use) | Secrets as variables inside the container | Apache-2.0 | v0.4.2, 19 Aug 2025 | Go CLI | Rejected. The value is exposed, and there is no release in 12 months. |
| LLM gateway pattern ([Claude Code docs](https://code.claude.com/docs/en/llm-gateway), [Codex through a gateway](https://coder.com/docs/ai-coder/ai-gateway/clients/codex)) | `ANTHROPIC_BASE_URL` with a gateway credential, where "the provider key stays server-side". For Codex, `[model_providers.x]` with `base_url`, `env_key`, `wire_api = "responses"` and `supports_websockets = false`. | n/a | documented by the vendors | Agent settings | High. The trusted side strips every credential header and sets the real one, so the pin needs no CA and no sentinel shape. |

- **Over-specification.** The shape grammar, echo replacement, mint scanning and member registry exist because the swap happens inside intercepted TLS, where the guest must hold a string that passes format checks. Behind a base URL, the guest can hold any placeholder.
- **Recommendation: simplify to the gateway pattern of tokenizer and the LLM gateways.**
  - The egress process serves one `httputil.ReverseProxy` route for each bound provider. It strips `authorization`, `x-api-key` and `proxy-authorization`, sets the bound header, and dials the provider over verified TLS. That is about 300 to 500 lines.
  - The agent reaches the route at a plain-HTTP base URL inside the sandbox, for example a path on the guest's existing proxy port, carried over the link. Nothing new listens on the host.
  - The `CONNECT` allowlist refuses the model host, so the gateway is the only way to it.
  - Keep `secretmem`. Cut `secretbind` down to its header formats.
  - Git over HTTPS uses `url.<base>.insteadOf` to the same pattern, or the SSH key door. Everything else stays `--expose`.

  Effort M. It removes about 10,000 of the 11,173 lines. Risk: medium.
  - An agent that has no base-URL setting needs interception later, or `--expose`.
  - Claude Code's OAuth token through a gateway needs `Authorization` and `anthropic-beta` passed on, as its docs say. P2.3's manual check confirms it, and also that an `http://` base URL is accepted.
- **Spec impact:** §7.3 (bound secrets go through a base URL first, and the swap inside interception comes later), §8.6 `SecretBinding` (add the base-URL variable or provider block, and drop the `Prefix` shapes and, if `env_key` works, Codex's `Store`), §7.2 "Model hosts are always intercepted" becomes "always through the gateway", and G4's tripwire applies only where flows can be read. 0032 holds as it is.

#### 5. Model API guard, usage and budgets (plan tasks P2.4, P2.16)

- **Requirement (spec §7.2 "Model APIs", §17.8):** refuse tool types that make the provider fetch any URL or call MCP servers, content given by URL, and file uploads. Allow only listed paths. Count the tokens that each answer reports, and refuse past a budget with an error that no client retries.
- **Today:** `model*.go` has 2,818 lines and `websocket*.go` 1,021. The code counts at "the original physical connection writers", and has a separate lane for WebSocket accounting.
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [LiteLLM proxy](https://github.com/BerriAI/litellm) | Keys, budgets, usage, routing and guardrail hooks | MIT core, with an `enterprise/` folder under its own licence | v1.111.0, 25 Nov 2026 | Python daemon | Low. It is heavy on every host, and two of its PyPI releases were compromised in March 2026 (isolation.md §1.3). |
| [Portkey gateway](https://github.com/Portkey-AI/gateway) | Routing, guardrails and usage | MIT | v1.15.2, 12 Jan 2026 | Node daemon | Low. It needs a JS runtime, and has had no release for 10 months. |
| [Envoy AI Gateway](https://github.com/envoyproxy/ai-gateway) | Token usage, rate limits and provider routing | Apache-2.0 | v1.3.0, 18 Nov 2026 | Go control plane on Envoy | Low. It needs Envoy, and was built for Kubernetes first. |
| [Bifrost](https://github.com/maximhq/bifrost) | A full gateway with budgets, governance and a UI | Apache-2.0 | releases weekly (27 Nov 2026) | Go daemon | Low to medium. Far more than we need, and no rule for tool types. |
| [agentgateway](https://github.com/agentgateway/agentgateway) | An LLM and MCP gateway | Apache-2.0 | v1.7.0, 27 Oct 2026 | Rust daemon | Low. Another daemon on each host. |
| [Helicone](https://github.com/Helicone/helicone) | Observability for model calls | Apache-2.0 | Aug 2025 | TypeScript daemon | Rejected: no release in 12 months, and hosted first. |
| [coder/aibridge](https://github.com/coder/aibridge) | Records usage and tool calls for Anthropic and OpenAI | AGPL-3.0 | moved into coder/coder | Go library | Rejected: it cannot be linked. |
| [anthropic-sdk-go](https://github.com/anthropics/anthropic-sdk-go), [openai-go](https://github.com/openai/openai-go) | Types for stream events and usage | Apache-2.0 | v2.15.0 on 24 Nov 2026, v3.108.0 on 26 Nov 2026 | Go libraries | Medium, for decoding usage. A plain struct is enough. |
| [tiktoken-go](https://github.com/pkoukk/tiktoken-go) | Counting tokens on the client | MIT | v0.1.8, Sep 2025 | Go library | Not needed. Answers carry their usage. |

- **Recommendation: keep ours, and simplify.** It belongs inside the gateway of section 4. No gateway product has the rule "refuse these tool types and URL sources", so each would need a plugin. Each also brings a large runtime to every host for one JSON check.
  - A guard of about 200 lines: walk the body with `encoding/json`, and fail closed on a tool type that the list does not name.
  - About 200 lines of usage from the final stream events.
  - Defer the budget refusal and the WebSocket path, and turn Codex's WebSockets off.

  Effort S to M. It removes about 3,400 lines. Risk: low to medium. Request shapes change with each API release, so keep the recorded-traffic tests of P2.4.
- **Spec impact:** §7.2 "Model APIs" (drop the WebSocket paragraph and say that Codex runs with `supports_websockets = false`), §17.8 (drop counting at the physical writers and exact accounting of cut streams), and P2.16 moves after the first product.

#### 6. Service rules and the push door (plan task P2.5)

- **Requirement (spec §7.2 "Rules for services", §5.5):** with a granted GitHub token, refuse repository creation, gists, workflow writes, runner tokens, and ref moves outside the session's prefix. Pushes over git go only to the session's refs. Registries allow only GET and HEAD. Minted credentials are replaced.
- **Today:** 2,816 lines. They hold a hand-written GraphQL lexer and parser (`graphql_*`, 1,002 lines), REST ref rules, the receive-pack pkt-line reader, the push door, and `mint.toml` and `minted.json` with their code.
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [vektah/gqlparser](https://github.com/vektah/gqlparser) | Parses GraphQL documents into operations and selections | MIT | v2.5.64, 22 Oct 2026 | Go library | High. It replaces about 1,000 lines with about 150. |
| [go-git](https://github.com/go-git/go-git) `plumbing/format/pktline`, `protocol/packp` | Reads the receive-pack command list | Apache-2.0 | v5.19.3, 4 Oct 2026 | Go library | Medium. It is a large module, and our reader is small. |
| [gitleaks](https://github.com/gitleaks/gitleaks) | Credential patterns | MIT | v8.30.1, 21 Mar 2026 | Data and a Go library | Already used as data. Keep it. |
| [OPA](https://github.com/open-policy-agent/opa) | Could express the REST rules in Rego | Apache-2.0 | v1.23.0, 26 Nov 2026 | Go library | Low. It adds a language for about 20 rules. |

- **Over-specification.** None of these rules matters until a token that can write sits on the trusted side. With no bound GitHub or npm token, `npm publish`, repository creation and pushes fail at the service with 401. The daily path takes work out through `pull`.
- **Recommendation: defer the whole task.** Splice registries and code hosts in v1. When the task returns, adopt gqlparser, keep the rule tables as TOML, and prefer fine-grained tokens and deploy keys (section 9). Effort: S now, M later. It removes about 2,800 lines from v1. Risk: low.
- **Spec impact:** the presets table (GET and HEAD only applies when a registry token is bound), §7.2 "Rules for services" and the push door move after the first product, and the `balanced` preset splices code hosts.

#### 7. Unknown sites, holds, the classifier and the judge (plan tasks P2.6, P2.11, P2.12)

- **Requirement (spec §7.2 "Unknown sites" and "Held requests", §7.8):** decide a flow that no rule covers: ask, deny, allow and log, or classify. A held request outlives each client's own timeout.
- **Today:** holds, once rules and the unknown action take 1,224 lines. The classifier and judge take 3,832: fixed denies, confusable names with the Unicode 15 data, RDAP, a popularity file, model and command judges, and an evaluation harness.
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [sandbox-runtime](https://github.com/anthropic-experimental/sandbox-runtime) ask callback, and the `srt-proxy` decider | Deny list, then allow list, then a callback or a process on an fd. It fails closed after 5 s. | Apache-2.0 | v0.0.83 | TypeScript | Medium, as a model to copy. Its protocol is simple. |
| [coder/httpjail](https://github.com/coder/httpjail) `--sh` and `--proc` | A rule program on stdin and stdout | CC0-1.0 | v0.6.2 | Rust | Medium, as a model to copy. |
| Docker Sandboxes' approvals | Refuses at once, and lets a later retry pass after the answer | closed | n/a | product | The behaviour is documented, and the spec cites it. |

- **Over-specification.** The spec says that the classifier "is not a wall". The hold timers depend on research into each client's timeouts (question 70), and Docker Sandboxes does without them.
- **Recommendation: simplify P2.6, and defer P2.11 and P2.12.** Refuse at once with the one-line 403 and the grant command. Set `needs_input` in the launcher. An answer adds a rule, so the next try passes, and agents and package managers do retry. If parallel work with nobody watching needs a judge, keep only `judge = "command"`, a program like srt's decider. Effort S. It removes about 4,500 lines from v1. Risk: low. The first try to a new host fails once.
- **Spec impact:** §7.2 "Held requests" drops `hold`, `ask_open` and the 3 s transparent hold. §7.8 and `[classifier]` move after the first product. Question 70 closes.

#### 8. Policy layers, live changes and config (plan tasks P1.12 for the network, P2.9, P2.14; spec §8)

- **Requirement (spec §8.1 to §8.8):** rules live in TOML on the laptop, in layers: built-in, all sessions, host, repository, profile and session. The narrowest layer with a matching rule decides, and a deny wins inside a layer. Locks hold, and a repository's own file only narrows. A change reaches running sandboxes, and a lockdown closes them.
- **Today:** 3,213 lines in egress: layer values, merges, content-hash rule IDs, layer hashes, private grants, expiry and lockdown. Beside them, `nettarget` has 693 lines (names, wildcards, and the Public Suffix List through `x/net`), plus `grant` 85, `configdata` 383 and `repopolicy` 243.
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [OPA / Rego](https://github.com/open-policy-agent/opa) | A general policy engine | Apache-2.0 | v1.23.0, 26 Nov 2026 | Go library | Low. It adds a language and many dependencies, and it does not remove the layer merge. |
| [cedar-go](https://github.com/cedar-policy/cedar-go) | Authorisation in which a `forbid` beats a `permit` | Apache-2.0 | v1.9.4, 23 Nov 2026 | Go library | Low. An allowlist of hosts has no principals. A deny-wins rule is 20 lines. |
| [smokescreen ACL](https://github.com/stripe/smokescreen) | Allow lists for each role, a global deny, and a `report` mode | MIT | v1.0.1 | YAML | A model to copy for "report" (log and allow). |
| [sandbox-runtime lists](https://github.com/anthropic-experimental/sandbox-runtime) | `deniedDomains` first, then `allowedDomains`, changed live through `--control-fd` | Apache-2.0 | v0.0.83 | JSON | The simple semantics that we could copy. |
| [BurntSushi/toml](https://github.com/BurntSushi/toml) | Parsing | MIT | already used | Go library | Keep it. |

- **Recommendation: keep plain TOML, and simplify the semantics.** Copy srt: any deny beats any allow. A lock is a deny that a session cannot remove. A rule made `--in` a session adds to the allow list. A live change swaps the sets in one step for new flows. A lockdown restarts the egress process with the locked set, which closes every open flow. Drop rule IDs made from hashes, layer hashes, and compare-and-swap replaces. Keep `nettarget`. Effort M, mostly specs and tests. It shrinks about 3,200 lines to about 800. Risk: low. A session can no longer allow a host that all sessions deny. The user removes that deny instead.
- **Spec impact:** §8.2 rules 2 and 4, §8.8 steps 1 to 4 (closing open flows, hashes, `policy-pending.json`), and P2.14 shrinks from M to S.

#### 9. SSH keys (plan task P2.13)

- **Requirement (spec §7.9):** a sandbox gets only the named keys. The agent lists keys and signs, refuses add, remove, lock and extensions, and writes an audit line for each signature.
- **Today:** `internal/supervisor/sshagent*.go` (about 640 lines) on `golang.org/x/crypto/ssh/agent`, with a filter of about 140 lines on the request types. `internal/guest/sshagent.go` adds 176.
- **Options:** OpenSSH's own `ssh-agent`, once per session, with `ssh-add -h` [destination constraints](https://www.openssh.org/agent-restrict.html). It is BSD, a separate executable, and maintained. But the guest can still send add, remove and lock to it, it writes no audit line for each signature, and its constraints need the host keys known in advance.
- **Recommendation: keep ours.** It already rests on a maintained library. No tool gives "list and sign only, audited, over a link stream". Effort none. Risk: low.
- **Spec impact:** none.

#### 10. Package managers and registry gates (plan tasks P2.8, P7.1)

- **Requirement (spec §7.10, §7.11):** package managers skip install scripts and versions younger than N days. The trusted side refuses young versions, versions in the OSV malicious feed, and archives fetched from hosts that are not registries.
- **Today:** P2.8 is built, as data: `internal/guest/pkgsettings*.go` (217 lines) and a golden file. P7.1 is not built. `registry_gate.go` is a mutex, and no feed, publish-time lookup or Go index mirror exists.
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| Native age settings: npm `min-release-age`, pnpm `minimumReleaseAge`, uv `exclude-newer`, pip `--uploaded-prior-to` | Age gates in the tools themselves | various | current | Settings | High. P2.8 already writes them. Guest root can undo them. |
| [ossf/malicious-packages](https://github.com/ossf/malicious-packages) | `MAL-` records in OSV format | Apache-2.0 | commits daily | Data | High, to ship with each release. |
| [osv-scanner](https://github.com/google/osv-scanner) | Lockfile scans, with offline databases | Apache-2.0 | v2.7.1, 9 Nov 2026 | Go CLI and library | Medium. An optional offline check of lockfiles on `pull`. |
| [deps.dev API](https://github.com/google/deps.dev) | Publish times and versions | Apache-2.0 (code) | active | Hosted API | Rejected for runtime use. A third party would see every package name (0031). |
| [DataDog supply-chain-firewall](https://github.com/DataDog/supply-chain-firewall) | Wraps npm and pip, using DataDog's dataset and the OSV.dev API | Apache-2.0 | v4.1.0, 17 Sep 2026 | Go CLI | Low. Its local mode calls OSV.dev. It runs in the guest, where the guest can undo it. |
| [safedep/pmg](https://github.com/safedep/pmg) | A package proxy with its own CA, cooldowns and threat intelligence | Apache-2.0 | v0.37.0, 26 Nov 2026 | Go CLI | Low. Each package is checked against SafeDep's cloud API, which is phoning home. |
| Aikido Safe Chain, Socket's free firewall, GitHub's package firewall | Install firewalls | AGPL-3.0 (earlier research), closed, Enterprise preview | active | various | Rejected, as before. |

- **Recommendation: keep P2.8, and simplify P7.1.** Drop the trusted-side age gate, which is the hardest part: publish-time lookups, PyPI files and the Go index window. The guest settings already cover the age gate for npm, pnpm, uv and pip. If a check that the guest cannot change is wanted, build only the feed check. Ship the `MAL-` records with each release, and match the ecosystem, name and version from the download path. That needs registry interception, so it comes with goproxy (section 3). Effort: none now, then S. P7.1 shrinks from M to S. Risk: low. The docs already say that guest settings are defence in depth.
- **Spec impact:** §7.11 shrinks to the feed. The publish-time table and "The Go window" go.

#### 11. Decoys (plan task P7.2)

- **Requirement (spec §21.7):** decoy credential files in the home, whose values are tripwires in the egress process.
- **Today:** built: guest 294 lines, supervisor 224, egress 306, plus redaction.
- **Options:** [Thinkst canarytokens](https://github.com/thinkst/canarytokens), under GPL-3.0 with wide extra terms on derived works, and no releases. Its tokens alert through Thinkst's server or a server of your own that listens on the network.
- **Recommendation: keep ours.** It is small and local, and canarytokens would phone home or need a listener. Without interception in v1, a decoy fires only in host names and gateway requests. Leave it on, at low priority. Effort none.
- **Spec impact:** §21.7 notes that the tripwire covers only readable flows.

#### 12. Other tasks in range (plan tasks P2.9, P2.10, P2.15, P2.17)

- **P2.15, editors over SSH:** already on `x/crypto/ssh` and `pkg/sftp`. Keep it. It is off the daily path.
- **P2.17, agent rows:** data. Keep them. Add each row's base-URL and gateway settings (section 4).
- **P2.9, a repository's own file:** keep it on the simpler semantics of section 8.
- **P2.10, walls by default:** it depends on P2.3, P2.6 and P2.8. With the slim forms above, it can follow the gateway and the refuse-then-retry approvals.

---

### Top swaps by impact on the daily-use path

The path: a session on the owner's Linux server, where the agent reaches its model API and package registries, and no secret is exposed in the sandbox.

1. **The model API goes through a gateway route on the trusted side, with no TLS interception (sections 4 and 5).** The agent's base URL is a plain path inside the sandbox. The egress process strips credentials, sets the real one and dials the provider over verified TLS. The model host is never allowed for `CONNECT`. This gives the identity pin and a place for the tool-type guard and usage counting, and it needs no CA in the guest. It removes the need for about 14,000 lines (bound secrets, model and WebSocket code). Claude Code documents this route, and so does Codex with `supports_websockets = false`. Effort M.
2. **Splice registries and code hosts (sections 3 and 6).** With no token in the sandbox, a publish or push fails at the service. v1 then needs no interception at all. Retire the HTTP/2 physical layer and decision 0052's exact-toolchain and `http2legacy` pins. That removes a build risk on every release, and about 8,000 lines from the path. Effort S.
3. **The proxy front on goproxy, with the SNI check on `crypto/tls` (section 1).** Port smokescreen's address test cases, and close the gap for 6to4 and Teredo addresses. goproxy's MITM is then ready for the later interception steps. Effort M.
4. **Refuse, then let the retry pass, instead of holds. Defer the classifier and judge (section 7).** That removes about 4,500 lines and the per-client timeout research. Effort S.
5. **Flat deny-wins policy, swapped in one step for new flows (section 8).** It shrinks the policy code from about 3,200 lines to 800, and makes P2.14 small. Effort M, mostly specs.
6. **No guest DNS on the process wall (section 2), and only a feed check for registries later (section 10).** The explicit proxy needs no DNS, and P2.8's guest settings already enforce the release age. Effort S.

After these, v1 of the egress process is the goproxy front, the range and SNI checks, the gateway routes with the guard and usage, flat policy and audit lines. That is about 3,000 to 4,000 lines, against 36,787 today. The existing tests for ranges, SNI, smuggling and recorded model traffic can serve as its acceptance tests.

## Part C: hosts, sessions, files, the client and the CLI

Checked on 27 November 2026, against the branch `docs/isolated-sessions` (head `672a0146`) and `main` (`c32cf0ba`). Licences and release dates come from the GitHub API on that day. "Maintained" means a release in the last 12 months (since 27 November 2025).

### The headline

- `main` holds the working product: about **44,600** lines of Go (non-test). Its `internal/host` is 11,000 lines and its `internal/client` 5,200.
- The branch holds about **450,000** lines. `internal/host` grew to 101,800 and `internal/client` to 98,600. About 89,000 host lines and 91,000 client lines are in files that `main` does not have.
- The largest single block is not isolation. It is lifecycle proof machinery: custody, purge, native managers, cold and warm hand-offs, descriptor retirement. That is about 44,000 lines in `internal/host` alone (decisions 0050 and 0051, both still "proposed").
- On the branch, even an unwalled (`none`) session runs through a supervisor, a link and a guest init. On `main` it was one holder under `systemd-run --user`.
- Most of this area needs no new library. It needs less code: OpenSSH, systemd, git and the standard library already meet the requirement. The few real library swaps are go-toml v2, `x/crypto/ssh` (channels and `agent`), go-selfupdate and Renovate.

### 1. Whole-product references

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [DevPod](https://github.com/loft-sh/devpod) | Client-only dev environments on any SSH host; providers as small CLIs; injects its agent over SSH stdio | MPL-2.0 | No: v0.6.15, 10 Mar 2025. Upstream has stalled. Fork [skevetter/devpod](https://github.com/skevetter/devpod) (Devsy) had v0.26.3 on 2 Oct 2026, with about 200 stars | CLI and Go libraries | Shows that "client only, any SSH host" works without servers. Built around devcontainers; attach is plain `ssh`, not byte-exact; has no holder or replay. Use as a reference for the provider model and agent injection only. |
| [Coder](https://github.com/coder/coder) | Workspaces, agent, reconnecting PTY, port forwarding | AGPL-3.0 | Yes: v2.38.11, 25 Nov 2026 | Server, agent and CLI | Needs `coderd`, an agent that dials out, and DERP relays. That breaks "nothing listens, no relays". AGPL cannot be linked. Ideas only. Its [registry](https://github.com/coder/registry) (Apache-2.0) has agent install modules. |
| [Gitpod](https://github.com/gitpod-io/gitpod) | Gitpod Classic | AGPL-3.0 | No: 2022.11.3, Jun 2023 | Server | Classic is retired, and the newer product is closed. [openvscode-server](https://github.com/gitpod-io/openvscode-server) (MIT) is an editor server, which is out of scope. Nothing to use. |
| [container-use](https://github.com/dagger/container-use) | A container and a git branch for each agent; `log`, `checkout`, `merge` | Apache-2.0 | No: v0.4.2, 19 Aug 2025 (pushes continue) | CLI and MCP server on Dagger | Its branch-per-environment flow matches the seed-and-pull design (spec 6.1, 6.10). Borrow the UX. Do not depend on it: it is not maintained by our rule, and it needs the Dagger engine. |
| [sandbox-runtime](https://github.com/anthropics/sandbox-runtime) (`srt`) | A process sandbox (bubblewrap with seccomp on Linux, Seatbelt on macOS), and domain allow-lists through HTTP and SOCKS proxies | Apache-2.0 | Yes: v0.0.83, 13 Nov 2026 ("beta research preview") | Node CLI and library | Its proxies use Unix sockets on Linux and a loopback port on macOS. It could wrap the agent as a separate executable, as a `process` wall with network rules. That matters to the walls audit more than to this one. |
| [OpenHands](https://github.com/OpenHands/OpenHands) runtime | A Docker sandbox with an action server | MIT (core) | Yes: v1.46.0, 25 Nov 2026 | Python server | The action server talks HTTP to the backend. It runs its own agent loop, not the agents' TUIs. No fit. |
| [E2B](https://github.com/e2b-dev/E2B), [runtime](https://github.com/e2b-dev/runtime) | Firecracker sandboxes, and an exec and process API | Apache-2.0 | Yes: SDK 26 Nov 2026; infra 2026.30, Sep 2026 | SDK and hosted service | Already a provider (`host create e2b`). The self-hosted infra is a whole cluster. Keep it as a provider only. |
| [Daytona](https://github.com/daytonaio/daytona) | Sandboxes | Was AGPL-3.0 | No: archived in June 2026, and development moved to a private codebase | Server | No fit. |
| [Herdr](https://github.com/herdrdev/herdr) | A remote agent workspace: SSH bootstrap, bridge, reconnect, image paste, resume table | Apache-2.0 | Yes: v0.9.5, 14 Oct 2026 | Rust app | The research verdict still holds (herdr.md 8.2): it re-renders cells, so it is not byte-exact. Borrow its modules as designs; Rust cannot be linked. |

**Verdict.** No whole product meets the core: byte-exact attach, replay by offset, no server and no relay. The research was right to build the core. But none of these products carries custody, purge or native-manager layers either. Each one stops a unit or a container and deletes a folder.

### 2. Session holding and attach (spec 6.3, 6.8; decisions 0002, 0004, 0028)

- **Requirement:** keep each agent's PTY alive without a client. Attach byte for byte, replay from the client's last byte offset, and redraw from a terminal model when the offset is too old. Never send RIS. Report the agent's state.
- **Today:** `internal/host` holder, screen, vt, session ops, restore and resume (about 12,000 lines; 5,500 of them on `main`), plus `internal/ring` (76), `term` (799), `keys` (548), `proto` (2,623) and the client's attach, input and conn files (about 4,500). It works on `main`.

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [tmux](https://github.com/tmux/tmux) | Persistence, redraw, control mode | ISC | Yes: 3.8, 9 Sep 2026 | Daemon and CLI | Re-renders, so no native scrollback or OSC passthrough. Fails byte-exact. |
| [zellij](https://github.com/zellij-org/zellij) | A multiplexer with sessions | MIT | Yes: v0.46.1, 24 Nov 2026 | Daemon and CLI | Re-renders. Fails byte-exact. |
| [zmx](https://github.com/neurosnap/zmx) | Persistence with libghostty-vt redraw | MIT | Yes: v1.0.2, 24 Nov 2026 | CLI | Rejected in 0002 (RIS on detach, no stable interface, crashes). Now at 1.0, but it still has no offset replay or status engine. |
| [shpool](https://github.com/shell-pool/shpool) | Raw passthrough with native scrollback; redraws on reattach | Apache-2.0 | Yes: v0.11.7, 13 Nov 2026 | Rust daemon | The nearest match. One daemon per user, though, not one per session. No offset replay, no per-session cgroup, no hooks. Worth reading. |
| [dtach](https://github.com/crigler/dtach) | Detach and attach only | GPL-2.0 | No: 0.9 (2016) | CLI | No redraw model, no replay. |
| [abduco](https://github.com/martanne/abduco) | Detach and attach only | ISC | No: v0.6, Feb 2020 | CLI | Not maintained. |
| [Eternal Terminal](https://github.com/MisterTea/EternalTerminal) | A resumable transport | Apache-2.0 | Yes: et-v7.1.1, 29 Oct 2026 | Daemon | Listens on a port. Breaks the rules. |

- **Recommendation: keep ours.** No tool gives byte-exact passthrough with replay from a byte offset and a per-session memory cap. This is the product's core, and it is small. Effort: none. Risk: low.
- **Spec impact:** none. Section 11 below proposes taking `none` sessions off the supervisor path.

### 3. Session lifecycle: start, stop, destroy, restore, purge (P1.11, H15; spec 6.6–6.8, 14.12; decisions 0050, 0051)

- **Requirement:** start a session that survives logout. Stop it fully, so that no process outlives the stop. Destroy its files. Start working sessions again after a reboot. Remove everything on `host rm --purge`.
- **Today:** about **44,000** lines in `internal/host`. They cover purge (11,800 lines in 66 files), custody (4,400), `none` (5,000), native (3,200), slot (3,700), ordinary (3,100), auxiliary (2,900), startup (2,200), stopped (2,000), original, cold, live, members and peer. Add 6,000 lines of hand-written D-Bus code, 2,200 for the client side of purge and 2,200 for the footprint ledger and the `host rm` CLI. Purge alone has a "fixed purge reader in a separate restricted guest" (spec 6.7). `main` did all of this with `systemd-run --user`, a `setsid` fallback and `rm`.

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| systemd user units (`systemd-run --user`, `systemctl --user stop/kill`, `--collect`, `KillMode=control-group`) | One cgroup per session. A stop that waits until the cgroup is empty. A memory cap. Linger for boot | LGPL-2.1 (system tool) | Yes | Host service manager | Meets the whole requirement on Linux. A cgroup-v2 stop leaves no writer behind, which is the guarantee that custody tries to prove by hand. |
| [coreos/go-systemd](https://github.com/coreos/go-systemd) `dbus` | `StartTransientUnitContext` with a job-result channel, plus `StopUnit`, `KillUnit`, `SetUnitProperties` and `GetUnitProperties` | Apache-2.0 | Yes: v22.7.0, 28 Jan 2026 | Go library (on godbus) | Use it wherever job results or properties are needed, instead of raw `godbus` calls. |
| [godbus/dbus](https://github.com/godbus/dbus) | Raw D-Bus | BSD-2-Clause | Yes: v5.2.4, Oct 2026 | Go library | Already used, but by hand. Too low-level for this job. |
| Process groups and pidfds (`setsid`, `kill(-pgid)`, `unix.PidfdOpen`) | Fallback without a user manager | Go and `x/sys` | Yes | Standard library | `main`'s fallback. Enough for containers and minimal hosts. |

- **Recommendation: simplify.** One transient unit per session, as on `main`. Stop is `systemctl --user stop` (or `KillUnit`, then stop). Destroy is a stop, then `os.RemoveAll` on a folder that is never reused, because session IDs are unique. Restore is `main`'s restore unit. Purge is: stop every unit with the prefix, disable the restore unit, revert the slice, undo a short JSON list of what setup turned on (linger, terminfo), then remove the three folders. Reject 0050 and 0051. Effort: **M**, mostly deletion. It removes about 44,000 host lines and 4,000 client lines. Risk: medium, because the tests written for the custody paths go too, so the plain paths need e2e tests for stop, destroy, reboot and purge.
- **Spec impact:** 6.6 to 6.8 and 14.12 shrink to one page. The purge-reader text in 6.7 goes. Decisions 0050 and 0051 are rejected. A new record says "a session is a systemd unit; a stop is the unit's stop".

### 4. SSH transport and host keys (H2, H3; spec 14.7.1, 14.8.5; decisions 0003, 0034, 0035)

- **Requirement:** reach any host that `ssh host` reaches (ProxyJump, IAP, SSM, agents, FIDO keys). Fail when a host's key changes, whatever the user's `StrictHostKeyChecking` says. Keep one fast shared connection per host.
- **Today:** `ssh*` and `pin*` files, plus the native log and collector files: about 7,400 client lines. About 5,200 of those are the pin machinery: the `-E` log verdict parser, a FIFO pump, staged pins, a mutation scope, and the ControlMaster "epoch" index with 128-bit labels. `main`'s `ssh.go` was 247 lines.

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [OpenSSH](https://www.openssh.com/) `ssh`, `ssh-keygen`, `ssh-keyscan` | Full `ssh_config`, `HostKeyAlias`, `UserKnownHostsFile`, `StrictHostKeyChecking=yes`, ControlMaster with `%C` | BSD | Yes: 10.6p2 | System CLI | Meets the whole requirement. 0003 still holds: Zed, VS Code, Mutagen, Herdr and DevPod all shell out to `ssh`. |
| [`x/crypto/ssh`](https://pkg.go.dev/golang.org/x/crypto/ssh) and [`knownhosts`](https://pkg.go.dev/golang.org/x/crypto/ssh/knownhosts) | Fingerprints, known_hosts lines, an in-process client | BSD-3-Clause | Yes: v0.59.0, Nov 2026 | Go library | Good for writing and checking pin lines (`knownhosts.Line`, `ssh.FingerprintSHA256`). Not a transport: it lacks the user's config and agents. |

- **Recommendation: simplify, keeping OpenSSH.** Pin with `HostKeyAlias` and a private `known_hosts-<host>` file, plus the options block of 0034. Get the first key in one of two ways. (a) `--host-key SHA256:…`, checked against the output of `ssh-keyscan`. (b) One connection that the user's own OpenSSH settings verify (it asks at a terminal), after which the client reads the host's public keys over that verified session. Drop the log-verdict parser, the staging and the epoch index. ControlPath stays `%C` in a short private folder, as on `main`. Effort: **S–M**. It removes about 5,000 lines. Risk: low, because OpenSSH still does every check.
- **Spec impact:** 14.7.1 shrinks from about 90 lines to about 15. 0035 is replaced by a short record.

### 5. Host identity, tiers, grants and the ledger (H4, H5; spec 14.7.2, 14.8)

- **Requirement:** do not send a secret, or the laptop's SSH agent, to a machine where someone else has root, unless the user says so.
- **Today:** about 7,200 lines (tier, grant, grants, ledger, outbound, sent, baseline), plus host ID code. There is a separate `tiers.toml` "so an older client cannot drop it", three tiers, a ledger of everything sent, and a 13-row grant table.
- **Options:** no library covers this. It is policy. Docker contexts, kubectl and DevPod have nothing like it. Codespaces and Coder enforce such rules on a server.
- **Recommendation: simplify.** Keep one `trusted = true|false` per host in `hosts.toml` (the "own" and "shared" of today). A secret goes to an untrusted host only when its `[secrets.NAME] on_hosts` lists that host (spec 18.2 already has `on_hosts`). Work out the "rotate these" list from config at `host rm`. Drop the ledger, `throwaway`, the host-ID downgrade and the 13-row table. Effort: **S**. It removes about 6,000 lines. Risk: low.
- **Spec impact:** 14.8 shrinks to one table of four rows. 14.6.1 (`tiers.toml`) goes. 0036 is replaced.

### 6. Host add, prepare, helper and runtime delivery, upgrades (H9, H10, H14, C7; spec 14.2, 14.4, 14.9, 14.11)

- **Requirement:** `host add` installs a static helper that matches the client, checked by SHA-256. It turns on linger and terminfo. `host upgrade` updates the helper. The client itself can update.
- **Today:** the helper upload is small (`bootstrap` and `helpers`, about 900 lines) and fine. The root step is 2,300 lines (run, print or skip, undo scripts, records). Runtime delivery and the release manifest (`internal/supply`, `runtimecache`) are 7,400 lines. The client has no self-update.

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [creativeprojects/go-selfupdate](https://github.com/creativeprojects/go-selfupdate) | Self-update from GitHub releases, with checksum and pluggable signature validators | MIT | Yes: v1.7.1, 10 Nov 2026 | Go library | Fits `update` for script installs. A custom validator can check the SSH signature of 0017 with `hiddeco/sshsig`, which is already a dependency. Skip it for Homebrew installs. |
| [minio/selfupdate](https://github.com/minio/selfupdate) | Binary swap | Apache-2.0 | No: v0.6.0, Jan 2023 | Go library | Not maintained. |
| [oras-go](https://github.com/oras-project/oras-go) | OCI artefacts by digest | Apache-2.0 | Yes: v3.0.0, 1 Nov 2026 | Go library | Only if microvm runtimes and images ship. One digest-checked path for both. |
| Herdr and Zed remote install | Probe, consent, upload, checksum, versioned path | Apache-2.0 / GPL | Yes | Design | Our upload already does this. |

- **Recommendation: keep the helper upload. Adopt go-selfupdate for the client** (S). Make the root step "print the commands" only for v1. The daily path at `none` needs no root, beyond the linger fix that `main` already prints. Defer H9 and the release manifest with the microvm. Effort: **S**. It removes about 2,000 lines now, and 7,400 with H9 deferred.
- **Spec impact:** 14.2.1 loses the runtime and root-step screens for `none` hosts. 14.4 becomes "print" only. 14.9 moves with phase 3.

### 7. Placement, facts, health and host rules (H11–H14, H19; spec 14.10, 14.11, 18.2)

- **Requirement:** pick the host the user names. Otherwise use the default host, or the only one.
- **Today:** about 9,400 lines: `place*`, `auto*`, `hostrule*`, host placement, facts, health, drain and `vmneed`. There is a ranking with nine rules, a `placements.json` under `flock`, and fact fan-out every 10 s.
- **Options:** Docker and kubectl contexts, and DevPod's default provider: a flag wins, and one default applies (locations.md section 7). Schedulers such as Nomad or Kubernetes are far too large, and need servers.
- **Recommendation: simplify and defer.** Keep `new <host>`, `-` and `host default`. Defer `new auto`, facts, drain, health items and rule lists. If a rule per repository is wanted, `[repos."github.com/acme/*"] host = "x"` is about 150 lines over the existing config. Effort: **S** (deletion). It removes about 9,000 lines. Risk: low.
- **Spec impact:** 14.10.2, 14.10.3 and 14.10.5, 14.11 and most of 18.2 move to a later phase.

### 8. Hosts behind NAT and in more than one location (H16; spec 14.13, 18.5, 18.6)

- **Requirement:** reach home and office hosts with no relay of ours and no listening port. The laptop starts every connection.

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [Tailscale](https://github.com/tailscale/tailscale) | The user's own overlay; direct on a LAN; Tailscale SSH | BSD-3-Clause | Yes: v1.104.7, 12 Nov 2026 | Daemon (the user's) | Best default. Its coordination server and DERP are the user's choice, not the tool's. |
| [Headscale](https://github.com/juanfont/headscale) | A self-hosted Tailscale control server | BSD-3-Clause | Yes: v0.29.8, 26 Nov 2026 | Server (the user's) | For users who want no third party. |
| OpenSSH `ProxyJump` | Through any reachable machine | BSD | Yes | Built in | Works today, with no code. |
| [cloudflared](https://github.com/cloudflare/cloudflared) | Access tunnels | Apache-2.0 | Yes: 2026.11.2 | Daemon (the user's) | A third-party relay that the user chooses. Document it, never ship it. |

- **Recommendation: keep it as docs, as planned.** The only code is the route word from `ssh -G` in `host doctor` (about 50 lines). Effort: **S**. The reasons in 14.13 still hold.
- **Spec impact:** none.

### 9. Machines that join (H20; spec 18.3)

- **Requirement:** let a machine be set up from its own keyboard, and then be added safely by the laptop.
- **Today:** `internal/join` (5,400 lines), client `invite*` (4,900), `macfacts` and the CLI: about **11,600** lines. They hold invite and reply codes in base32 groups with check characters, a nonce store, a root-owned ledger, a leave script and sshd drop-ins.

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| OpenSSH `ssh-copy-id`, `ssh-keygen -lf` | Puts the laptop's key on the machine; prints the machine's fingerprint for `--host-key` | BSD | Yes | System CLI | Covers the whole trust exchange that the codes carry, in two commands. |
| Tailscale SSH | SSH authorised by tailnet ACLs, with no key copy | BSD-3-Clause | Yes | Daemon (the user's) | The easiest join when both sides use a tailnet. |
| DevPod (SSH provider) | No join at all: add any SSH host | MPL-2.0 | No (see section 1) | CLI | Shows that client-only tools need no join step. |

- **Recommendation: drop H20 for v1.** Write a how-to instead: on the machine, run `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`. Put the laptop's public key in `authorized_keys`. Then run `host add <name> user@addr --host-key SHA256:…`. For a dedicated, restricted account, print the script, as the root step does. Effort: **S**. It removes about 11,600 lines. Risk: low.
- **Spec impact:** 18.3, the `host invite`, `host join`, `host leave` and `--accept` rows of 9.1, and the code errors of 18.8 all go.

### 10. A Mac as a host (H21; spec 18.4, 19.4; decision 0044)

- **Requirement:** a spare Mac runs sessions as a host, starts them again at boot, and stays reachable.
- **Today:** spec 18.4 adds a hidden account, a hand-written LaunchDaemon plist, a pinned Lima download, an sshd drop-in, a facts script and `pmset` changes. It says `limactl autostart` "cannot do this" because of the hidden account.

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [Lima](https://github.com/lima-vm/lima) with [`autostart enable --condition=boot`](https://lima-vm.io/docs/usage/autostart/) | A Linux VM (`vz`) that starts at boot as a chosen user, before any login, with KeepAlive. Writes `~/.lima/<vm>/ssh.config` | Apache-2.0 | Yes: v2.3.0, 6 Nov 2026 | CLI | Meets the need under the user's own account. The helper then runs on Linux, unchanged. |
| [Colima](https://github.com/abiosoft/colima) | Wraps Lima | MIT | Yes: v0.10.3, 4 Jun 2026 | CLI | Adds nothing that we need. |
| [Apple container](https://github.com/apple/container) | One small VM per container | Apache-2.0 | Yes: 1.7.1, 6 Nov 2026 | CLI and launch agent | Needs macOS 26, and fails without a login session (locations.md 1.5). Not now. |
| A native macOS helper | Unwalled sessions with no VM (creack/pty works on darwin; launchd in place of systemd) | ours | n/a | Code | About M of work. No cgroup memory cap. A later option. |

- **Recommendation: adopt Lima as it is.** The Mac's user installs Lima, makes the VM from the existing `host create lima` template (with `hostLoopback: false`), and runs `limactl autostart enable --condition=boot <vm>`. The laptop reaches the VM by `ProxyJump` through the Mac's sshd to Lima's SSH port, or by Tailscale inside the VM. Drop the hidden account, the plist writer, the facts script and the sshd drop-in. Effort: **S** (a how-to, and perhaps `host add --via <mac>`). Risk: medium. The research notes that Apple does not support Virtualization.framework in a daemon, but that risk is the same for both designs.
- **Spec impact:** 18.4 shrinks to a how-to. 0044 still applies (Lima's loopback port).

### 11. Shells, exec and the link (P1.13; spec 6.11, 10.1–10.5)

- **Requirement:** a shell and one-off commands beside the agent, in the same sandbox, with exit codes. Many streams over one connection to the guest.
- **Today:** `internal/link` (2,700 lines) is a custom multiplexer: stream IDs, credit windows, half-close and reset codes (spec 10.1). `sshserve` (about 600 lines) already wraps `x/crypto/ssh` for editors.

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [`x/crypto/ssh`](https://pkg.go.dev/golang.org/x/crypto/ssh) channels, over the existing socket pair | Typed channels with extra data (the header), per-channel windows, `CloseWrite`, `pty-req`, `window-change`, `exit-status`, `subsystem sftp` | BSD-3-Clause | Yes: v0.59.0 | Go library (already a dependency) | Covers 10.1 to 10.3 and gives `exec` exit codes and shells for free. Nothing listens, because it runs over a socket pair. |
| [xtaci/smux](https://github.com/xtaci/smux) | Stream multiplexing with per-stream flow control | MIT | Yes: v1.5.58, 1 Nov 2026 | Go library | A lighter option, but with no exec or PTY semantics. |
| [hashicorp/yamux](https://github.com/hashicorp/yamux) | Stream multiplexing | MPL-2.0 | No tagged release since Sep 2024 | Go library | Works, but fails our maintenance rule. |

- **Recommendation: wrap `x/crypto/ssh`,** if walls stay. For `none` sessions, `shell` is a new session in the same folder, as 6.11 already says, so it needs no link. Effort: **M**. It removes about 2,700 lines of link code and the stream parts of `proto`. Risk: medium, because it touches the supervisor and guest of another audit.
- **Spec impact:** 10.1 and 10.2 become "SSH channels, by type". 10.3 keeps its ops.

### 12. Files in and out: seed, pull, cp, files from the laptop, editors (P1.6, P1.10, P1.14, P1.15, P2.15, P5.2; spec 6.1, 6.10, 17.3–17.7, 21.1)

- **Requirement:** get work back to the laptop safely, get files in and out, and let editors and `rsync` reach a session.
- **Today:** an unwalled session's files are on the host, so `scp` and `git` work as they are. Walled sessions use a seed bundle and pull through a bundle, plus custom `put`, `get` and `cp.list` streams, `filecopy` staging and the input files: about 3,200 lines, plus `input*` at 1,500.

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| git bundles (`git bundle create`, `verify`, `fetch`) | The branch and its history, checked | GPL-2.0 (executable) | Yes | System CLI | Already the chosen path. It is the standard tool for this. Keep it. |
| [pkg/sftp](https://github.com/pkg/sftp) | An SFTP server in the guest, and a client | BSD-2-Clause | Yes: v1.13.13, 24 Nov 2026 | Go library (already used) | With the ssh-proxy of P2.15, `scp`, `sftp`, `rsync` and editors all work unchanged. |
| [rsync](https://github.com/RsyncProject/rsync) | Fast deltas over any `ssh` command | GPL-3.0 | Yes: v3.5.2, 3 Nov 2026 | Separate executable | Works through `-e` or a ProxyCommand. Needs rsync in the guest image. |
| [Mutagen](https://github.com/mutagen-io/mutagen) | Two-way sync, forwards | MIT, plus SSPL parts | No: v0.18.1, Feb 2025 | Daemon | Not maintained by our rule, and SSPL is a risk. |
| [Unison](https://github.com/bcpierce00/unison) | Two-way sync | GPL-3.0 | Yes: v2.54.0, May 2026 | Separate executable | Needs the same version at both ends. Too fragile. |

- **Recommendation: adopt git and sftp.** Keep seed and pull through git bundles. Build the P2.15 ssh-proxy (an `x/crypto/ssh` server on stdio, already in `sshserve`) before P5.2. Then `cp` becomes `sftp`, `scp` or `rsync` through `ssh <session>.<host>.<name>`, with the review of flagged files run on the result. Drop the custom `put` and `get` streams and `filecopy` staging. For unwalled sessions, document `scp` and `rsync` with the user's own config. Effort: **M**. It removes about 3,000 lines. Risk: low.
- **Spec impact:** 21.1 and the `put`, `get` and `cp.*` rows of 10.2 to 10.5 change. P2.15 moves before P5.2.

### 13. Ports (P1.9; spec 7.4)

- **Requirement:** open an agent's dev server on the laptop.
- **Today:** `SSH.Forward` uses its own connection with `-L`, and a Unix socket for a walled session. `rport` reverse forwards add their own streams.
- **Options:** OpenSSH `-L` and `-O forward` (BSD; maintained; built in), including StreamLocal forwards to a Unix socket. Nothing else is needed.
- **Recommendation: keep OpenSSH. Drop reverse forwards** (`forward --reverse`, `port.reverse`) for v1. Effort: **S**. Risk: low.
- **Spec impact:** 7.4 loses `--reverse`. The `rport` row of 10.2 goes.

### 14. Config, layers and `config check` (C9, P1.12; spec 8)

- **Requirement:** read `config.toml` and `hosts.toml`, merge layers, and report each mistake as "file:line: unknown key … Did you mean …".
- **Today:** BurntSushi/toml for decoding. `internal/tomlinspect` (10,200 lines) is a fork of BurntSushi's lexer and Go's number parsing, made to get key positions. Its own `PINNED-SOURCE.md` says it is a "source-only prototype" whose compilation and tests "remain unrun". Add the CLI's own lexer (about 500 lines) and the agent-settings inspection that uses the fork (about 3,200).

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [BurntSushi/toml](https://github.com/BurntSushi/toml) | Decoding, `MetaData.Undecoded()`, positions for syntax errors | MIT | Yes: v1.6.0, 18 Dec 2025 | Go library (in use) | Gives no key positions. That gap is why the fork exists. |
| [pelletier/go-toml v2](https://github.com/pelletier/go-toml) | `DisallowUnknownFields` returns a `StrictMissingError` with one `DecodeError` per key, each with `Position()` (row and column) and `Key()` | MIT | Yes: v2.4.3, 5 Jul 2026 | Go library | Meets C9 directly. A nearest-key hint is a short edit-distance loop. |

- **Recommendation: adopt go-toml v2** for `config check` (or for all decoding), and delete `tomlinspect` and the CLI lexer. Effort: **S–M**. It removes about 10,700 lines, and up to 3,200 more in agent-settings inspection. Risk: low.
- **Spec impact:** CLI spec 13 says "go-toml strict decoding". No decision record is needed beyond a dependency note.

### 15. The CLI and the launcher (C5–C7, C10, C11, P1.8; spec 9, CLI spec)

- **Requirement:** one grammar, aliases with warnings, "Did you mean", completion of hosts and sessions, `--json`, and a launcher that agrees with the CLI.
- **Today:** cobra (Apache-2.0, v1.10.2, Dec 2025) and Bubble Tea v2 are used well: about 590 cobra references and many `ValidArgsFunction`s. The CLI grew from 4,100 to 21,300 lines, mostly through new commands (policy, prepare, join, purge, rewind, cp, image).

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [cobra](https://github.com/spf13/cobra) | `Aliases`, `Deprecated`, `MarkDeprecated` for flags, `SuggestFor` and Levenshtein suggestions, completion | Apache-2.0 | Yes: v1.10.2, 4 Dec 2025 | Go library (in use) | Covers C5, C6 and C10. Use its deprecation and suggestion features instead of `deprecated.go` and hand-written `suggest`. |
| [charmbracelet/fang](https://github.com/charmbracelet/fang) | Styled help, errors, `--version` and man pages for cobra | MIT | Yes: v2.0.1, 11 Mar 2026 | Go library | Chosen in client-stack.md but never adopted. Optional. The project has its own look (0012). |
| Bubble Tea v2, Lip Gloss v2 | The launcher | MIT | Yes | Go library (in use) | Keep. |

- **Recommendation: keep.** Trim the commands that belong to deferred features. C5 to C11 are each **S** when they lean on cobra. Risk: low.
- **Spec impact:** none, beyond the command rows that other sections drop.

### 16. Agent install and login (C8; CLI spec 12)

- **Requirement:** install Claude Code or Codex on a host and log in from the laptop's terminal.
- **Today:** the agent table holds each agent's official installer and login command (`curl … claude.ai/install.sh`, `claude auth login`, `codex login --device-auth`), and runs them in a session. This is already reuse.
- **Options:** the [Codex docs](https://developers.openai.com/codex/auth) recommend `codex login --device-auth` for headless hosts; it needs device-code sign-in turned on in ChatGPT's settings. The [Claude Code docs](https://code.claude.com/docs/en/authentication) give `claude setup-token`, which makes a long-lived `CLAUDE_CODE_OAUTH_TOKEN`. Coder's [registry](https://github.com/coder/registry) modules (Apache-2.0) and [anthropics/devcontainer-features](https://github.com/anthropics/devcontainer-features) (MIT, last push Dec 2025) do the same installs.
- **Recommendation: keep.** Add the `setup-token` path to the docs as a secret for headless hosts, and name the ChatGPT setting in the Codex hint. Effort: **S**.
- **Spec impact:** none.

### 17. Alerts, events and the terminal observer (P1.16, P7.3; spec 7.7, 10.8, 17.6)

- **Requirement:** tell the user when a session needs input, through the user's own channel.
- **Today:** `internal/alerts` (2,400 lines; 1,700 of them on `main`) has command, ntfy and webhook destinations, plus a hand-written OTLP JSON format.

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [ntfy](https://github.com/binwiederhier/ntfy) | Push to a phone, self-hosted or ntfy.sh | Apache-2.0 / GPL-2.0 | Yes: v2.38.3, 27 Nov 2026 | Server (the user's) | Already a destination. One HTTP POST. |
| [shoutrrr](https://github.com/nicholas-fedor/shoutrrr) (maintained fork) | More than 20 services by URL | MIT | Yes: v0.25.1, 24 Nov 2026 | Go library | Optional. A webhook and a command already cover the rest. |

- **Recommendation: keep ours** (it is small). Defer OTLP, `events --follow` and P7.3 until walls ship. Effort: none.
- **Spec impact:** none.

### 18. The SSH agent for a session (H5 `--ssh-agent`; spec 7.9)

- **Requirement:** let one trusted, unwalled session sign with the laptop's agent. Optionally limit it to some keys.
- **Today:** client `agent_firewall`, binding and certificate code, and the supervisor's `sshagent`: about 1,400 lines that parse the agent protocol by hand.
- **Options:** [`x/crypto/ssh/agent`](https://pkg.go.dev/golang.org/x/crypto/ssh/agent) (BSD-3-Clause, maintained) gives `NewClient`, `ServeAgent` and the `ExtendedAgent` interface. A filtering agent is a wrapper of about 100 lines. For `none`, OpenSSH's own `ForwardAgent` on that session's connection is enough.
- **Recommendation: adopt `x/crypto/ssh/agent`** where a filter is needed, and OpenSSH forwarding otherwise. Effort: **S**. It removes about 1,300 lines.
- **Spec impact:** 7.9 names the library.

### 19. Today's holes (P0.1–P0.9; spec 19)

- **Requirement:** close the known holes in the shipped product: the Ghostty pin and the size cap, peer checks on sockets, setup trust tied to the script's content, safe copies, Lima loopback, and a watch on pins.
- **Today:** mostly small and sound. Two parts are heavier than they need to be: the version-2 setup-trust file (with locks, `.bad.N` files and migration), and a 1,800-line Python watcher.

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| Go `os.Root` (1.24 and later) | Opens files with no escape through symlinks | BSD-3-Clause | Yes | Standard library | Already used by P0.6 and P0.7. Good reuse. |
| [Renovate](https://github.com/renovatebot/renovate) | Custom regex managers for `build.zig.zon` and `supply.lock`; git-refs and GitHub-release data sources | AGPL-3.0 (run as a tool, never linked) | Yes: 45.3.2, 27 Nov 2026 | CI app | Replaces most of P0.9. A 30-line check for the four Ghostty fix commits stays, because no advisory database lists them. |

- **Recommendation: keep P0.1, P0.2, P0.7 and P0.8.** Make the P0.6 file a plain JSON map with an atomic rename. Replace the P0.9 watcher with Renovate and a tiny check. Effort: **S**. It removes about 1,500 Python lines and about 400 Go lines.
- **Spec impact:** 19.2 (the file) and 19.5 shrink.

### What in internal/host and internal/client is core and what is removable

Rough non-test line counts on the branch, grouped by file-name prefix. "On main" is the size of the same group on `main`.

| Package and group | Prefixes (examples) | Lines | On main | Verdict |
| --- | --- | --- | --- | --- |
| host: core holding, attach and session ops | holder, bridge, screen, vt, session, manifest, memory, idle, alerts, events, restore, resume, new, stop, delete, worktree, setup, doctor, install, pick | ~30,300 | ~11,000 | **Core.** Keep. Trim the sandbox ops out of `bridge`, `new` and `resume`. |
| host: custody, purge, native managers, cold and warm hand-offs, retirement | purge, custody, none, native, slot, ordinary, auxiliary, startup, stopped, original, cold, live, members, peer, warm, walled, supervisor | ~44,000 | 0 | **Removable.** Systemd units and `rm` cover it (section 3). |
| host: walls, runtime, images, VM drivers, doors | sandbox, image, runtime, gvisor, kernel, chv, firecracker, nitro, checkpoint, rewind, fork, seed, pull, policy, sshkeys | ~21,900 | 0 | Defer with walls (the other audits). |
| host: fleet | placement, health, drain, footprint, hostid | ~4,600 | 0 | Defer. Keep a short footprint list for purge. |
| client: core | config, ssh (base), conn, attach, input, clip, paste, transport, bootstrap, helpers, session, resume, machine, provider, fix, state, trace | ~25,000 | ~5,200 | **Core.** Keep. |
| client: pins, tiers, grants, ledger, purge, host-state machinery, root step | pin, tier, grant, ledger, outbound, sent, baseline, host_purge, host_state, host_membership, rootstep | ~26,300 | 0 | **Mostly removable.** Keep about 1,500 lines (sections 4 to 6). |
| client: sandbox, policy and doors | sandbox, image, policy, network, secret binds, seed, pull, cp, rewind, editor, agent firewall and settings | ~36,100 | 0 | Defer with walls. Keep `pull` for worktrees. |
| client: fleet and join | place, auto, hostrule, invite, macfacts | ~10,600 | 0 | **Removable** (sections 7 and 9). |
| Related packages | tomlinspect 10,200; join 5,400; link 2,700; filecopy 1,400 | ~19,700 | 0 | Replace (sections 9, 11, 12 and 14). |

Taken together, the daily-use path needs about 55,000 lines in these two packages: the two core rows. That is about 145,000 fewer than today. For scale, the packages of the other audits are egress (36,800), supervisor (21,500), jail (17,500), wall (17,300), guest (15,400), image (11,000) and slotlaunch (8,900).

### Top swaps by impact on the daily-use path

The path: a Mac client and a Linux server host, `host add`, a new session with Claude Code or Codex, attach and detach, files in and out, stop and destroy, and a Mac as a host.

1. **New session, stop and destroy: go back to `main`'s holder under `systemd-run --user`** for unwalled sessions. Drop custody, purge, the native managers and the hand-written D-Bus code. Use `coreos/go-systemd/dbus` only where a job result is needed. Removes about 48,000 lines. Effort M. This is the biggest win, and it puts the path that worked on `main` back.
2. **Host add: OpenSSH-only pinning** (`HostKeyAlias`, a private `known_hosts`, `--host-key` or one verified first connection), and **one trusted flag per host** in place of tiers, grants and the ledger. Make the root step "print" only. Removes about 13,000 lines. Effort S–M.
3. **A Mac as a host: Lima under the user's own account, with `limactl autostart enable --condition=boot`**, reached by `ProxyJump` or Tailscale. **Drop `host join` and `host invite`** in favour of `ssh-copy-id` and `--host-key`. Removes about 11,600 lines. Effort S.
4. **Config: go-toml v2 strict errors in place of the `tomlinspect` fork.** Removes about 10,700 lines (up to 13,900). Effort S–M. It touches `config check` and every config read.
5. **Defer placement, facts, health, drain and host rules.** Keep `new <host>`, `-` and `host default`. Removes about 9,400 lines. Effort S.
6. **Files in and out:** for unwalled sessions, document `scp` and `rsync` and keep image-paste upload and `pull` over git. For walls later, use the ssh-proxy with `pkg/sftp` and git bundles, not custom `cp` streams. Removes about 3,000 lines. Effort S now, M later.
7. **Claude Code and Codex:** keep the agents' own installers and logins (already data). Document `claude setup-token` and `codex login --device-auth`. Effort S.
8. **The Mac client:** no stack change (Go, Bubble Tea v2, system `ssh`, `x/clipboard`). Add `update` with `creativeprojects/go-selfupdate` for script installs. Effort S.
9. **If walls ship: SSH channels from `x/crypto/ssh` in place of `internal/link`**, and `x/crypto/ssh/agent` in place of the agent firewall. Removes about 4,000 lines. Effort M.
10. **Project hygiene: Renovate in place of the 1,800-line pin watcher.** Effort S.

**Records to change:** reject 0050 and 0051. Replace 0035 (pinning) and 0036 (tiers and the grant gate) with short records. Withdraw H20 and the 18.3 design. Amend 0029, so that a `none` session is a plain holder unit with no supervisor. 0002, 0003, 0004 and 0034 still hold.

## Part D: supply chain, releases, image verification and CI

Checked on 27 November 2026, on the branch `docs/isolated-sessions`. Release dates come from each project's GitHub releases API or package index on that day. Line counts leave out tests unless they say so.

### What is there today

| Area | Lines | Tests |
| --- | --- | --- |
| `internal/supply` (manifest, lock, known-bad, fs-verity, sealed copies, exec-mode leases) | 4,983 | 3,971 |
| of which the P4.2 parts (`exec*`, `sealed*`, `fsverity*`) | 2,490 | 1,314 |
| `internal/runtimecache` (host store, part files, prune, exec modes) | 2,461 | 2,267 |
| `internal/cachefs` (shared by the caches) | 1,329 | |
| `internal/image` (pull, flatten, convert, store, agents disk, saved images) | 10,965 | 5,452 |
| of which registry, reference, transfer and flatten | 1,326 | |
| of which signature discovery | 261 | |
| of which the default image (`default_*`) | 722 | |
| `internal/imageverify` + `tools/imageverify` (a separate Go module) + the client's launcher of it | 162 + 131 + 408 | 279 + |
| `cmd/supply` (write and check the manifest, pins, default image, gVisor) | 2,126 | 1,228 |
| `guest/` scripts (kernel and tools build, boot test, default image) and `guest/verity` | 783 sh + 177 Go | 391 |
| `third_party/gvisor` (`hostguard.patch` and its input lists) | 6,980 + 436 | |
| gVisor build scripts (`build-gvisor-runtime.py`, `prepare-gvisor-build.py`, `gvisor-native-repro.sh`, their tests) | 564 | |
| `scripts/repro.sh` | 103 | |
| `scripts/verify-tag.sh` and its test | 143 | 433 |
| `scripts/pins-watch.py` and `scripts/pinswatch/` | 962 | 868 |
| Workflows: `ci.yml` 537, `release.yml` 385, `microvm-conformance.yml` 327, `pins-watch.yml` 97, others 283 | 1,629 | |
| `.goreleaser.yaml` 191, `install.sh` 77 | 268 | 111 |

### Five findings that matter most

1. **No release since 0.1.2 (17 October 2026), and the pipeline cannot make one.** The `release` job needs `microvm-conformance`, which needs the ID of a finished conformance run on four self-hosted runners (bare metal and nested, amd64 and arm64). It also needs two patched-gVisor Bazel builds and two default-image builds. `cmd/supply check` runs with every `-require-*` flag. So a client-and-helper release waits for the hardest phase (microvm) and a later one (gvisor, P6.1).
2. **Every pull request builds the patched gVisor twice on each architecture, and the default image twice on each architecture,** then two GoReleaser builds (`reproducible`, `gvisor-repro`, `test-images`). The `install.sh` test waits for all of them, so a Bazel failure blocks the check of the one-command install.
3. **Four signing paths:** an SSH signature on `checksums.txt`, cosign keyless on the same file, `actions/attest-build-provenance`, and the SLSA generic generator. The generator is no longer maintained (notice of 7 August 2026).
4. **Most of the custom code defends against another process of the same host user**, racing a check (exec-mode leases, sealed copies, the separate verifier process, staged executables). Plan P4.1 already says that the host check "cannot defend a host whose login account is compromised". The rest automates rare human work (the pins-watch range grammar).
5. **New facts that change the research.** Firecracker now ships 6.18 guest configs. Cloud Hypervisor v55.0 shipped on 19 November. sigstore-go is at v1.4.1. GitHub immutable releases are generally available. Ghostty's advisories are still in neither OSV nor GitHub's global database.

---

#### Release signing and provenance (P4.1 in part, decision 0017)

- **Requirement (spec 14.9.2, 20.7, decision 0017):** `curl … | sh` on a clean Mac or Linux box must check a signature before it installs, with nothing else installed. A client from `go install` must check the helpers it downloads. Anyone may also check a transparency log.
- **Today:** `install.sh` (77 lines) and `internal/client/helpers/fetch.go` check `checksums.txt.sig` with `ssh-keygen -Y verify`. `release.yml` also signs with cosign keyless (GoReleaser `signs`), runs `attest-build-provenance`, and calls `slsa-github-generator` as a separate job. Syft SBOMs come from GoReleaser.
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| OpenSSH `ssh-keygen -Y` | SSH signatures that every host already can check | BSD | Ships with each OS | CLI | In use. The only option that needs nothing installed. Key lives in a GitHub secret. |
| [GitHub artifact attestations](https://github.com/actions/attest-build-provenance) | SLSA v1 provenance as a Sigstore bundle, in Rekor | MIT | v4.2.2, 6 Aug 2026 | Action | In use. `gh attestation verify` checks it, offline with `--bundle --custom-trusted-root`. |
| [GitHub immutable releases](https://github.blog/changelog/2025-10-28-immutable-releases-are-now-generally-available/) | Locks assets and tag after publication, and adds a release attestation | Service | GA 28 Oct 2025 | Setting | Not on. Stops a later swap of assets, which today only the signature catches. `gh release verify-asset`. |
| [cosign](https://github.com/sigstore/cosign) | Keyless blob signatures | Apache-2.0 | v3.1.6, 25 Nov 2026 | CLI | In use, and redundant with attestations, which are also Sigstore bundles in Rekor. |
| [slsa-github-generator](https://github.com/slsa-framework/slsa-github-generator) | SLSA L3 provenance | Apache-2.0 | v2.1.0, Feb 2025. "No longer actively maintained" | Reusable workflow | Fails the maintenance rule. Its own notice points to GitHub attestations. |
| [sigstore-go](https://github.com/sigstore/sigstore-go) | Verify Sigstore bundles in Go, offline | Apache-2.0 | v1.4.1, 22 Nov 2026 | Library | Could let the client check attestations itself. Not needed while `ssh-keygen` works. |
| [minisign](https://github.com/jedisct1/minisign) | Small signatures | ISC | 0.12, Jan 2025 | CLI | Fails the maintenance rule, and is not on clean machines. Reject. |
| [age](https://github.com/FiloSottile/age) | Encryption | BSD-3-Clause | v1.3.2, 29 Aug 2026 | CLI, library | Not a signing tool. No fit. |
| [quill](https://github.com/anchore/quill) | macOS signing and notarisation from Linux, with GoReleaser | Apache-2.0 | v0.7.1, 10 Mar 2026 | CLI | Later: needs an Apple Developer ID. Would remove the cask's quarantine workaround. |

- **Recommendation: simplify.** Keep the SSH signature and GitHub attestations. Delete the `provenance` job and the GoReleaser `signs` block. Turn on immutable releases for the repository. Effort S. Removes about 25 workflow and config lines, one job and one secret-free signing path. Risk low: `install.sh` and the client do not change.
- **Spec impact:** a new record that replaces 0017 (an accepted record is never edited): "SSH signature for install, GitHub attestations for the log, immutable releases". `docs/contributing/releasing.md`. Spec 20.7 "The limit" (the draft without `checksums.txt.sig`) gets simpler.

#### Release pipeline and CI coupling (P3.8, P4.1, P4.3)

- **Requirement (spec 20.1, 20.6, P3.8):** each release ships what it claims, built from the signed tag. Microvm assets ship only after the microvm wall passes its conformance suite.
- **Today:** `release.yml` (385 lines) chains `microvm-conformance` → `default-image` and `gvisor-runtime` → `release` → `provenance` → `notes`. `ci.yml` runs `reproducible`, `gvisor-repro` and `test-images` on every pull request, and `install` needs them. `microvm-conformance.yml` (327 lines) runs on self-hosted runners chosen by repository variables.
- **Options:** no outside tool is the answer; this is workflow structure. GoReleaser (MIT, v2.20.3, 20 Nov 2026) already builds the client and helper reproducibly with `-trimpath` and `mod_timestamp`. GitHub's `paths` filters and `schedule` triggers move the heavy jobs.
- **Recommendation: simplify.** (1) Make the release job need only the tag gate and GoReleaser. Build kernels, the tools image, the default image and gVisor in jobs that run only when the release manifest names those items, behind one switch per wall. (2) Gate microvm items on conformance, not the whole release. (3) Run `gvisor-repro`, `reproducible` and the image half of `test-images` on a weekly schedule and on pull requests that touch `guest/`, `third_party/` or their scripts. (4) Let `install` depend on a plain snapshot build. Effort S to M. Shrinks per-PR CI by six heavy jobs. Risk low: the checks still run before a release that ships those items.
- **Spec impact:** spec 20.1 "Tests" and 20.6, plan P3.8 (conformance blocks the microvm items, not the release), `docs/contributing/releasing.md` and `testing.md`.

#### The release manifest and the check before the bridge (P4.1, H8 folded in)

- **Requirement (spec 14.9.1, 14.9.2):** the client holds the digest of every trusted file of its release. `dial` runs the bridge only after the host's helper matches. No host decides what is trusted.
- **Today:** `internal/supply/supply.go`, `version.go`, `content.go`, `boot.go`, `tools.go`, `vmm*.go`, `build.go` and friends (about 1,500 lines outside P4.2 and the lock), `cmd/supply write|check|assets` (about 900 lines), `scripts/write-release-manifest.sh`. The dial check is a `sha256sum -c` line in the staging shell (`internal/client/install_owned.go`).
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| GoReleaser `checksums.txt` | SHA-256 of every asset | MIT | v2.20.3, 20 Nov 2026 | CLI | In use. No sizes, URLs, root hashes or advisory fields. |
| [in-toto attestations](https://github.com/in-toto/in-toto-golang) | A signed list of subjects and digests | Apache-2.0 | v0.11.0, 4 May 2026 | Library | The attestations already carry this. The client would still embed a list. Over-built for the need. |
| [go-tuf](https://github.com/theupdateframework/go-tuf) (TUF) | Signed metadata, expiry, rollback defence | Apache-2.0 | v2.6.0, 27 Nov 2026 | Library | The spec rejected keys and expiry because the client embeds digests. That reason holds. Reject. |
| coreutils `sha256sum -c` | The check on the host | GPL-3.0, separate program | Ships with each distribution | CLI | In use. Right tool. |

- **Recommendation: keep ours, and shrink it.** No tool gives "the client embeds digests and checks the host's file before the bridge". But for the first release the client already embeds the helper, the shadow terminal and the namespace launcher, so it can hash what it embeds. The manifest is needed only for items that the client does not carry: VMM, kernel, tools image, images, `runsc`. Ship a v1 manifest with `helper`, `vt` and `userns` only, and drop `cmd/supply check`'s `-require-*` flags for walls that are not shipping. Effort S. Removes nothing yet, but takes about 1,500 lines off the path to the first release. Risk low.
- **Spec impact:** spec 14.9.1 (kinds per release), plan P4.1 (scope for the first release).

#### Runtime delivery and the host cache (H9)

- **Requirement (spec 14.9.3, 14.9.4):** a host gets runtime files from the release or from the laptop, checks size and SHA-256 against the client's digest, keeps the two newest runtime labels, and never takes a file from another host.
- **Today:** `internal/runtimecache` (2,461 lines) with exclusive part files, publication gates, records, repair, purge and exec-mode provisioning, on top of `internal/cachefs` (1,329 lines). `scripts/mirror-vmm.sh` and `internal/supply/vmm_mirror.go` copy upstream VMMs into the project's release.
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| Go `net/http`, `crypto/sha256`, `os.Rename` | Download, hash, atomic rename | BSD-3-Clause | Go 1.27 | Standard library | Meets the whole requirement in about 200 lines, as Go's own module cache does. |
| [oras](https://github.com/oras-project/oras) / [oras-go](https://github.com/oras-project/oras-go) | Runtime files as OCI artefacts on ghcr, pulled by digest | Apache-2.0 | oras v1.4.0, 17 Nov 2026; oras-go v2.7.0, 23 Oct 2026 | CLI, library | Works, but adds a registry step to the release for no gain over release assets. |
| [go-containerregistry](https://github.com/google/go-containerregistry) | The same, by digest | Apache-2.0 | v0.22.1, 4 Sep 2026 | Library | Same. Better used for images (below). |
| [hashicorp/go-getter](https://github.com/hashicorp/go-getter) | Download with checksum | MPL-2.0 | v2.2.6, 22 Oct 2026 | Library | Weak copyleft, large. No gain. Reject. |

- **Recommendation: simplify, and defer.** The first release needs no runtime delivery: the helper already travels over SSH with a SHA-256 check. When the microvm wall ships, write the store as a plain content-addressed folder: download to a random temporary name, stop at the size, check SHA-256, `rename`, and prune by label. Drop exec-mode provisioning and publication leases. Fetch Apache or BSD binaries (Cloud Hypervisor, Firecracker) from the upstream URL with the pinned digest; mirror only GPL items and their source. Effort M. Shrinks about 3,800 lines to about 300. Risk medium: the concurrency cases of spec 14.9.3 need a test, but `rename` gives them.
- **Spec impact:** spec 14.9.3, 14.9.4 and the "Every `url` points at the project's own release" rule of 14.9.1, the planned record 0039, plan H9.

#### Run verified bytes only (P4.2)

- **Requirement (spec 20.5):** a change to a store file after its check must not change what runs.
- **Today:** about 2,490 lines in `internal/supply` (fs-verity ioctls with hand-written UAPI constants, sealed `memfd` copies, exec-mode leases, guards and candidates), and exec-mode files in `internal/runtimecache`.
- **Options:** [`golang.org/x/sys/unix`](https://pkg.go.dev/golang.org/x/sys/unix) (BSD-3-Clause, v0.49.0, in use) already has `FS_IOC_ENABLE_VERITY`, `FS_IOC_MEASURE_VERITY`, `FsverityEnableArg`, `FsverityDigest`, `MemfdCreate` and the `F_SEAL_*` flags. `fsverity-utils` (CLI) gives the test oracle. runc's sealed-binary code solves the same problem but is internal to runc, so it cannot be imported.
- **Recommendation: defer, then simplify.** This guards against the same-user race that P4.1's own docs say it cannot fully stop. It is not needed for a first release, nor to boot a first microvm. When it comes back: check SHA-256 into a sealed `memfd` and `execve` it (about 150 lines, with `x/sys/unix`), and add fs-verity only if a measurement shows the copy costs too much. Effort S to defer. Takes about 2,500 lines and their 1,300 test lines off the critical path. Risk low.
- **Spec impact:** spec 20.5 and 20.10 (the 24 MiB of sealed copies in the reserve), question 82, plan P4.2 moves after P3.8.

#### Reproducible builds (P4.3)

- **Requirement (spec 20.6):** two builds from empty caches give the same bytes, so anyone can check a release.
- **Today:** `scripts/repro.sh` (103 lines) runs two GoReleaser builds with pre-built pairs of the default image (`guest/base-image/repro.sh`) and of gVisor (`gvisor-native-repro.sh`), plus the kernels. CI runs all of it on each pull request.
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| Go `-trimpath` with GoReleaser `mod_timestamp` | Reproducible Go binaries | BSD / MIT | In use | Toolchain | Already enough for the client and the helper. |
| [Kernel reproducible-builds guide](https://docs.kernel.org/kbuild/reproducible-builds.html) and `SOURCE_DATE_EPOCH` | The steps for the kernel | GPL-2.0 docs | Current | Docs | In use. Right. |
| [diffoscope](https://diffoscope.org/) | Explains a difference | GPL-3.0+ | 340, 27 Nov 2026 | CLI | Use on failure only. Already in the spec's hint. |
| [reprotest](https://salsa.debian.org/reproducible-builds/reprotest) | Builds twice with varied environments | GPL-3.0+ | 0.7.33, 21 Sep 2026 | CLI | Could replace the hand-made two-pass loop for one artefact. Optional. |
| [oss-rebuild](https://github.com/google/oss-rebuild) | Third-party rebuilds of packages | Apache-2.0 | Active (pushed 18 Nov 2026) | Service | For ecosystems, not this release. No fit. |
| Nix, Guix | Hermetic builds | LGPL / GPL | Active | Tools | Option only: heavy for contributors. Reject for now. |

- **Recommendation: simplify.** Keep the double build for Go binaries in CI (cheap). Move kernel, tools image, default image and gVisor repro to a weekly job, and to pull requests that change their inputs. Use diffoscope when they differ. Effort S. Removes the input-pair plumbing of `repro.sh` (about 60 lines) and three per-PR jobs. Risk low.
- **Spec impact:** spec 20.6 "Tests" (CI weekly, not each pull request), plan P4.3.

#### The guest kernel and the tools image (P3.1)

- **Requirement (spec 20.1):** a guest kernel for amd64 and arm64 with dm-init and dm-verity (no initramfs), EROFS, vsock, Landlock and no modules, plus a small EROFS tools image with a verity tree.
- **Today:** `guest/build.sh` (357 lines) builds both in a pinned Debian container from a kernel.org tarball checked against `guest/supply.lock`. `guest/kernel/check-config.sh` (173 lines), full configs (7,716 generated lines), `guest/verity` (177 lines, hcsshim's `dmverity`), `mkfs.erofs` built from source.
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [Firecracker guest configs](https://github.com/firecracker-microvm/firecracker/tree/main/resources/guest_configs) | `microvm-kernel-ci-{x86_64,aarch64}-6.18.config` | Apache-2.0 | v1.18.0, 9 Nov 2026 | Config files | Now exists (spec 20.1 marked it UNVERIFIED). Has modules on and no dm-verity or EROFS, so it needs a fragment. |
| [Cloud Hypervisor kernels](https://github.com/cloud-hypervisor/linux/releases) | Prebuilt `vmlinux-x86_64` and `Image-arm64` | GPL-2.0 | ch-release-v7.2.8, 25 Nov 2026 | Binaries | `ch_defconfig` lacks `DM_INIT` and `DM_VERITY`, and arm64 lacks EROFS. Not usable as is. |
| [Kata kernel fragments and `build-kernel.sh`](https://github.com/kata-containers/kata-containers/tree/main/tools/packaging/kernel) | Fragment-based microVM kernel builds; the confidential fragment sets `DM_VERITY` and `DM_INIT`, and `fs.conf` sets EROFS | Apache-2.0 | 4.4.0, 17 Nov 2026 (kernel 6.18.62) | Scripts, binaries in `kata-static` | Closest prebuilt fit. Its config is not ours (io_uring, debug info); arm64 coverage UNVERIFIED. |
| Kernel `scripts/kconfig/merge_config.sh` | Base config plus fragments | GPL-2.0 | In each kernel | Script | The standard way to keep a short fragment instead of a full `.config`. |
| [erofs-utils](https://github.com/erofs/erofs-utils) `mkfs.erofs` | The tools image | GPL-2.0+, separate program | v1.9.4 | CLI | In use. Debian ships it; building from source is only for the 1.9.3 reproducibility fix. |
| cryptsetup `veritysetup` | Verity trees | GPL-2.0+, separate program | Current | CLI | Alternative to `guest/verity`. No gain: hcsshim is already a dependency. |

- **Recommendation: keep ours, wrapped smaller.** No prebuilt kernel meets dm-init, dm-verity, EROFS and no modules at once. Keep `guest/supply.lock` and the pinned builder. Replace the two full configs with Firecracker's 6.18 config plus one fragment, merged by `merge_config.sh`, and keep `check-config.sh` as the gate. Build only when the microvm wall ships. Effort S. Shrinks 7,716 config lines to about 60 fragment lines. Risk low.
- **Spec impact:** spec 20.1 (the UNVERIFIED config question is answered: Firecracker publishes one), build facts 5.1.

#### The default image (P3.2, default image)

- **Requirement (spec 20.2 "The default image"):** a pinned Debian image with git, curl and CA certificates, and no proprietary agent, pinned by digest and root hash in the release manifest.
- **Today:** `guest/base-image/*.sh` (262 lines), `cmd/supply default-image` (277 lines) and `internal/image/default_*` (722 lines) build an image from a Debian snapshot in Docker, write OCI by hand, publish to the project's registry, and rebuild it twice per pull request per architecture.
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [Docker official `debian`](https://hub.docker.com/_/debian) and [`buildpack-deps:trixie-scm`](https://hub.docker.com/_/buildpack-deps) | Debian images, built reproducibly by [debuerreotype](https://github.com/debuerreotype/debuerreotype); `trixie-scm` has git, curl and CA certificates | MIT (tooling) | Rebuilt 24 Nov 2026; debuerreotype 0.17, Dec 2025 | Images | Meets the need with no build. Pin by digest. More packages than the minimum. |
| [BuildKit](https://github.com/moby/buildkit) | `SOURCE_DATE_EPOCH` with `rewrite-timestamp=true` for reproducible OCI | Apache-2.0 | v0.35.1, 19 Nov 2026 | CLI | If the project keeps its own image: a 5-line Dockerfile replaces the custom OCI writer. |
| [mmdebstrap](https://gitlab.mister-muffin.de/josch/mmdebstrap) + [umoci](https://github.com/opencontainers/umoci) or `crane` | A reproducible Debian root from a snapshot, then OCI | MIT; Apache-2.0 | 1.7.0 in Debian; umoci v0.6.0, Oct 2025 | CLI | Smallest Debian image. More steps than BuildKit. |
| [apko](https://github.com/chainguard-dev/apko) | Reproducible minimal images with SBOMs | Apache-2.0 | v1.5.3, 23 Nov 2026 | CLI | APK packages only (Wolfi), not Debian. Changes the base. |
| [ko](https://github.com/ko-build/ko) | Images of Go programs | Apache-2.0 | v0.19.1, 29 Jun 2026 | CLI | No fit: the image is not a Go program. |

- **Recommendation: adopt** the official image, pinned by digest, as the default. At release, copy it to the project's registry with `crane copy` (the digest does not change), so hosts avoid Docker Hub's anonymous limits. CI converts it once and records its root hash. Effort S. Removes about 1,260 lines, two CI jobs and the `default-image` release job. Risk low: the trust root becomes Docker's official-image build, which users of Docker already accept.
- **Spec impact:** spec 20.2 "The default image", 14.9.1 (the `image` item), plan P3.2.

#### Image pulls and flattening (P3.2 registry side)

- **Requirement (spec 20.2, 20.8):** resolve a tag on the laptop with the user's login, pull by digest with every blob checked, flatten layers with whiteouts under caps, and convert to ext4 with a verity tree.
- **Today:** `internal/image/registry.go`, `registry_auth.go`, `reference.go`, `transfer.go`, `flatten.go`, `flatten_read.go` (1,326 lines) and signature discovery (261 lines), plus client-side resolve and credentials (about 400 lines). Conversion uses hcsshim's `tar2ext4` (MIT, v0.15.0, in use).
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [go-containerregistry](https://github.com/google/go-containerregistry) | `name`, `remote` (pull by digest, blobs checked), `authn.DefaultKeychain` (Docker config and credential helpers), `layout` (the OCI cache), `remote.Referrers`, `mutate.Extract` (flatten with whiteouts) | Apache-2.0 | v0.22.1, 4 Sep 2026 | Library | Best fit. A thin reader on `Extract` keeps the caps and the `..` and device rules. |
| [oras-go](https://github.com/oras-project/oras-go) | Registry client, referrers | Apache-2.0 | v2.7.0, 23 Oct 2026 | Library | Good transport, but no flatten. Second choice. |
| [regclient](https://github.com/regclient/regclient) | Registry client | Apache-2.0 | v0.11.7, 21 Oct 2026 | Library, CLI | Fine, smaller community. |
| [containers/image](https://github.com/containers/container-libs) | Full image library | Apache-2.0 | Moved to `container-libs`, active | Library | Heavy (C dependencies for some transports). Reject. |
| `mkfs.erofs` with an OCI source | Builds EROFS straight from a registry | GPL-2.0+, separate program | v1.9.x; marked experimental | CLI | Later: would replace flatten and convert in one step. Not yet stable. |

- **Recommendation: adopt go-containerregistry** when the microvm wall comes back. Keep `tar2ext4` and the jail. Effort M. Removes about 1,500 lines (registry, auth, transfer, flatten, referrer discovery). Risk medium: the spec's rule 6 (sorted PAX output) differs from `Extract`'s order. The root hash stays deterministic for one implementation, which is all that two honest hosts need, so rule 6 can go.
- **Spec impact:** spec 20.2 "Fetch" and "Flatten rules" (rule 6), 20.8 step 1, plan P3.2 and P4.5.

#### Image signatures (P4.5)

- **Requirement (spec 20.8):** with `[sandbox.image_policy]`, the laptop checks a cosign signature offline before it pins an image.
- **Today:** a separate Go module `tools/imageverify` (131 lines) wraps sigstore-go v1.4.0, behind a request and response protocol (`internal/imageverify`, 162 lines). The client stages and runs it from a sealed descriptor on Linux, and checks its folder before and after on macOS (408 lines). The release builds it for four platforms, and `repro.sh` tracks it.
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [sigstore-go](https://github.com/sigstore/sigstore-go), linked into the client | Offline bundle checks | Apache-2.0 | v1.4.1, 22 Nov 2026 | Library | The same check with no protocol or staging. Costs binary size and dependencies. |
| [cosign](https://github.com/sigstore/cosign) `verify` | Discovery and verification of image signatures, OCI referrers included | Apache-2.0 | v3.1.6, 25 Nov 2026 | CLI | Users who want a policy often have it. The client can call it, as it calls `ssh`. |
| [Notation](https://github.com/notaryproject/notation) | Notary Project signatures | Apache-2.0 | v1.3.2, Apr 2025 | CLI, library | Fails the maintenance rule, and is another ecosystem. Reject. |

- **Recommendation: defer.** Image policy is opt-in and comes after the first release. When it comes back, link sigstore-go into the client (or call `cosign verify` when `cosign` is on `PATH`), and use go-containerregistry's `remote.Referrers` for discovery. Delete the separate module, its protocol, its staging and its release artefacts. Effort S. Removes about 700 lines and four release binaries. Risk low: the separate process defended against the laptop user's own processes, which the README says it cannot do anyway.
- **Spec impact:** spec 20.8 "Signatures", plan P4.5, the image-verifier entries in 14.9.1.

#### Pins watch and known-bad versions (P0.9, P4.6)

- **Requirement (spec 19.5, 20.9, question 42):** learn within days when an advisory covers a pin that no package ecosystem tracks (Ghostty, kernel, erofs-utils, Cloud Hypervisor, Firecracker, gVisor). Refuse a runtime with a known advisory.
- **Today:** `scripts/pinswatch/` (962 lines, 868 test lines) with an allow-list grammar for advisory ranges, commit ancestry through the compare API, OSV queries and issue creation. `internal/supply/knownbad.go` (252 lines) holds the match rule. Dependabot covers Go modules, npm and Actions. `govulncheck` v1.10.0 runs in CI.
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [Renovate](https://github.com/renovatebot/renovate) custom managers | Bump pull requests for versions and commits in any file (`supply.lock`, `build.zig.zon`), from GitHub tags and releases | AGPL-3.0, separate service | 45.3.2, 27 Nov 2026 | App or Action | Good for bumps. Does not read repository advisories. |
| [OSV API](https://google.github.io/osv.dev/api/) / [osv-scanner](https://github.com/google/osv-scanner) | Advisories by commit, from CVE `GIT` ranges | Apache-2.0 | v2.7.1, 9 Nov 2026 | API, CLI | Covers Cloud Hypervisor: CVE-2026-45782 has a `GIT` range. Misses Ghostty's advisories (checked again on 27 Nov 2026). |
| [govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) | Go toolchain and modules | BSD-3-Clause | v1.10.0, 17 Nov 2026 | CLI | In use. Keep. |
| Dependabot | Go, npm, Actions bumps with a 7-day cooldown | Service | Current | Service | In use. Cannot read custom pin files. |
| GitHub repository advisories API | Each project's own advisories | Service | Current | API | The only source for Ghostty. Ranges are free text. |

- **Recommendation: simplify.** Keep the daily job, but make it a notifier: list each pinned repository's advisories, query OSV by commit, and open one issue for each advisory ID that `pins-watch-reviewed.toml` does not name yet. A person reads the range and records the verdict, as today. Drop the range grammar and ancestry engine. Add Renovate for bump pull requests. Keep `knownbad.go` (it is data and a version compare). Effort S. Shrinks about 1,830 lines (code and tests) to about 150. Risk low: the job still fails closed on anything new, and the volume is a few advisories a month.
- **Spec impact:** spec 19.5 ("The check", "The grammar", "The tag rule", tests 3 to 21), plan P0.9, `docs/contributing/releasing.md`.

#### The signed-tag release gate (P4.4, decision 0041)

- **Requirement (spec 20.7):** a release builds only from an annotated tag signed by a key on `main`'s list.
- **Today:** `scripts/verify-tag.sh` (143 lines) runs `git verify-tag` with `gpg.ssh.allowedSignersFile` from `main`. Its test is 433 lines with stand-in binaries for many edge cases.
- **Options:** `git verify-tag` with SSH signatures (git, in use: the right tool). [gitsign](https://github.com/sigstore/gitsign) (Apache-2.0, v0.18.0, 20 Oct 2026) gives keyless tag signatures, but a stolen GitHub login could also sign. GitHub tag rulesets and environment rules (service) limit who can push `v*` and who may read the release secrets.
- **Recommendation: keep ours, and stop adding to it.** The script is `git verify-tag` with care, and it is done. Cut the test to the four acceptance cases of plan P4.4 if it ever needs work. Effort none now. Risk none.
- **Spec impact:** none now. Spec 20.7's 18 test cases could shrink in a later pass.

#### gVisor runtime (P6.1, third_party/gvisor)

- **Requirement (spec 21.4):** a gvisor wall with a pinned `runsc` that the release manifest names by digest.
- **Today:** a 6,980-line patch on upstream `release-20261102.0` adds a pre-Go ELF entry that clears dumpability, a fixed controller, a Gofer identity map and an fd-parking helper. CI and release build five binaries with Bazel 8.8.1 on both architectures. `scripts/` adds 564 lines, `cmd/supply gvisor_*` 577 lines. Upstream is already at `release-20261123.1` (26 Nov 2026), so the patch needs a rebase for each upstream release.
- **Options:**

| Project | What it gives | Licence | Maintained (last release) | Form | Fit and gaps |
| --- | --- | --- | --- | --- | --- |
| [Upstream gVisor releases](https://gvisor.dev/docs/user_guide/install/) | `runsc` for x86_64 and arm64, `SHA256SUMS` and `SHA512SUMS` on GitHub releases, a GPG-signed apt repository | Apache-2.0 | release-20261123.1, 26 Nov 2026 | Binaries | Pin by SHA-256 in the manifest, as for the VMM. No Sigstore signatures. Lacks the patch's same-user ptrace defence. |
| Upstreaming the patch | One source of truth | Apache-2.0 | | Pull request | Unlikely as it stands: 7,000 lines of project-specific controller logic. A small "no dump before init" option might be accepted; open an issue first. |
| A separate account for `runsc` (plan H17) or the existing root step | The kernel's own uid boundary instead of a dumpability race | | | Design | Answers the same threat with no fork. |

- **Recommendation: defer, then adopt upstream.** Take gVisor out of the release and per-PR CI now: the gvisor wall is phase 6. When it returns, pin upstream `runsc` by digest, and answer the same-user threat with a separate uid or the root step. Effort S to remove from CI and release; M later. Removes about 8,500 lines of patch, inputs and scripts, two CI jobs and one release job. Risk low for the first release.
- **Spec impact:** spec 21.4, 14.9.1 (`gvisor` kind), 20.9 (`gvisor` advisories), `docs/contributing/gvisor-*.md`, plan P6.1.

#### SBOMs and setup trust (P0.6)

- **SBOMs:** GoReleaser runs [syft](https://github.com/anchore/syft) (Apache-2.0, v1.57.2, 16 Nov 2026) for each archive. [cyclonedx-gomod](https://github.com/CycloneDX/cyclonedx-gomod) (Apache-2.0, v1.12.0, 22 Aug 2026) is the alternative for Go-module detail. **Keep syft.** No code to remove.
- **Setup trust (spec 19.2):** a SHA-256 over the setup, resume and stop scripts, kept on the laptop. direnv's `allow` and mise's `trust` use the same pattern, but neither is a library. **Keep ours:** it is small and done.

---

### Top swaps by impact on a first working release

The first release needs three things: install with one command, a verified client and helper, and a Linux host that can run sessions. Today's `install.sh`, the SSH signature and the `sha256sum -c` before the bridge already meet the first two. What blocks the release is the coupling of the pipeline to walls that are not finished.

| # | Swap | Removes or unblocks | Effort | Risk |
| --- | --- | --- | --- | --- |
| 1 | Cut `release.yml` down to the tag gate, GoReleaser, the SSH signature and attestations. Ship microvm, gvisor and image items only when their wall ships, behind one switch each. Conformance gates those items, not the release. | Unblocks the first release since 0.1.2 | S | Low |
| 2 | Drop the SLSA generator job and cosign keyless signing; turn on GitHub immutable releases. New record replacing 0017. | One unmaintained dependency, one redundant signature path | S | Low |
| 3 | Take patched gVisor out of release and per-PR CI. Later, pin upstream `runsc` by digest. | About 8,500 lines, 3 heavy jobs, Bazel in CI | S now | Low |
| 4 | Move the image, kernel and gVisor double builds to a weekly job and to pull requests that touch their inputs. Let the `install.sh` test depend on a plain snapshot. | Six heavy jobs from each pull request; the install check stops waiting on Bazel | S | Low |
| 5 | Ship a v1 manifest with `helper`, `vt` and `userns` only; hash what the client embeds. | About 1,500 lines off the critical path | S | Low |
| 6 | Defer P4.2 (exec-mode leases, sealed copies, fs-verity) and P4.5 (the separate verifier). | About 3,200 lines and 1,600 test lines off the critical path | S | Low |
| 7 | Default image: the official Debian image by digest, copied to the project's registry. | About 1,260 lines, two CI jobs, one release job | S | Low |
| 8 | Pins watch as a notifier, plus Renovate for bumps. | About 1,650 lines of Python and tests | S | Low |
| 9 | When microvm returns: go-containerregistry for pulls and flattening; a 300-line runtime store; kernel config as Firecracker 6.18 plus a fragment. | About 5,000 lines and 7,700 config lines | M | Medium |

Swaps 1 to 4 are workflow and config changes of a few days, and give a releasable, verified client and helper. Whether the process wall itself is ready to ship is outside this audit. Swaps 5 to 8 keep the custom code out of that release without deleting work the owner may want later. Swap 9 is the reuse plan for the microvm phase.
