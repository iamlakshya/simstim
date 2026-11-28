---
status: proposed
date: 2026-11-28
---

# 0057 Model API gateway route without TLS interception

## Context

Sandboxed agents need to reach their model providers (such as Anthropic and OpenAI) without holding the real API key or token inside the sandbox ([0032](0032-secret-values-in-memory-only.md)). The earlier integration stack intercepted TLS for every model host with a per-session CA, a custom HTTP/2 physical write layer, a WebSocket frame reader and an exact Go toolchain pin ([0052](0052-exact-http2-legacy-build.md), rejected by [0053](0053-rebuild-with-open-source-parts.md)).

Both first-class agents already support routing model traffic through a local HTTP gateway URL: Claude Code reads `ANTHROPIC_BASE_URL`, and Codex reads a `[model_providers.<name>]` block with `base_url`, `env_key`, `wire_api = "responses"` and `supports_websockets = false`.

## Options

1. Intercept TLS for model hosts with a per-session CA, sentinel shape grammars and a WebSocket frame parser.
2. Serve a plain-HTTP reverse proxy route (`/gw/<route>`) inside the session's egress process using Go's `net/http/httputil.ReverseProxy`, refuse direct `CONNECT` to model hosts, strip guest credentials, inject the real key over verified upstream TLS, and enforce the model API guard and usage counter on the gateway path.

## Decision

Option 2.

- **Gateway routes on the existing proxy path.** The egress process serves `/gw/<route>/...` through `httputil.ReverseProxy` on its `goproxy` `NonproxyHandler`, reached via the guest's existing loopback proxy over the link. Nothing new listens on the host ([0030](0030-local-sockets-do-not-listen.md)).
- **No TLS interception and no guest CA.** The guest speaks plain HTTP over its isolated loopback and private link to the egress process; the egress process dials the upstream model host over verified TLS 1.2+ using system roots and single-lookup `SafeDialer` checks.
- **No direct `CONNECT` to model hosts.** Model API hosts declared in the agent table or configured as gateway routes are always refused for direct `CONNECT`, forward-proxy HTTP, transparent splice and guest DNS, even under `preset = "open"` or explicit allow rules. A key planted inside the sandbox cannot reach the provider directly.
- **Credential stripping and in-memory injection.** The gateway drops every credential header from the guest (`Authorization`, `X-Api-Key`, `Proxy-Authorization`, `Api-Key` and the route's own header) and injects only the bound secret held in memory by the egress process ([0032](0032-secret-values-in-memory-only.md)), wiping the byte slices on shutdown.
- **Model guard and usage counting.** The gateway validates allowed paths, decompresses `gzip`/`deflate` request bodies up to 64 MiB, fails closed on unlisted `tools[].type` values, provider MCP servers and URL-fetched content (returning HTTP 403 with `x-should-retry: false`), refuses WebSocket upgrades (`supports_websockets = false`), and observes streamed SSE and plain JSON responses to count tokens per model and session.

## Consequences

- Good, because model credentials stay outside the sandbox without a guest CA, TLS interception, sentinel format grammars or the custom HTTP/2 and WebSocket machinery retired by [0053](0053-rebuild-with-open-source-parts.md).
- Good, because every model request passes through one small stdlib `httputil.ReverseProxy` route where the identity pin, tool guard and token usage counter run directly.
- Bad, because an agent that supports no custom base URL needs either `--expose` or later `goproxy` MITM interception when that path returns.
- Bad, because Codex runs over HTTPS streaming rather than WebSockets (`supports_websockets = false`) while on the gateway route.
