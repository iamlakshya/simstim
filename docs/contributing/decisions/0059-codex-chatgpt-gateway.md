---
status: proposed
date: 2026-11-28
---

# 0059 Bound Codex ChatGPT sign-in through the egress gateway

Extends [0032](0032-secret-values-in-memory-only.md) and [0057](0057-model-gateway-route.md). Answers [question 72](isolation-spec.md#15-open-questions).

## Context

Users who subscribe to ChatGPT sign in to Codex with `codex login --device-auth` instead of an OpenAI API key. Codex stores the resulting ChatGPT OAuth bundle (`access_token`, `refresh_token`, `id_token` and `account_id`) in `~/.codex/auth.json` when `cli_auth_credentials_store = "file"` is set, sends requests to `chatgpt.com/backend-api/codex/responses` and `chatgpt.com/backend-api/codex/models` carrying `Authorization: Bearer <access_token>` and `Chatgpt-Account-Id: <account_id>`, and refreshes expired access tokens with a JSON `POST` to `auth.openai.com/oauth/token`.

Unlike static API keys, ChatGPT access tokens expire and OpenAI rotates single-use refresh tokens on every refresh. Leaving the user's real `~/.codex/auth.json` inside a sandbox exposes all three tokens to any compromised tool or dependency, while stripping write-back breaks concurrent or subsequent host sessions once the refresh token rotates. [0032](0032-secret-values-in-memory-only.md) explicitly deferred any OAuth broker that writes refreshed tokens back to a file until a dedicated record covers it.

## Options

1. Keep ChatGPT sign-in unbound: users sign in inside each sandbox and expose the tokens there.
2. Route ChatGPT requests through a plain-HTTP `/gw/chatgpt` gateway route on the egress process ([0057](0057-model-gateway-route.md)), with the egress process injecting `Authorization` and `Chatgpt-Account-Id` and refreshing tokens at `auth.openai.com/oauth/token`, while the sandbox receives only a non-secret placeholder `.codex/auth.json` and gateway `.codex/config.toml`. Support both host sign-in (updating only the host user's pre-existing `~/.codex/auth.json` under an advisory lock) and laptop-pushed `CODEX_AUTH_JSON` (held strictly in memory with no disk write).
3. Push the laptop's `~/.codex/auth.json` and copy it into a new simstim-managed token store on the host.

## Decision

Option 2.

- **Gateway route and model guard.** Codex declares a `SubscriptionGateway` on `/gw/chatgpt` pointing at `https://chatgpt.com`, with `chatgpt.com` registered as a gateway-only model host so direct `CONNECT chatgpt.com:443` and guest DNS are always refused. `ModelGuard` permits only `POST /backend-api/codex/responses`, `POST /backend-api/codex/responses/compact`, `POST /backend-api/codex/responses/input_tokens` and `GET /backend-api/codex/models`, enforces the same server-tool and URL-input rules as `api.openai.com`, refuses WebSocket upgrades (`supports_websockets = false`), and counts token usage on `/backend-api/codex/responses`. Non-model ChatGPT endpoints (such as `/backend-api/ps/mcp` and `/backend-api/wham/*`) are refused with HTTP 403.
- **Placeholder inside the sandbox.** Guest init writes a session-local `.codex/config.toml` (`cli_auth_credentials_store = "file"`, `model_provider = "gateway"`, `chatgpt_base_url = "<proxy>/gw/chatgpt/backend-api"`, `base_url = "<proxy>/gw/chatgpt/backend-api/codex"`, `requires_openai_auth = true`, `supports_websockets = false`) and a non-secret placeholder `.codex/auth.json` (mode `0600`) with a far-future JWT `exp` claim so Codex never attempts local token refresh. Real tokens never enter the sandbox.
- **Refresh on the trusted side.** The egress process refreshes proactively within 5 minutes of the access token's JWT `exp` and reactively once on HTTP 401 from `chatgpt.com`, serialised per route in memory. Every refresh obeys policy (`DecideGateway` on `auth.openai.com`), single-lookup `SafeDialer` checks, verified TLS 1.2+ with redirects refused, and memory wiping on close. Failed refreshes return HTTP 401 with `x-should-retry: false` and a `Try:` hint.
- **Narrow extension to [0032](0032-secret-values-in-memory-only.md) for host sign-in only.** When a Codex walled session starts with no API key and no `CODEX_AUTH_JSON` secret, and the host user already has a valid ChatGPT `~/.codex/auth.json` (for example from `simstim agents login codex <host>`), the supervisor arms egress from that file. On refresh, egress acquires an advisory `flock` lock, re-reads `~/.codex/auth.json` to adopt tokens already rotated by another session, and atomically updates only that pre-existing regular file (mode `0600`, preserving unknown fields). simstim never creates a new token file if the user has logged out or deleted `~/.codex/auth.json`.
- **Memory-only path for laptop sign-in.** When `CODEX_AUTH_JSON` is configured in `[secrets]` on the laptop, it binds to `chatgpt.com` and stays strictly in egress memory with zero host disk writes, preserving [0032](0032-secret-values-in-memory-only.md) unchanged. Documentation notes OpenAI's single-use refresh-token rotation so users keep a dedicated login or sign in on the host.

Option 1 leaves ChatGPT tokens readable inside every sandbox. Option 3 creates a new secret store on the host, which breaks [0032](0032-secret-values-in-memory-only.md) without need.

## Consequences

- Good, because ChatGPT subscribers can run sandboxed Codex sessions without exposing OAuth tokens to the sandbox and without paying separately for API credits.
- Good, because `chatgpt.com` traffic is restricted to Codex model endpoints under `ModelGuard`, blocking account, connector and MCP endpoints on the same origin.
- Good, because concurrent host sessions share a single advisory lock and adopt rotated tokens on disk instead of invalidating each other with `refresh_token_reused`.
- Bad, because host sign-in relies on the host user's pre-existing `~/.codex/auth.json` remaining on disk outside the sandbox and receives atomic updates from the egress process when tokens rotate.
- Bad, because a laptop-pushed `CODEX_AUTH_JSON` that shares the laptop's own live Codex login will invalidate the laptop's copy after its first refresh.
