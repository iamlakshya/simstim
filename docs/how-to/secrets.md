# Pass secrets to a session

An agent sometimes needs a key in its environment: an API key instead of a login, or a token for a tool it runs. simstim can fetch the key on your laptop when a session starts, from your keychain or password manager, and put it in that session's environment. simstim stores no secrets: it keeps only the names, and the command that fetches each value.

## Name each secret and its command

In `config.toml` on your laptop (`simstim config path` prints where it is), add a `[secrets]` table. Each key is the variable's name; each value is a command that prints the secret:

```toml
[secrets]
OPENAI_API_KEY = "op read op://dev/openai/key"
ANTHROPIC_API_KEY = "security find-generic-password -s anthropic-api-key -w"
GITHUB_TOKEN = "gh auth token"
```

The command runs with `sh -c` on your laptop. What it prints is the value, less one final line break. Never put a value itself in `config.toml`.

| Store | Command |
| --- | --- |
| 1Password CLI | `op read op://<vault>/<item>/<field>` |
| macOS Keychain | `security find-generic-password -s <service> -w` |
| pass | `pass show <path>` |
| Bitwarden CLI | `bw get password <item>` |
| Linux Secret Service | `secret-tool lookup <attribute> <value>` |

## Ask for secrets when you start a session

<!-- test: help (needs a host and a secret) -->
```sh
simstim new my-server --agent claude --secret ANTHROPIC_API_KEY
```

`--secret` takes one name, and you can repeat it. The command of each runs first, with up to 60 s each, so a password manager can ask you to unlock it.

## Give a host secrets for every session

In `hosts.toml`, a host's `secrets` lists names that every new session on it gets, from the command line and from the launcher alike:

```toml
[[host]]
name = "my-server"
ssh = "my-server"
secrets = ["ANTHROPIC_API_KEY", "GITHUB_TOKEN"]
```

`--secret` adds to the list. In the launcher, a command cannot ask you anything, because the launcher owns the screen: unlock your password manager first, or start the session with `simstim new`.

## Where a value goes, and where it never goes

1. The client runs the command on your laptop, and sends the value in the request that starts the session, over the same SSH connection as everything else.
2. On the host, the helper writes only the names into the session's record. It hands the values to the session's holder over the session's private socket, before the agent starts.
3. The holder puts them in the agent's environment, and nowhere else. A setup script that runs in the session before the agent sees them too.

The value is never written to a file on either side: not the session's record, a log, the `ls --json` list or an error message. It is never on a command line, so other users cannot see it in `ps`. Printing it by mistake in the code shows `[redacted]`.

The agent, and the programs it starts, can read the value, as they can read any variable in their environment. So can anything running as your user on the host, in `/proc`. Pass a secret only to a host you trust with it.

## After a resume

A [resume](restore-after-reboot.md#bring-back-one-session) fetches the session's secrets again, on your laptop. The host's record names the secrets, so a host could ask for any secret in your `config.toml`. So a resume fetches without asking only the ones that the host's `secrets` in `hosts.toml` lists. Pass any other secret that the session started with again:

<!-- test: help (needs a host and a secret) -->
```sh
simstim resume my-server/app --secret GITHUB_TOKEN
```

Without it, `resume` fetches nothing, and says which secrets the host asks for. The launcher cannot pass them, so `u` says the same.

## After a host restart

When the host restarts, the session comes back without its secrets: nothing on the host has them, and your laptop may be closed. The agent starts anyway. Its terminal says which secrets it lacks, and the launcher shows "started without its secrets" on the session. To get them back, start a new session.
