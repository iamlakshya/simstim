# Open a sandbox with SSH or an editor

Prepare the SSH configuration on your laptop:

```sh
simstim ssh-config
```

The command checks the system SSH parser before it writes anything. If that
parser accepts shell characters in a hostname, update OpenSSH and try again.
The check uses the parser's behaviour, so distributions with a backported fix
can pass too.

Setup writes `~/.ssh/simstim/config` and its pinned `known_hosts` file. It then
asks before adding their `Include` to the top of `~/.ssh/config`. Declining
keeps the generated files ready for `ssh -F ~/.ssh/simstim/config`.

Each laptop keeps one Ed25519 host key in its private client state. Setup does
not replace an existing key. The SSH server runs in the local client on
standard input and output. It opens no listening port and installs no SSH
server in the sandbox. The existing connection to your saved host proves your
identity; this local endpoint accepts only the user `agent`.

## Plain SSH, SCP and SFTP

An alias is `<session>.<host>.simstim`. Each part is lower-case letters, digits
or hyphens. Use an exact session name or its complete ID. An ambiguous name is
refused.

<!-- test: help (needs an existing sandbox) -->
```sh
ssh auth-fix.box.simstim
sftp auth-fix.box.simstim
scp local.txt auth-fix.box.simstim:/work/local.txt
scp auth-fix.box.simstim:/work/result.txt ./result.txt
```

A shell gets an ephemeral terminal inside the sandbox. It is not a session
row and does not restart after a stop. A command runs through the guest shell
and returns its exit status. File paths are served inside the sandbox as
`agent`.

SSH wakes a stopped sandbox through the normal resume workflow. If waking
needs a new terminal answer or a password-manager prompt, run
`simstim resume box/auth-fix` in a terminal first, then reconnect. SSH protocol
bytes are never read as answers to those prompts.

## Editor upload settings

VS Code and Cursor can install their remote server from the laptop. In their
settings, set:

```json
{
  "remote.SSH.localServerDownload": "always"
}
```

Then choose the SSH alias and open `/work`, or the project folder in it.
For Zed, enable `upload_binary_over_ssh` in the SSH connection settings and
choose the same alias. Uploading the server from the laptop needs no sandbox
network allowance. See the current [VS Code SSH guide](https://code.visualstudio.com/docs/remote/ssh)
and [Zed remote development guide](https://zed.dev/docs/remote-development)
for the settings of your editor version.

Remote editor extensions can run commands on this laptop through their
remote features. For a repository you do not trust, use plain SSH or SFTP, or
the editor's restricted mode. Setup prints this trust notice once.

## Optional server downloads in the sandbox

The laptop-upload settings above need no added network grants. If you choose
to download servers inside the sandbox, add this optional profile to your
`config.toml` and select `--profile editor` when starting a session:

```toml
[profiles.editor.network]
allow = [
  "update.code.visualstudio.com:443",
  "vscode.download.prss.microsoft.com:443",
  "zed.dev:443",
]
```

The same profile is in [the example configuration](../../examples/editor-profile.toml).
These are the exact HTTPS hosts named by the
[VS Code server installation guide](https://code.visualstudio.com/docs/remote/ssh)
and [Zed's remote development guide](https://zed.dev/docs/remote-development).
Setup does not install or select this profile.

Zed can redirect its server download to a release asset on another host. If
that request is held, check the destination and add only the exact host that
you intend to allow. This profile does not grant wildcard domains, extension
marketplaces, extension dependencies or direct Cursor server downloads. Use
the laptop-upload setting for Cursor.

## Forward a granted guest port

SSH can reach only the guest's loopback and a port with an existing live grant.
For example:

<!-- test: help (needs an existing sandbox and a server on port 3000) -->
```sh
simstim policy allow port 3000 --in box/auth-fix
ssh -N -L 8080:127.0.0.1:3000 auth-fix.box.simstim
```

Open `http://127.0.0.1:8080` on the laptop. A deny or an expired grant closes
its flows. SSH cannot create a grant by requesting a forward.

Remote forwarding, Unix-socket forwarding, agent forwarding, X11 forwarding,
environment requests and unknown requests are refused. Channel opens are
limited to 20 per second. Each SSH connection records `ssh_open`; the launcher
shows `ssh` on the selected row while that connection remains open.

## Remove the configuration

```sh
simstim ssh-config --remove
```

Removal deletes the generated files and only their exact `Include`. It keeps
the laptop host key, so a later setup retains the same pin.
