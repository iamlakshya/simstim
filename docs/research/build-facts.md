# Facts for the build

Checked on 10 November 2026.

An audit of the design on 10 November 2026 found facts that would change the build, but had no source or carried the mark UNVERIFIED. This note holds the checks of those facts. The spec's sections 7, 8, 14, 17 and 19 to 21 link here. [Hosts in two locations](locations.md) holds the checks of Mac hosts, of joining a machine and of picking a host.

The rules for the note:

- Facts come from vendor docs, source code at a tagged release, man pages, release notes and advisories. Each one links its source, with the version or date that it applies to.
- "Secondary" marks a blog post, a forum answer or a third-party project.
- **UNVERIFIED** marks a claim that no primary source confirmed. The spec names the task step that checks it first.
- No host name, address or account of a real user appears here.

## Summary

1. **Agent data.** Codex session IDs are UUIDv7, `--yolo` is an alias, and Codex uses WebSockets by default. Only secondary sources give Claude Code's OAuth header. The six new agents are partly verified (section 1).
2. **Model APIs.** Clients retry a bare 429. A refusal that must stop a client needs `x-should-retry: false` and an error body that the client treats as final. Anthropic's usage in `message_delta` is cumulative (section 2).
3. **Holds and timeouts.** Behind a transparent proxy, many clients give up in 5 to 15 s, and pip gives up at 15 s with either proxy (section 2.6).
4. **Package managers.** Each tool has its own setting and unit for a minimum release age. Cargo has none that is stable. For Go, the risk is a toolchain download, and only the index's timestamp is a publish time that a gate can trust (section 3).
5. **Linux walls.** A user-namespace profile is a bypass unless the program that it starts runs without capabilities, as Ubuntu's bubblewrap profile does. Debian and Ubuntu make `/dev/kvm` 0660 `root:kvm`. Server images lack `acl` and `uidmap` (section 4).
6. **The microvm.** Cloud Hypervisor's CI holds the VMM's own memory to 10 MiB. The same tar gives the same root hash. ext4 has no reflinks (section 5).
7. **Pins.** The Ghostty fixes need the tag `vt-1.0.0` and Zig 0.17. OSV does not hold these advisories. No outside source sets the 7-day rule (section 6).
8. **Later phases.** Decoy credentials have caught a real breach outside a sandbox. Nix's build users are the nearest pattern for slot accounts (section 7).

## 1. Agent data

Checked on 10 November 2026, against each agent's own docs or its source at the named tag. "Source" means a researcher read the code at that tag. "Secondary" means a source other than the vendor. Spec 7.3, 8.6 and 17.9 use these facts.

### 1.1 Claude Code

Docs read on 10 November 2026, release 2.1.3xx.

- Install: `curl -fsSL https://claude.ai/install.sh | bash`. The npm package `@anthropic-ai/claude-code` needs Node 22 or later, and installs the same native binary ([setup](https://code.claude.com/docs/en/setup)).
- Login: `claude auth login`. `claude auth status` exits 0 or 1. On Linux the token is in `~/.claude/.credentials.json` (0600), or under `CLAUDE_CONFIG_DIR`. A keyless Console sign-in keeps a profile in `~/.config/anthropic/`, outside the config folder ([authentication](https://code.claude.com/docs/en/authentication)).
- Headers: `ANTHROPIC_API_KEY` goes in `X-Api-Key`, and `ANTHROPIC_AUTH_TOKEN` in `Authorization: Bearer`. The docs name no header for `CLAUDE_CODE_OAUTH_TOKEN` ([authentication](https://code.claude.com/docs/en/authentication)).
- The OAuth token has the prefix `sk-ant-oat01-`. It goes in `Authorization: Bearer`, with `anthropic-beta: oauth-2025-04-20`. Secondary: pi 1.5.2 sends these ([anthropic-messages.ts](https://github.com/earendil-works/pi/blob/v1.5.2/packages/ai/src/api/anthropic-messages.ts)). UNVERIFIED in a first-party source.
- Hosts: `api.anthropic.com` (the API). `platform.claude.com` (OAuth exchange, refresh and revoke). `claude.ai`, `claude.com` and `sso.claude.com` (sign-in). `downloads.claude.ai` (the updater). `mcp-proxy.anthropic.com` (claude.ai connectors). Two Datadog hosts are optional ([network config](https://code.claude.com/docs/en/network-config)).
- Resume: `claude --resume <id>` also takes a name or a `.jsonl` path. `--session-id` "must be a valid UUID" ([CLI reference](https://code.claude.com/docs/en/cli-reference)).
- Danger flags: `--dangerously-skip-permissions` equals `--permission-mode bypassPermissions`, and both spellings parse. `--allow-dangerously-skip-permissions` adds bypass to the Shift+Tab cycle. `permissions.defaultMode: "bypassPermissions"` in user settings or in `--settings` also starts bypass. Project settings cannot. Managed `permissions.disableBypassPermissionsMode: "disable"` blocks it ([permission modes](https://code.claude.com/docs/en/permission-modes)).
- More arguments that change approvals, checked on 11 November 2026:
  - The modes are `default` (alias `manual`), `acceptEdits`, `plan`, `auto`, `dontAsk` and `bypassPermissions`. "Auto mode lets Claude execute without routine permission prompts" ([permission modes](https://code.claude.com/docs/en/permission-modes), [settings reference](https://code.claude.com/docs/en/settings-reference)).
  - `--allowedTools` (alias `--allowed-tools`): "Tools that execute without prompting for permission", as a list (`"Bash(git log *)" "Bash(git diff *)" "Read"`). `--add-dir` "grants file access" to more folders, also as a list (`claude --add-dir ../apps ../lib`). `--plugin-dir` takes one path each time. `--plugin-dir` and `--plugin-url` load a plugin for the session. `--settings` takes "Path to a settings JSON file or an inline JSON string". `--agents` takes JSON ([CLI reference](https://code.claude.com/docs/en/cli-reference)).
  - `permissions.allow`: "Approve listed tool uses without a prompt". `permissions.additionalDirectories`: "Give Claude file access to directories outside the current one" ([settings reference](https://code.claude.com/docs/en/settings-reference)). A subagent's `permissionMode` takes the same modes ([subagents](https://code.claude.com/docs/en/sub-agents)).
  - `sandbox.autoAllowBashIfSandboxed` (default `true`) runs "sandboxed commands without a permission prompt", and `sandbox.enabled` (default `false`) turns the Bash sandbox on. So `sandbox.enabled: true` alone runs sandboxed Bash without a prompt ([settings reference](https://code.claude.com/docs/en/settings-reference#sandboxautoallowbashifsandboxed), 11 November 2026).
  - A `PermissionRequest` hook that returns `"behavior": "allow"` makes "Claude Code answer the request on your behalf" ([hooks guide](https://code.claude.com/docs/en/hooks-guide)). In a `PreToolUse` hook, `permissionDecision: "allow"` "skips the permission prompt" ([hooks reference](https://code.claude.com/docs/en/hooks)).
- Proxy: it reads `https_proxy`, `HTTPS_PROXY`, `http_proxy`, `HTTP_PROXY` and `NO_PROXY`, with no SOCKS. WebSocket connections use the proxy, except to loopback. CA: `NODE_EXTRA_CA_CERTS` plus the OS store (`CLAUDE_CODE_CERT_STORE`). The binary "does not itself invoke Node" ([network config](https://code.claude.com/docs/en/network-config)).
- Files: `~/.claude/`, `~/.claude.json`, transcripts in `~/.claude/projects/`. The binary `~/.local/bin/claude` links into `~/.local/share/claude/versions/` ([setup](https://code.claude.com/docs/en/setup)).

### 1.2 Codex

Source at tag [rust-v0.167.0](https://github.com/openai/codex/tree/rust-v0.167.0) (6 November 2026). Paths are under `codex-rs/`.

- Install: `curl -fsSL https://chatgpt.com/codex/install.sh | sh`, or `npm install -g @openai/codex`. The installer downloads from `releases.openai.com`, then from GitHub ([README](https://github.com/openai/codex#readme)).
- Login: `codex login` (a browser and `localhost:1455`), `--device-auth` (beta), `--with-api-key` (the key on stdin) and `--with-access-token`. `codex login status` exits 0 when logged in. The token is in `~/.codex/auth.json` only when `cli_auth_credentials_store` is `file`. `keyring` and `auto` use the OS keyring, and `ephemeral` keeps it in memory ([auth](https://learn.chatgpt.com/docs/auth), 10 November 2026). The old page `developers.openai.com/codex/auth` now answers 308 to that page.
- Credential: `Authorization: Bearer`. The interactive TUI does not use `OPENAI_API_KEY` as its credential. It only fills in the login form. `CODEX_API_KEY` works for `codex exec` and some subcommands only (source `login/src/auth/manager.rs`, `app-server/src/in_process_bootstrap.rs`). Confidence medium.
- Hosts: an API key uses `api.openai.com/v1`. A ChatGPT login uses `chatgpt.com/backend-api/codex` (responses and models) and `auth.openai.com` (login, `/oauth/token` refresh and `/oauth/revoke`) (source `model-provider-info/src/lib.rs`, `login/src/auth/manager.rs`).
- WebSocket: the default OpenAI provider has `supports_websockets: true`, with the beta header `responses_websockets=2026-02-06`. It falls back to HTTPS (source `model-provider-info/src/lib.rs`, `core/src/client.rs`).
- IDs: "Codex-generated thread IDs are UUIDv7" (source `protocol/src/thread_id.rs`). `codex resume` takes a UUID or a session name, `--last` and `--all`.
- Fork: the unit test `resume_and_fork_preserve_no_daemon` shows that `codex --no-daemon resume` and `codex --no-daemon fork <id>` keep `--no-daemon` (source `cli/src/main.rs`).
- Danger flags: `--dangerously-bypass-approvals-and-sandbox` and its alias `--yolo`. Also `--sandbox danger-full-access` (`-s`), `--dangerously-bypass-hook-trust`, `-c sandbox_mode="danger-full-access"`, `-c approval_policy="never"` and profile files through `--profile` (`-p`). `--approve-for-me` (alias `--not-so-yolo`) is a reviewed auto-approval. `--full-auto` is gone (source `utils/cli/src/shared_options.rs`).
- More arguments that change approvals, checked on 11 November 2026 at the same tag:
  - `-a`, `--ask-for-approval`, whose value `never` means "Never ask for user approval" (source `utils/cli/src/approval_mode_cli_arg.rs`, `tui/src/cli.rs`). `--search` turns on web search with "no per-call approval" (`tui/src/cli.rs`). `--add-dir` gives "Additional directories that should be writable" (`utils/cli/src/shared_options.rs`).
  - `--approve-for-me` sets `approvals_reviewer="auto_review"` (`take_auto_review_config_overrides`). The keys `default_permissions` ("Default permissions profile to apply"), `permissions` ("Named permissions profiles"), `profile` ("Profile to use from the `profiles` map") and `sandbox_workspace_write` ("Sandbox configuration to apply if `sandbox` is `WorkspaceWrite`") change approvals or the sandbox too (`core/config.schema.json`).
  - `--enable FEATURE` is "Equivalent to `-c features.<name>=true`", and `--disable FEATURE` to `features.<name>=false`. Both take one known feature each time (`FeatureToggles` in `cli/src/main.rs`). Of the known features (the `features` table in `core/config.schema.json`), only `web_search` and `web_search_request` give a key that the danger text refuses. `features.web_search_request = true` means `live`. The schema gives `features.web_search` no description, so the rule refuses it too.
  - The `-c` forms of the flags. `--search` pushes `web_search="live"` (`tui/src/startup_orchestration.rs`). `web_search` takes `disabled`, `cached`, `indexed` or `live`, and its default is `cached` ([config reference](https://learn.chatgpt.com/docs/config-file/config-reference), 11 November 2026). `tools.web_search` and `features.web_search` are booleans, and the legacy `features.web_search_request = true` maps to `live`. `--add-dir` sets `additional_writable_roots`, whose config form is `sandbox_workspace_write.writable_roots`. `--dangerously-bypass-hook-trust` sets `bypass_hook_trust`, which has no config key. Hook trust is kept as hashes under `hooks.state` (`core/config.schema.json`). `--profile` loads `$CODEX_HOME/<name>.config.toml`, which has no `-c` form. The `profile` key picks from the `profiles` map.
  - A `-c` override keeps its key as written, and parses its value as TOML, or else takes it as a string without its quotes (`utils/cli/src/config_override.rs`). So a TOML escape in a value decodes before Codex uses it.
- Proxy: `HTTPS_PROXY`, `HTTP_PROXY`, `ALL_PROXY` and `NO_PROXY`. For `wss` it tries `HTTPS_PROXY`, then `HTTP_PROXY`, then `ALL_PROXY`. CA: `CODEX_CA_CERTIFICATE`, else `SSL_CERT_FILE`, "for login, HTTPS requests, and secure WebSocket connections". It is a native Rust binary (source `http-client/src/outbound_proxy.rs`, [auth](https://learn.chatgpt.com/docs/auth)).
- Files: `~/.codex` (`CODEX_HOME`), with `sessions/YYYY/MM/DD/rollout-*-<uuid>.jsonl`, `archived_sessions/` and `state_5.sqlite`, which holds thread names (source `rollout/src/lib.rs`). Confidence medium that a resume needs more than `sessions/`.

### 1.3 The six best-effort agents

Each row was checked on 10 November 2026. A blank or UNVERIFIED cell means that no first-party source gave the value.

| Agent | Install | Login and credential | Resume and IDs | Danger flags | Hosts | Proxy and CA | Sources |
| --- | --- | --- | --- | --- | --- | --- | --- |
| GitHub Copilot CLI (npm 1.0.109) | `curl -fsSL https://gh.io/copilot-install \| bash`, `npm install -g @github/copilot` (Node 22 or later) | `copilot login` (`--device-code`). Token in the system store, else `~/.copilot/config.json`. No status command. `COPILOT_GITHUB_TOKEN`, then `GH_TOKEN`, then `GITHUB_TOKEN`: a fine-grained token with "Copilot Requests". Header UNVERIFIED. | `copilot --resume=<id>` (the value is optional, so `=`). `--session-id` takes a valid UUID. | `--allow-all`, `--yolo`, `--allow-all-tools`, `--allow-all-paths`, `--allow-all-urls`, the variable `COPILOT_ALLOW_ALL` | `github.com/login/*`, `api.github.com/user`, `api.github.com/copilot_internal/*`, `*.githubcopilot.com`, `copilot-proxy.githubusercontent.com` | `HTTPS_PROXY`, `HTTP_PROXY`. CA: OS store and `NODE_EXTRA_CA_CERTS`. The page covers all Copilot clients. WebSocket UNVERIFIED. | [install](https://docs.github.com/en/copilot/how-tos/copilot-cli/set-up-copilot-cli/install-copilot-cli), [commands](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-command-reference), [allowlist](https://docs.github.com/en/copilot/reference/copilot-allowlist-reference), [network](https://docs.github.com/en/copilot/concepts/network-settings) |
| Kiro CLI (3.x) | `curl -fsSL https://cli.kiro.dev/install \| bash`, binary `kiro-cli` | `kiro-cli login` (`--use-device-flow`). `kiro-cli whoami` shows the state, exit code UNVERIFIED. Store `~/.local/share/kiro-cli/data.sqlite3` (secondary: Docker's Kiro page). `KIRO_API_KEY` (`ksk_`) works only without the TUI. Header UNVERIFIED. | `kiro-cli chat --resume-id <id>`, `--resume` for the latest. IDs are UUIDs. | `--trust-all-tools`, `--trust-tools <list>` | `runtime.us-east-1.kiro.dev`, `management.us-east-1.kiro.dev`, `q.us-east-1.amazonaws.com`, `prod.us-east-1.auth.desktop.kiro.dev`, and `eu-central-1` twins. Sign-in: `app.kiro.dev`, `cognito-identity.us-east-1.amazonaws.com`. | `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY` from 1.8.0. CA, WebSocket and runtime UNVERIFIED. | [CLI](https://kiro.dev/docs/cli/), [commands](https://kiro.dev/docs/reference/cli-commands/), [auth](https://kiro.dev/docs/getting-started/authentication/), [firewalls](https://kiro.dev/docs/privacy-and-security/firewalls/), [sessions](https://kiro.dev/docs/cli/chat/session-management/), [setup](https://kiro.dev/docs/cli/setup/) |
| Factory Droid | `curl -fsSL https://app.factory.ai/cli \| sh`, `npm install -g droid` | `/login` in the REPL. No `droid login` command. Tokens in the OS keyring, with a fallback file whose path is UNVERIFIED. `FACTORY_API_KEY` (`fk-`), header UNVERIFIED. | `droid --resume <id>` (`-r`). ID format UNVERIFIED. | `--skip-permissions-unsafe`, `--auto high` (also `low`, `medium`). `--settings <path>` can set `sessionDefaultSettings.autonomyLevel`. | `api.factory.ai`, `app.factory.ai`, `auth.factory.ai`. Sign-in and refresh: `api.workos.com`, `auth.workos.com`. `relay.factory.ai` is Remote Control. | `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY`. CA: `NODE_EXTRA_CA_CERTS` or the OS store. | [quickstart](https://docs.factory.com/droid-cli/quickstart), [CLI reference](https://docs.factory.com/droid-cli/cli-reference), [exec](https://docs.factory.com/droid-exec/overview), [network](https://docs.factory.com/enterprise/network-and-deployment), [settings](https://docs.factory.com/droid-cli/settings) |
| pi (1.5.2) | `curl -fsSL https://pi.dev/install.sh \| sh`, `npm install -g --ignore-scripts @earendil-works/pi-coding-agent` (Node 22.19 or later) | `pi auth login <provider>` or `/login`. Store `~/.pi/agent/auth.json`. One variable for each provider, such as `ANTHROPIC_API_KEY`. | `pi --session <id>` (ID, prefix or path), `pi --continue`. Default IDs are UUIDv7, and `--session-id` also takes letters, digits, `.`, `_` and `-`. | None. Pi "does not ask for approval before every tool call". | None fixed: they follow the provider. | Its own undici `EnvHttpProxyAgent` from `HTTP_PROXY` and `HTTPS_PROXY`. CA: not in pi's docs. | [repository at v1.5.2](https://github.com/earendil-works/pi/tree/v1.5.2/packages/coding-agent): `README.md`, `docs/cli.md`, `docs/providers.md`, `docs/security.md`, `src/core/http-dispatcher.ts` |
| Kimi Code (2.5.3) | `curl -fsSL https://code.kimi.com/kimi-code/install.sh \| bash`, `npm install -g @moonshot-ai/kimi-code` (Node 22.19 or later), binary `kimi` | `kimi login` (device code, exits 0 or 1) or `/login`. Store `~/.kimi-code/credentials/kimi-code.json`. `KIMI_API_KEY` in `Authorization: Bearer` (source). | `kimi --session <id>` (`-S`), `kimi --continue`. IDs are `session_<UUIDv4>`. | `--auto`, `--yolo` (`-y`), with hidden aliases `--yes` and `--auto-approve` | `api.kimi.com`, `auth.kimi.com`. Platform keys: `api.moonshot.ai` or `api.moonshot.cn`. Telemetry: `telemetry-logs.kimi.com`. | Its own undici `EnvHttpProxyAgent` from `HTTPS_PROXY`, `HTTP_PROXY`, `ALL_PROXY`. CA UNVERIFIED. | [getting started](https://www.kimi.com/code/docs/en/kimi-code-cli/guides/getting-started), [kimi command](https://www.kimi.com/code/docs/en/kimi-code-cli/reference/kimi-command), [source](https://github.com/MoonshotAI/kimi-code) at `main` (2.5.3) |
| Mistral Vibe (3.1.2) | `curl -LsSf https://mistral.ai/vibe/install.sh \| bash`, `uv tool install mistral-vibe` (Python 3.12 or later) | `vibe --setup` saves `MISTRAL_API_KEY` in `~/.vibe/.env` (0600). A browser sign-in through `auth.mistral.ai` keeps tokens in the system keyring. Header UNVERIFIED. | `vibe --resume <id>`, `vibe --continue`. ID format UNVERIFIED. | `--auto-approve` (alias `--yolo`), `--agent auto-approve`, `--trust` (trusts the folder) | `api.mistral.ai`, `auth.mistral.ai` | `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY`, `NO_PROXY` (CIDR too). CA: `SSL_CERT_FILE`, `SSL_CERT_DIR`. WebSocket UNVERIFIED. | [source](https://github.com/mistralai/mistral-vibe) at `main` (3.1.2): `README.md`, `docs/proxy-setup.md`, `vibe/cli-rust/src/cli.rs` |

Notes on the table:

- Kimi Code replaced the Python `kimi-cli`, whose last release was 1.52.0 in September 2026. Both install `kimi`.
- pi's old npm package under its author's own scope is deprecated. The official package is `@earendil-works/pi-coding-agent`.
- Copilot's host list covers all Copilot clients, and it names hosts for each plan: `individual`, `business` and `enterprise`.

### 1.4 Node and the proxy variables

Docs at [v26.13.0](https://github.com/nodejs/node/blob/v26.13.0/doc/api/cli.md).

- `NODE_USE_ENV_PROXY=1` came in 24.0.0 and 22.21.0, at stability 1.1 ([cli.md](https://github.com/nodejs/node/blob/v26.13.0/doc/api/cli.md)).
- The `http` and `https` agents follow it from 24.5.0 and 22.21.0 ([http.md](https://github.com/nodejs/node/blob/v26.13.0/doc/api/http.md)).
- At start, with a proxy variable set, Node makes undici's global dispatcher an `EnvHttpProxyAgent`. That covers `fetch` ([pre_execution.js](https://github.com/nodejs/node/blob/v26.13.0/lib/internal/process/pre_execution.js)).
- WebSocket: the docs say nothing. Node's global `WebSocket` uses the global dispatcher, so it is probably covered. The `ws` package is UNVERIFIED.
- On Node: pi and Kimi Code, which set their own dispatcher, and Copilot CLI and Droid when installed with npm. Not on Node: Claude Code, Codex and Mistral Vibe. Kiro is UNVERIFIED. So `NODE_USE_ENV_PROXY` does nothing for the two first-class agents.

### 1.5 ChatGPT logins in Docker Sandboxes

- Traffic leaves through a host proxy: a forward proxy, or a transparent proxy for clients that ignore proxy settings. The proxy swaps a stand-in for the real credential in a header ([credentials](https://docs.docker.com/ai/sandboxes/configuration/credentials/), [isolation](https://docs.docker.com/ai/sandboxes/security/isolation/), 10 November 2026).
- For Codex, the sign-in runs on the host, and "the proxy handles token refresh" ([credentials](https://docs.docker.com/ai/sandboxes/configuration/credentials/)). So a header swap can carry a ChatGPT login when the trusted side also refreshes the token.
- UNVERIFIED: whether OpenAI rotates the refresh token at each refresh, which would end the laptop's own login when the same token is shared.

### 1.6 Still UNVERIFIED

- Claude Code: the OAuth header in a first-party source. Whether `--settings` can set `disableBypassPermissionsMode`.
- Codex: whether a resume by UUID needs `state_5.sqlite`. Whether the TUI reads `auth.json` without `-c cli_auth_credentials_store="file"` when no keyring exists.
- Copilot CLI: the header, any token exchange host, WebSocket use, its runtime, a status command.
- Kiro CLI: the header for `KIRO_API_KEY`, CA settings, WebSocket use, its runtime, a first-party page for its store.
- Droid: the credential file, the session ID format, the header, its runtime, and whether `--skip-permissions-unsafe` works in the TUI.
- pi and Kimi Code: CA settings in their own docs.
- Mistral Vibe: the session ID format, the header and WebSocket use.

## 2. Model APIs

Checked on 10 November 2026. SDKs read at anthropic-sdk-typescript `sdk-v1.8.0`, anthropic-sdk-python `v2.8.0`, openai-node `v7.55.0` and openai-python `v3.49.0`, all of 10 November 2026. Codex source read at `main`. Spec 7.2 and 17.8 use these facts.

### 2.1 Usage in answers

- Anthropic: `message_start` carries `message.usage` with `input_tokens`, `cache_creation_input_tokens`, `cache_read_input_tokens` and a small `output_tokens`. Each `message_delta` carries `usage`, and "the token counts shown in the `usage` field of the `message_delta` event are *cumulative*". The last delta can also hold `input_tokens`, the cache fields and `server_tool_use.web_search_requests` ([streaming](https://platform.claude.com/docs/en/build-with-claude/streaming)).
- So a counter takes the `message_start` values, then replaces each field with its value in the last `message_delta`. It never adds deltas.
- OpenAI Responses: `response.completed` carries the whole `response`, whose `usage` has `input_tokens`, `input_tokens_details.cached_tokens`, `cache_write_tokens`, `output_tokens`, `output_tokens_details.reasoning_tokens` and `total_tokens`. `response.incomplete` and `response.failed` also carry a `response` ([response_usage.py](https://github.com/openai/openai-python/blob/v3.49.0/src/openai/types/responses/response_usage.py), `response_completed_event.py` at the same tag).
- OpenAI Chat Completions: a stream has usage only when the client sets `stream_options.include_usage`. Then "an additional chunk will be streamed before the `data: [DONE]` message", and every other chunk has `usage` set to null ([create](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)). A proxy that does not change the request gets no count without it.
- `/v1/messages/count_tokens` is free. `/v1/messages/batches` returns results later, outside any count of a single request.

### 2.2 Retries, and a refusal that stops the client

| Client | Statuses it retries | Signals it reads | Long delays | Source |
| --- | --- | --- | --- | --- |
| Anthropic SDKs, TypeScript and Python | 408, 409, 429, every 5xx. 2 retries by default. | `x-should-retry` first: `"false"` stops, `"true"` forces a retry. Then `retry-after-ms`, then `retry-after`. | It obeys any positive delay: up to about 24.8 days in TypeScript, and 4,294,967 s (about 49.7 days) in Python. Without a header: 0.5 s doubling, at most 8 s, with up to 25 % jitter. | `src/client.ts` at `sdk-v1.8.0`, `_base_client.py` and `_constants.py` at `v2.8.0`, [errors](https://platform.claude.com/docs/en/api/errors) |
| OpenAI Node SDK | the same | `x-should-retry` as above | It ignores a server delay above 60 s, and uses its own backoff | `src/client.ts` at `v7.55.0` |
| OpenAI Python SDK | the same | `x-should-retry` as above | Above 120 s (`MAX_RETRY_AFTER_DELAY`) it does not retry | `_base_client.py` and `_constants.py` at `v3.49.0` |
| Claude Code | transient failures, up to 10 times (`CLAUDE_CODE_MAX_RETRIES`), throttling 429 and 529 | A gateway spend-cap 429 with `x-should-retry: false` is shown without a retry. A 429 that reports a spend limit fails at once. | `CLAUDE_CODE_RETRY_WATCHDOG=1` retries 429 and 529 without end, but not spend-limit 429s | [errors](https://code.claude.com/docs/en/errors.md), [gateway spend limits](https://code.claude.com/docs/en/claude-apps-gateway-spend-limits.md) |
| Codex | any 429 but those below | It does not read `x-should-retry`. `error.type` `usage_limit_reached`, `usage_not_included` or `insufficient_quota`, or `error.code` `insufficient_quota`, `credit_balance_exhausted`, `organization_spend_limit_exceeded`, `project_spend_limit_exceeded` or `organization_usage_limit_exceeded`, make a fatal error. | Issue reports say that it waits out `Retry-After` with no cap (secondary) | [api_bridge.rs](https://github.com/openai/codex/blob/main/codex-rs/codex-api/src/api_bridge.rs), [PR 44492](https://github.com/openai/codex/pull/44492) (merged 10 September 2026), [issue 58883](https://github.com/openai/codex/issues/58883) |

- Claude Code's apps gateway caps spend with "a `429` with `error.type: billing_error` and header `x-should-retry: false`" ([gateway spend limits](https://code.claude.com/docs/en/claude-apps-gateway-spend-limits.md)).
- A tier spend-cap 429 from Anthropic "has no `retry-after` header". An organisation spend limit is a 400 `invalid_request_error` ([errors](https://platform.claude.com/docs/en/api/errors)).
- Codex's rules were read at `main`, not at a tag. Confidence medium.

### 2.3 Error shapes

- Anthropic: `{"type":"error","error":{"type":"…","message":"…"},"request_id":"req_…"}`. Types: 400 `invalid_request_error`, 401 `authentication_error`, 402 `billing_error`, 403 `permission_error`, 404 `not_found_error`, 409 `conflict_error`, 413 `request_too_large`, 429 `rate_limit_error`, 500 `api_error`, 504 `timeout_error`, 529 `overloaded_error`. After a 200 in a stream, an error is an SSE `event: error` with the same object ([errors](https://platform.claude.com/docs/en/api/errors)).
- OpenAI: `{"error":{"message":"…","type":"…","param":null,"code":null}}`. 400 is `invalid_request_error`. 429 is `rate_limit_error` or `insufficient_quota` ([error codes](https://developers.openai.com/api/docs/guides/error-codes)). 403 `request_forbidden` comes from a captured body only (secondary: [issue 42151](https://github.com/openclaw/openclaw/issues/42151)). Confidence medium.

### 2.4 Server tools and fields that make the provider fetch

Anthropic `POST /v1/messages` (also `count_tokens` and `batches`), from the [tool reference](https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-reference):

- `tools[].type` web fetch: `web_fetch_20250910`, `web_fetch_20260209`, `web_fetch_20260309`, `web_fetch_20260318`. Web fetch takes any URL found in user messages or client tool results, so a file that the agent read counts. `url_sources` can turn the second source off ([web fetch](https://platform.claude.com/docs/en/agents-and-tools/tool-use/web-fetch-tool)).
- Web search: `web_search_20250305`, `web_search_20260209`, `web_search_20260318`. The query goes to a search provider.
- Code execution: `code_execution_20250825`, `code_execution_20260120`, `code_execution_20260521`, legacy `code_execution_20250522`. Web fetch and web search from `20260209` turn code execution on by themselves.
- The MCP connector: a top-level `mcp_servers[]` with `type: "url"`, `url`, `name` and `authorization_token`, and `tools[].type: "mcp_toolset"` with `mcp_server_name`. Beta headers `mcp-client-2025-11-20` and `mcp-client-2026-09-15`, older `mcp-client-2025-04-04` ([MCP connector](https://platform.claude.com/docs/en/agents-and-tools/mcp-connector)).
- Server-run tools with no outside fetch: `advisor_20260301`, `tool_search_tool_regex_20251119`, `tool_search_tool_bm25_20251119`.
- `image` and `document` blocks with `source.type: "url"`: the API fetches that URL ([PDF support](https://platform.claude.com/docs/en/build-with-claude/pdf-support), [vision](https://platform.claude.com/docs/en/build-with-claude/vision)).

OpenAI `POST /v1/responses`, and `response.create` in WebSocket mode, from openai-python `v3.49.0` (`types/responses/tool.py`, `web_search_tool.py`, `computer_tool.py`, `response_input_file_param.py`) and the [MCP guide](https://developers.openai.com/api/docs/guides/tools-connectors-mcp):

- `tools[].type: "mcp"` with any of `server_url`, `connector_id` or `tunnel_id`. Other fields: `server_label`, `authorization`, `headers`, `require_approval`, `allowed_tools`, `defer_loading`.
- `web_search` and `web_search_2025_08_26` (field `external_web_access`), `file_search` (`vector_store_ids`), `code_interpreter` (`container`), `computer` (run by the client), `image_generation`, `local_shell`, `programmatic_tool_calling`.
- Input items `input_image.image_url` ("A fully qualified URL") and `input_file.file_url`: the API fetches those URLs.

A new dated tool type appears every few months, so a guard that lists the refused names misses the next one. A guard that lists the allowed types fails closed.

### 2.5 WebSocket mode

- The Responses API takes `response.create` messages over a WebSocket on `/v1/responses`. A connection lasts up to 60 minutes ([WebSocket mode](https://developers.openai.com/api/docs/guides/websocket-mode.md)).
- Codex uses it by default, and falls back to HTTPS (1.2).
- UNVERIFIED: how the API reports an error on an open WebSocket, and what Codex does when the server closes the socket with code 1008.

### 2.6 Client timeouts under a hold

In explicit mode the egress process can hold a flow before it answers `CONNECT`. The client then waits under a long read or header timer. In transparent mode (microvm) the client has already sent its ClientHello, so a short connect or TLS timer runs.

| Client | Default timers | Hold before the `CONNECT` reply | Hold in the TLS handshake |
| --- | --- | --- | --- |
| Anthropic and OpenAI Python SDKs | 600 s, connect 5 s | survives (read timer) | fails at 5 s |
| Anthropic and OpenAI SDKs on Node | 600 s, undici connect 10 s, headers 300 s | survives with `NODE_USE_ENV_PROXY=1` | fails at 10 s (medium) |
| Claude Code | `API_TIMEOUT_MS` 600 s, first byte 180 s "including through an HTTPS proxy" | survives | survives (documented timers only) |
| Codex | UNVERIFIED | UNVERIFIED | UNVERIFIED |
| pip | `--timeout` 15 s, 5 attempts | fails at 15 s for each attempt | fails at 15 s for each attempt |
| uv | read 30 s, connect 10 s (`UV_HTTP_CONNECT_TIMEOUT`, 0.10.0) | probably fails at 10 s (UNVERIFIED) | fails at 10 s |
| npm | `fetch-timeout` 300 s | survives (medium) | UNVERIFIED |
| pnpm 11 | `fetchTimeout` 60 s, undici | survives (medium) | fails at 10 s (medium) |
| cargo | `http.timeout` 30 s | survives | survives, with 10 s to spare against a 20 s hold |
| Go command | `CONNECT` 1 minute, TLS 10 s | survives (medium) | fails at 10 s (medium) |

Sources: the SDKs' `_constants.py` (`Timeout(600, connect=5.0)`), and [httpcore's proxy code](https://raw.githubusercontent.com/encode/httpcore/master/httpcore/_async/http_proxy.py), where `start_tls` uses the connect timer. Also [undici Client](https://raw.githubusercontent.com/nodejs/undici/main/docs/docs/api/Client.md), [Claude Code variables](https://code.claude.com/docs/en/env-vars), [pip 26.3](https://pip.pypa.io/en/stable/cli/pip/), [uv variables](https://docs.astral.sh/uv/reference/environment/), [npm config](https://docs.npmjs.com/cli/v12/using-npm/config), [pnpm network](https://pnpm.io/settings/network), [cargo config](https://doc.rust-lang.org/cargo/reference/config.html). The SDKs now import `httpx2`, which nobody read. Go's `TLSHandshakeTimeout` of 10 s and its 1-minute `CONNECT` guard come from memory of `net/http`, and are UNVERIFIED for `cmd/go`. Confidence medium.

## 3. Package managers and registries

Checked on 10 November 2026. Spec 7.10 and 7.11 use these facts.

### 3.1 Minimum release age

| Tool | Setting | Unit | Since | Notes | Source |
| --- | --- | --- | --- | --- | --- |
| npm | `min-release-age`, also `before` (a date) | days | 11.10.0 (February 2026) | `min-release-age-exclude` exists | [config](https://docs.npmjs.com/cli/v12/using-npm/config) (12.2.2), [PR 8965](https://github.com/npm/cli/pull/8965) |
| pnpm | `minimumReleaseAge` | minutes | 10.16. Default 1440 from 11.0. | 11 reads it only from `pnpm-workspace.yaml` or `~/.config/pnpm/config.yaml` | [settings](https://pnpm.io/settings/dependency-resolution), [10.16](https://pnpm.io/blog/releases/10.16), [11.0](https://pnpm.io/blog/releases/11.0) |
| Yarn (Berry) | `npmMinimalAgeGate` | minutes, or a duration such as `"1w"` | 4.10.0 (secondary only, UNVERIFIED) | A day suffix has a reported bug (yarnpkg/berry#6991, secondary), so use a plain number | [yarnrc](https://yarnpkg.com/configuration/yarnrc) |
| Bun | `[install] minimumReleaseAge` | seconds | 1.3 (secondary only, UNVERIFIED) | `minimumReleaseAgeExcludes` | [bunfig](https://bun.com/docs/runtime/bunfig) |
| uv | `exclude-newer` | RFC 3339 time, `"7 days"` or `P7D` | relative values since 0.9.17 (9 December 2025) | Per file upload time. `UV_EXCLUDE_NEWER` mishandles a duration ([issue 18829](https://github.com/astral-sh/uv/issues/18829)). | [settings](https://docs.astral.sh/uv/reference/settings/), [issue 16814](https://github.com/astral-sh/uv/issues/16814) |
| pip | `--uploaded-prior-to` | ISO 8601 time, or `P<n>D` | 26.0. `P<n>D` from 26.1 (inferred). | Needs upload times from the index | [user guide](https://pip.pypa.io/en/latest/user_guide) |
| cargo | none in stable | | | Nightly only: `cargo generate-lockfile -Zunstable-options --publish-time`, tracked in rust-lang/cargo#15353 | [unstable](https://doc.rust-lang.org/nightly/cargo/reference/unstable.html) |
| Go | none | | | | [go ref](https://go.dev/ref/mod) |

Confidence: high for npm, pnpm, uv, pip and cargo. Medium for Yarn and Bun.

### 3.2 Install scripts and source builds

| Tool | Default for dependency scripts | Off switch | Variable | File | Source |
| --- | --- | --- | --- | --- | --- |
| npm 12 | blocked unless in `allowScripts` | `ignore-scripts=true`, which overrides `allowScripts` | `npm_config_ignore_scripts=true` | user or global npmrc | [config](https://docs.npmjs.com/cli/v12/using-npm/config), [npm 12 changes](https://github.blog/changelog/2026-06-09-upcoming-breaking-changes-for-npm-v12/) |
| npm 11 | run, with warnings from 11.16.0 | the same | the same | the same | as above |
| pnpm 10 and 11 | blocked unless in `allowBuilds`. 11 sets `strictDepBuilds: true`, which fails the install. | `ignoreScripts: true` | `pnpm_config_*` from 11 (exact names UNVERIFIED) | `~/.config/pnpm/config.yaml` | [build settings](https://pnpm.io/settings/build), [11.0](https://pnpm.io/blog/releases/11.0) |
| Yarn (Berry) | `enableScripts` is `false` by default | `enableScripts: false` | `YARN_ENABLE_SCRIPTS=false` | `~/.yarnrc.yml` | [yarnrc](https://yarnpkg.com/configuration/yarnrc) |
| Bun | blocked, except `trustedDependencies` and a built-in list | `install.ignoreScripts` | `NPM_CONFIG_IGNORE_SCRIPTS=true` | `~/.bunfig.toml` | [lifecycle](https://bun.com/docs/pm/lifecycle) |
| pip | builds source distributions, which runs their build code | `--only-binary :all:` | `PIP_ONLY_BINARY=:all:` | `/etc/pip.conf` | [pip install](https://pip.pypa.io/en/stable/cli/pip_install/) |
| uv | builds source distributions | `no-build = true` | `UV_NO_BUILD=1` (0.1.40) | `/etc/uv/uv.toml` | [uv variables](https://docs.astral.sh/uv/reference/environment/) |
| cargo | `build.rs` and procedural macros always run | none | none | none | |
| Go | no install scripts | | | | 3.4 |

- A repository can approve its own scripts: `allowScripts` in `package.json`, `allowBuilds` in `pnpm-workspace.yaml`, `trustedDependencies` in `package.json`.
- A pip requirements file can carry `--no-binary`.
- pnpm 11 reads only auth settings from `.npmrc`, and ignores every `npm_config_*` variable ([11.0](https://pnpm.io/blog/releases/11.0)).

### 3.3 Which setting wins

- npm reads the command line, then the environment, then npmrc files. So `npm_config_*` in the environment beats a repository's own `.npmrc` ([npmrc](https://docs.npmjs.com/cli/v12/configuring-npm/npmrc)).
- For pip and uv, an environment variable also outranks a project's files. Confidence high.
- For pnpm, Yarn and Bun, whether a project file beats a user file or a variable is UNVERIFIED. Confidence medium.
- Guest root and the agent itself can unset any variable. These settings are defence in depth.

### 3.4 Go

- "Since Go 1.16, the `go` command acts as if `-mod=readonly` were set". So `GOFLAGS=-mod=readonly` changes nothing. It does not stop `go get`, `go mod tidy` or `go install pkg@version` from fetching new versions ([go ref](https://go.dev/ref/mod)).
- `GOVCS` defaults to `public:git|hg,private:all`. "`GOPRIVATE` is a default value for `GONOPROXY` and `GONOSUMDB`" ([go ref](https://go.dev/ref/mod)).
- `GOTOOLCHAIN=auto` is the default. It "downloads and runs the specified Go toolchain" that `go.mod` asks for, as the module `golang.org/toolchain`, checked against the checksum database. `local` always uses the bundled toolchain ([toolchains](https://go.dev/doc/toolchain)).
- The go command runs no module code when it downloads or builds. `go generate` runs only by hand (from memory of [cmd/go](https://pkg.go.dev/cmd/go), not read again). cgo runs the C compiler with flags from an allow list. Confidence medium for the cgo note.
- So a sandbox can set `GOTOOLCHAIN=local`, `GOPROXY=https://proxy.golang.org` with no `,direct`, `GOSUMDB=sum.golang.org` (the default) and `GOVCS=*:off`, and leave `GONOSUMDB`, `GOPRIVATE`, `GONOPROXY` and `GOINSECURE` unset.

### 3.5 Publish times that a gate can trust

- npm: the full packument's `time` maps each version to its publish time, plus `created` and `modified`. Abbreviated metadata (`application/vnd.npm.install-v1+json`) has only `modified`, so a gate fetches the full packument itself ([package metadata](https://github.com/npm/registry/blob/main/docs/responses/package-metadata.md)). The full packument of typescript was 15.9 MB on 9 November 2026 ([isolation note](isolation.md#81-registry-firewall-node-31)).
- PyPI: the JSON API gives `upload_time_iso_8601` for each file, and the Simple JSON index gives `upload-time` for each file ([JSON API](https://docs.pypi.org/api/json/), [index API](https://docs.pypi.org/api/index-api/)). A new wheel on an old release is new, so a gate checks each file.
- crates.io: `/api/v1/crates/{name}/{version}` gives `version.created_at` (live answer for [serde 1.0.0](https://crates.io/api/v1/crates/serde/1.0.0)). The index format has an optional `pubtime` field ([registry index](https://doc.rust-lang.org/cargo/reference/registry-index.html)). Which entries carry `pubtime` is UNVERIFIED.
- Go: the `.info` `Time` is the commit time, and optional. A module's author sets the commit date, so it can be backdated ([go ref](https://go.dev/ref/mod)). index.golang.org gives `Timestamp`, "the time it was first cached by proxy.golang.org". The index is a feed with a `since` parameter, not a lookup for one module ([index](https://index.golang.org/)).
- UNVERIFIED: how soon index.golang.org lists a version after its first fetch, and how many entries a 7-day window holds.

## 3a. Live policy changes

Checked on 10 November 2026. Spec 8.8 uses these facts.

- **Docker Sandboxes, approvals.** "The request that triggered the approval doesn't wait for your answer. It's denied, and approving the destination affects later requests." "Agents that retry a failed request pick up the new access on their next attempt." The sandbox gets the message "Approval required for api.example.com". An approval covers only the sandbox that asked ([approvals](https://docs.docker.com/ai/governance/local/approvals/)).
- **Docker Sandboxes, organisation policy.** "Changes take up to 5 minutes to reach developer machines." "The refreshed policy applies to new connections only." A connection that the old policy allowed "stays open even if the new policy denies it", and "Restart the sandbox to close those connections". `sbx policy refresh` skips the wait ([troubleshooting](https://docs.docker.com/ai/governance/troubleshooting/)).
- **Docker Sandboxes, local policy.** A deny from any source wins over an allow, and a rule is global or scoped to one sandbox ([policy](https://docs.docker.com/ai/sandboxes/security/policy/)). The local pages do not say when `sbx policy allow` or `deny` reaches a running sandbox. UNVERIFIED.
- **Envoy, listener updates.** "When a listener is updated, an entirely new listener is created", and warmed first. The old listener drains its connections for `--drain-time-s`, then closes the rest. An in-place update of a TCP listener keeps the connections of filter chains that did not change ([LDS](https://www.envoyproxy.io/docs/envoy/latest/configuration/listeners/lds), docs 1.41.0-dev).
- **Envoy, network RBAC.** By default the policy applies "once when the first byte of data arrives". `CONTINUOUS` applies it "on every message boundary" ([RBAC](https://www.envoyproxy.io/docs/envoy/latest/api-v3/extensions/filters/network/rbac/v3/rbac.proto), docs 1.41.0-dev).
- **OpenSnitch.** A change to the configuration or to the rules in `/etc/opensnitchd/rules/` reloads them "automatically. No restart is needed." `FlushConnsOnStart` kills established connections at each reload ([configurations](https://github.com/evilsocket/opensnitch/wiki/Configurations)). The daemon's flag `-no-live-reload` turns the reload off ([man page](https://manpages.debian.org/trixie/opensnitch/opensnitchd.1.en.html), Debian package 1.6.9-3). Its prompt timeout is UNVERIFIED: sources give 15 s and 30 s.
- **What follows.** No comparable tool documents a time for a local change to reach a running proxy. Two of the three keep open connections under the old rule unless told otherwise. The 2 s target in spec 12 is this design's own target, which P2.14 measures.

## 4. Linux walls

Checked on 10 November 2026. Source code was read at the named tag or branch. The spec uses these facts in sections 4.1, 5.1, 5.6, 11 and 14.4.

### 4.1 Ubuntu's user-namespace restriction

- An unconfined process that makes a user namespace moves to the profile `unprivileged_userns`, which has `audit deny capability`. So `unshare(CLONE_NEWUSER)` succeeds, but the new namespace has no capability ([unprivileged_userns](https://gitlab.com/apparmor/apparmor/-/raw/master/profiles/apparmor.d/unprivileged_userns), master, November 2026).
- A profile with `userns,` (or `allow userns create,`) lets its program make user namespaces with capabilities ([`AppArmor` wiki](https://gitlab.com/apparmor/apparmor/-/wikis/unprivileged_userns_restriction)).
- The defaults come from `/usr/lib/sysctl.d/10-apparmor.conf` in Ubuntu's `apparmor` package ([noble-updates](https://git.launchpad.net/ubuntu/+source/apparmor/plain/debian/usr/lib/sysctl.d/10-apparmor.conf?h=applied/ubuntu/noble-updates), and the branches `plucky-updates`, `questing-updates` and `resolute-updates`):

| Release | `restrict_unprivileged_userns` | `restrict_unprivileged_unconfined` | `bwrap-userns-restrict` loaded |
| --- | --- | --- | --- |
| 23.10 | opt-in | 0 | no |
| 24.04 | 1 | 0 | no: it is in `apparmor-profiles` extras |
| 25.04, 25.10, 26.04 | 1 | 1 | yes |

- Ubuntu's `apparmor` 4.1.0~beta4-0ubuntu1 (plucky, 11 February 2025) sets `restrict_unprivileged_unconfined=1`, moves `bwrap-userns-restrict` out of extras, and deletes the busybox and nautilus profiles (Ubuntu `apparmor` changelog). 26.04 ships `unshare-userns-restrict` commented out.
- Ubuntu's own fix for bubblewrap uses two profiles. The `bwrap` profile may make the namespace and use capabilities. It starts every program through the stack `pix /** -> &bwrap//&unpriv_bwrap`, "stacked like this due to no-new-privs restriction". The child profile `unpriv_bwrap` has `audit deny capability`, so "its children do not have capabilities" ([bwrap-userns-restrict](https://gitlab.com/apparmor/apparmor/-/raw/master/profiles/apparmor/profiles/extras/bwrap-userns-restrict), master, November 2026, `abi <abi/5.0>`).
- Ubuntu calls an unconfined profile that allows `userns` for a program that can run arbitrary code "a trivial bypass" ([Ubuntu Discourse](https://discourse.ubuntu.com/t/understanding-apparmor-user-namespace-restriction/58007), 27 March 2025).
- Qualys's bypass 3 preloads a library into a program whose profile allows `userns` ([Qualys](https://www.qualys.com/2025/three-bypasses-of-Ubuntu-unprivileged-user-namespace-restrictions.txt), 27 March 2025). A static binary has no dynamic loader, so `LD_PRELOAD` does not reach it.
- So a launcher that keeps capabilities and then starts a binary that the login user can write is a bypass in Ubuntu's sense. The fix is the two-profile pattern above. The launcher does every step that needs a capability, and its children run in a child profile that denies capabilities.
- UNVERIFIED: what an unconfined process on 24.04 and 25.10 can still do after `unshare(CLONE_NEWUSER)`. To check: run `unshare -Urn true` and `unshare -Urm mount -t tmpfs t /mnt` as a normal user. P1.5's test checks it.
- UNVERIFIED: that the stacked transition works with the `AppArmor` 4.0 of Ubuntu 24.04. P1.5's test checks it.

### 4.2 What a jail needs a user namespace for

- Enforcing a Landlock ruleset needs `CAP_SYS_ADMIN` in the task's namespace or `no_new_privs`. So a jail gets Landlock with no privilege ([Landlock](https://docs.kernel.org/userspace-api/landlock.html), 7.4.0-rc2). `no_new_privs`, `PR_SET_DUMPABLE` and seccomp need no privilege either.
- Landlock alone stops `ptrace` of processes outside the domain: "the tracee must be in a sub-domain of the tracer" (same page).
- Mount, network and other namespaces need `CAP_SYS_ADMIN` in the owning user namespace ([user_namespaces(7)](https://man7.org/linux/man-pages/man7/user_namespaces.7.html)).
- Landlock ABIs and kernels ([rust-landlock compat.rs](https://github.com/landlock-lsm/rust-landlock/blob/main/src/compat.rs), main, November 2026): ABI 4 is 6.7, ABI 6 is 6.12 and ABI 7 is 6.15. ABI 8 is 7.0, ABI 9 is 7.1 and ABI 10 is 7.2. ABI 12 adds `LANDLOCK_PERMISSION_NAMESPACE_USE` (7.4.0-rc2 docs). Probe the ABI, never the kernel version.

| Jail feature | Needs a user namespace | Without one |
| --- | --- | --- |
| `Binds` (a new mount namespace) | yes | Landlock refuses access, but the paths stay visible |
| `NetNone` (an empty network namespace) | yes | seccomp refuses `AF_INET` and `AF_INET6`. Landlock TCP rules need ABI 4 (6.7), UDP rules ABI 10 (7.2). |
| `Subuid` | yes, through `newuidmap` | none |
| Hide `$XDG_RUNTIME_DIR` and the SSH agent's socket | a mount namespace | Landlock controls `connect` to a Unix socket path only from ABI 9 (7.1), `LANDLOCK_ACCESS_FS_RESOLVE_UNIX` |
| The host's abstract Unix sockets | a network namespace hides them | Landlock scopes them from ABI 6 (6.12) |

- Ubuntu 24.04's kernel 6.8 has Landlock ABI 4. Without a namespace, a jail there can still reach the user's systemd bus, the SSH agent's socket and the host's abstract sockets.

### 4.3 `/dev/kvm`, its ACL and the `kvm` group

- Upstream systemd sets `/dev/kvm` to 0666 since v236 (December 2017): "/dev/kvm ... now have the 0666 permissions by default" ([NEWS](https://github.com/systemd/systemd/blob/main/NEWS), "CHANGES WITH 236"). The rule is `KERNEL=="kvm", GROUP="kvm", MODE="{{DEV_KVM_MODE}}"`, and the option `dev-kvm-mode` defaults to `'0666'` ([meson_options.txt](https://github.com/systemd/systemd/blob/main/meson_options.txt)).
- Debian 12, 13 and unstable, and Ubuntu 22.04 to 26.04, build systemd with `-Ddev-kvm-mode=0660`. So `/dev/kvm` is 0660 `root:kvm` on both ([Debian trixie rules](https://salsa.debian.org/systemd-team/systemd/-/blob/debian/trixie/debian/rules), 257.9-1~deb13u1, also `debian/bookworm`, and [Ubuntu noble rules](https://git.launchpad.net/ubuntu/+source/systemd/plain/debian/rules?h=applied/ubuntu/noble-updates), 255.4-1ubuntu8.11, also `questing` and `devel`).
- A udev `change` event keeps an ACL entry ([udev-node.c](https://github.com/systemd/systemd/blob/main/src/udev/udev-node.c), main). udev calls `fchmod` only when the mode bits differ, and nothing in it touches ACLs. This is source reading. UNVERIFIED by test: run `udevadm trigger --action=change /dev/kvm`, then `getfacl /dev/kvm`. H7's test checks it.
- A reboot loses the ACL, because `/dev` is a devtmpfs, "a tmpfs/ramfs filesystem instance early at bootup" ([drivers/base/Kconfig](https://github.com/torvalds/linux/blob/master/drivers/base/Kconfig), `DEVTMPFS`).
- Firecracker's guide grants `/dev/kvm` with the `kvm` group or with `setfacl` ([getting started](https://github.com/firecracker-microvm/firecracker/blob/main/docs/getting-started.md)).
- The `kvm` group also opens `/dev/vhost-net` and `/dev/vhost-vsock`, which share its group and mode.
- A process keeps its supplementary groups in a user namespace unless something calls `setgroups`. crun's `run.oci.keep_original_groups` depends on this ([crun.1](https://github.com/containers/crun/blob/main/crun.1.md)). Under a systemd user manager, the groups are those that the manager had when it started: "only holds the groups the user had when that manager was started" ([podman run, keep-groups](https://docs.podman.io/en/latest/markdown/podman-run.1.html)).
- So an ACL that names the login uid does not cover a VMM that runs as a subuid. That VMM reaches `/dev/kvm` only through the `kvm` group, and only after the user manager has restarted with that group.
- A `systemd-tmpfiles` line can set an ACL without `setfacl`: `a+ /dev/kvm - - - - u:<user>:rw` in `/etc/tmpfiles.d/`, applied at once with `systemd-tmpfiles --create <file>` ([tmpfiles.d](https://www.freedesktop.org/software/systemd/man/latest/tmpfiles.d.html), "a, a+ ... Set POSIX ACLs"). UNVERIFIED: that the boot-time pass over `/dev` applies it again after a reboot. P3.7 tests it on a VM that the test makes ([question 79](../contributing/isolation-spec.md#15-open-questions)).

### 4.4 Packages on server images

- `setfacl` is in the package `acl`, with Debian priority `optional` ([acl 2.3.2-2 control](https://sources.debian.org/data/main/a/acl/2.3.2-2/debian/control)).
- `newuidmap` and `newgidmap` are in the package `uidmap`.
- Ubuntu server cloud images 24.04, 25.10 and 26.04, and the Debian 13 generic cloud image (build 20261103), include neither `acl` nor `uidmap`. They do include `libacl1` ([noble manifest](https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.manifest), also `questing` and `resolute`, and the [Debian 13 image list](https://cloud.debian.org/images/cloud/trixie/latest/debian-13-generic-amd64.json)).
- `usermod --add-subuids` is in the package `passwd`, which these images include. A subuid range without `newuidmap` gives a jail nothing.

### 4.5 cgroup delegation to the user manager

- `user@.service` has `Delegate=pids memory cpu` today ([user@.service.in](https://github.com/systemd/systemd/blob/main/units/user%40.service.in), main). `pids` and `memory` date from about v238 (commit `acc8059`, 1 March 2018). `cpu` came in v252: "The cpu controller is delegated to user manager units by default" ([NEWS](https://github.com/systemd/systemd/blob/main/NEWS), "CHANGES WITH 252"). `io` and `cpuset` are not delegated by default.
- The user manager reads its available controllers only when it starts. In v257, `manager_setup_cgroup` runs from `manager_new`, not from `manager_reload` ([cgroup.c](https://github.com/systemd/systemd/blob/v257/src/core/cgroup.c), [manager.c](https://github.com/systemd/systemd/blob/v257/src/core/manager.c)). A re-execution counts as a start.
- `systemctl --user daemon-reexec` re-executes the user manager as the user, with no root. Running units stay. The manager re-executes on `SIGRTMIN+25` for system and user managers since v250 (NEWS, "CHANGES WITH 250").
- On systemd 257 and later, `systemctl reload user@<uid>.service` also re-executes it (`ReloadSignal=RTMIN+25` in `user@.service.in` at v257). v255 and v256 lack it. Ubuntu 24.04 has 255, and Debian 12 has 252.
- UNVERIFIED: that PID 1 gives the running `user@<uid>.service` the `io` controller at `daemon-reload`. To check: add the drop-in, run `daemon-reload`, check that `user@<uid>.service/cgroup.controllers` lists `io`, run `systemctl --user daemon-reexec`, then write a session's `io.max`. H7's test checks it.

### 4.6 Netlink in the seccomp profile

- glibc's `getifaddrs` opens a `NETLINK_ROUTE` socket and fails when it cannot ([ifaddrs.c](https://sourceware.org/git/?p=glibc.git;a=blob;f=sysdeps/unix/sysv/linux/ifaddrs.c), HEAD). libuv's `uv_interface_addresses` returns that error: `if (getifaddrs(&addrs)) return UV__ERR(errno);` ([linux.c](https://github.com/libuv/libuv/blob/v1.x/src/unix/linux.c), 1.53.1).
- So Node's `os.networkInterfaces()` throws `ERR_SYSTEM_ERROR` when netlink is refused ([os.js](https://github.com/nodejs/node/blob/main/lib/os.js), `getCheckedFunction`). Go's `net.Interfaces` also uses netlink ([interface_linux.go](https://github.com/golang/go/blob/master/src/net/interface_linux.go), `NetlinkRIB`).
- glibc's `getaddrinfo` with `AI_ADDRCONFIG` survives a refused netlink: it assumes both families ("Be pessimistic", `check_pf.c`).
- A service that refused `AF_NETLINK` failed with "uv_interface_addresses returned Unknown system error 97" until `AF_NETLINK` was allowed (secondary: [peertube_ynh commit](https://git.yunohost.org/YunoHost-Apps/peertube_ynh/commit/74e1a030660ddfe317180f3c2c512b78a4be8d2a)).
- Docker's default seccomp profile allows `AF_NETLINK`. The old form refused only `AF_VSOCK` (40) ([moby v28.0.0](https://github.com/moby/moby/blob/v28.0.0/profiles/seccomp/default.json)). The current form is an allowlist that includes 16 and leaves out 38 and 40 ([moby/profiles](https://github.com/moby/profiles/blob/main/seccomp/default.json), main). Flatpak always allows `AF_NETLINK` ([flatpak-run.c](https://github.com/flatpak/flatpak/blob/main/common/flatpak-run.c), `socket_family_allowlist`).
- In an empty network namespace, a `NETLINK_ROUTE` read shows only `lo`. A change needs `CAP_NET_ADMIN` over the namespace: `if (kind != RTNL_KIND_GET && !netlink_net_capable(skb, CAP_NET_ADMIN)) return -EPERM;` ([rtnetlink.c](https://github.com/torvalds/linux/blob/master/net/core/rtnetlink.c), 7.4-rc2).
- So a process wall can allow `socket(AF_NETLINK, *, NETLINK_ROUTE)` and refuse every other netlink protocol, such as `NETLINK_NETFILTER`, `NETLINK_AUDIT`, `NETLINK_KOBJECT_UEVENT` and `NETLINK_SOCK_DIAG`.

### 4.7 gVisor without root

Read at gVisor `release-20261102.0`:

- `--rootless` re-executes `runsc` in a new user namespace that maps only the caller to root. It needs unprivileged user namespaces, so Ubuntu's restriction applies. `runsc create` refuses it: "Rootless mode not supported with create". `runsc run` refuses it with `--network=sandbox`, and accepts `--network=none` or `host` ([create.go](https://github.com/google/gvisor/blob/release-20261102.0/runsc/cmd/create.go), [run.go](https://github.com/google/gvisor/blob/release-20261102.0/runsc/cmd/run.go)). It has no save and restore, and it ignores cgroup errors.
- `--network` takes `sandbox` (the default), `host` or `none`. `--ignore-cgroups` exists. `--host-uds` takes `none|open|create|all`, default `none`. `--directfs` defaults to true ([flags.go](https://github.com/google/gvisor/blob/release-20261102.0/runsc/config/flags.go)).
- `--pass-fd` exists on `create`, and so on `run`, and on `exec`, as `M:N` (host:guest). It may be given more than once (`create.go`, `exec.go`).
- The OCI user-namespace mode ("Method 3") needs `newuidmap` for more than one id. The guide says that it "currently lacks support for network namespacing" ([rootless.md](https://github.com/google/gvisor/blob/release-20261102.0/g3doc/user_guide/rootless.md)).
- A non-root caller with `--network=none` and `--directfs=false` makes its own user namespace (`nobody` mapped to the caller) and network namespace ([namespace.go](https://github.com/google/gvisor/blob/release-20261102.0/runsc/specutils/namespace.go), `MaybeRunAsRoot`, [sandbox.go](https://github.com/google/gvisor/blob/release-20261102.0/runsc/sandbox/sandbox.go)). The guide and the code differ here, so this is medium confidence.
- So both `runsc --rootless` and a non-root `runsc` call `unshare(CLONE_NEWUSER)`. On Ubuntu 23.10 or later, the gvisor wall needs the same `AppArmor` step as the process wall. With the namespace launcher, `runsc` runs as uid 0 inside namespaces that the launcher made ("Method 2").

## 5. The microvm and its build

Checked on 10 November 2026. Kernel docs were read as rendered for Linux 7.4.0-rc2. Source code was read at the named tag or branch. Spec 20 uses these facts, with spec 12 and 13.

### 5.1 The guest kernel's options

- These symbols exist in Linux 7.4-rc2: `CONFIG_VMGENID`, `CONFIG_MEMCG`, `CONFIG_SECURITY_LANDLOCK`, `CONFIG_VIRTIO_BALLOON`, `CONFIG_VSOCKETS`, `CONFIG_VIRTIO_VSOCKETS`, `CONFIG_DM_VERITY`, `CONFIG_DM_INIT` and `CONFIG_EROFS_FS`. `MEMCG` needs `CGROUPS`, and `VIRTIO_BALLOON` selects `PAGE_REPORTING`. The sources are the [virt Kconfig](https://github.com/torvalds/linux/blob/master/drivers/virt/Kconfig), and the `Kconfig` files of `init`, `security`, `drivers/virtio`, `net/vmw_vsock`, `drivers/md` and `fs/erofs`.
- Landlock works only when `landlock` is also in `CONFIG_LSM`. The upstream default puts it first.
- `CONFIG_VSOCKETS_LOOPBACK` is `default y`. A config must turn it off by name.
- `CONFIG_DM_INIT` depends on `BLK_DEV_DM=y`, and gives the boot parameter `dm-mod.create=` ([dm-init](https://docs.kernel.org/admin-guide/device-mapper/dm-init.html)).
- dm-init allows the `verity` target. Its format is `<name>,<uuid>,<minor>,<flags>,<table>`, with a semicolon between devices. `dm-mod.waitfor=<device>,…` waits for block devices that probe late ([dm-init](https://docs.kernel.org/admin-guide/device-mapper/dm-init.html)).
- `panic_on_corruption` is a documented verity option ([verity](https://docs.kernel.org/admin-guide/device-mapper/verity.html)).
- On x86-64, Cloud Hypervisor boots a `vmlinux` ELF built with PVH support, or a bzImage. On AArch64 its README boots `arch/arm64/boot/Image` ([README](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/README.md), main, 10 November 2026).
- moby's `contrib/check-config.sh` takes a kernel config path as its first argument. It prints a "Generally Necessary" section for Docker, which starts with the namespaces, `CGROUPS`, `MEMCG`, `KEYS` and `VETH` ([check-config.sh](https://github.com/moby/moby/blob/master/contrib/check-config.sh), master, 10 November 2026).
- UNVERIFIED: whether Firecracker or Cloud Hypervisor publishes a guest config for Linux 6.18. P3.1 step 1 checks it.

### 5.2 Cloud Hypervisor

- The latest release is v54.0, of 14 October 2026 ([v54.0](https://github.com/cloud-hypervisor/cloud-hypervisor/releases/tag/v54.0)). In it:
  - "The main thread now runs under its own seccomp filter once it has started the worker threads." Before, the main thread had no filter (#9062).
  - "`image_type` must now be specified for every disk that is not a `vhost-user` disk." Format autodetection, deprecated since v52.0, is gone.
  - "VMs now have a VM Generation ID device." It is always present. A guest needs `CONFIG_VMGENID` to reseed after a restore.
  - Unaligned requests to raw disks with `direct=on` "now fail with an I/O error". Before, the VMM emulated them.
- The release assets are `cloud-hypervisor-static` (7.02 MB) and `cloud-hypervisor-static-aarch64` (5.55 MB), with `ch-remote` builds and a source archive `cloud-hypervisor-v54.0.tar.xz` ([assets](https://github.com/cloud-hypervisor/cloud-hypervisor/releases/expanded_assets/v54.0)).
- A major release comes about every 6 weeks. It "would receive bug fixes for the next two cycles (~12 weeks) and then be considered EOL" ([releases.md](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/releases.md)). v54.0 is then supported until about 6 January 2027 (computed).
- Security fixes come in a point release, after an embargo of up to 14 days ([SECURITY.md](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/SECURITY.md)).
- GHSA-f47p-p25q-83rh (CVE-2026-45782, High, CVSS 4.0 8.9, 14 May 2026): a use-after-free in virtio-block async I/O, the default, which allows a guest escape. Affected v21.0 to v51.1. Fixed in v51.2 and v52.0 ([advisory](https://github.com/cloud-hypervisor/cloud-hypervisor/security/advisories/GHSA-f47p-p25q-83rh)).
- GHSA-jmr4-g2hv-mjj6 (CVE-2026-27211, High, 7.2, 20 February 2026): host file reads through a QCOW2 backing file. Affected v34.0 to v50.0. Fixed in v50.1 and v51.0 ([advisory](https://github.com/cloud-hypervisor/cloud-hypervisor/security/advisories/GHSA-jmr4-g2hv-mjj6)).
- No advisory is newer than GHSA-f47p on 10 November 2026. So v52.0 is the advisory floor, and v52.0 is out of support.
- Disk options include `readonly`, `direct`, `backing_files`, `sparse`, `serial` and `image_type` ([config.rs](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/vmm/src/config.rs)). The balloon takes `size`, `deflate_on_oom` and `free_page_reporting`.
- vsock: the host connects to the VMM's Unix socket and sends `CONNECT <port>` once for each connection. A guest connection to the host reaches the Unix socket `<path>_<port>` only when something listens there. The option is `--vsock cid=3,socket=<path>` ([vsock.md](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/vsock.md)). The page shows no reply to `CONNECT`. Firecracker documents `OK <port>` for the same hybrid design ([Firecracker vsock](https://github.com/firecracker-microvm/firecracker/blob/main/docs/vsock.md)). UNVERIFIED for Cloud Hypervisor: P3.3's link test reads the reply.
- The README sets the minimum host kernel at 6.8 ([README](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/README.md)).

### 5.3 Firecracker

- CVE-2026-122192 (GHSA-48ww-rqpp-2mgm, High, CVSS 3.1 7.5, 4 November 2026): affected 0.23.0 to 1.16.1, fixed in 1.16.2 and 1.17.0. It applies only to a VM restored from a snapshot with the File memory backend and a balloon or virtio-mem device ([advisory](https://github.com/firecracker-microvm/firecracker/security/advisories/GHSA-48ww-rqpp-2mgm)). The design cold-boots, so it is not exposed.
- CVE-2026-5747 (GHSA-776c-mpj7-jm3r, CVSS 4.0 8.7, 7 April 2026): affected 1.13.0 to 1.14.3 and 1.15.0, fixed in 1.14.4 and 1.15.1. PCI is opt-in, with `--enable-pci` ([advisory](https://github.com/firecracker-microvm/firecracker/security/advisories/GHSA-776c-mpj7-jm3r)).
- 1.13.0 added virtio-pci as an opt-in (#5364). 1.18.0 (9 November 2026, the latest) adds Nitro Sandboxes (#6393): `/dev/nitro_enclaves` and a `PUT /enclave` API ([CHANGELOG](https://github.com/firecracker-microvm/firecracker/blob/main/CHANGELOG.md)).
- A Nitro sandbox is "PCIe only. The legacy MMIO transport is not available" ([comparison with KVM](https://github.com/firecracker-microvm/firecracker/blob/firecracker-v1.18/docs/nitro-sandboxes/comparison-with-kvm.md)).
- The parent kernel is Amazon Linux 2023 `kernel6.18` from `6.18.51-120.162.amzn2023`. The guide lists `cpu_pool/`, `mem_pool/` and the `pcie_slots` files under `/sys/class/misc/nitro_enclaves/`, but does not call them a test of support. Its sign of an old driver is the start error "Failed to query hypervisor-supported flags" ([getting started](https://github.com/firecracker-microvm/firecracker/blob/firecracker-v1.18/docs/nitro-sandboxes/getting-started.md)). UNVERIFIED: the sysfs files as a sign of support.
- Support for the 1.16 line ends on 3 December 2026 ([release policy](https://github.com/firecracker-microvm/firecracker/blob/main/docs/RELEASE_POLICY.md)).
- The specification bounds the VMM's memory overhead at 5 MiB or less, with 1 vCPU and 128 MiB. Vsock connections can raise it ([SPECIFICATION.md](https://github.com/firecracker-microvm/firecracker/blob/main/SPECIFICATION.md)).

### 5.4 Images: `tar2ext4` and dm-verity

- `github.com/Microsoft/hcsshim/ext4/tar2ext4` has `Convert(r, w, options…)`. `AppendDMVerity` is an option to `Convert`, not to `ConvertTarToExt4`. `MaximumDiskSize` defaults to 16 GB. `ConvertWhiteout` turns OCI whiteouts into overlay whiteouts ([tar2ext4.go](https://github.com/microsoft/hcsshim/blob/v0.15.0/ext4/tar2ext4/tar2ext4.go)).
- `AppendDMVerity` calls `dmverity.ComputeAndWriteHashDevice`. It writes a verity superblock, padding to 4096 bytes, then the tree, after the file system. The hash is SHA-256. The salt is 32 zero bytes. `MerkleTree` and `RootHash` are exported ([dmverity.go](https://github.com/microsoft/hcsshim/blob/v0.15.0/ext4/dmverity/dmverity.go)).
- The verity superblock has a random UUID from `crypto/rand`. So two conversions of one tar differ in the file, but not in the root hash ([dmverity.go](https://github.com/microsoft/hcsshim/blob/v0.15.0/ext4/dmverity/dmverity.go)).
- The ext4 part looks deterministic for one tar stream. Its superblock has no UUID and no times. Directory entries and xattrs are sorted. Inode numbers follow the tar's order, and times come from its headers ([compact.go](https://github.com/microsoft/hcsshim/blob/main/ext4/internal/compactext4/compact.go)). The image is marked `RO_COMPAT_READONLY`. Confidence medium: this comes from reading the source. P3.2's test converts one tar twice.
- The licence is MIT ([LICENSE](https://github.com/microsoft/hcsshim/blob/v0.15.0/LICENSE)). The latest release is v0.15.0, of 15 October 2026. Its `go.mod` says `go 1.26.8` ([proxy](https://proxy.golang.org/github.com/!microsoft/hcsshim/@latest)).
- A verity table can name one file for both the data and the tree. Its hash start block is then the count of data blocks. A verity superblock before the tree adds one block (inferred from the layout above).

### 5.5 Reproducible kernels and EROFS images

- The kernel's documented steps ([reproducible builds](https://docs.kernel.org/kbuild/reproducible-builds.html)):
  1. Set `KBUILD_BUILD_TIMESTAMP`. It covers the version string, the initramfs and `CONFIG_IKHEADERS`.
  2. Set `KBUILD_BUILD_USER` and `KBUILD_BUILD_HOST`.
  3. Set `SOURCE_DATE_EPOCH` for outside code that uses `__DATE__`.
  4. In an out-of-tree build, add `-fdebug-prefix-map` to `KCFLAGS` and `KAFLAGS`, and turn off options that embed paths.
  5. Module signing differs between builds. `CONFIG_MODULES=n` avoids it.
  6. With `CONFIG_RANDSTRUCT`, give a fixed `scripts/basic/randstruct.seed`.
  7. Git state changes `kernel.release` through `scripts/setlocalversion`.
- `mkfs.erofs`, by erofs-utils version ([man page](https://github.com/erofs/erofs-utils/blob/dev/man/mkfs.erofs.1), [ChangeLog](https://github.com/erofs/erofs-utils/blob/dev/ChangeLog)):
  - `SOURCE_DATE_EPOCH` since 1.2 (December 2020). It clamps times, and does not set every time to the epoch ([main.c](https://github.com/erofs/erofs-utils/blob/dev/mkfs/main.c)).
  - `--all-root`, `--force-uid`, `--force-gid` and `-T` since 1.3 (June 2021). Since at least 1.8.3, `-T` alone acts as `--all-time`.
  - `-U <clear|random>` since 1.8.3 (December 2024). `-U` needs a value.
  - Reproducibility fixes: `i_ino` (1.8.3), fragments (1.8.7) and sorted inline xattrs (1.9.3, 5 August 2026). The latest is 1.9.4, of 19 August 2026.

### 5.6 Memory that a cgroup charges

- Cloud Hypervisor's integration test asserts a VMM overhead of 10 MiB or less (12 MiB with SEV-SNP), for a 512 MiB guest. It sums the RSS of every mapping except guest RAM, from `/proc/<pid>/smaps` ([utils.rs](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/cloud-hypervisor/tests/common/utils.rs), `test_memory_overhead` in `tests/integration.rs`).
- cgroup v2 charges a memory area "to the cgroup which instantiated it". `memory.stat` reports `file` (the page cache, tmpfs and shared memory), `pagetables`, and `sec_pagetables`, which "currently includes KVM mmu allocations on x86 and arm64" ([cgroup v2](https://docs.kernel.org/admin-guide/cgroup-v2.html)).
- So host page cache from a VMM's disk reads counts against the session's cap. Cloud Hypervisor's `direct=on` opens a disk without the page cache ([config.rs](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/vmm/src/config.rs)).
- One page-table entry is 8 bytes for each 4 KiB page. Two tables map guest RAM: the VMM's own, and KVM's. That is at most 1/256 of guest RAM (computed). Huge pages make it much smaller.
- No primary source gives a figure for the supervisor, the egress process or the guest's own kernel and processes. The research's 55 to 90 MiB and 10 to 20 MiB are estimates (UNVERIFIED, [isolation research](isolation.md#10-platforms)). P3.8 measures them.

### 5.7 fs-verity

- ext4 and f2fs support fs-verity since Linux 5.4, and btrfs since 5.15. ext4 needs the `verity` feature: `mkfs.ext4 -O verity` or `tune2fs -O verity` ([fs-verity](https://docs.kernel.org/filesystems/fsverity.html)).
- `FS_IOC_ENABLE_VERITY` needs write access to the inode, not root. It must run on a read-only descriptor, and fails with `ETXTBSY` while any process has the file open for writing.
- A verity file cannot be opened for writing or truncated, whatever its mode. Its flag cannot be cleared on ext4 or f2fs.
- `FS_IOC_MEASURE_VERITY` returns the file's fs-verity digest in constant time. The digest is a hash of a descriptor that holds the Merkle tree's root hash and the file's size. It is not the SHA-256 of the file.
- UNVERIFIED: whether Debian and Ubuntu make ext4 with `verity` by default. P4.2's probe tries the ioctl and does not assume.

### 5.8 Reflinks

- ext4 has no `remap_file_range`, so it does not support `FICLONE` ([ext4 file.c](https://github.com/torvalds/linux/blob/master/fs/ext4/file.c), 7.4-rc2). XFS and Btrfs set it.
- `mkfs.xfs` turns reflink on by default since xfsprogs 5.1.0 (19 July 2019), and it needs `crc=1` ([CHANGES](https://git.kernel.org/pub/scm/fs/xfs/xfsprogs-dev.git/plain/doc/CHANGES)).
- bcachefs supports reflinks, but left mainline in Linux 6.18 and ships as a DKMS module (secondary, [LWN](https://lwn.net/Articles/1040120)).
- OpenZFS has block cloning with `FICLONE` from 2.2 ([zfs-2.2.3](https://github.com/openzfs/zfs/releases/tag/zfs-2.2.3)). Its default changed between releases (UNVERIFIED for current releases).

## 6. Pins, advisories and signatures

Checked on 10 November 2026. Spec 20.7 to 20.9 and questions 30, 38 and 42 use these facts.

### 6.1 Ghostty and its advisories

Checked on 10 November 2026. The spec uses these facts in sections 3.2, 19.1 and 19.5.

- The shadow terminal pins Ghostty commit `ad97d331` (14 October 2026) in `vt/build.zig.zon`, with Zig 0.16.0. On 11 November 2026, P0.1 (spec 19.1) moved the pin to `vt-1.0.0` (`0fa3d577`) and the build to Zig 0.17.0.
- Ghostty published four advisories that affect libghostty-vt, the library that the shadow terminal embeds. GitHub published the first three on 27 October 2026 ([repository advisories](https://api.github.com/repos/ghostty-org/ghostty/security-advisories)):

| Advisory | What | Affected | Fix | The pin `ad97d331` |
| --- | --- | --- | --- | --- |
| [GHSA-w9q4-5jvv-mcv4](https://github.com/ghostty-org/ghostty/security/advisories/GHSA-w9q4-5jvv-mcv4) | Use-after-free when a clear by scrolling prunes the cursor's page. Terminal output alone triggers it. Medium, CVSS 3.1 6.5. | The app 1.0.0 to 1.3.1. libghostty-vt before `e257bb45`, "in its default configuration, since its default scrollback limit is small". | `e257bb45` (26 October), app 1.4.0 | Affected |
| [GHSA-jqr7-69mm-v35h](https://github.com/ghostty-org/ghostty/security/advisories/GHSA-jqr7-69mm-v35h) | Integer overflow (CWE-190) when a very wide page is narrowed. A panic in ReleaseSafe, not memory corruption from output. Medium, 5.5. | `main` from `d7bb4b863` up to but not including `0af5c9e55`. The app is "not realistically affected". The advisory names apps "that take terminal sizes from untrusted input, such as a remote client". | `0af5c9e55` (26 October) | Affected |
| [GHSA-qjfx-r533-6vx3](https://github.com/ghostty-org/ghostty/security/advisories/GHSA-qjfx-r533-6vx3) | Use-after-free when a scrollback line limit prunes the cursor's page on a one-row terminal. Medium, 5.3. | `>= 5b2d3b7d`, `< 65ff54c6`. Released apps are not affected. | `65ff54c6` (13 October) | Fixed: the pin holds `65ff54c6` |
| [GHSA-3r5j-c6rj-xq2v](https://github.com/ghostty-org/ghostty/security/advisories/GHSA-3r5j-c6rj-xq2v) | Memory growth through the Kitty clipboard protocol. Low. | libghostty-vt | `44c4c8f5` | Fixed: the pin holds `44c4c8f5` |

- The fixes are [PR 14856](https://github.com/ghostty-org/ghostty/pull/14856) and [PR 14857](https://github.com/ghostty-org/ghostty/pull/14857), merged on 26 October 2026.
- The compare API shows the pin "behind" both `e257bb45` and `0af5c9e55`, and "ahead" of `65ff54c6` (`https://api.github.com/repos/ghostty-org/ghostty/compare/<fix>...ad97d331`).
- `vt-1.0.0` holds all four fixes. On 11 November 2026, for P0.1, the compare API answered "ahead" for `e257bb45`, `0af5c9e55`, `65ff54c6` and `44c4c8f5` against its commit `0fa3d577` (`https://api.github.com/repos/ghostty-org/ghostty/compare/<fix>...0fa3d577a5614150ff8e2c152de5fb9d5c6d2dec`).
- `e257bb45` and `0af5c9e55` are on diverged lines, so neither commit holds the other. `v1.4.0` (27 October 2026) and `vt-1.0.0` (6 November 2026, commit `0fa3d577`) hold both. `vt-1.0.0` is the first libghostty-vt release ([PR 15126](https://github.com/ghostty-org/ghostty/pull/15126): "The first tag will be `vt-1.0.0`").
- The fix commits and both tags need Zig 0.17. `build.zig.zon` at `vt-1.0.0` says `minimum_zig_version = "0.17.0"`, and `src/build/zig.zig` matches the major and minor version only. Zig 0.17.0 shipped on 1 October 2026 ([Zig downloads](https://ziglang.org/download/index.json)).
- The shadow terminal sets `max_scrollback_lines = 10_000` (`vt/src/main.zig`). That is the set-up of GHSA-qjfx, but the pin already has its fix. No source says whether 10,000 lines makes GHSA-w9q4 harder to reach.
- Ghostty has no limit of 4096 columns. `CellCountInt` is `u16`, and `Capacity.maxCols` clamps to its largest value, 65535 (`src/terminal/page.zig` at `v1.4.0`). GHSA-jqr7 says "cap the number of columns" and gives no number. So a cap of 4096 is this project's own choice ([question 76](../contributing/isolation-spec.md#15-open-questions)). P0.1 measured the memory at that size and capped rows at 2048 (spec 19.1).

**Where the advisories are listed.**

- OSV answers "Vulnerability not found" for GHSA-w9q4, GHSA-jqr7, GHSA-qjfx and GHSA-3r5j ([OSV](https://api.osv.dev/v1/vulns/GHSA-w9q4-5jvv-mcv4)).
- GitHub's global advisory database does not hold them either: `https://api.github.com/advisories/GHSA-w9q4-5jvv-mcv4` answers 404. They have no CVE ID and use the "other" ecosystem, and GitHub reviews advisories only for its listed ecosystems ([about the advisory database](https://docs.github.com/en/code-security/security-advisories/working-with-global-security-advisories-from-the-github-advisory-database/about-the-github-advisory-database)).
- So no package scanner matches them to a Zig pin.
- The repository advisory API lists them: `GET /repos/ghostty-org/ghostty/security-advisories?state=published` ([REST docs](https://docs.github.com/en/rest/security-advisories/repository-advisories)). It answered without a token in a test, but a job should send `GITHUB_TOKEN` for the rate limit.
- Their ranges are free text, such as `< e257bb45…` and "main from d7bb4b863 up to but not including 0af5c9e55". A job must take the commit hashes out of the text and test ancestry, with `git merge-base --is-ancestor <fix> <pin>` or the compare API. In the compare API, "ahead" or "identical" means that the pin holds the fix, and "behind" or "diverged" means that it does not.
- OSV's commit query works for Ghostty entries that have a CVE. CVE-2026-26982 has a `GIT` range (introduced `0`, fixed `703d11c6`), and `POST /v1/query {"commit": "<v1.2.0 commit>"}` returns it ([OSV](https://api.osv.dev/v1/vulns/CVE-2026-26982), modified 28 October 2026). The v1.2.3 commit, on a release branch, does not match.

### 6.2 Signed tags

- Git 2.34.0 added SSH signing (`gpg.format=ssh`). Its release notes say to avoid the `ssh-keygen` of OpenSSH 8.7, and to use 8.8 or later ([2.34.0 notes](https://github.com/git/git/blob/master/Documentation/RelNotes/2.34.0.adoc)).
- `gpg.ssh.allowedSignersFile` gives trust `fully` only to a listed key. "Otherwise … git verify-commit/tag will fail" ([gpg config](https://github.com/git/git/blob/master/Documentation/config/gpg.adoc)).
- OpenSSH 8.1 added `ssh-keygen -Y sign` and `-Y verify` ([8.1](https://www.openssh.com/txt/release-8.1)). OpenSSH 8.2 added `ecdsa-sk` and `ed25519-sk` keys ([8.2](https://www.openssh.com/txt/release-8.2)).
- Yubico documents git signing with `ed25519-sk`: Git 2.34 or later, OpenSSH 8.2 or later, and 8.4 or later for `verify-required`. Each signature needs a touch ([Yubico](https://developers.yubico.com/SSH/Securing_git_with_SSH_and_FIDO2.html), undated vendor guide).
- `actions/checkout` before v6.0.2 could replace an annotated tag with a lightweight one. "Fix tag handling: preserve annotations" landed in v6.0.2 (#2356) ([CHANGELOG](https://github.com/actions/checkout/blob/main/CHANGELOG.md)). `git verify-tag` fails on a lightweight tag. This repository's release job pins v7.0.1.
- On a tag push, `GITHUB_SHA` is the "tip commit pushed to the ref" ([events](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows)). So a signers file read from that checkout can carry a key that the same push added.
- UNVERIFIED: GitHub's docs do not say in these words that the workflow file of a tag push also comes from the tagged commit. P4.4's docs treat it as so.

### 6.3 Image signatures

- sigstore-go is at v1.4.0, of 29 October 2026 ([proxy](https://proxy.golang.org/github.com/sigstore/sigstore-go/@latest)). Its README calls it "stable and ready for production use" ([README](https://github.com/sigstore/sigstore-go/blob/main/README.md)). docs.sigstore.dev still says "beta", which is out of date.
- It verifies a bundle against "your own custom trusted root" (`root.NewTrustedRootFromPath`), with `verify.NewShortCertificateIdentity(issuer, issuerRegex, san, sanRegex)` for the identity. The bundle carries the log proof, so the check needs no network ([verification.md](https://github.com/sigstore/sigstore-go/blob/main/docs/verification.md)).
- The README omits "container image verification". Its OCI example fetches the legacy `sha256-<hex>.sig` tag and its simple-signing layer itself, then builds a bundle ([example](https://github.com/sigstore/sigstore-go/blob/main/examples/oci-image-verification/main.go)).
- cosign v3.0.0 made the new protobuf bundle format the default, and stores container signatures "as an OCI Image 1.1 referring artifact" ([CHANGELOG](https://github.com/sigstore/cosign/blob/main/CHANGELOG.md)). So a client must read OCI referrers, and the legacy tag for older signatures.
- The trusted root rotates, so "offline" means "with a recent `trusted_root.json`" (inference).
- `hiddeco/sshsig`: a Go SSHSIG library, Apache-2.0, v0.2.0 of 12 April 2025, last commit 13 June 2025, before 1.0, with one maintainer. `Verify` uses `x/crypto/ssh`, which supports `sk-ssh-ed25519@openssh.com`. It accepts an empty namespace, so the caller must pass one ([pkg.go.dev](https://pkg.go.dev/github.com/hiddeco/sshsig)).

### 6.4 The time to answer an advisory

No outside source sets a time for a project like this one. The 7 days of [question 42](../contributing/isolation-spec.md#15-open-questions) are a project choice. Comparable policies:

- Firecracker patches each supported minor release, for at least 6 months each, with no time limit ([release policy](https://github.com/firecracker-microvm/firecracker/blob/main/docs/RELEASE_POLICY.md)).
- Cloud Hypervisor puts security fixes in a point release, after an embargo of up to 14 days ([SECURITY.md](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/SECURITY.md)).
- Go fixes PRIVATE-track issues in the next monthly minor release ([policy](https://go.dev/doc/security/policy)).
- The linux-distros list allows at most 14 days of embargo, and prefers under 7. Members must release fixes "within 10 days … of the issues being made public" ([distros](https://oss-security.openwall.org/wiki/mailing-lists/distros)). This is the nearest outside norm.
- All the Ghostty advisories of 2026 are Medium or Low. A rule only for high severity would not cover GHSA-w9q4, so the spec also counts any advisory whose range holds a current pin.

## 7. Later phases

Checked on 10 November 2026, for spec section 21 (tasks P5.2 to H18). Section 4.7 has the gVisor flags, section 5.8 the reflinks, and section 5.3 Firecracker and Nitro Sandboxes.

### 7.1 Decoy credentials caught a real breach

**Answer:** Canary credentials have caught at least one real theft of CI secrets, at the moment when the attacker checked them. Credential-stealing worms check the keys that they find. No source measures decoys inside a developer sandbox, or their rate of false alarms.

- Grafana Labs keeps AWS API key canary tokens in GitHub secrets, at the organisation level and in each repository. In its April 2025 incident, "TruffleHog's sts:GetCallerIdentity call against our tokens triggered the canary".
- Grafana's post-incident review says: "Our canary tokens alerted us when their use had been attempted." The incident started on 26 April 2025.
- The Shai-Hulud worm (September 2025) took keys from the environment and used "Trufflehog to scan the filesystem and check if keys are valid". It sent them to a webhook.site endpoint and a public GitHub repository. This is a vendor advisory, a secondary source.

**Evidence:**

- https://grafana.com/blog/canary-tokens-learn-all-about-the-unsung-heroes-of-security-at-grafana-labs/ (25 August 2025): the token types, where they live, and the TruffleHog call.
- https://grafana.com/blog/2025/05/15/grafana-security-update-post-incident-review-for-github-workflow-vulnerability-and-whats-next/ (16 May 2025): the alert and the dates.
- https://semgrep.dev/blog/2025/security-advisory-npm-packages-using-secret-scanning-tools-to-steal-credentials (15 September 2025): the worm's use of TruffleHog, and its exfiltration.

**Confidence:** high for the Grafana facts. Medium for the worm, from one secondary source.

**Change for the docs:** spec 21.7 and question 85 cite these facts. Research 8.2 kept decoys without evidence. These facts are the evidence.

### 7.2 Where AWS tools and git read credentials

**Answer:** The AWS CLI uses the `default` profile unless a command names another. git reads `~/.git-credentials` through the `store` credential helper. So a decoy in a named AWS profile, or in `~/.git-credentials`, is not sent by these tools by default.

**Evidence:**

- https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-files.html: "By default, the AWS CLI uses the settings found in the profile named `default`."
- https://git-scm.com/docs/git-credential-store: the helper is turned on with `git config credential.helper store`, and it reads `~/.git-credentials`. The page does not say that git reads the file without the helper.

**Confidence:** high for the AWS CLI. Medium for git, because the page is silent about git without a helper. P7.2's quiet test checks both.

### 7.3 Build users in Nix

**Answer:** Nix runs builds as build users from a store that root owns. The build users are in one group, `nixbld`, and in no other group. Nix takes a free build user under a file lock in `userpool/`. It kills every process of a build user with a child that takes the uid and calls `kill(-1, SIGKILL)`. No more builds run at once than there are build users. The multi-user installer makes 32 by default.

**Evidence:**

- https://nix.dev/manual/nix/2.18/installation/multi-user: "the Nix store and database are owned by some privileged user (usually root)". Builders run "under special user accounts". "There can never be more concurrent builds than the number of build users".
- https://github.com/NixOS/nix/blob/2.24-maintenance/scripts/install-multi-user.sh, line 25: `readonly NIX_USER_COUNT=${NIX_USER_COUNT:-32}`.
- https://github.com/NixOS/nix/blob/2.24-maintenance/src/libutil/unix/processes.cc: `killUser`, "we fork a process, switch to uid, and send a mass kill".
- https://github.com/NixOS/nix/blob/2.24-maintenance/src/libstore/unix/user-lock.cc: the lock files in `userpool/`.

**Confidence:** high.

**Change for the docs:** spec 21.9 and question 89 cite Nix as the nearest outside pattern. The number 16 stays a project choice.

### 7.4 E2B's guest kernel config

**Answer:** E2B's published Firecracker kernel configs enable user namespaces and seccomp filters. The x86_64 configs also enable Landlock, first in `CONFIG_LSM`. The arm64 config does not enable Landlock. The kernels are Linux 6.1, so Landlock there has no TCP rules, which need ABI 4 and Linux 6.7. The repository is archived, and its README says that kernel work moved to `e2b-dev/infra`. So the kernel that E2B runs today may differ.

**Evidence:**

- https://github.com/e2b-dev/fc-kernels, `kernel_versions.txt`: `6.1.102` and `6.1.158`.
- `configs/x86_64/6.1.158.config` and `configs/x86_64/6.1.102.config`: `CONFIG_USER_NS=y`, `CONFIG_SECCOMP_FILTER=y`, `CONFIG_SECURITY_LANDLOCK=y`, and `CONFIG_LSM="landlock,lockdown,yama,…"`.
- `configs/arm64/6.1.158.config`: `CONFIG_USER_NS=y`, `CONFIG_SECCOMP_FILTER=y`, and `# CONFIG_SECURITY_LANDLOCK is not set`.

**Confidence:** high for the files. Low for the kernel that runs today. UNVERIFIED: step 1 of P7.4 runs the sandbox probe in a new E2B machine.

**Change for the docs:** spec 11 and 21.8 say that the level of an E2B machine is the probe's answer. The research's "E2B is `none`" goes.
