# Add a machine from its own keyboard

Use an invite when you can type on a Linux machine before the laptop can log in. For a Mac, follow [A Mac as a host](a-mac-as-a-host.md). The laptop still starts every connection. No service calls the laptop, and no new port is opened.

On the laptop, make an invite:

```sh
simstim host invite workbox
```

It makes an Ed25519 key for this host and keeps the invite for one hour. Its private filenames use an opaque suffix, so names such as `Box` and `box` keep separate keys even on a disk that ignores case. The exact name stays in the invite record. Use `--ttl 3h` for more time. Use `--key ~/.ssh/id_ed25519.pub` to offer your own public key instead. Load its matching private key into your SSH agent. The generated public identity selects only that key. Labels passed with `--label key=value` stay on the laptop.

Copy the code through a channel you trust. Check characters catch typing mistakes. They do not prove who sent a code. Anyone who sees an invite can make a reply, so compare the host key fingerprints on both screens.

On the machine, install the CLI, then run:

```sh
simstim host join
```

Paste the invite at its prompt. The command lists its changes and their undo before asking for consent. It needs sudo. It creates a dedicated account with a private group and a locked password, unless that account already exists. `--account worker` selects another non-root account. `--address` can name an address that the laptop can reach. Repeat it for more than one address. `--port` selects a non-standard SSH port.

The key permits a terminal and local forwards, including Unix sockets. The account's sshd policy refuses remote forwards, agent forwarding and X11 forwarding. The join checks the policy with `sshd -T`, tests the configuration and reloads the active SSH service. A persistent SSH service is reloaded even when a socket starts it. A service started separately for each connection needs no reload. If the main sshd configuration does not include its drop-in folder, add the line that the error shows before retrying.

The join enables linger, then probes sandbox access as the account with `runuser`. Root's own access to `/dev/kvm` cannot hide a missing permission for that account. Only the fixed root steps selected by that probe run.

The machine prints a reply code and its `SHA256:` host key fingerprints. On the laptop:

```sh
simstim host add workbox --accept -
```

Paste the reply. Compare **every** fingerprint with the machine's screen. A no leaves the invite open and pins nothing. Choose the tier yourself: the reply's suggestion has no default. The normal host preparation then installs the matching helper, prepares the runtime and tests its sandbox wall.

For a pipe, give every answer explicitly:

```sh
# The code goes through standard input, never as an argument.
simstim host join --yes < invite.txt
simstim host add workbox --accept - \
  --host-key SHA256:<fingerprint> --tier own --yes < reply.txt
```

Repeat `--host-key` for every key in the reply, including any SSH hops. Without those flags and `--tier`, a piped acceptance stops with exit code 4. Delete copied code files when finished.

If the machine is reached through an existing bastion, select its existing config explicitly:

```sh
simstim host add workbox --accept - --ssh-config ./hosts.conf
```

That config must resolve `workbox` to an ordered `ProxyJump` chain. The existing user-owned keys and trust settings authenticate the bastions. Every bastion must already have a trusted host key. The invite key authenticates only the reply's hops and final account. No SSH agent is forwarded.

The client checks the complete `Include` closure before resolving it. It resolves frozen copies and stores separate projections for the bastions and the invited account. Later connections require the original source files, identities and public-key pins to remain unchanged. The generated configs do not read later user or system includes. Run a fresh invite after intentionally changing the route.

This path currently accepts ordinary `Host`, static `Include` and ordered `ProxyJump` entries. It refuses `Match`, executable `ProxyCommand`, `KnownHostsCommand` and `LocalCommand` before resolving anything. Write nested jump settings as one ordered chain. Ordinary host addition keeps its existing SSH config behaviour.

A reply expires with its invite. A malformed field, changed check character or unknown code version stops before any host registration or pin. The generated SSH files have an internal `joined_ssh` ownership reference in `hosts.toml`. Keep that reference and those files unchanged. `host rm` can then remove only this client's original generated files; an edited or replaced file stops that cleanup.

An accepted invite is consumed only by the original successful host registration. A later sandbox preparation failure keeps that registered host and its SSH configuration.

## Leave the machine

On the machine, run:

```sh
simstim host leave
```

Use the same `--account` as the join. The command reads the root-owned ledger in `/var/lib/simstim/join/` and undoes its entries, newest first. It refuses records that another user can change, including writable or linked ancestors. The recorded `leave.sh` in that folder is also available to root.

Undo preserves an existing account. It stops all processes of the original account that this join created, then removes that account and its home. It checks the original UID, private group, account marker and home inode before doing so. A file or account that has changed stops the undo and keeps the ledger for inspection. The command does not overwrite that later change. Finish the undo before making a fresh invite for the same account.

A failed or interrupted join retains its ledger. Run the same invite again to continue, while it is still valid, or use `host leave` to undo the recorded work. Keep the leave script until the undo has finished.
