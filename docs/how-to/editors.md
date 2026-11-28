# Open a session in an editor, scp, sftp or rsync

Set up an SSH `ProxyCommand` block so editors and standard file tools can connect to any session as `<session>.<host>.simstim`:

<!-- test: help (edits ~/.ssh) -->
```sh
simstim ssh-config
```

This writes `~/.ssh/simstim/config` and `~/.ssh/simstim/known_hosts` with a per-laptop Ed25519 host key, and asks before adding `Include ~/.ssh/simstim/config` to the top of `~/.ssh/config`. Run `simstim ssh-config --remove` to undo both.

SSH ends in the client on your laptop, so no SSH daemon listens in the session. Only interactive shells, `exec`, `subsystem sftp` (`github.com/pkg/sftp`), and loopback `direct-tcpip` forwards are allowed. Agent forwarding (`-A`), X11 (`-X`), reverse port forwarding (`-R`), `SendEnv`, and non-loopback forwards are refused.

## Plain tools

Once `ssh-config` is set up, standard SSH tools work unchanged:

<!-- test: skip (needs a running session) -->
```sh
ssh auth-fix.my-server.simstim
scp ./data.csv auth-fix.my-server.simstim:data.csv
sftp auth-fix.my-server.simstim
rsync -av ./dir/ auth-fix.my-server.simstim:dir/
```

## VS Code, Cursor and Zed

Open `<session>.<host>.simstim` as a Remote-SSH host:

- **VS Code and Cursor:** set `"remote.SSH.localServerDownload": "always"` so the editor uploads its server over SFTP without needing extra network rules.
- **Zed:** enable `upload_binary_over_ssh` in your Zed settings.

> [!WARNING]
> VS Code and Cursor run a remote extension host that can execute commands on your laptop by design. For a repository you do not trust, use plain `ssh`, `scp`, `sftp` or [`cp`](copy-files.md), or open the editor in restricted mode.
