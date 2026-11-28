# Add a host

Any Linux host you can reach with `ssh` can run sessions: amd64 or arm64, with `sh`, `uname` and `sha256sum`. systemd is used when it is there, so sessions survive a logout and come back after a reboot.

## With the guided setup

`simstim setup` asks where agents will run, connects the host, then offers to install agents and log in to them ([getting started](../tutorials/getting-started.md#2-run-the-guided-setup)). To give it the host at once:

<!-- test: help (it changes the host) -->
```sh
simstim setup --host my-server
```

`--host` takes a saved host, a Host from your SSH config, or `user@host`. The rest of this page adds a host by hand.

## A host from your SSH config

If `ssh my-server` works, then:

```sh
simstim host add my-server --yes
```

The client uses the system `ssh`, so everything in `~/.ssh/config` applies: `ProxyJump`, keys, agents, certificates and Tailscale. It never edits your SSH config. Forwarding is the exception: the client never forwards your SSH agent or your X11 display to a host, even when your config turns it on ([security](../explanation/security.md)).

## A host by address

<!-- test: help (needs a reachable address) -->
```sh
simstim host add box admin@203.0.113.7 --yes
```

To check the host's SSH key up front, read its fingerprint on the machine with `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` and pass `--host-key`:

<!-- test: help (needs a reachable address and fingerprint) -->
```sh
simstim host add box admin@203.0.113.7 --host-key SHA256:3f9a…c1 --yes
```

## Pinned host keys and rekey

Every host added over SSH gets its host key pinned in `~/.config/simstim/ssh/known_hosts-<host>`. The client passes that pin on every `ssh` command line, so a `StrictHostKeyChecking no` or `UserKnownHostsFile /dev/null` in your SSH config cannot turn it off.

- If your OpenSSH settings already trust the host, `host add` pins its key without asking.
- If the host is new, `host add` lets `ssh` show the fingerprint and ask at a terminal. Pass `--host-key SHA256:…` to verify it without a prompt, or `--yes` to pin on first use.
- If a host's key later changes, every command stops with exit code 5 and shows both fingerprints. `host add` never replaces an existing pin.

If you reinstalled the host and know why its key changed, replace the pin with `host rekey`:

<!-- test: help (needs a saved host) -->
```sh
simstim host rekey my-server
```

`host rekey` shows the old and new fingerprints and asks you to type the host's name. In a script, pass `--host-key` and `--confirm`:

<!-- test: help (needs a saved host and fingerprint) -->
```sh
simstim host rekey my-server --host-key SHA256:77b0…02 --confirm my-server
```

## A repo's own SSH config

Some teams keep an SSH config in a repository, with jump hosts and names of their own. Name it with `--ssh-config`:

<!-- test: help (needs that config) -->
```sh
simstim host add dev-vm dev-vm --ssh-config ~/src/infra/ssh/config --yes
```

## This Mac as a host

On an Apple silicon Mac (macOS 15 or later), `--local` sets up this Mac as a host inside a pinned Lima VM under your own account, with automatic startup through `limactl autostart` ([use a Mac as a host](a-mac-as-a-host.md)):

<!-- test: help (it downloads Lima and makes a VM) -->
```sh
simstim host add --local
```

## What host add does

1. Pins the host's SSH key in `~/.config/simstim/ssh/known_hosts-<host>`, from your OpenSSH settings, `--host-key` or a first-use confirmation.
2. Checks that the host answers, and finds its system and CPU.
3. Asks before it installs the helper, unless you pass `--yes`, then uploads it into `~/.local/share/simstim/bin/` and checks its SHA-256. In a script, or with `--no-input`, it cannot ask, so it fails with exit code 4 unless you pass `--yes` ([scripts](scripting.md)).
4. Turns on systemd's linger for your user, so sessions keep running when you log out, and installs the unit that restores sessions after a reboot.
5. Runs the sandbox probe and prints a **Sandboxes** block showing the kernel, systemd, cgroup v2, `/dev/kvm`, `bubblewrap`, Landlock, seccomp and user namespaces. It pins the host's wall floor (`wall_floor` in `hosts.toml`) to the strongest level that probes OK, unless you pass `--floor` with a lower level ([sandboxes](sandboxes.md)).
6. Saves the host in `hosts.toml` ([configuration](../reference/config.md)).
7. Lists the agents it finds. At a terminal, when a first-class or supported agent is missing, it shows the agent checklist: type the numbers of the agents to install ([install agents](install-agents.md)). `--yes` skips the checklist.

## Check a host

```sh
simstim host test my-server
simstim host show my-server
simstim host doctor my-server
```

`host show` prints the host's saved settings, sandbox floor and each wall driver's probe. `host doctor` lists what the host has and what is missing: systemd, linger, sandboxes, the agents and the helper. `host doctor --fix` offers to fix what it can ([troubleshoot](troubleshoot.md#let-the-doctor-fix-it)).
