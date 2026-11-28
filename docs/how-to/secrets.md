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

## Bound secrets in a sandbox

In a walled session (`--wall process`), secrets do not enter the sandbox environment. The host's egress process holds bound secrets in memory only and injects them into outbound requests over verified TLS on its gateway route (`/gw/<route>`):

- **Model API keys bind automatically.** For Claude Code (`ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `CLAUDE_CODE_OAUTH_TOKEN`) and Codex (`OPENAI_API_KEY`, `CODEX_API_KEY`), define the secret under `[secrets]` in `config.toml`. When you run `simstim new my-server --wall process --agent claude` or `--agent codex`, the client fetches the matching secret automatically, the egress process injects the real key on `/gw/anthropic` or `/gw/openai`, and the sandbox receives only a non-secret placeholder plus `ANTHROPIC_BASE_URL` or a session-local `.codex/config.toml` (`supports_websockets = false`). The user's own `~/.codex/config.toml` on the host is never touched.
- **Claude Code subscription token (`claude setup-token`).** To use a Claude Pro or Max subscription inside a walled session without an API key, run `claude setup-token` once on your laptop, store the resulting `sk-ant-oat01-...` token in your password manager, and configure `CLAUDE_CODE_OAUTH_TOKEN` under `[secrets]`:

  ```toml
  [secrets]
  CLAUDE_CODE_OAUTH_TOKEN = "op read op://dev/claude/oauth-token"
  ```

  When `ANTHROPIC_API_KEY` is not configured (or when you pass `--secret CLAUDE_CODE_OAUTH_TOKEN`), simstim binds `CLAUDE_CODE_OAUTH_TOKEN` to `api.anthropic.com` on `/gw/anthropic`. The egress process injects `Authorization: Bearer <token>`, strips any `x-api-key` header, and preserves Claude Code's `anthropic-beta` header (`oauth-2025-04-20`).

- **Codex with ChatGPT sign-in.** A ChatGPT subscription reaches a walled Codex session through `/gw/chatgpt` (`chatgpt.com`), where the egress process injects `Authorization: Bearer` and `Chatgpt-Account-Id`, refreshes expired access tokens at `auth.openai.com/oauth/token`, and allows only Codex model endpoints (`/backend-api/codex/responses`, `/responses/compact`, `/responses/input_tokens` and `/models`). The sandbox receives only a non-secret placeholder `.codex/auth.json` and gateway `.codex/config.toml`. Two setups work:
  1. **Sign in on the host (recommended).** Run `simstim agents login codex my-server` (which runs `codex login --device-auth` on the host and saves `~/.codex/auth.json`). When you start a walled Codex session with no `OPENAI_API_KEY`, `CODEX_API_KEY` or `CODEX_AUTH_JSON`, the supervisor arms the egress gateway from the host's `~/.codex/auth.json`. When tokens rotate, egress coordinates concurrent sessions with an advisory lock and atomically updates that pre-existing file (mode `0600`) without ever putting real tokens into the sandbox.
  2. **Push from your laptop (`CODEX_AUTH_JSON`).** Define `CODEX_AUTH_JSON` under `[secrets]` with a command that prints a ChatGPT `auth.json` document (for example `op read op://dev/codex/auth-json`). simstim holds the tokens strictly in egress memory and writes nothing to the host disk. Because OpenAI rotates single-use refresh tokens on every refresh, use a dedicated login for laptop-pushed `CODEX_AUTH_JSON` (otherwise the first refresh on the host invalidates your laptop's own copy), or sign in on the host instead.
- **Custom destination bindings.** Use a `[secrets.NAME]` table in `config.toml` to bind any secret to its allowed hosts:

```toml
[secrets.ACME_TOKEN]
run = "op read op://dev/acme/token"
hosts = ["api.acme.example"]
header = "Authorization"       # optional; default is "Authorization"
format = "Bearer {value}"      # optional; also supports "{value}" and "basic:user:{value}"
route = "acme"                 # optional; defaults to a slug derived from the host
```

- **Unbound secrets fail closed.** In a walled session, passing `--secret NAME` for a secret with no bound hosts fails with `secret_unbound`. Add `hosts` under `[secrets.NAME]`, or pass `--expose NAME` to place that secret in the sandbox environment explicitly.

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
