---
status: proposed
date: 2026-11-28
---

# 0038 TLS interception on the trusted side

## Context

Walled sessions enforce a network allowlist in a per-session egress process on the trusted side of the host ([0031](0031-the-egress-process-is-not-a-relay.md)). Splicing raw TCP bytes after checking TLS SNI is enough for ordinary hosts, and model API calls already travel over the plain-HTTP `/gw/<route>` gateway without TLS interception ([0057](0057-model-gateway-route.md)).

Other hosts need request-level enforcement over HTTPS. Package registries (`registry.npmjs.org`, `pypi.org`, `proxy.golang.org`, `crates.io`, `rubygems.org`) and object stores (`*.s3.amazonaws.com`, `storage.googleapis.com`, `*.blob.core.windows.net`) must allow `GET` and `HEAD` while refusing writes such as `npm publish` (`PUT`). Policies also need explicit method and path rules (such as `GET api.example.com/v1/*` or `DELETE api.example.com/v1/**`).

At the same time, intercepting TLS introduces two risks: a leaked CA private key would let an attacker sign certificates for any host, and loose HTTP parsing opens request-smuggling bypasses between the proxy and the origin server.

## Options

1. Splice all HTTPS traffic and give up method, path and read-only registry rules.
2. Generate one persistent host-wide CA on disk and reuse it across sessions.
3. Generate an ephemeral ECDSA P-256 CA in the egress process's memory for each session, intercept only hosts covered by a class rule or a method/path rule (skipping the splice-only list and keeping model hosts on `/gw/<route>`), enforce canonical request parsing before forwarding via Go's `httputil.ReverseProxy`, and offer HTTP/2 over ALPN.

## Decision

Option 3.

- **Ephemeral in-memory CA per session.** The egress process generates a fresh ECDSA P-256 CA at each start and keeps its private key only in memory, wiping the key material on shutdown ([0032](0032-secret-values-in-memory-only.md)). Two sessions always receive two different CAs. Only the public certificate PEM crosses the control stream to guest init, which writes `ca.pem` and a combined `ca-bundle.pem` inside the sandbox's private runtime folder and sets `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE`, `GIT_SSL_CAINFO`, `CURL_CA_BUNDLE`, `NODE_EXTRA_CA_CERTS` (the CA alone) and any agent `CAEnv` variables.
- **Selective interception.** Only hosts matching a built-in class rule (package registries and object stores) or an explicit method/path rule are intercepted. Hosts on the embedded splice-only list (`internal/egress/rules/splice.toml`, for pinned certificates and mTLS) are always spliced after the SNI check and never receive a secret. Model API hosts stay on the plain-HTTP `/gw/<route>` gateway ([0057](0057-model-gateway-route.md)); direct `CONNECT` to model hosts remains refused.
- **Strict upstream TLS.** Upstream connections always verify certificates against the host's system roots with TLS 1.2 or newer through the session's single `http.Transport` and single-lookup `SafeDialer`.
- **Canonical request matching and smuggling refusal.** Before matching method/path rules or forwarding upstream, the egress process checks the TLS ClientHello SNI and refuses ECH, rejects HTTP/1.1 framing smuggling at the wire level (duplicate `Host`, both `Content-Length` and `Transfer-Encoding`, multiple `Content-Length`, obsolete line folding, bare `LF`/`CR`, nested `CONNECT` and unsupported `Transfer-Encoding`), refuses `Host` or HTTP/2 `:authority` or absolute targets that differ from SNI, caps queries at 8 KiB, normalises paths against traversal and control characters, and bounds gzip/deflate decoding to 64 MiB. Rules match the canonical request, and the canonical request is forwarded through `httputil.ReverseProxy` on top of `elazarl/goproxy` and Go's standard library `net/http` HTTP/2 server ([0053](0053-rebuild-with-open-source-parts.md)).
- **HTTP/2 over ALPN.** Every intercepted host advertises `h2` and `http/1.1` through ALPN so busy clients do not stall behind HTTP/1.1 connection limits.

## Consequences

- Good, because package registries and object stores stay read-only (`GET` and `HEAD`) by default while custom method and path rules work over HTTPS without writing any CA private key to disk.
- Good, because reusing `elazarl/goproxy` and standard library `net/http` (`httputil.ReverseProxy` and HTTP/2) avoids the custom HTTP/2 physical write layer and exact-toolchain lock rejected by [0053](0053-rebuild-with-open-source-parts.md), while Go 1.26.11 carries the fixes for CVE-2026-39825 and CVE-2026-56866.
- Good, because model API traffic stays on `/gw/<route>` without TLS interception, and pinned or mTLS clients stay on the splice path via `splice.toml`.
- Bad, because a tool inside the sandbox that ignores both the standard CA environment variables and its agent's `CAEnv` fails TLS verification on intercepted hosts until it is configured or its host is moved to `splice.toml`.
