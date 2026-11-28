# Install an agent on a host

simstim installs an agent with its vendor's own installer, in a session on the host, so you see the installer's output and can answer its questions. Then you log in with the agent's own login, in a session. simstim never logs in for you, and never sees your login.

## Pick them from a checklist

`simstim setup` ends with a checklist of every agent it can install. First-class and supported agents come first; best-effort ones follow, marked as such. The agents the host has are ticked already, with their state: installed, logged in, or not.

```text
   1  [x] Claude Code      installed, logged in
   2  [ ] Codex            not installed
   3  [ ] opencode         not installed
   4  [ ] Antigravity CLI  not installed
  Best effort, kept working by contributors:
   5  [ ] Gemini CLI       not installed
```

Type the numbers of the agents to install, such as `2 3`. The setup installs them one after another, each in its own session that you watch, as `agents install` does below. Then, for each agent that is not logged in, it offers the agent's own login in a session. When you leave a session, by detaching or when it ends, the setup checks the host again and says how it went.

- On a host you have already set up, run `simstim setup --host my-server`.
- `host add` shows the same checklist, in a terminal, when a first-class or supported agent is missing.
- In the launcher, the add-host wizard shows it after "ready", and `s` opens the whole setup.
- For a script, name the agents: `simstim setup --host my-server --agents claude,codex --yes`. Without a terminal, the setup waits for each installer to end, and leaves the logins to you.

## Install one agent

<!-- test: help (it runs an installer on the host) -->
```sh
simstim agents install claude my-server
```

The client:

1. Checks the host. If the agent is already installed, it says where and stops. `--reinstall` runs the installer again, for example to update the agent (`--force` still works for now, and warns).
2. Prints the install command and the vendor page it comes from.
3. Starts a session named `install-claude` that runs the command, and attaches to it. Answer the installer's questions there. Detach with `ctrl+\` at any time (or the key set in `attach.detach_key`); the installer keeps running, and `simstim attach my-server/install-claude` brings you back.
4. When the installer ends, checks the host again, updates the host's agents disk when supported, and prints the command to log in: `Next, log in: simstim agents login claude my-server`.

With one host, you can leave out the host. With more than one, the command names them and fails. `--host my-server` works too, in place of the second word. `simstim agents ls` lists every agent and its install command, and [the agent reference](../reference/agents.md#install-and-log-in) lists each command's source.

## Log in

Each agent logs in its own way. `agents login` starts the agent's own login in a session on the host, and attaches to it:

<!-- test: help (it starts a login on the host) -->
```sh
simstim agents login claude my-server
```

Follow the agent's prompts in the session. Most print a link to open in your browser, and ask for the code it shows. The client:

1. Checks the host. The agent must be installed, and a login shell must find it.
2. When a `login-claude` session already runs on the host, attaches to it, and says so.
3. Otherwise, starts a session named `login-claude` that runs the agent's login, such as `claude auth login` or `codex login --device-auth`, and attaches to it. An agent whose first start is its login, such as Antigravity CLI, starts as itself. The session gets no secret.
4. When you leave the session, checks the login again and prints the agent's line, as `host doctor` does. If you detached and the login still runs, the client says how to attach to it again.

Detach with `ctrl+\` (or `attach.detach_key` in `config.toml`). The client names the session by its ID, such as `my-server/k3v7qa2m`, because two logins that start at the same moment share the name `login-claude`.

The login runs again also when the agent is logged in, for example to change the account. The agent writes its credentials on the host. simstim never reads, stores or passes them.

- **Codex on a headless host:** `simstim agents login codex my-server` runs `codex login --device-auth`. Turn on device-code sign-in in your ChatGPT security settings if Codex asks for it.
- **Claude Code on a headless host or in a walled session:** run `claude setup-token` on a machine with a browser to create a long-lived `CLAUDE_CODE_OAUTH_TOKEN`, save a command that prints it under `[secrets]` in `config.toml`, and pass `--secret CLAUDE_CODE_OAUTH_TOKEN` to `new` (see [pass secrets to a session](secrets.md)). Walled sessions never read host login files, so `agents login` adds a note when a host's sandbox floor is above `none`.
- **Agents without a login of their own:** an agent that takes an API key instead, such as Aider, fails with the variable names it reads. Add the key under `[secrets]` in `config.toml` and pass it with `--secret`.

Without a terminal, or with `--no-input`, the command starts the login and does not attach. It prints `Started my-server/k3v7qa2m. Attach to finish the login: simstim attach my-server/k3v7qa2m` on stderr and exits with 0. With `--json`, it prints the session as JSON instead, with `"started": true`, or `false` when the login already ran ([scripting](scripting.md#read-json)).

## Check the agents

`host add` and `host doctor` check each first-class and supported agent, and any other agent that is installed:

<!-- test: help (needs a host) -->
```sh
simstim host doctor my-server
```

```text
Agents:
  ✓ Claude Code      /home/me/.local/bin/claude, logged in (`claude auth status` says so)
  ! Codex            /home/me/.local/bin/codex, not logged in (`codex login status` says not)
    Try: simstim agents login codex my-server
  ✕ opencode         /home/me/.opencode/bin/opencode is not on PATH in a login shell
    Try: on the host, add it to PATH: echo 'export PATH="/home/me/.opencode/bin:$PATH"' >> ~/.profile
  ? Antigravity CLI  /home/me/.local/bin/agy, login unknown (the agent has no status command or credentials file to read)
    Try: if it asks you to log in: simstim agents login antigravity my-server
```

| Mark | Meaning |
| --- | --- |
| ✓ | Installed, found by a login shell, and logged in |
| ! | Installed, but not logged in |
| ? | Installed; the host cannot tell whether it is logged in |
| ✕ | Not installed, or not on `PATH` in a login shell |

Sessions find agents through your login shell, the way `ssh my-server` does. An agent the installer put in a folder that is not on `PATH` there, such as `~/.local/bin` on some systems, is installed but cannot start: add the folder to `PATH` in `~/.profile` on the host.

The host decides "logged in" from the agent's own signs, kept as data in the agent table: a variable such as `ANTHROPIC_API_KEY` set in the login shell, the agent's own status command (`claude auth status`, `codex login status`), or its credentials file. It reads no file's content and shows no command's output.

## Agents in microvm sandboxes

Microvm sandboxes get every agent installed on the host from one read-only disk image mounted at `/opt/agents`, with `/opt/agents/bin` first on `PATH`.

- **When the disk updates:** `agents install` and guided `setup` rebuild the disk when an install finishes. Before a microvm starts, the host also checks whether installed agents changed and rebuilds the disk when needed.
- **Format:** when `mkfs.erofs` (`erofs-utils`) is installed on the host, the helper writes an EROFS image (`images/agents/<sha256>.erofs`). When `mkfs.erofs` is absent, it writes an ext4 image (`images/agents/<sha256>.img`) in Go with no extra tool needed.
- **No self-updates inside a sandbox:** `/opt/agents` is mounted read-only (`EROFS` on any write), walled sessions disable agent self-updaters (`DISABLE_AUTOUPDATER=1` for Claude Code and `NO_UPDATE_NOTIFIER=1` for Codex), and updater hosts such as `downloads.claude.ai` stay blocked by default. To update an agent, run `simstim agents install <agent> <host> --reinstall` on the host; running sandboxes keep their current disk until their next start.
- **Cleanup:** `prune` keeps the newest agents disk and any disk still named in a non-deleted session's manifest, and deletes older ones.
