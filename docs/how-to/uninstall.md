# Uninstall

## From each host

Stop your sessions first (`simstim ls`, then `simstim kill <host>/<session>`). Then, on the host:

<!-- test: skip (removes the helper from the host) -->
```sh
systemctl --user disable --now simstim-restore.service
rm -f ~/.config/systemd/user/simstim-restore.service
systemctl --user revert simstim.slice
rm -rf ~/.local/share/simstim ~/.local/state/simstim
```

`systemctl --user revert` removes the memory limit that the helper put on the sessions' slice ([memory](memory.md)). It undoes what `systemctl --user set-property` stored.

Linger, which `host add` turned on, stays on; `loginctl disable-linger` turns it off if nothing else needs it.

## From the laptop

<!-- test: skip (removes the client) -->
```sh
rm -f ~/.local/bin/simstim
rm -rf ~/.config/simstim ~/.cache/simstim ~/.local/state/simstim
```

The client never edited `~/.ssh/config`, so there is nothing to undo there.
