# Sandboxes

A sandbox runs an agent session inside a wall on a Linux host, so the agent sees only the files and network destinations that you allow.

## Wall levels

Each host supports one or more wall levels, from weakest to strongest:

| Level | What builds it | Requirements |
| --- | --- | --- |
| `none` | No wall | Any Linux host that runs sessions today |
| `process` | [bubblewrap](https://github.com/containers/bubblewrap) (`bwrap`), Landlock and seccomp | Linux 5.13 or later, `bwrap`, unprivileged user namespaces, Landlock ABI 1 or more, seccomp |
| `gvisor` | Pinned upstream [gVisor](https://gvisor.dev/) `runsc` (systrap) | Linux 5.6 or later (`amd64` or `arm64`), pinned `runsc` runtime, unprivileged user namespaces |
| `microvm` | Cloud Hypervisor on KVM | Linux 6.8 or later, `/dev/kvm` open to your user, pinned runtime |

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
- **Private home:** `/home/agent` is a fresh mode-0700 tmpfs on every start, so startup files such as `~/.bashrc` never persist across restarts. Only the agent's conversation folders (`StateDirs`, such as `.claude/projects`, `.codex/sessions` and `.local/share/opencode`) and the sandbox's own [kept caches](#kept-caches) stay across `stop` and `resume`.
- **System and agent files:** `/usr` and system library directories are mounted read-only, `/etc` is filtered through an allowlist (hiding `/etc/environment`, `/etc/shadow`, `/etc/sudoers` and `/etc/ssh`) with synthetic `passwd`, `group`, `hosts` and `resolv.conf`, and agent binaries outside `/usr` are mounted read-only under `/opt/agents/` without exposing your host home path.
- **Kernel confinement:** the sandbox runs as uid 1000 with zero capabilities, `NoNewPrivs=1`, a seccomp-bpf filter blocking namespace creation, mount calls, `bpf`, `io_uring`, non-allowlisted socket families and foreign ABIs (i386 `int 0x80` and x32), and Landlock filesystem and IPC scoping applied by `wall-init` before reading from the link.
- **Network:** the sandbox has an empty network namespace (loopback only). Direct outbound TCP and UDP fail closed. `HTTP_PROXY` and `HTTPS_PROXY` point at `http://127.0.0.1:3128`, where `wall-init` forwards flows over `internal/link` to the host's `egress` process under the session's network policy.

- **Files, SSH and git:** `cp` and `ssh-proxy` open `sftp` and `exec` link streams through the supervisor so SFTP and shell commands run inside the sandbox as `agent` at `/work/<name>`, never on the host. `pull` builds a verified git bundle from the host workspace path through `git-op`.

Use `stop`, `resume` and `rm` as with any other session: `stop` tears down the sandbox while keeping `/work`, `StateDirs` and kept caches, `resume` starts a fresh sandbox with those folders reattached, and `rm` removes the session, its state and its caches completely.

## Kept caches

Because `/home/agent` resets on every start, package-manager and build caches are kept separately inside each session's own private folder (`~/.local/state/simstim/sessions/<id>/caches/`) and bind-mounted into `/home/agent/` on every start. Reinstalls after `stop` and `resume` stay fast without sharing caches across sandboxes.

Built-in kept cache paths (defined with upstream documentation sources in `internal/sandbox/caches.toml`):

| Tool | Path under `/home/agent` |
| --- | --- |
| npm | `.npm/_cacache` |
| pnpm | `.local/share/pnpm/store`, `.cache/pnpm` |
| Yarn Berry | `.yarn/berry/cache` |
| pip | `.cache/pip` |
| uv | `.cache/uv` |
| Cargo | `.cargo/registry`, `.cargo/git` |
| Go | `go/pkg/mod`, `.cache/go-build` |
| Gradle | `.gradle/caches` |
| Maven | `.m2/repository` |

Kept caches are on by default (`sandbox.keep_caches = true`). To add extra relative paths under `/home/agent`, drop a built-in path with a leading `!`, or turn kept caches off, configure `[sandbox]` in `config.toml` or override it per host under `[host.sandbox]` in `hosts.toml`:

```toml
[sandbox]
keep_caches = true
caches = ["!.gradle/caches", ".cache/ccache"]
```

Per-host overrides in `hosts.toml` layer on top of `config.toml`:

```toml
[[host]]
name = "my-vps"
ssh = "dev@my-vps"

[host.sandbox]
keep_caches = true
caches = ["!.m2/repository"]
```

- **Never shared between sandboxes:** each sandbox mounts only its own session cache folder. Two sandboxes never see each other's caches (see [security model](../explanation/security.md)).
- **Executable and startup folders are never kept:** `.cargo/bin`, `go/bin`, `.local/bin`, shell startup files (`.bashrc`, `.profile`, `.zshrc`), hook configs (`.claude/settings.json`) and credential folders (`.ssh`, `.aws`, `.gnupg`) are always refused in `config check` and by the host and wall drivers.
- **`stop` keeps caches; `rm` deletes them:** stopping a session leaves its caches intact for `resume`; deleting a session with `rm` removes its caches completely.

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

## Session sizes

Set CPU and memory limits with `--cpus` and `--memory`, or under `[sandbox]` in `config.toml` (and per host under `[host.sandbox]` in `hosts.toml`). `--memory` and `--memory-max` set the same memory limit and cannot be passed together.

<!-- test: help (needs a reachable host) -->
```sh
simstim new my-vps --agent claude --cpus 2 --memory 4G
simstim resume my-vps/auth-fix --cpus 4 --memory 8G
simstim attach my-vps/auth-fix --cpus 4 --memory 8G
```

Limits are applied through systemd unit properties (`MemoryMax=` and `CPUQuota=`). A new size takes effect at the next start or wake; passing `--cpus` or `--memory` to `resume` or `attach` on a running session changes nothing and prints a notice on stderr. On walled sessions, `[sandbox] disk_max` adds a soft disk cap checked every minute alongside the 5% host free-space floor; exceeding either stops the session with `disk_full`.

## Environment variables

Pass non-secret environment variables with `--env K=V`, `--env K` (reading `K` from your laptop's environment), `--env-file <path>`, or `[sandbox.env]` in config. Narrower layers win over wider layers, and later flags win over earlier flags. `--env-file` reads one `K=V` or `K` per line, skipping blank lines and `#` comments without stripping quotes or expanding variables.

<!-- test: help (needs a reachable host) -->
```sh
simstim new my-vps --agent claude --env LOG_LEVEL=debug --env-file ./dev.env
```

Any variable listed under `[secrets]` is refused with `env_is_secret` so credentials always travel through `--secret`. Every session also receives its identity variables (`SIMSTIM_SESSION`, `SIMSTIM_HOST`, `SIMSTIM_AGENT` and `SIMSTIM_SANDBOX`), which always take precedence, and walled sessions set `BROWSER` to a guest helper that prints login URLs in the session terminal.

Everything set through `--env`, `--env-file` or `[sandbox.env]` can be read by processes inside the session; never put secrets there.

## Copy files from your laptop

Use `--copy <laptop path>[:<guest path>]` or `[sandbox] copy_in` to copy skills, settings or fixtures from your laptop into a session over SFTP (`github.com/pkg/sftp`) before the agent starts:

<!-- test: help (needs a reachable host) -->
```sh
simstim new my-vps --agent claude --copy ~/.claude/skills --copy ./fixture.json:/mnt/fixture.json
```

Without a guest path, paths under your laptop's home folder land under `/home/agent/` (the walled session home) and other paths land under `/mnt/<name>`. A guest path starting with `~/` expands to `/home/agent/`. Private files matching the paste guard's rules (`~/.ssh`, `~/.aws`, `~/.gnupg`, `*.pem`, `*.key`, `.env`, `id_*`, `credentials*`) are refused with `copy_sensitive`, and each start caps laptop copies at `[sandbox] copy_max` (64 MiB by default) and 10,000 files.

When a host restores a session after a reboot without your laptop connected, `ls` marks the row with `(copies not sent)`, and your next `attach` uploads the configured files again.

## Seed repositories from your laptop

Use `--seed <folder>` to bundle a git repository from your laptop with `git bundle create`, upload it over SFTP, and clone it into the session with your laptop's `origin` URL and git `user.name` / `user.email`:

<!-- test: help (needs a reachable host) -->
```sh
simstim new my-vps --agent claude --seed .
simstim new my-vps --agent claude --seed . --seed-dirty
```

With `--seed-dirty`, uncommitted working-tree changes and non-ignored untracked files are added as one extra commit without changing `git status`, writing any ref on your laptop or running repository clean filters. Untracked files sent are printed on stderr, and private names are refused with `copy_sensitive`. Bundles larger than `[sandbox] seed_max` (512 MiB by default) are refused.

Pass `--seed` more than once to clone several repositories into the same session, and pick which one to bring back with `pull --repo <name>`:

<!-- test: help (needs a reachable host) -->
```sh
simstim new my-vps --agent claude --seed ../api --seed ../web
simstim pull my-vps/api --repo web
```

## Inside a microvm sandbox

Inside a Cloud Hypervisor microvm, `wall-init` boots as PID 1 from the `dm-verity`-verified tools image and prepares the guest before accepting `internal/link` over `virtio-vsock`:

- **Overlay root and session disk:** the verified root image mounts read-only under an overlay filesystem. By default (`sandbox.system = "reset"`), the overlay upper layer lives in RAM and resets on every start. Pass `--keep-system` (or set `sandbox.system = "keep"` in `config.toml`) to keep root filesystem changes on the session disk across stops. The session disk's `/work` folder and each agent's conversation `StateDirs` (such as `.claude/projects`, `.codex/sessions` and `.local/share/opencode`) stay across restarts, while `/home/agent` is always a fresh mode-0700 tmpfs on every start so files such as `~/.bashrc` do not persist.
- **Separate `holder` and `agent` users:** the guest holder drops privileges to uid `900` (`holder`) with `NoNewPrivs=1` and `PR_SET_DUMPABLE=0`, while the agent, shells and SFTP run as uid `1000` (`agent`). Without `sandbox.sudo = true`, the agent cannot `ptrace` the holder or inspect its socket. Setting `sandbox.sudo = true` in `config.toml` grants passwordless `sudo` to `agent` inside a microvm (and marks the session row with `root`); weaker walls refuse it with `sudo_wall`.
- **Transparent proxy and synthetic DNS:** `wall-init` creates a `dummy0` default route (falling back to a `veth` pair when the guest kernel omits the `dummy` driver), binds the explicit proxy on `127.0.0.1:3128`, the transparent proxy on `127.0.0.1:3129` (`SO_ORIGINAL_DST`), and the DNS stub on `127.0.0.53:53` (pointing `/etc/resolv.conf` at it). Guest `nftables` rules redirect all non-loopback IPv4 TCP to `127.0.0.1:3129` and reject UDP, ICMP and non-loopback IPv6. The host `egress` process answers DNS in `synthetic` mode (`198.18.0.0/15`) and maps redirected flows back to allowed hostnames.
- **Docker inside a microvm:** pass `--docker` to `new` (or set `sandbox.docker = true` in `config.toml`) with an image that includes `dockerd` (selected via `--image` or `sandbox.image`). `wall-init` puts `agent` in the `docker` group and activates `dockerd` lazily on first connection to `/run/docker.sock`. The session row shows `docker`. Non-microvm walls refuse `--docker` with `docker_wall`, and images without `dockerd` fail start with `docker_missing`.
- **Clean shutdown and memory claims:** on `stop`, `SIGTERM` or an ACPI power-button event from `ch-remote`, `wall-init` stops holders and `dockerd`, terminates remaining processes, flushes and unmounts the session disk, calls `sync(2)` and powers off with `reboot(2)`. Alongside heartbeats, `wall-init` sends periodic `mem` claims from guest `/proc/meminfo` while the host cgroup cap remains authoritative.

Microvm options on `new` and in `config.toml`:

<!-- test: help (needs a reachable host) -->
```sh
simstim new my-vps --image buildpack-deps:trixie-scm --keep-system --docker --detach
```

```toml
[sandbox]
image = "buildpack-deps:trixie-scm"
system = "reset" # reset | keep
sudo = false
docker = false
```
