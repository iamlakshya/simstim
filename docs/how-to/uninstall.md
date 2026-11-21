# Uninstall

## Remove a host's files

Run this on the laptop:

<!-- test: help (removes the host) -->
```sh
simstim host rm my-server --purge
```

Type the host's name when asked. The command removes files and settings with original native records, handles the selected root undo and forgets the host last. Unknown, replaced or unsupported resources stay for inspection. If the attempt stops, the host stays saved.

Removal drains new starts and waits for already admitted placement work before inspecting sessions. Save and push your work first. Working sessions, unsaved worktrees and sandbox commits that have not been pulled stop removal. `--force` accepts work loss. Native process closure and storage ownership checks still apply. A legacy or already ended session without a supported native owner can stop removal even with `--force`.

Ordinary host worktrees currently need `--force`: their native work inspection is not yet supported. This refusal happens before stopping their holders. Before cleanup has begun, it lists the affected sessions and withdraws only this removal's drain mark. A separate user drain setting stays in place. Walled work checks use the supported original native controller and fixed reader; an unavailable or uncertain reading stops removal.

Initial worktree includes stay within the original repository and worktree directories; a changed named directory makes the copy skip files. Original member records bind each directory to its sandbox. Original worktree records bind the created directory and branch. Removal preserves older or replaced directories without those records. External repository branches and Git administrative records remain for separate inspection. An ordinary session without a user service manager joins its original holder, main process and terminal; escaped same-user programs can remain under that weaker boundary.

The helper undoes recorded outside changes in reverse order, including its units, slice setting and terminfo. Native records determine which linger change can be undone. Legacy or unknown linger changes stay in the report. A copied footprint file cannot establish resource ownership.

Agent login files have a separate question, with a default of no. `--delete-logins` answers yes. Root undo uses the selected entries in this laptop's original root-step history. With input off, it prints the script. Choose run, print or skip explicitly:

<!-- test: help (removes the host) -->
```sh
simstim host rm my-server --purge --confirm my-server --root-step print
simstim host rm my-server --purge --confirm my-server --delete-logins --root-step run
```

Printing or skipping leaves the root changes in place, and the report says so. SSH key lines stay in `authorized_keys`. Remove your key on the host when it is no longer needed, and rotate the secrets in the printed reminder.

Keep the local removal record after a failure and run the same command again. Retry uses that original attempt and never installs another helper. A temporary checked cleanup command can finish a saved acknowledgement. If the original native custody is unavailable, remaining resources are preserved and the host stays saved.

With `--json`, stdout contains one public removal outcome. Progress and root output use JSON lines on stderr. For a machine made by `host create`, add `--destroy` to delete the machine after the purge. Without `--purge`, `host rm` only forgets the host.

## When the laptop cannot reach the host

At the host's own keyboard, stop the sessions and inspect the files before removing them. These manual steps also cover older installations without native creation records. Check which programs still need linger or the slice setting first.

<!-- test: skip (removes the helper from the host) -->
```sh
systemctl --user disable --now simstim-restore.service simstim-idle.timer
systemctl --user revert simstim.slice
rm -f ~/.config/systemd/user/simstim-restore.service
rm -f ~/.config/systemd/user/simstim-idle.service ~/.config/systemd/user/simstim-idle.timer
rm -rf ~/.local/share/simstim ~/.local/state/simstim ~/.config/simstim
```

Run the recorded root undo as the machine's owner. Turn linger off only when nothing still needs it. Inspect agent login files and SSH key lines before removing them. Forget the host on the laptop afterwards.

## Remove the client

<!-- test: skip (removes the client) -->
```sh
rm -f ~/.local/bin/simstim
rm -rf ~/.config/simstim ~/.cache/simstim ~/.local/state/simstim
```

The client never edited `~/.ssh/config`, so there is nothing to undo there.
