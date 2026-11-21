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
simstim host add my-server --tier own --yes
```

The client uses the system `ssh`, so everything in `~/.ssh/config` applies: `ProxyJump`, keys, agents, certificates and Tailscale. It never edits your SSH config. Forwarding is the exception: the client never forwards your SSH agent or your X11 display to a host, even when your config turns it on ([security](../explanation/security.md)).

Use `own` only when you alone have root. Use `shared` for a teammate's or company's machine. Without `--tier`, a terminal asks which tier to use. `--yes` needs an explicit tier ([choose a tier](choose-a-host-tier.md)).

## A host by address

<!-- test: help (needs a reachable address) -->
```sh
simstim host add box admin@203.0.113.7 --tier own --yes
```

## A repo's own SSH config

Some teams keep an SSH config in a repository, with jump hosts and names of their own. Name it with `--ssh-config`:

<!-- test: help (needs that config) -->
```sh
simstim host add dev-vm dev-vm --ssh-config ~/src/infra/ssh/config --tier shared --yes
```

## What host add does

1. Pins the host's key, checks that the host answers, and finds its system and CPU ([the host key](#the-host-key)). It then uses `--tier`, or asks for the tier at a terminal.
2. Asks before it installs the helper, unless you pass `--yes`, then uploads it into `~/.local/share/simstim/bin/` and checks its SHA-256. In a script, or with `--no-input`, it cannot ask, so it fails with exit code 4 unless you pass `--yes` ([scripts](scripting.md)).
3. Turns on systemd's linger for your user, so sessions keep running when you log out, and installs the unit that restores sessions after a reboot.
4. Saves the host in `hosts.toml` ([configuration](../reference/config.md)).
5. Prepares the host's sandbox runtime and tests its strongest runnable sandbox wall, unless you pass `--no-sandbox` ([prepare sandboxes](#prepare-sandboxes)).
6. Lists the agents it finds. At a terminal, when a first-class or supported agent is missing, it shows the agent checklist: type the numbers of the agents to install ([install agents](install-agents.md)). `--yes` skips the checklist.

## Prepare sandboxes

By default, `host add` prepares sandboxes right after saving the host. You can also run preparation at any time on a saved host:

<!-- test: help (needs a saved host) -->
```sh
simstim host prepare my-server
simstim host prepare my-server --root-step print
simstim host prepare my-server --images laptop
```

`host prepare` runs four separate steps against the saved host:

1. Probes the host's kernel, cgroups, `/dev/kvm` and registered sandbox drivers.
2. Installs any missing runtime or default image files pinned by this client (`--images` chooses `auto`, `host` or `laptop`). Shared hosts ask before an upgrade.
3. Handles optional root setup (`--root-step` takes `run`, `print` or `skip`; `--delegate-io` and `--separate-accounts` add those optional steps). Running `sudo` always asks for its own consent and never reuses an earlier answer. With `--yes`, optional root steps print to a file unless `--root-step` names another mode and passwordless `sudo` is ready.
4. Runs a short fixed sandbox test boot and verifies clean-up. Only a complete test boot sets or raises the host's `wall_floor` and refreshes its cached placement facts; a stronger existing floor is kept. If no wall can run yet, or the test boot fails, the saved host keeps its previous floor and reports what is still needed.

Pass `--no-sandbox` to `host add` to save the host without running sandbox preparation. `--no-sandbox` cannot be combined with `--images`, `--root-step`, `--delegate-io` or `--separate-accounts`.

## The host key

`host add` pins the host's key. From then on, the client connects to that host only when it shows that key, so it never connects to another machine at the same address. `host add` says where the pin came from:

```text
Connecting to my-server (my-server)…
  ✓ connected in 1.9s: linux/amd64
  ✓ host key SHA256:3f9aJm0QxV1oW4e8tYkz2bH7cN5dR6sL1uP9gA0iE2c pinned, from your known_hosts
```

- **From your known_hosts.** `host add` asks `ssh` whether your own SSH settings trust the key that the host shows, as `ssh my-server` would decide: your known_hosts files, hashed or not, a certificate authority, a `KnownHostsCommand` or DNS. When they do, it pins that key. A key that your settings revoke is never pinned. When your known_hosts holds another key for the host, `host add` stops with exit code 5. Check the key on the host, and pass it with `--host-key`.
- **Matched --host-key.** Pass the key's fingerprint, as `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` prints it on the host. `host add` pins that key only, and fails when the host shows another one.
- **On first use.** When your settings do not know the key, `host add` reads the key that the host shows. At a terminal, ssh shows the key's fingerprint and asks. With `--yes`, `host add` pins the key and says how to check it on the host. In a script without `--yes` or `--host-key`, it fails with exit code 4.

The connections that ask and read a key offer no key and no password. `host add` writes the pin only once it saves the host, so a `host add` that fails leaves no pin. It never replaces the pin of a host that you saved before, whatever its target: only `host rekey` does.

The launcher's add-host wizard pins the key in the same way, before it connects. When your settings do not know the key, it shows the key's fingerprint and the command that prints it on the host, and pins the key only when you press Enter.

<!-- test: help (needs a reachable address) -->
```sh
simstim host add box admin@203.0.113.7 --host-key SHA256:3f9aJm0QxV1oW4e8tYkz2bH7cN5dR6sL1uP9gA0iE2c
```

The pin is the file `~/.config/simstim/ssh/known_hosts-<key>`, where `<key>` is the host's name in lower case, a dash and 32 hex characters, such as `known_hosts-box-26f8567f2569182294c3fa5b9f9cb227`. Its first line names the host, and the client uses no file that names another. Two hosts whose names differ only in case each have their own. A host's name cannot end with a dot. Every `ssh` command for the host checks the key against that file alone, with options on the command line that win over every config file. So `StrictHostKeyChecking no`, `UserKnownHostsFile /dev/null`, a known hosts file of the system, DNS records or a `KnownHostsCommand` in your SSH config cannot pass another key. A jump host still follows your own config: the pin covers the host at the end of the path. A jump host cannot choose the pin either: the client reads ssh's own verdict on the host at the end, never the jump host's words. `host create lima` pins the new VM's key on first use.

A host that you added with an older version gets its pin at its next connection, once, in the same way. When your settings do not know its key, the client pins it on first use only when your own config takes new keys without a question. Otherwise ssh asks at a terminal. In a script or the launcher, that connection fails with exit code 4: run any command for the host at a terminal once, or `simstim host rekey my-server --host-key SHA256:… --confirm my-server`. No connection to a saved host goes without a pin. A pin that an older version wrote as `known_hosts-<name>` moves to its new name at the host's next connection. `host rm` removes the pin with the host.

### A changed key

When the host shows another key, every command stops, and exits with code 5:

```text
Error: box's host key changed: SHA256:3f9aJm0QxV1oW4e8tYkz2bH7cN5dR6sL1uP9gA0iE2c was pinned, and the host shows SHA256:77b0Kd2nWq8YtE5rU1oP3iA6sF9gH4jL7zX0cV2bN8m.

Try: If you reinstalled box: simstim host rekey box. Otherwise, do not connect.
```

Someone may be between you and the host. If you know why the key changed, for example because you reinstalled the host, replace the pin. `host rekey` reads the key again, shows the pinned key and the new one, and asks you to type the host's name:

<!-- test: help (it changes the pin) -->
```sh
simstim host rekey box
```

In a script, `--confirm box` gives the name, and `--host-key SHA256:…` makes sure the new key is the one you expect. Without `--confirm`, a script fails with exit code 4 before it connects.

Replacing the pin also sets the host's tier to `shared`. Check who has root before you [raise its tier again](choose-a-host-tier.md#check-or-change-a-tier).

## Check a host

```sh
simstim host show my-server
simstim host test my-server
simstim host doctor my-server
```

`host show` describes the host, as the launcher's host details (`i`) do. It prints what this computer saved: the SSH target, the labels, the machine, and the names of the host's secrets. Then it prints what the host says about itself: its system, the helper, the agents with their versions, and the memory for sessions. It neither starts a stopped host nor installs a helper. A host that does not answer is not an error: `host show` prints the saved part, then why the host did not answer. `--json` prints the same for scripts.

Ctrl+C stops `host show` without printing a partial result. It also stops a removal while the client checks the machine or its sessions. No question or deletion follows that cancel, even with `--force`.

`host doctor` lists what the host has and what is missing: systemd, linger, the agents and the helper. `host doctor --fix` offers to fix what it can ([troubleshoot](troubleshoot.md#let-the-doctor-fix-it)).

## Update the helper

Each client installs the helper of its own version, next to the others, the first time it connects to a host. To install it without the other steps of `host add`:

<!-- test: help (it installs the helper) -->
```sh
simstim host upgrade my-server
```

`host upgrade` reads the host without starting the helper. When this client's helper is there, it says so and changes nothing. Otherwise it installs the helper, then connects. It asks nothing, because the command is your yes. It removes no helper, so the helper of a newer client stays. Running sessions keep their own helper, and new sessions use the new one. `--json` prints what it did: `unchanged` or `installed`.

## Remove a host

<!-- test: skip (it removes the host) -->
```sh
simstim host rm my-server
```

`host rm` forgets the host: it leaves `hosts.toml`, with its generated SSH config. Its sessions and the helper stay on the host and keep running. Run `host add` again to get them back.

It removes only generated configs that it still owns and that no remaining saved host references. A config that you replaced, or that another host includes, stays. Cancellation before cleanup keeps the pin and configs in place.

Once cleanup begins, it retires the pin first. If removing a generated config or saving the host list fails, the host stays saved and its stored key guard refuses the missing pin. Fix the cleanup or save error and run `host rm` again.

A host that `host create` made also has a machine. At a terminal, `host rm` says what the machine holds, and asks you to type the host's name to delete the machine too. With `--destroy --confirm <host>`, it deletes the machine without the question ([destroy a host](create-a-host.md#destroy-a-host)).

The launcher reads the saved host again after another command changes it. When its route or machine changed, the old sessions and details leave the screen. A failed or cancelled add closes its unused connection.

A removal and `doctor --fix` keep an SSH config that another saved host uses. This includes a file reached through a link or a different spelling on a file system that ignores letter case. A cancelled forget keeps its config.
