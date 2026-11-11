---
status: accepted
date: 2026-11-11
---

# 0034 The client puts the options that keep a host safe on the `ssh` command line

This record adds a rule to [0003](0003-system-openssh.md), and changes none of it. It starts with forwarding: no host gets the laptop's SSH agent, its X11 display or its Kerberos credentials, and only `forward` brings up port forwards. Task H2 of [the isolation plan](../isolation-plan.md) builds it first, on its own ([question 40](../isolation-spec.md#15-open-questions)).

## Context

The client runs the system `ssh` with a generated config ([0003](0003-system-openssh.md)). That config includes the host's own SSH config, then `~/.ssh/config`, then the system's config, and puts the client's defaults last (`writeWrapper` in `internal/client/ssh.go`). For most options, ssh keeps the first value that it finds. So any option in the user's config wins over the client's defaults.

Many people set `ForwardAgent yes` for all hosts, so that `git push` on a server uses the keys on their laptop. With the client, that line reaches every host and every session. ssh_config(5) says that a user who can bypass file permissions on the host can use the forwarded agent while the connection is up. Any process of the host user can do the same: an agent under prompt injection, or a package's install script. It can then log in to other machines as the user ([connectivity 7.2](../../research/connectivity-and-providers.md#72-ssh-agent-forwarding)). A host that runs coding agents is the place where untrusted code runs, so this is a real hole ([isolation spec, 3.2](../isolation-spec.md#32-trust-boundaries)).

More settings can reach a host in the same way:

- `ForwardX11 yes` gives the host a way into the laptop's X11 display.
- `GSSAPIDelegateCredentials yes` gives the host the laptop's Kerberos ticket, which reaches other Kerberos services as the user.
- `PermitLocalCommand yes` with a `LocalCommand` runs a program on the laptop at each connection. The client connects often and in the background, so the program runs at times that the user does not expect.
- `LocalForward`, `RemoteForward`, `DynamicForward` and `Tunnel` open ports or a network device with each connection. A remote forward or a tunnel lets the host reach the laptop.

Three facts of OpenSSH shape the fix:

- **A `ProxyJump` hop is a second `ssh`.** ssh starts it with the same `-F` file, but with none of the `-o` options of the command line. Options on the command line do not reach the hop.
- **List options do not keep the first value.** `SendEnv`, `LocalForward`, `RemoteForward`, `DynamicForward` and `IdentityFile` add to a list in the order that ssh reads them. An option on the command line cannot remove what a config file adds later. Such an option needs an option on the command line that clears the list, such as `ClearAllForwardings`, or a line at the end of the generated config, such as `SendEnv -*`.
- **A shared master keeps what it forwarded.** A master that started with agent forwarding gives the forwarded agent to every session that it serves. A master that an older client started may still run, for up to 10 minutes, with the agent that the user's config forwarded.

## Options

1. Keep the client's defaults at the end of the generated config, as today. The user's config wins.
2. Put the options at the top of the generated config only, before its `Include` lines.
3. Put the options on each `ssh` command line only.
4. Put the options on each `ssh` command line, and also at the top of the generated config.
5. Leave forwarding to the user's config, and show a warning when it is on, as the research first said.

## Decision

Option 4.

- **Every `ssh` command** that the client runs has `-o ForwardAgent=no -o ForwardX11=no -o PermitLocalCommand=no -o GSSAPIDelegateCredentials=no`. This includes the bridge, `Run`, `forward`, the launcher's shell, `pull`'s fetch, the debug bundle's commands, the guided setup's first check and the shared connection's control commands.
- **Every generated config starts with the same four lines, and `ClearAllForwardings yes`**, before its first `Include`. They reach a `ProxyJump` hop, which reads the generated config but not the command line, so the hop also opens none of the forwards or the tunnel that a config names. `forward` keeps its ports, because its own command line says `ClearAllForwardings=no`, and ssh reads the command line first. The guided setup's first check, which runs before the host is saved, gets a generated config of its own, which includes the user's config after these lines.
- **Every command but `forward`** also has `-o ClearAllForwardings=yes`, which clears the port forwards and the tunnel that a config names. The bridge and `Run` have it, and so do the launcher's shell, `pull`'s fetch, the debug bundle's commands and the guided setup's first check.
- **`forward` has a connection of its own** (`-o ControlMaster=no -o ControlPath=none`). Its forwards, and those of the user's config, end when it ends, and never stay in the master that the bridge uses. It has `-o ClearAllForwardings=no`, so that a `ClearAllForwardings yes` in the user's config cannot clear its own `-L` options. Before it starts, it reads `ssh -G` with its own options. It refuses to start when the config adds a `RemoteForward` or a `Tunnel` for the host, or sets `ForkAfterAuthentication yes`, which would leave the forward running in the background after `forward` ends. It names the line. OpenSSH 8.7 added `ForkAfterAuthentication`. OpenSSH 8.0 to 8.6, which the client supports, reject the option on the command line and in a config file. So the generated config turns it off only where the local ssh knows the option, and `forward`'s refusal is the guard everywhere. It also refuses when it cannot read every line that `ssh -G` prints. Every `ssh -G` that the client runs stops waiting for its output 2 s after it ends or is stopped, so a child of ssh that holds the output open cannot hold the client.
- **The shared connections have a new socket name.** The client never uses a master that an older client started, so no session gets an agent that such a master forwarded. When the client cannot make a private folder for the sockets, each command has a connection of its own, so the user's own `ControlPath` never names a master. The guided setup's first check and the debug bundle's log have connections of their own, so they never use a master that the user's config names.
- **Later options follow the same rule, where it works.** An option that keeps a host safe, and whose first value wins, goes on the command line and at the top of the generated config. [0035](../isolation-plan.md#decision-records) pins host keys this way. A list option needs a clearing option or a line at the end of the generated config. The tier task that limits `SendEnv` adds `SendEnv -*` at the end ([isolation spec, 14.8.5](../isolation-spec.md#1485-where-the-client-checks)).
- **No option turns forwarding back on.** A session that needs the laptop's agent will get it only through an explicit grant on an `own` host (`new --ssh-agent`, [isolation spec, 14.8.2](../isolation-spec.md#1482-what-each-tier-may-get)), which a later task builds.

Option 1 is the hole. Option 2 misses the `ssh` calls that do not read the generated config. Option 3 misses the `ProxyJump` hop. Option 4 closes both, and a test of the arguments still reads the options. Option 5 leaves the hole open for anyone who does not read the warning, and a host that the user trusts today can be compromised tomorrow.

## Consequences

- Good, because a `ForwardAgent yes` in any config file no longer reaches a host, or the hop on the way to it.
- Good, because the options are on the command line of every `ssh` that the client starts, where a unit test reads them and `ps` shows them.
- Good, because the same rule carries the options of later tasks, such as pinned host keys, and the record says where it does not work.
- Bad, because a user who pushed from a host with the laptop's agent can no longer do so. They need a key or a token for that host ([connectivity 7.3](../../research/connectivity-and-providers.md#73-per-host-credentials-for-agents)).
- Bad, because X11 programs in a session can no longer draw on the laptop, and the launcher's shell on a host forwards no agent.
- Bad, because the port forwards in a user's config no longer come up with the bridge or the other commands. They still come up with `forward`, and end with it.
- Bad, because `forward` refuses to start for a host whose config has a `RemoteForward`, a `Tunnel` or `ForkAfterAuthentication yes`, until the user removes that line.
- Bad, because `forward` logs in again on its own connection. A key that asks for a touch asks once more.
- Bad, because the first connection after an upgrade starts a new master, and an old master stays until its 10 minutes end.
- Bad, because an ssh built without GSSAPI does not know `GSSAPIDelegateCredentials`. It prints "Unsupported option" and goes on (OpenSSH's `readconf.c`).
