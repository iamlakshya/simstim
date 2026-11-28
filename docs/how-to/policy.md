# Control a sandbox's network access

A walled session has no network card and no route to the host or the internet. Every outbound connection and DNS query passes through a per-session egress process on the trusted side of the host.

The egress process opens no listening TCP or UDP port on the host ([decision 0030](../contributing/decisions/0030-local-sockets-do-not-listen.md)). It receives flows from the sandbox over a private link socket pair, checks each destination against the session's policy, and connects straight to the allowed host ([decision 0031](../contributing/decisions/0031-the-egress-process-is-not-a-relay.md)).

## Presets

Each walled session starts with one preset:

| Preset | Allows on port 443 | Meant for |
| --- | --- | --- |
| `locked` | Only the agent's own model API and login hosts from the agent table | Untrusted repositories and research |
| `balanced` | `locked`, plus common package registries (`registry.npmjs.org`, `pypi.org`, `files.pythonhosted.org`, `proxy.golang.org`, `sum.golang.org`, `crates.io`, `index.crates.io`, `static.crates.io`, `rubygems.org`) and code hosts (`github.com`, `api.github.com`, `objects.githubusercontent.com`, `raw.githubusercontent.com`, `codeload.github.com`, `gitlab.com`) | Daily work |
| `open` | Every public host and port outside the refused address ranges and denies | Exploration |

## Policy format

A session's network policy is plain TOML with flat deny-wins semantics ([decision 0053](../contributing/decisions/0053-rebuild-with-open-source-parts.md)):

```toml
session = "devbox/auth-fix"
agent = "claude"

[sandbox]
preset = "balanced"

[network]
allow = ["api.stripe.com", "*.internal.example:8443", "example.com:80"]
deny = ["pastebin.com"]
unknown = "deny"

[lock]
network.deny = ["evil.example"]
```

- **Any deny beats any allow.** A deny rule overrides `network.allow`, live session grants, agent hosts and presets.
- **Locks stay fixed.** A host in `[lock]` cannot be opened by a live grant or a policy reload.
- **Ports.** An `allow` entry without a port allows port 443. Write `host:port` to allow another port. A `deny` entry without a port denies every port on that host.
- **Method and path rules.** Prefix an allow, deny or lock rule with an HTTP method (`GET`, `HEAD`, `POST`, `PUT`, `PATCH`, `DELETE` or `OPTIONS`) or add a canonical path pattern (`*` for one segment, `**` for any depth, or a trailing `/` for a prefix):
  - `allow = ["GET api.example.com/v1/*", "POST api.example.com/v1/items"]`
  - `deny = ["DELETE api.example.com/v1/**"]`
  - Any host covered by a method or path rule is intercepted over HTTPS so the proxy enforces the rule on each canonical request.
- **Wildcards.** `*.example.com` matches subdomains such as `api.example.com`, never the apex `example.com`. Broad wildcards on a public or shared suffix (such as `*.com`, `*.co.uk`, `*.vercel.app` or `*.github.io`) are refused in allow rules; name a specific host such as `app.vercel.app` instead.
- **Unknown hosts.** With `unknown = "deny"` (the default), any unmatched host gets an HTTP 403 whose body names the grant command. With `unknown = "allow"`, unmatched public hosts pass and are logged as `allow-unknown`.

Live commands (`policy allow`, `policy deny` and `policy ls`) are wired on top of this engine in task P1.8.

## Interception, registries and splice-only hosts

Most allowed hosts are spliced as raw TCP bytes after the port-443 TLS SNI check. The egress process terminates TLS only for hosts that need HTTP-level enforcement ([decision 0038](../contributing/decisions/0038-tls-interception-on-the-trusted-side.md)):

- **Package registries (`internal/egress/rules/registries.toml`):** `registry.npmjs.org`, `pypi.org`, `files.pythonhosted.org`, `upload.pypi.org`, `proxy.golang.org`, `sum.golang.org`, `crates.io`, `index.crates.io`, `static.crates.io` and `rubygems.org` allow `GET` and `HEAD` only by default. Writes such as `npm publish` (`PUT`), unpublish, dist-tag updates, PyPI uploads and `cargo publish` fail with HTTP 403 and a `Try:` hint unless an explicit method/path rule allows them.
- **Object stores (`internal/egress/rules/object_stores.toml`):** `*.s3.amazonaws.com`, `s3.amazonaws.com`, `storage.googleapis.com` and `*.blob.core.windows.net` are restricted to `GET` and `HEAD` whenever a grant allows the host, unless an explicit method/path rule allows writes.
- **Method and path rules:** any host referenced by a method- or path-scoped rule in `allow`, `deny`, `lock` or a live grant is intercepted.
- **Splice-only list (`internal/egress/rules/splice.toml`):** hosts whose clients pin certificates or require mTLS (such as `mtls.googleapis.com`, `*.mtls.googleapis.com`, `controlplane.tailscale.com`, `gdmf.apple.com` and `mesu.apple.com`) are never intercepted and never receive a secret.
- **Model APIs stay on the gateway:** model hosts still travel over plain-HTTP `/gw/<route>` routes without TLS interception ([decision 0057](../contributing/decisions/0057-model-gateway-route.md)).

For intercepted hosts:

- **Per-session in-memory CA:** the egress process generates a fresh ECDSA P-256 CA at each start, keeps its private key only in memory (never in any file or over fd 3) and wipes the key on exit ([decision 0032](../contributing/decisions/0032-secret-values-in-memory-only.md)). Guest init writes the public certificate to `/run/simstim/ca.pem` and a combined system-plus-session bundle to `/run/simstim/ca-bundle.pem`, setting `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE`, `GIT_SSL_CAINFO`, `CURL_CA_BUNDLE`, `NODE_EXTRA_CA_CERTS` (the CA alone) and any agent-specific `CAEnv` variables (such as `CODEX_CA_CERTIFICATE` for Codex).
- **HTTP/2 over ALPN and verified upstream TLS:** every intercepted host advertises `h2` and `http/1.1` through ALPN, and forwards requests through one shared `http.Transport` with TLS 1.2+ and full system-root certificate verification.
- **Canonical request checks:** before matching rules or forwarding upstream, the egress process checks ClientHello SNI and refuses ECH, refuses HTTP/1.1 smuggling vectors (duplicate `Host`, both `Content-Length` and `Transfer-Encoding`, multiple `Content-Length`, obsolete line folding, bare `LF`/`CR`, unsupported `Transfer-Encoding` and nested `CONNECT`), refuses any `Host`, HTTP/2 `:authority` or absolute target that differs from SNI, caps queries at 8 KiB, rejects traversal and control characters in paths, and bounds `gzip`/`deflate` decoding to 64 MiB. Intercepted requests log `"mode": "intercept"` with redacted canonical paths and no query strings.

## Refused addresses and TLS checks

Before dialling any host, the egress process resolves the name once and checks every returned address. If any address is refused, the whole flow fails closed with HTTP 403 and nothing is dialled. The dial then uses the verified IP address directly, with no second DNS lookup, so DNS rebinding cannot redirect a connection to a private address.

These addresses are refused unless an explicit IP or CIDR grant names them:

- `0.0.0.0/8`, RFC 1918 (`10/8`, `172.16/12`, `192.168/16`), CGNAT (`100.64/10`), loopback (`127.0.0.0/8`, `::1/128`), link-local and cloud metadata (`169.254.0.0/16`, `fe80::/10`, `fd00:ec2::254/128`)
- IETF protocol (`192.0.0.0/24`), benchmarking and synthetic DNS (`198.18.0.0/15`), multicast (`224.0.0.0/4`, `ff00::/8`), reserved (`240.0.0.0/4`), IPv6 unspecified (`::/128`), unique local (`fc00::/7`), discard (`100::/64`) and deprecated site-local (`fec0::/10`)
- IPv4 addresses embedded inside IPv6 transition formats: IPv4-mapped (`::ffff:0:0/96`), IPv4-translated (`::ffff:0:0:0/96`), deprecated IPv4-compatible (`::/96`), NAT64 (`64:ff9b::/96` when the inner IPv4 is refused, and all of `64:ff9b:1::/48`), 6to4 (`2002::/16`) and Teredo (`2001::/32`)
- Every address assigned to the host's own network interfaces, read when the egress process starts

On port 443, the egress process inspects the TLS ClientHello with Go's `crypto/tls` before dialling upstream. It refuses any flow whose SNI does not match the `CONNECT` host, and refuses Encrypted Client Hello (ECH) extensions (`0xfe0d` and `0xfd00`).

## DNS policy

Every sandbox DNS query crosses the private link to the egress process, which answers with `golang.org/x/net/dns/dnsmessage` and never leaks disallowed names upstream:

- **Process walls (bubblewrap):** direct DNS stays disabled (`/etc/resolv.conf` names no resolver and the sandbox has zero capabilities to bind port 53), so direct lookups such as `getent hosts example.com` fail closed and tools resolve names through the explicit loopback proxy on the trusted side.
- **Guest DNS stub (microVMs and walls with a bound stub):** `wall-init` listens on UDP and TCP loopback sockets (`127.0.0.53:53` by default) and forwards raw DNS wire messages over `dns` link streams to the egress process.
- Disallowed, locked and denied names receive `NXDOMAIN` without querying any upstream resolver. Non-`IN` class queries receive `REFUSED`.
- `HTTPS` and `SVCB` queries always receive empty `NOERROR` answers without reaching upstream, so clients do not learn ECH keys.
- In `real` mode, only `A` and `AAAA` records for allowed names are resolved, unknown names under `deny` or `ask` receive `NXDOMAIN` without reaching upstream, and any name resolving to a refused IP receives `NXDOMAIN`.
- In `synthetic` mode (used by microVMs for transparent routing), allowed names — and unknown names when `unknown = "ask"` or `unknown = "allow"` — receive unique addresses from `198.18.0.0/15` without querying upstream; the egress process maps the address back to the name and applies policy when a connection reaches it. Under `unknown = "deny"`, unknown names receive `NXDOMAIN`.

## Audit log

The egress process sends one JSON line per connection, gateway request or DNS query back to the supervisor over its control stream:

```json
{"v":1,"t":"2026-11-27T10:12:03Z","kind":"net","host":"registry.npmjs.org","port":443,"mode":"splice","method":"CONNECT","status":200,"up":512,"down":2048,"rule":"preset:balanced","decision":"allow"}
```

Audit lines never include query strings, header values or bodies. Before writing a path, the egress process replaces every exposed or bound secret value with `[NAME]` and cuts the path to 256 bytes.

## Model gateway and guard

Model API hosts (such as `api.anthropic.com` and `api.openai.com`) are never allowed for direct `CONNECT`, forward-proxy HTTP, transparent splice or guest DNS, even under `preset = "open"` or an explicit allow rule ([decision 0057](../contributing/decisions/0057-model-gateway-route.md)). A key planted inside the sandbox cannot reach the model provider directly.

Instead, the agent speaks plain HTTP over its isolated loopback proxy and private link to a gateway route served by the egress process (`http://127.0.0.1:3128/gw/<route>`):

- **Claude Code:** sets `ANTHROPIC_BASE_URL=http://127.0.0.1:3128/gw/anthropic` with a non-secret placeholder in its credential variable.
- **Codex:** writes a `[model_providers.gateway]` block in `.codex/config.toml` pointing `base_url` at `http://127.0.0.1:3128/gw/openai/v1` with `wire_api = "responses"` and `supports_websockets = false`.

For each gateway request, the egress process:

1. Refuses WebSocket upgrades, path traversal, unlisted endpoints (such as `/v1/messages/batches` and `/v1/batches`) and file uploads (`/v1/files`) unless granted.
2. Decompresses `gzip` or `deflate` request bodies up to 64 MiB and inspects the JSON body. Any `tools[].type` not on the agent's allowlist, any provider-side MCP server (`mcp_servers`, `mcp_toolset` or OpenAI `mcp`), any URL-fetched image or document, and web search under the `locked` preset are refused with HTTP 403, `x-should-retry: false` and the provider's JSON error shape.
3. Strips every guest credential header (`Authorization`, `X-Api-Key`, `Proxy-Authorization`, `Api-Key`), injects the real key held only in the egress process's memory ([decision 0032](../contributing/decisions/0032-secret-values-in-memory-only.md)), and dials the provider over verified TLS 1.2+ with no TLS interception.
4. Counts token usage from streamed SSE and plain JSON responses (estimating `(request + response) / 4` when a stream ends without usage) and attaches the count to the audit event and the `usage` control query.

To grant a refused model capability for a session, add `[network.model_tools]` in the session policy (valid names: `web_fetch`, `web_search`, `code_execution`, `mcp`, `url_content`, `files`) or allow it live through the `policy.allow_tools` control operation.

## Model usage and budgets

The egress process counts tokens per session and per model from the responses that cross its model gateway routes:

- **Anthropic Messages:** reads `usage` from plain JSON or SSE streams (`message_start`, then cumulative replacement from the last `message_delta`, never summing deltas), counting `input_tokens + cache_creation_input_tokens + cache_read_input_tokens + output_tokens` plus `server_tool_use.web_search_requests`. `/v1/messages/count_tokens` counts nothing.
- **OpenAI Responses:** reads `usage` from plain JSON or `response.completed`, `response.incomplete` and `response.failed` stream events (`input_tokens`, `cached_tokens`, `cache_write_tokens`, `output_tokens`, `reasoning_tokens` and `total_tokens`).
- **OpenAI Chat Completions:** reads `usage` from plain JSON or from the final stream chunk when the client sets `stream_options.include_usage`.
- **Estimates:** when a stream ends without usage (such as a cut connection or a Chat Completions stream without `include_usage`), the egress process estimates `(request_bytes + response_bytes) / 4` rounded up and marks the audit line `"usage": "estimate"`.
- **WebSockets:** because the model gateway runs Codex over HTTPS streaming (`supports_websockets = false`, [decision 0057](../contributing/decisions/0057-model-gateway-route.md)) and refuses WebSocket upgrades, no WebSocket frame reader or bounded 128-response WebSocket tracker is needed on this route.

`simstim show`, `simstim show --json` and `simstim ls --json` include the session's cumulative `usage` (tokens, request counts, estimated request counts, per-model breakdown and active budget). Prices are never shown because provider rates change.

Set `network.model_budget` in `config.toml` or the session policy TOML to cap a session's model tokens (`0` means no limit):

```toml
[network]
model_budget = 2000000
upload_budget = "16M"
```

Before each billable model request, the egress process compares the session's cumulative tokens with `network.model_budget`. Any request already in flight finishes normally. Once the budget is reached, every later model request is refused before dialling upstream:

- **Anthropic:** HTTP `429` with `x-should-retry: false`, no `Retry-After` header, and `{"type":"error","error":{"type":"billing_error","message":"…"}}`.
- **OpenAI:** HTTP `429` with `x-should-retry: false`, no `Retry-After` header, and `{"error":{"type":"insufficient_quota","code":"insufficient_quota","param":null,"message":"…"}}`.
- Each refusal message includes a `Try:` hint showing how to raise `network.model_budget`, and the supervisor sends the `model_budget` alert once for that cap.
- Reloading the policy with a higher `network.model_budget` lets the next model request through immediately without restarting the session.

## Upload budgets

For every non-model host, a walled session may upload up to `network.upload_budget` per host in each rolling hour (`16M` by default; set `"0"` to disable). If an upload over `CONNECT` or plain HTTP reaches the cap, the egress process stops forwarding further upload bytes, logs an `upload_budget` audit event, and refuses later flows to that host with HTTP `403` and a `Try:` hint until the hour rolls over or `network.upload_budget` is raised.

## Process hardening

On Linux, the egress process clears `PR_SET_DUMPABLE` and sets `RLIMIT_CORE` to 0 at start. If the egress process is killed, new flows fail closed and no second egress process starts.
