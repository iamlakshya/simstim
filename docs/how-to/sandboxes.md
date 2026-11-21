# Choose a sandbox wall

## One provider machine per session

`simstim new --machine e2b --name task` makes a shared provider machine named `m-task` and prepares a sandbox inside it. Its fresh probe decides the available wall. The shared floor is `gvisor`; a weaker wall needs `--wall process --downgrade` or `--wall none --downgrade`. The machine is removed when you remove its original session with `simstim rm m-task/task`.

A machine receives a secret only when `[secret_tiers]` marks that name `shared` and the request explicitly names it with `--expose`. A bound secret is refused before its value command runs. The saved session records the exposed mode, so resume keeps the same restriction. Use a low-value credential that this machine may read. See [create a host](create-a-host.md#a-machine-for-one-session) for provider configuration and cleanup.

The classifier's key needs that same original explicit exposure. A new resume answer cannot replace the session's creation facts. Named SSH keys remain outside the guest environment, but their raw values reach the machine's signer. They need `shared` in `[secret_tiers]` and a fresh answer that the machine's owner can read them at each delivery. The grant history marks them as exposed to the host. Raising the saved host tier cannot bypass these checks, or allow alert tokens onto a session machine.

## Choose a level

A wall separates a session from its host. New sessions use the host's pinned floor by default. A policy can ask for a stronger level. You can also name a level for one start:

```sh
simstim new box --wall microvm --agent codex
```

| Level | Boundary |
| --- | --- |
| `none` | No sandbox wall |
| `process` | Namespaces, Landlock and seccomp on the host kernel |
| `gvisor` | A separate user-space kernel |
| `microvm` | A virtual machine with its own kernel |

A process wall shares the host kernel. A microvm has its own kernel, but still depends on the host kernel and its virtualisation software. The host's root user can read a session's files and memory. Choose the host's [trust tier](choose-a-host-tier.md) accordingly.

An explicit level must be available. The client refuses when it cannot build that wall. It does not silently replace a microvm with a process wall.

## Check the host

```sh
simstim host show box
simstim host doctor box
simstim host doctor box --json
```

The sandbox checks report each driver's result and the reason for a refusal. Access to `/dev/kvm` alone does not make a microvm available. The host also needs the supported kernel and architecture, a usable namespace jail, cgroup v2, the systemd user manager and verified runtime files.

Use [host preparation](give-a-host-the-root-step.md) to review optional root steps. `host prepare box --root-step print` writes the exact script for the host's owner. It runs nothing with root access.

A KVM access warning means the real device refused an open and the other
checked VM prerequisites are present. An ACL or group change may help, but
host security policy can still deny access. Preparation probes the device again
after a root step. The warning alone never makes a wall ready or pins a floor.

An installed runtime and a successful boot check are separate facts. The runtime must match this client's signed release. A completed boot check applies to those runtime bytes, that host boot, that kernel and that helper. Downloading files does not prove that they boot.

Update an older helper with:

<!-- test: help (updates the helper on a reachable host) -->
```sh
simstim host upgrade box
```

## Use Firecracker on an older kernel

The microvm level selects an available driver. Cloud Hypervisor is preferred when both drivers work. The Firecracker driver supports Linux kernel series 5.10, 6.1 and 6.18, on amd64 and arm64. It requires Firecracker 1.17.0 or later. Release builds pin the actual runtime bytes.

Firecracker uses its default MMIO transport. This driver does not enable PCI, restore memory snapshots, attach a network device or expose a metadata service. The guest uses the existing supervised link for its network policy and terminal streams.

The tools, base image and installed agents are read only. The session disk holds writable work. Firecracker cannot share a host folder. Use [copy](copy-files.md) for reviewed files or [pull](pull.md) for work with Git history.

## Keep the wall and its files

Each session records its selected wall. Resuming checks the saved wall and current policy again. Another terminal in the same sandbox uses that wall and its existing policy. See [shells and commands](shells.md).

Stopping keeps the workspace and configured state. Removing the primary session destroys its sandbox and its other terminals after confirmation. Removing a child terminal removes only that terminal. See [work with sessions](sessions.md).

A gVisor home starts in a fresh private folder for each cold start. The gofer uses that same folder, so a live fork can copy current agent files, including database journals. Files in this home reach the host's disk, including credentials an agent writes itself. Configured state folders remain separate writable binds. The host counts the fresh home in the session's disk limit and free-space checks. It removes the home only after the original runtime and its file readers have ended. If the original folder has been replaced or cleanup is uncertain, the helper preserves it and reports the failure. A new start never adopts an old home folder.

Leaving the wall unspecified is the same as `--wall auto`: use the pinned floor. The client checks that the selected level is available; a fresh probe does not replace that saved choice. Run `host prepare` when a saved host has no floor.

Naming a weaker level requires `--downgrade`. `--wall none` always needs it, including on a host whose floor is `none`. For example:

```sh
simstim new box --wall none --downgrade
```

The session records this exception and shows a downgrade warning. It changes only that start and keeps the host's saved floor. A shared host never uses a process wall. A stopped session keeps the wall and downgrade intent recorded when it was created.

The wall and the network rules solve different problems. Review the [network policy](policy.md) and [secret permissions](secrets.md) before giving a sandbox credentials.

## Decoy credential files

Walled sandboxes contain these fake credentials. They grant access to no account.

| Name | File in the sandbox home | Form | Credential hosts |
| --- | --- | --- | --- |
| `aws` | `~/.aws/credentials` | A `prod-admin` profile, with an `AKIA` access key ID and a 40-character secret key. There is no `default` profile. | `sts.amazonaws.com`, `sts.*.amazonaws.com` |
| `git` | `~/.git-credentials` | `https://deploy:<token>@github.com`, with a `ghp_` token and 36 following characters. The sandbox does not enable Git's `store` helper. | `github.com`, `api.github.com` |

The files use mode 0600. Each cold start makes new values. A fork's children get their own values. Image captures leave these files out. A request carrying a decoy raises `secret_misuse` and is refused. This includes a key check to a credential host that policy already refuses. The row shows `decoy used` in red. The session keeps running.

The AWS CLI selects `default` unless you choose another profile. Git reads this file through its `store` helper. Selecting `prod-admin` or enabling that helper can therefore raise an alert. Set `[sandbox] decoys = false` to write no decoy files and register no decoy tripwires.

A valid opt out in any layer keeps decoys off, even when a later layer sets `true`. A setting that conflicts with a policy lock refuses the start with `policy_locked`. The refused start creates no decoy files or tripwires.

Definitions and formats: [AWS profiles](https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-files.html), [AWS identifiers](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_identifiers.html), [Git credential storage](https://git-scm.com/docs/git-credential-store) and [GitHub token formats](https://github.blog/engineering/platform-security/behind-githubs-new-authentication-token-formats/).

Decoy checks use the existing network audit. An observable use is refused and recorded as `decoy`, with the decoy's name and the destination. The session keeps running. The proxy checks URLs, headers and DNS names, plus the first 1 MiB of each intercepted request body. It checks raw values and standard base64 values embedded at all three byte alignments. Other allowed TLS flows still splice. The proxy cannot inspect their encrypted bodies.

Decoy hosts use local inspection even when a network rule denies them. Their DNS answer may name the session's existing synthetic route. This opens no origin connection and grants no network access. The usual network and address rules still decide whether a clean request may leave.

The decoy choice applies at the next start. A live policy update keeps the current generation and its tripwires, including redaction. Turning decoys on live does not create files or values in a running session. Network and deny changes still apply at once.

## Package manager settings

Walled sessions use `sandbox.package_managers = "guarded"` by default. These settings reduce the chance of installing a new malicious release or running an install script. They work alongside the network policy. The agent, guest root or a repository setting may change them.

| Tool | Guarded settings |
| --- | --- |
| npm | Disable install scripts. Set the minimum release age in days. |
| pnpm | Write `~/.config/pnpm/config.yaml` with install scripts disabled and the minimum release age in minutes. |
| Yarn | Disable install scripts. Set the minimum release age in minutes. |
| Bun | Disable install scripts. Write matching `.bunfig.toml` files in the home and its fixed `.config` directory, with the minimum release age in seconds. |
| pip | Set `PIP_UPLOADED_PRIOR_TO` to a fixed UTC cutoff. This uses the timestamp form supported from pip 26.0. |
| uv | Set `UV_EXCLUDE_NEWER` to the same fixed UTC cutoff. |
| Go | Set `GOTOOLCHAIN=local`, `GOPROXY=https://proxy.golang.org`, `GOSUMDB=sum.golang.org` and `GOVCS=*:off`. Leave `GOFLAGS` unchanged. |
| cargo | Add no setting. There is no stable switch for build scripts and procedural macros. |

Choose `strict` to keep the guarded settings and add `PIP_ONLY_BINARY=:all:` and `UV_NO_BUILD=1`. Choose `off` to add no package variable and write no package file. An explicit name in `sandbox.env`, including an empty value, takes precedence over an environment default.

Set `network.min_release_age` to whole days from `0d` to `365d`. The default is `7d`. Values such as `90m` are refused with `release_age_invalid`. At `0d`, the guest adds no age variable or file line. Each tool keeps its own defaults. For example, pnpm 11 retains its built-in one-day floor unless you change its configuration.

The pip and uv cutoff is the cold start time minus the chosen age. It stays fixed until the next cold start. A long-running session therefore becomes stricter. A policy change updates the setting for the next start and leaves the running guest's settings in place.

Package files are created with mode 0600 in the fresh guest home, before setup commands and agent installation. An existing file or a symbolic link in its parent path causes a refusal. The helper never replaces a foreign file. Bun gets both fixed files because an absolute `XDG_CONFIG_HOME` can select its XDG file instead of its home file. Environment values do not choose where the helper writes.

## Pin a registry image

```sh
simstim image pull ghcr.io/example/dev:1 my-server
simstim new my-server --image ghcr.io/example/dev:1
```

The laptop records the first digest in `images.lock` and prints it. A start uses that pin. It never follows a tag that moved. A later pull reports both digests and refuses the change. Review the new image, then run `image pull` with `--update` to replace the pin.

To require a cosign signature, add this policy to `config.toml`:

```toml
[sandbox.image_policy]
identity = "release@example.com"
issuer = "https://accounts.example.com"
trusted_root = ""
```

Both the certificate identity and its issuer must match exactly. Leave `trusted_root` empty to use the public Sigstore root embedded in the client. Set it to a Sigstore trusted root file on the laptop for another instance or a newer key. A relative path starts at the original `config.toml` folder. A changed root file also makes the next use check the signature again.

The laptop fetches signatures from the image's registry with your registry login. It tries OCI referrers, the referrers tag and the older cosign `.sig` tag. Verification runs offline in the release's checked verifier. It requires a transparency log entry and an observed timestamp. Registry credentials and signature policy files stay on the laptop.

An index signature covers its platforms. A platform signature covers only that platform. A change to the selected policy checks an existing pin again before the host uses it, including a cached image. A failed check leaves the existing pin in place. The default image keeps the release manifest's pin. A saved image keeps its separate original-host checks below.

A client-issued resume or restore checks the session's original image under the current policy before fetching secret values. Updating a tag's pin does not replace that session's original image. The host compares the client's expected image with its own original protected boot request before starting.

An automatic restore while the laptop is offline keeps the original protected image and the policy last delivered to the host. It cannot see a later change made only in the laptop's configuration. It contacts no policy service. The next client-issued start checks the current laptop policy again.

## Idle stop and measured wake

Automatic microvm idle stop remains off while its conformance measurements
are pending. The two-hour default needs measured wake on both bare-metal
and nested KVM hosts within the documented target. A stopped row or a
successful build does not establish that result. Contributors can use the
[conformance guide](../contributing/microvm-conformance.md).

## Save tools as an image

Set up system tools in a running microvm sandbox, then save them for later starts on the same host:

```sh
simstim image save my-server/tools devtools
simstim new my-server --image devtools
```

The saved name takes 1 to 64 lower-case letters, numbers, dots, underscores or hyphens. It starts with a letter or number. Add `--replace` to replace that name on the same host.

The review lists changed shell start files, git configuration, executable folders, system services and privileged files. It also lists paths left out of the image. Read flagged files before answering yes, or pass `--allow-flagged` after reviewing them. Without input, flagged changes fail with `image_flagged`. `--json` prints the saved result on stdout and each review message as a JSON warning on stderr.

Workspaces, temporary files, agent installations and retained state stay out. So do known login paths, decoys and the credential paths refused by image paste. A scan refuses known session secret values, sentinels and decoys with `image_secret`. A login made inside the sandbox and copied to another path can remain: the laptop does not know its value. Treat saved images as private to their host, and review changes from a sandbox you do not trust.

`image ls` and live session details mark a saved image as "made on <host>". The launcher shows that origin only while connected to the same host identity.

A saved image stays on the host that made it. The laptop records its digest for that host in `images.lock`. Starting it on another host fails with `image_elsewhere`. A cancelled or declined review drops its prepared image. Unkept images expire after one hour.

To prepare separate Linux sandbox accounts, run `simstim host prepare <host> --separate-accounts`. The client stages the signed launcher, shows each account change and its undo, then uses the same root-step consent. The option is off by default. Existing sandbox disks keep their original account mode.

## Keep disk checkpoints

A microvm takes a checkpoint after a finished turn, at most once every
30 seconds. It keeps the newest 10. Change the number in any policy layer:

```toml
[sandbox]
checkpoints = 10
```

Set it to 0 to turn checkpoints off. Process and gvisor walls do not have
one session disk, so their checkpoints are off. A real file-clone probe in
the session folder decides whether its host filesystem supports checkpoints.
An unsupported filesystem reports `rewind_off`. ext4 cannot clone files.

Checkpoints pause when free space falls below `sandbox.disk_max`. Their
status appears in session details. Each checkpoint keeps the claimed branch
heads and each session's conversation ID beside the disk. It holds no memory
or terminal output.

Use `simstim rewind box/task --list` to list fixed checkpoint times. Select a
number to restore its disk and conversations. The helper checks current
policy and room, obtains fresh credentials, then stops the original runtime
and makes a safety checkpoint before replacing the disk. See
[fork and rewind](fork.md#return-to-a-checkpoint) for confirmation and retry
behaviour.

## Check the host kernel

Run `simstim host doctor <host>` after a kernel update. The helper checks the
running kernel against an embedded list of known local root bugs. The list is
refreshed with each release. It includes the advisory link and the date when
its version ranges were checked.

A distribution package match takes precedence over an upstream range. A
version in an upstream range gets a warning because distributions backport
fixes. An unreadable version is reported as unknown. A documented mitigation
is reported only when every required fact is observed.

The check never lowers a pinned floor, changes a level or blocks a start. It
does not install a kernel, unload a module or reboot the host. Ask the host's
owner before a reboot.

## EC2 Nitro Sandboxes

On a supported EC2 instance with Nitro Enclaves enabled at launch,
`--wall microvm` can use the `nitro` driver without nested virtualisation.
Intel, AMD and Graviton are supported. Check the current
[AWS instance list](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/amazon-ec2-nitro-sandboxes.html).
The parent needs the Nitro Sandboxes driver, documented in Amazon Linux 2023
`kernel6.18` from version `6.18.51-120.162.amzn2023`.

Run `simstim host prepare <host>`. The optional `nitro-acl` root step opens the
device now. `nitro-udev` keeps access after reboot. The client installs no system
package; install the host's `acl` package yourself if `setfacl` is missing.
A successful sandbox test boot records the wall floor. Where Nitro and nested
KVM both pass, Nitro takes precedence.

To raise the slot budget, run `simstim host prepare <host> --nitro-slots 20`.
The review states the donation: each group of eight extra slots uses 128 MiB.
Slots can only grow, and that memory stays donated until reboot. Undo records
that retained change. Ask the host's owner before any reboot.

For predictable contiguous memory, the owner can reserve a pool at boot with
`nitro_enclaves.mempool=4G`, for example. This requires a kernel command-line
change and reboot, so it is never a root preparation step. Without a pool, the
driver allocates contiguous memory at each start and may fail when fragmented.
Admission refuses a guest beyond the available memory, vCPU or slot budget.

The guest uses direct mode, with no attestation and no network device. The
ordinary vsock link carries its terminal and approved network traffic. Donated
RAM has no balloon. Parent cgroup CPU limits do not throttle Nitro guest CPUs.
