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

## A repo's own SSH config

Some teams keep an SSH config in a repository, with jump hosts and names of their own. Name it with `--ssh-config`:

<!-- test: help (needs that config) -->
```sh
simstim host add dev-vm dev-vm --ssh-config ~/src/infra/ssh/config --yes
```

## What host add does

1. Checks that the host answers, and finds its system and CPU.
2. Asks before it installs the helper, unless you pass `--yes`, then uploads it into `~/.local/share/simstim/bin/` and checks its SHA-256.
3. Turns on systemd's linger for your user, so sessions keep running when you log out, and installs the unit that restores sessions after a reboot.
4. Saves the host in `hosts.toml` ([configuration](../reference/config.md)).
5. Lists the agents it finds. In a terminal, when a first-class or supported agent is missing, it shows the agent checklist: type the numbers of the agents to install ([install agents](install-agents.md)). `--yes` skips the checklist.

## Check a host

```sh
simstim host test my-server
simstim host doctor my-server
```

`host doctor` lists what the host has and what is missing: systemd, linger, the agents and the helper. `host doctor --fix` offers to fix what it can ([troubleshoot](troubleshoot.md#let-the-doctor-fix-it)).
