# Shell completion

Shell completion fills in commands, flags, saved hosts, cached sessions and agents when you press Tab. Package managers such as Homebrew install the scripts for you. Otherwise, install the script for your shell once.

## zsh

Write the script into a folder on your `fpath`, before `compinit` runs, then start a new shell:

<!-- test: skip (writes into fpath) -->
```sh
simstim completion zsh > "${fpath[1]}/_simstim"
```

If you load completions directly from `~/.zshrc`, add this line after `compinit` instead:

<!-- test: skip (shell startup line) -->
```sh
source <(simstim completion zsh)
```

## bash

This script needs the `bash-completion` package. To load it in every new shell, add this line to `~/.bashrc`:

<!-- test: skip (shell startup line) -->
```sh
source <(simstim completion bash)
```

Or write the script once into your user completion folder:

<!-- test: skip (writes into user completion folder) -->
```sh
mkdir -p ~/.local/share/bash-completion/completions
simstim completion bash > ~/.local/share/bash-completion/completions/simstim
```

## fish

Save the script in fish's completions folder:

<!-- test: skip (writes into fish completions folder) -->
```sh
mkdir -p ~/.config/fish/completions
simstim completion fish > ~/.config/fish/completions/simstim.fish
```

## PowerShell

Add this line to your PowerShell `$PROFILE`:

```powershell
simstim completion powershell | Out-String | Invoke-Expression
```

## Where values come from

Completion reads local files only and never connects to a host, so Tab stays fast even when a host is offline or asks for a key. An empty or invalid completion request returns no values or warnings. It does not offer file names. Flags for local paths keep their normal file completion.

- Host names come from `hosts.toml`, with each host's SSH target or provider beside it.
- Session addresses (`<host>/<session>`) come from the last list that the launcher, the picker or `simstim ls` saw. When two sessions on one host share a name, both complete as `<host>/<id>`. Completion also uses the ID when a name contains terminal controls or could identify another session. A list older than a minute notes its age, such as `seen 5m ago`.
- Agent names come from the built-in agent table.
- Secret names for `--secret` come from `[secrets]` in `config.toml`. Their commands do not run during completion.

Pass `--no-descriptions` to any [`simstim completion`](../reference/cli/completion.md) command if you want values without descriptions.

Completion preserves local secret names in full. Its 4 MiB file limit applies to the open file. Pipes and devices give no values and never wait for input.

`policy rm` completes rule IDs from local all-session, host, repository and profile layers. Each ID shows its target. Profile completion uses the same config schema. Neither runs a command or connects to a host.

`policy allow --tools` completes capability names from the same fixed table that validates an allowance. Comma-separated lists keep their earlier selections and do not offer them twice.

Session-valued `--in` flags use the same cached addresses as `attach`. With an
explicit host, `new <host> --in` offers only that host's cached sessions.
`policy allow` and `policy deny` complete the doors accepted by those commands.
`policy log --door` offers only `net`.

Cancellation stops local file completion without returning partial values. Each
read keeps the original descriptor, checks cancellation between bounded reads,
and keeps the existing four-MiB limit. It takes no host or cache lock.

`help` completes the current command tree, including help topics such as
`concepts` and `exit-codes`. Old hidden command names are not offered.
