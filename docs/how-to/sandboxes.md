# Sandboxes

A sandbox runs an agent session inside a wall on a Linux host, so the agent sees only the files and network destinations that you allow.

## Wall levels

Each host supports one or more wall levels, from weakest to strongest:

| Level | What builds it | Requirements |
| --- | --- | --- |
| `none` | No wall | Any Linux host that runs sessions today |
| `process` | [bubblewrap](https://github.com/containers/bubblewrap) (`bwrap`), Landlock and seccomp | Linux 5.13 or later, `bwrap`, unprivileged user namespaces, Landlock ABI 1 or more, seccomp |
| `gvisor` | Upstream `runsc` (later phase) | Linux 5.6 or later, `runsc`, unprivileged user namespaces |
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
