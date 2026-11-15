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

Run `simstim config check` after editing a secret. It checks command strings and structured definitions with the same rules as a session start. It reports unknown fields, wrong types, conflicting key spellings and invalid bindings at their original lines. It also checks unused definitions and `secret_tiers`. The check never runs a secret command.

## Ask for secrets when you start a session

<!-- test: help (needs a host and a secret) -->
```sh
simstim new my-server --agent claude --secret ANTHROPIC_API_KEY
```

`--secret` takes one name, and you can repeat it. The command of each runs first, with up to 60 s each, so a password manager can ask you to unlock it.

The command gets your terminal when stdin and stderr are terminals, also when stdout goes to a file, as in `simstim new my-server --detach > out.txt`. Its questions and messages then reach you on stderr. With `--no-input`, `--json` or `SIMSTIM_NO_INPUT` set, it gets no terminal, and a password manager that must ask fails instead ([scripts](scripting.md)).

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

The launcher may keep your last valid display settings after a broken edit. Secret commands still require the current config and permissions. Fix the file before starting or resuming a session that needs them.

For a grant that needs live consent, the launcher asks with no as its default. Some grants require the exact saved host name. Enter never approves a yes or no question. A cancelled or replaced question cannot approve later work. The answer applies only to that operation.

The launcher and start notes label only known exposed credentials as exposed. Bound credentials keep their delivery mode. Missing, future or incomplete delivery metadata is shown as unknown. These labels grant no access and never arm a credential.

The SSH agent channel uses a dedicated helper process. It checks the bounded
identity hello before opening the channel. Detach revokes its owned endpoint
and active connections. If cleanup cannot finish, the helper reports a resource
refusal and keeps the ownership witnesses for a later checked recovery.

A positively known original legacy unwalled session can have no public
stand-ins. Pull review still checks freshly read local secret values. An
incomplete modern receipt keeps the pulled work in quarantine.

Saved public receipt metadata is checked as a whole. Current, minted and retired stand-ins must keep the original local name domain, bindings and token formats. Inconsistent metadata stops review before a fetch. Unknown future metadata is preserved for other sessions, but cannot authorise a selected review.
