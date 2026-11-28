# Sandboxes

A sandbox runs an agent session inside a wall on a Linux host, so the agent sees only the files and network destinations that you allow.

## Wall levels

Each host supports one or more wall levels, from weakest to strongest:

| Level | What builds it | Requirements |
| --- | --- | --- |
| `none` | No wall | Any Linux host that runs sessions today |
| `process` | [bubblewrap](https://github.com/containers/bubblewrap) (`bwrap`), Landlock and seccomp | Linux 5.13 or later, `bwrap`, unprivileged user namespaces, Landlock ABI 1 or more, seccomp |
| `gvisor` | Pinned upstream [gVisor](https://gvisor.dev/) `runsc` (systrap) | Linux 5.6 or later (`amd64` or `arm64`), pinned `runsc` runtime, unprivileged user namespaces |
| `microvm` | Cloud Hypervisor on KVM (later phase) | Linux 6.8 or later, `/dev/kvm` open to your user, pinned runtime |

## Check what a host supports

Run `host show` or `host doctor` to probe a host's sandbox features:

<!-- test: help (needs a reachable host) -->
```sh
simstim host show my-vps
simstim host doctor my-vps
```

The probe checks `bwrap` by running it in new user, mount, PID, network, IPC, UTS and cgroup namespaces, checks Landlock and seccomp in the kernel, and checks `/dev/kvm`, systemd and cgroup v2. When anything is missing, the output names what is missing with a `Try:` hint.

For scripts, `--json` includes the full probe under `sandbox`:

<!-- test: help (needs a reachable host) -->
```sh
simstim host show my-vps --json
simstim host doctor my-vps --json
```

## Start a sandboxed session

Pass `--wall process` to `new` to run a session inside a bubblewrap process wall:

<!-- test: help (needs a reachable host) -->
```sh
simstim new my-vps --wall process --dir /path/to/repo
```

Inside a process wall:

- **Workspace:** your chosen directory is mounted writable at `/work/<name>`. If you pass no directory (or `~` or `/`), the host creates a private scratch directory at `/work/scratch` instead of mounting your host home folder.
- **Private home:** `/home/agent` is a fresh mode-0700 tmpfs on every start, so startup files such as `~/.bashrc` never persist across restarts. Only the agent's conversation folders (`StateDirs`, such as `.claude/projects`, `.codex/sessions` and `.local/share/opencode`) stay across `stop` and `resume`.
- **System and agent files:** `/usr` and system library directories are mounted read-only, `/etc` is filtered through an allowlist (hiding `/etc/environment`, `/etc/shadow`, `/etc/sudoers` and `/etc/ssh`) with synthetic `passwd`, `group`, `hosts` and `resolv.conf`, and agent binaries outside `/usr` are mounted read-only under `/opt/agents/` without exposing your host home path.
- **Kernel confinement:** the sandbox runs as uid 1000 with zero capabilities, `NoNewPrivs=1`, a seccomp-bpf filter blocking namespace creation, mount calls, `bpf`, `io_uring`, non-allowlisted socket families and foreign ABIs (i386 `int 0x80` and x32), and Landlock filesystem and IPC scoping applied by `wall-init` before reading from the link.
- **Network:** the sandbox has an empty network namespace (loopback only). Direct outbound TCP and UDP fail closed. `HTTP_PROXY` and `HTTPS_PROXY` point at `http://127.0.0.1:3128`, where `wall-init` forwards flows over `internal/link` to the host's `egress` process under the session's network policy.

- **Files, SSH and git:** `cp` and `ssh-proxy` open `sftp` and `exec` link streams through the supervisor so SFTP and shell commands run inside the sandbox as `agent` at `/work/<name>`, never on the host. `pull` builds a verified git bundle from the host workspace path through `git-op`.

Use `stop`, `resume` and `kill` as with any other session: `stop` tears down the sandbox while keeping `/work` and `StateDirs`, `resume` starts a fresh sandbox with those folders reattached, and `kill` removes the session and its state completely.

## Pin a host's floor

A host's **floor** is the weakest wall level that new sessions on that host may get. `host add` and `host create` probe the host and pin the strongest level that probes OK, unless you pass `--floor` with a lower level.

To change a host's floor later:

<!-- test: help (needs a saved host) -->
```sh
simstim host set my-vps floor=process
```

`host set` checks the host first and refuses a floor stronger than the strongest level that the host can run.

## Install bubblewrap

When a Linux host lacks `bubblewrap` or blocks unprivileged user namespaces, `host doctor` prints one `sudo` command in its `Try:` hint (and lists it as a manual fix in `host doctor --fix`). The host helper never runs `sudo` for this step; you run the command once on the host.

On Debian 12 or 13 and Ubuntu 25.04 or later, install the distribution package:

```sh
sudo sh -c 'apt-get update && apt-get install -y bubblewrap'
```

On Ubuntu 24.04, Ubuntu restricts unprivileged user namespaces through AppArmor (`kernel.apparmor_restrict_unprivileged_userns = 1`). Install `apparmor-profiles` and `apparmor-utils` alongside `bubblewrap` and load Ubuntu's `bwrap-userns-restrict` profile in one `sudo` command:

```sh
sudo sh -c 'apt-get update && apt-get install -y bubblewrap apparmor-profiles apparmor-utils && install -m 0644 /usr/share/apparmor/extra-profiles/bwrap-userns-restrict /etc/apparmor.d/bwrap-userns-restrict && apparmor_parser -r /etc/apparmor.d/bwrap-userns-restrict'
```

If `bubblewrap` is already installed on Ubuntu 24.04, `host doctor` prints one `sudo` command to install and load `bwrap-userns-restrict` alone.

On Fedora or RHEL, run `sudo dnf install bubblewrap`. On Arch, run `sudo pacman -S bubblewrap`. On Alpine, run `sudo apk add bubblewrap`. On openSUSE, run `sudo zypper install bubblewrap`.

After installing `bubblewrap` or loading the AppArmor profile, run `simstim host doctor my-vps` again. Each sandbox probe probes the host afresh and refreshes `~/.local/state/simstim/walls.json`.

## Package managers

By default, a sandbox points npm, pnpm, Yarn, Bun, pip, uv, cargo and Go at the session's egress proxy (`HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY`, `NO_PROXY`, `NODE_USE_ENV_PROXY`, `npm_config_proxy`, `npm_config_https_proxy` and `CARGO_HTTP_PROXY`), and the default `balanced` network preset allows their registries (`registry.npmjs.org`, `pypi.org`, `files.pythonhosted.org`, `proxy.golang.org`, `sum.golang.org`, `crates.io`, `index.crates.io` and `static.crates.io`) on port 443. No registry mirror runs on the host.

Guest init also applies defensive package-manager settings to reduce the risk of a fresh poisoned release or an install script running code during dependency resolution. These settings are defence in depth only: guest root and the agent itself can unset environment variables or edit files inside `/home/agent`.

### Levels and release age

Set `sandbox.package_managers` and `network.min_release_age` in `config.toml`:

```toml
[sandbox]
package_managers = "guarded" # guarded | strict | off

[network]
min_release_age = "7d" # whole days from 0d to 365d
```

- **`guarded` (default):** skips dependency install scripts for npm, pnpm, Yarn and Bun; sets a minimum release age of `network.min_release_age` (7 days by default) for npm, pnpm, Yarn, Bun, pip and uv; and sets `GOTOOLCHAIN=local`, `GOPROXY=https://proxy.golang.org`, `GOSUMDB=sum.golang.org` and `GOVCS=*:off` for Go (without setting `GOFLAGS`). `cargo` has no stable release-age or build-script off switch.
- **`strict`:** everything in `guarded`, plus `PIP_ONLY_BINARY=:all:` and `UV_NO_BUILD=1` so pip and uv install prebuilt wheels only and refuse source builds. Note that a pip requirements file can still ask for `--no-binary`.
- **`off`:** guest init writes no defensive package-manager variables or home files. Proxy variables stay in place so installs still reach allowed registries through the egress proxy.

Set `network.min_release_age = "0d"` to keep script blocking and Go settings while turning off the release-age cutoff. Changes apply on the next session start.

### Loosening one tool

To override one variable for a tool (for example, to allow a private Go module proxy or temporarily allow npm install scripts), set it under `[sandbox.env]` in `config.toml`. Any name in `sandbox.env` replaces the value written by guest init:

```toml
[sandbox.env]
GOPROXY = "https://proxy.corp.example,https://proxy.golang.org"
GOPRIVATE = "corp.example/*"
```

## Use the gVisor wall (`--wall gvisor`)

The gVisor wall runs each session behind upstream gVisor's user-space kernel (the Sentry on the `systrap` platform) without requiring `/dev/kvm`.

1. Download and verify the pinned upstream `runsc` runtime on the host:

```sh
simstim-host wall install gvisor
```

This downloads the pinned `release-20261123.1` archive, checks its SHA-256 digest and the digest of `runsc` and its required `gvisor-bin/` sidecars, and stores them under `~/.local/share/simstim/artefacts/runtime/`.

2. On Debian 12 or 13, no root step is needed. On Ubuntu 24.04 or later (where `kernel.apparmor_restrict_unprivileged_userns = 1`), `runsc` needs its own AppArmor profile rather than `bwrap-userns-restrict`, because `bwrap-userns-restrict` denies capabilities that `runsc` uses inside its new user namespace. Run `simstim host doctor my-vps` to get the single `sudo` command that installs and loads `/etc/apparmor.d/simstim-runsc` for the pinned `runsc` binary.

3. Start a session with `--wall gvisor`:

<!-- test: help (needs a reachable host) -->
```sh
simstim new my-vps --wall gvisor --dir /path/to/repo
```

Inside a gVisor wall, the session gets the same workspace, fresh `/home/agent`, filtered `/etc`, `/opt/agents/` mounts, and loopback proxy (`127.0.0.1:3128`) through the host `egress` process as the process wall, with gVisor's Sentry (`--network=none --directfs=false --ignore-cgroups --host-uds=none`) and an OCI seccomp profile between the agent and the host kernel.
