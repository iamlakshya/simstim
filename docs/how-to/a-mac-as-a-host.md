# Use a Mac as a host

A Mac runs sessions inside a local Linux VM managed by [Lima](https://lima-vm.io). The normal Linux host helper runs inside that VM, so sessions get Linux isolation walls, systemd memory caps and restore after a restart. There is no hidden macOS account and no hand-written launchd plist.

## What you need

- An Apple silicon Mac running macOS 15 or later.
- An M3 chip or later for nested virtualisation inside the VM. M1 and M2 Macs turn nested virtualisation off automatically and run process walls instead.

## Add this Mac

<!-- test: help (it downloads Lima and makes a VM) -->
```sh
simstim host add --local
```

You can choose another host name, sizing or autostart mode:

<!-- test: help (it downloads Lima and makes a VM) -->
```sh
simstim host add my-mac --local --cpus 4 --memory 8G --autostart boot
```

What `host add --local` does:

1. Checks that this Mac has Apple silicon and macOS 15 or later.
2. Downloads the pinned Lima 2.3.0 release archive, verifies its SHA-256 digest and extracts it under `~/.local/share/simstim/lima/2.3.0/`. Homebrew updates cannot change the pinned binary.
3. Creates a plain `vz` Ubuntu LTS VM (`--plain`, no home-folder mounts and no automatic port forwards) with `networkOpts.hostLoopback: false` and fewer than half of the Mac's CPUs and memory.
4. Registers automatic startup through `limactl autostart enable`:
   - `boot` (the default) uses `limactl autostart enable --condition=boot --keep-alive`, which installs Lima's LaunchDaemon with `sudo` so the VM starts at boot before login.
   - `login` uses `limactl autostart enable --condition=login --keep-alive`, which installs Lima's LaunchAgent with no `sudo` so the VM starts when you log in. When input is off without passwordless `sudo`, an implicit default falls back to `login` and prints the `boot` command.
   - `off` skips autostart.
5. Installs the host helper inside the VM, turns on session persistence and restore after restart, and keeps idle power-off disabled (`--idle-stop never`) so the Mac host stays available.

simstim reaches the VM through the system `ssh` and Lima's own generated `ssh.config`. Your `~/.ssh/config` is never changed.

## Install agents and start a session

<!-- test: skip (requires a created Mac host) -->
```sh
simstim agents install claude --host mac
simstim new mac --agent claude
```

Press `Ctrl+\` to detach and leave the session running inside the VM, then attach again with `simstim attach`.

## Check health

<!-- test: skip (requires a created Mac host) -->
```sh
simstim host doctor mac
```

`host doctor` checks the VM's Linux setup, confirms that `laptop loopback` stays closed (`networkOpts.hostLoopback: false`), and verifies the `autostart` registration.

## Keep the Mac awake

- Keep the Mac on power with the lid open and good air flow. With the lid shut and no external display, macOS sleeps even when sleep on power is turned off.
- To keep the Mac awake on power, run `sudo pmset -c sleep 0` yourself; simstim never changes your power settings behind your back.
- After a reboot with FileVault enabled, log in once at the Mac to unlock the disk. launchd then starts the VM and the helper restores sessions that were running.

## Reach another Mac over SSH

To use a second Mac from your laptop, run `simstim host add --local` on that Mac, then reach its VM over SSH from your laptop with a `ProxyJump` through the Mac's sshd (or Tailscale inside the VM) and add it with `simstim host add`.

## Remove the Mac host

<!-- test: skip (it deletes a VM) -->
```sh
simstim host destroy mac
```

`host destroy` unregisters `limactl autostart`, deletes the Lima VM and its disk, and forgets the host from `hosts.toml`.
