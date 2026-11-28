# Shell completion

`simstim` prints a completion script for bash, zsh, fish and PowerShell. Pressing Tab fills in commands, flags, saved hosts, cached sessions, agents and fixed flag values.

If you installed `simstim` from Homebrew or a release package, the completion script may already be in place.

## bash

Install the `bash-completion` package first if you do not have it. To load completions in your current shell:

<!-- test: skip (loads completion into the current shell) -->
```sh
source <(simstim completion bash)
```

To load completions in every new shell, run one of these once and open a new shell:

<!-- test: skip (writes a system completion file) -->
```sh
# Linux
simstim completion bash | sudo tee /etc/bash_completion.d/simstim >/dev/null

# macOS (Homebrew)
simstim completion bash > "$(brew --prefix)/etc/bash_completion.d/simstim"
```

## zsh

Make sure zsh completion is turned on (`autoload -U compinit; compinit` in `~/.zshrc`). To load completions in your current shell:

<!-- test: skip (loads completion into the current shell) -->
```sh
source <(simstim completion zsh)
```

To load completions in every new shell, run one of these once and open a new shell:

<!-- test: skip (writes a zsh completion file) -->
```sh
# Linux
simstim completion zsh > "${fpath[1]}/_simstim"

# macOS (Homebrew)
simstim completion zsh > "$(brew --prefix)/share/zsh/site-functions/_simstim"
```

## fish

To load completions in your current shell:

<!-- test: skip (loads completion into fish) -->
```sh
simstim completion fish | source
```

To load completions in every new shell:

<!-- test: skip (writes a fish completion file) -->
```sh
simstim completion fish > ~/.config/fish/completions/simstim.fish
```

## PowerShell

To load completions in your current session:

```powershell
simstim completion powershell | Out-String | Invoke-Expression
```

To load completions in every new session, add that line to your `$PROFILE` file.

## Where the values come from

Completion reads local files only. It never dials a host, runs a provider script or opens a network connection, so Tab stays fast and never asks for a password or a host key.

- **Hosts** come from `hosts.toml` (`simstim config path` prints where it lives).
- **Sessions** (`<host>/<session>`, or `<host>/<id>` when two sessions on one host share a name) come from the last list that the launcher or `simstim ls` saw. When a host's cached list is more than 60 seconds old, the description notes its age, such as `seen 5m ago`. Run `simstim ls` or open the launcher to refresh the cache.
- **Agents** come from the built-in agent table, and **secrets** come from `[secrets]` in `config.toml`.
- **Settings and fixed values** (`host set` keys and values, `--wall`, `--floor`, `--debug`, `--autostart`, `--format`, `--events`) come from the command definitions.

See the [`completion`](../reference/cli/completion.md) reference for every shell flag, including `--no-descriptions`.
