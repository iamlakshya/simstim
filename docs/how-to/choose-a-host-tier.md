# Choose a host's tier

A host's tier records who else can read its memory and files. Choose it before you send work to the host.

| Tier | Choose it when |
| --- | --- |
| `own` | Only you have root on the machine. |
| `throwaway` | It is your own machine, made for one task, and you can lose it. Use only short-lived secrets there. |
| `shared` | Someone else has root, such as a teammate, your company or a hosted service. |

A teammate's machine is `shared`, even when you have an account of your own. Its owner can read every secret you send there. A sandbox cannot hide its memory or files from root on its host.

This change records tiers and checks host identity. The grant gate described in [the isolation spec](../contributing/isolation-spec.md#148-tiers-and-grants) is a separate planned change. Until it ships, check each secret and repository before you send it to a host.

## Add a host with its tier

At a terminal, `host add` asks who else has root. The question has no default answer. You can name the tier yourself:

<!-- test: help (needs a host to add) -->
```sh
simstim host add my-shared --tier shared
```

With `--yes`, you must also pass `--tier`. A missing tier stops the command with exit code 4:

<!-- test: help (needs a host to add) -->
```sh
simstim host add my-shared --tier shared --yes
```

Hosts that `host create lima` or `host create docker` makes get `own`. Hosts that `host create e2b` makes get `shared`.

The first run of this client keeps hosts that you already added as `own`. It records them in `tiers.toml`, beside `hosts.toml`. After that migration, a host without an entry counts as `shared`.

If an older client adds a host after that migration, the next use at a terminal asks its tier. The client checks a live connection with the pinned key first. The launcher shows the same question before foreground work. Its background connections ask nothing. There is no default choice. Pressing Enter alone in the launcher changes nothing.

Browsing a host in the launcher leaves a stopped machine stopped. An explicit attach keeps its usual wake behaviour.

The launcher stops its question reader before it hands over the terminal. When you detach, a fresh reader handles the next tier question. An interrupted question leaves the host at `shared` until you answer it.

Without input, the host stays `shared` until you answer. `--yes` does not choose a tier. You can also record the answer with `host set`, as below. A cancelled question or a failed save leaves the host at `shared`.

## Check or change a tier

<!-- test: help (needs a saved host) -->
```sh
simstim host ls
simstim host show my-shared
simstim host set my-shared tier=shared
```

Lowering a tier takes effect at once. Raising it connects to check the pinned host key, then asks you to type the host's name. In a script, supply that name with `--confirm`:

<!-- test: help (needs a verified host to raise) -->
```sh
simstim host set my-shared tier=own --confirm my-shared
```

Raise a tier only after you check who has root on the machine.

The helper keeps a host ID in its state folder. When that ID changes, the client warns and lowers the tier to `shared`. A copied state folder can keep the same ID. Treat the ID as a hint, and check a reinstall or an unexpected change with the owner.

[`host rekey`](add-a-host.md#the-host-key) also lowers the tier to `shared`. After you verify the machine and its new key, use `host set` to raise it if needed.

If a tier save fails during a rekey, the client keeps the old pin. If the tier saves but the new pin does not, the client refuses connections until you fix the save error and retry the rekey.

## Use a separate SSH key for each shared host

Give each shared host a key of its own. If one is exposed, you can revoke it without changing the keys for your other hosts.

1. Choose a new file name in `~/.ssh`. Keep any existing key. Create the new key and give it a passphrase:

   ```sh
   ssh-keygen -t ed25519 -f ~/.ssh/my-shared
   ```

2. Give the host's owner only `~/.ssh/my-shared.pub`. Ask them to add that public key to your account's `authorized_keys`. Keep `~/.ssh/my-shared` on your laptop.
3. Add a block to `~/.ssh/config`. Replace the address and account below with the host's own values:

   ```sshconfig
   Host my-shared
       HostName <host-address>
       User <account>
       IdentityFile ~/.ssh/my-shared
       IdentitiesOnly yes
       ForwardAgent no
   ```

4. Check the host key through a trusted route, then test `ssh my-shared`. Add the host with `--tier shared` as above.

The client keeps SSH agent forwarding off for its normal connections. Keep it off in your own SSH config too.

## Ask before terminal clipboard writes

For `shared` and `throwaway` hosts, set your terminal's clipboard writes to **ask**. The setting may be called OSC 52 or clipboard access. Check your terminal's settings for its name.

While attached, the host's terminal output reaches your terminal byte for byte. A tier does not filter that output. Asking before a clipboard write lets you refuse text that the host tries to copy onto your laptop.

See [image paste](image-paste.md#files-that-stay-on-your-laptop) for the files that the client refuses to upload from the clipboard.
