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

Use [host preparation](give-a-host-the-root-step.md) to review optional root steps. `host prepare --root-step print` writes the exact script for the host's owner. It runs nothing with root access.

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
