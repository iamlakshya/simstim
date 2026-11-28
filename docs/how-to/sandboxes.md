# Sandboxes

A sandbox runs an agent session inside a wall on a Linux host, so the agent sees only the files and network destinations that you allow.

## Wall levels

Each host supports one or more wall levels, from weakest to strongest:

| Level | What builds it | Requirements |
| --- | --- | --- |
| `none` | No wall | Any Linux host that runs sessions today |
| `process` | [bubblewrap](https://github.com/containers/bubblewrap) (`bwrap`), Landlock and seccomp | Linux 5.13 or later, `bwrap`, unprivileged user namespaces, Landlock ABI 1 or more, seccomp |
| `gvisor` | Pinned upstream [gVisor](https://gvisor.dev/) `runsc` (systrap) | Linux 5.6 or later (`amd64` or `arm64`), pinned `runsc` runtime, unprivileged user namespaces |
| `microvm` | Pinned [Cloud Hypervisor](https://www.cloudhypervisor.org/) on KVM | Linux 6.8 or later, `/dev/kvm` open to your user, `mkfs.ext4`, pinned runtime and root image |

## Check what a host supports

Run `host show` or `host doctor` to probe a host's sandbox features:

<!-- test: help (needs a reachable host) -->
```sh
simstim host show my-vps
simstim host doctor my-vps
```

The probe checks `bwrap` by running it in new user, mount, PID, network, IPC, UTS and cgroup namespaces, checks Landlock and seccomp in the kernel, and checks `/dev/kvm`, systemd and cgroup v2. For the Cloud Hypervisor `microvm` driver (`chv`), the probe checks Linux 6.8 or later, opens `/dev/kvm` read/write and verifies `KVM_GET_API_VERSION` returns `12`, records `systemd-detect-virt`, checks `mkfs.ext4`, and checks that the pinned `cloud-hypervisor`, `ch-remote` and guest kernel files are in the host's artefact store with a valid SHA-256 digest. When `/dev/kvm` is present but not open to your user, `host doctor` and `host doctor --fix` show the one-time `sudo` command to add your user to the `kvm` group (with `setfacl` as well when the `acl` package is installed), and `host show` names missing optional `acl` and `uidmap` packages. When anything is missing, the output names what is missing with a `Try:` hint.

`host doctor` also checks the host's Linux kernel against an embedded list of known local root vulnerabilities (starting with VsockDrop, `CVE-2026-53365`, and Vghost, `CVE-2026-74580`). On Debian and Ubuntu it compares the running kernel package version against the distribution's fixed package version using Debian version order; on other distributions it checks the upstream kernel version range and reports `maybe` so you can check your distribution's tracker. When an advisory names a mitigation (such as `vhost_vsock` not loaded for VsockDrop), `host doctor` reports the bug as mitigated. Kernel bug warnings never lower a host's wall floor or refuse a session start.

For scripts, `--json` includes the full probe under `sandbox` and each kernel bug check result (with its `id` and `status`) under `kernel_bugs`:

<!-- test: help (needs a reachable host) -->
```sh
simstim host show my-vps --json
simstim host doctor my-vps --json
```

## Start a sandboxed session

New sessions run in a sandbox by default (`sandbox.wall = "auto"`), at the host's pinned floor (`wall_floor` in `hosts.toml`):

<!-- test: help (needs a reachable host) -->
```sh
simstim new my-vps --dir /path/to/repo
```

You can also name a wall level explicitly with `--wall` (or set `[sandbox] wall` in `config.toml` or `[host.sandbox] wall` in `hosts.toml`):

<!-- test: help (needs a reachable host) -->
```sh
simstim new my-vps --wall process --dir /path/to/repo
```

How `new` picks the wall level:

- **Pinned host floor:** `host add` and `host create` pin `wall_floor` in `hosts.toml`. `new` runs at that floor by default.
- **Unpinned hosts:** a host saved without `wall_floor` probes on start, uses the strongest level it finds without pinning, and prints a notice pointing to `host add` or `host set`.
- **Config layers:** a `sandbox.wall` setting stronger than the host's floor raises the session's level; a setting weaker than the host's floor is raised back to the floor with a notice.
- **Downgrading below the floor (or to `none`):** running below the host's floor — or running at `none`, even on a host whose floor is `none` (such as a Docker test container) — is refused with exit code `5` (`wall_floor`) unless you pass `--downgrade`:

<!-- test: help (needs a reachable host) -->
```sh
simstim new my-vps --wall none --downgrade
```

- **Shared hosts:** on a `shared` host (such as an E2B sandbox), `new` never picks `process` without `--downgrade`; the minimum automatic level is `gvisor`.
- **Recording downgrades:** every downgraded start records `downgraded_from` in the session manifest, marks the row with `!` in `ls`, `show` and the launcher, and fires a `wall_downgraded` alert.
- **Agent install and login:** `agents install` and `agents login` modify host binaries and login files outside any sandbox, so they continue to run at `none` without requiring `--downgrade`. Their sessions still record the downgrade, show `!` and fire `wall_downgraded`.
- **Launcher:** the new-session form includes a **Wall** row (`auto (<floor>)`, `process`, `gvisor`, `microvm` and `none…`). Picking a level below the floor, or starting a session on a `none`-only host, opens a red confirmation sheet listing what the weaker level loses and requiring you to type the level's name before Enter starts the session.

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

Limits are applied through systemd unit properties (`MemoryMax=` and `CPUQuota=`). On a microvm sandbox, `--memory` sets the guest's RAM and the systemd unit adds a host reserve (`128 MiB + guest RAM / 256`) for Cloud Hypervisor and its kernel page tables. A new size takes effect at the next start or wake; passing `--cpus` or `--memory` to `resume` or `attach` on a running session changes nothing and prints a notice on stderr. On process walls, `[sandbox] disk_max` adds a soft folder cap checked every minute; on microvm sandboxes, `[sandbox] disk_max` is a hard cap on the sparse ext4 session disk (`disk.img`), so filling `/work` gives `ENOSPC` and host disk usage (`du`) never passes `disk_max`. On every walled session, when host free space drops below 5%, the supervisor stops the sandbox with `disk_full`.

## Microvm lifecycle

A microvm sandbox (`--wall microvm`) uses Cloud Hypervisor with a sparse, unencrypted ext4 session disk (`disk.img`) and follows the same `stop`, `attach`, `resume` and `rm` workflow as other sessions:

- **Stop uses no memory:** `simstim stop <host>/<session>` asks guest init to shut down, triggers an ACPI power-button event through `ch-remote`, and stops the session's systemd user unit. Once stopped, the unit is inactive and no VMM, supervisor or egress process runs for the session. Stop keeps no screen buffer and writes no memory snapshot.
- **Wake on attach or resume:** `simstim attach` or `simstim resume` cold-boots a fresh microvm against the preserved `disk.img`, so `/work` and agent conversation state stay intact while `/home/agent` starts clean.
- **Lazy restore after host reboot:** at host boot, explicitly stopped sessions stay stopped, and idle microvm sandboxes transition to `stopped` rather than cold-booting until you attach or run `host restore <host> --all`.
- **Idle stop (`[sandbox] idle_stop`):** off by default (`"never"` or empty). Set `idle_stop` under `[sandbox]` in `config.toml` (or per host under `[host.sandbox]` in `hosts.toml`) to a duration of one minute or more, such as `"30m"` or `"2h"`, to stop idle sandboxes automatically when no client is attached and no agent is working:

```toml
[sandbox]
idle_stop = "2h"
disk_max = "20G"
```

- **Destroy:** `simstim rm <host>/<session>` stops the unit and deletes the session folder, including `disk.img`, leaving no unit, process or socket behind.

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

## Use the microvm wall (`--wall microvm`)

The microvm wall boots each session in its own Cloud Hypervisor microVM on KVM with a pinned guest kernel and a `dm-verity`-verified tools image.

1. On a Linux host with `/dev/kvm`, add your user to the `kvm` group (once, as root):

```sh
sudo usermod -aG kvm "$USER"
```

Log out and start a fresh SSH login session (and restart your user systemd manager, `sudo systemctl restart "user@$(id -u)"`, if systemd is running) so the new group takes effect.

2. Download and verify the pinned Cloud Hypervisor VMM, `ch-remote` and Kata guest kernel, and build the verified tools image on the host:

```sh
simstim-host wall install microvm
```

(`chv` works as an alias for `microvm`.) This downloads the pinned artefacts from `guest/supply.lock`, checks every SHA-256 digest before installing into `~/.local/share/simstim/artefacts/runtime/chv-v55.0-k6.18.62/`, and builds the `dm-verity`-verified tools image from your host helper (using `mkfs.erofs` when installed, or an automatic ext4 fallback otherwise).

3. Confirm from your laptop that the host reaches the `microvm` level:

<!-- test: help (needs a reachable host) -->
```sh
simstim host doctor my-vps
```

Under `Sandboxes on my-vps:`, expect `Level now: microvm.`.

4. Pull the default root image (see [manage session images](images.md)), then start a session with `--wall microvm`:

<!-- test: help (needs a reachable host) -->
```sh
simstim image pull default my-vps
simstim new my-vps --wall microvm --dir /path/to/repo
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
