# Choose a sandbox wall

A wall separates a session from its host. Choose a level when you start a session:

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

A saved host floor sets the minimum requested by `--wall auto`. Naming a weaker level also requires `--downgrade`. This choice applies to that start. It does not lower the host's saved floor.

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
