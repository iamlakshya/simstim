# Decision records

Each record explains one choice: the context, the options and the outcome. Records are never edited after they are accepted; a new record replaces an old one.

| # | Decision | Status |
| --- | --- | --- |
| [0001](0001-go-and-bubble-tea.md) | Go for the client and host helper, Bubble Tea v2 for the screens | Accepted |
| [0002](0002-own-holder-per-session.md) | Our own holder process per session, not zmx | Accepted |
| [0003](0003-system-openssh.md) | The system OpenSSH client as the only transport | Accepted |
| [0004](0004-replay-by-offset.md) | Reattach by byte offset, with a full redraw as the fallback (a session on the alternate screen is drawn again, 0028) | Accepted |
| [0005](0005-apache-2-and-dco.md) | Apache-2.0 with a DCO (DCO part replaced by 0010) | Accepted |
| [0006](0006-defaults.md) | Detach key, image key and memory cap defaults (MemoryHigh part replaced by 0008, memory cap replaced by 0024) | Accepted |
| [0007](0007-name-in-one-place.md) | The product name lives in one package | Accepted |
| [0008](0008-memory-max-only.md) | Cap sessions with MemoryMax only, not MemoryHigh (replaces part of 0006, and one sentence of it is replaced by 0024) | Accepted |
| [0009](0009-light-launcher.md) | A light launcher, and a session that owns the whole terminal (replaces the first home-screen design; its look is replaced by 0012) | Accepted |
| [0010](0010-no-commit-trailers.md) | No sign-off or attribution lines in commits (replaces the DCO part of 0005) | Accepted |
| [0011](0011-instant-launcher.md) | The launcher remembers: it draws at once and opens where you left off | Accepted |
| [0012](0012-own-look.md) | Our own look: a signal, not a copy of Amp (replaces the look in 0009; the website no longer follows it, 0022) | Accepted |
| [0013](0013-host-providers.md) | Host providers: Lima and E2B first, over a byte-stream transport | Accepted |
| [0014](0014-code-layout.md) | Code layout: thin mains, an importable command tree, black-box tests under test/ | Accepted |
| [0015](0015-docs-toolchain.md) | Docs toolchain: Hugo with Hextra, a Diátaxis docs folder and generated reference (its toolchain replaced by 0022) | Accepted |
| [0016](0016-public-repo-layout.md) | Public repo layout: full community files at the root, GitHub config in .github/, private material out | Accepted |
| [0017](0017-release-signatures.md) | Release signatures that any machine with OpenSSH can check (extended by 0041, proposed; replaced by 0055, proposed) | Accepted |
| [0018](0018-feature-set.md) | The feature set: what we build now, next and later (its Now list extended by 0025) | Accepted |
| [0019](0019-alerts-and-hooks.md) | Alerts and event hooks run on the host (its once-a-minute rule for a session's end replaced by 0026, which also adds the event `memory`) | Accepted |
| [0020](0020-guided-setup.md) | A guided setup: one command and one sheet, over one engine | Accepted |
| [0021](0021-site-design.md) | The site: a landing page in the launcher's look, docs kept on Hextra, and a three.js field that loads last (its toolchain, landing sections, field, look and motion setting replaced by 0022) | Accepted |
| [0022](0022-site-rebuild.md) | The site: Starlight for the docs, three tabs, and a landing page that tells its story in a drawing (replaces the toolchain in 0015, parts of 0021, and 0012's hold on the website) | Accepted |
| [0023](0023-protect-main.md) | Changes reach main only through a pull request that passes CI | Accepted |
| [0024](0024-memory-cap.md) | A session's memory cap is the host's memory minus a reserve, with the same limit on all sessions together, and a setting to change it (replaces the cap in 0006, and part of 0008) | Accepted |
| [0025](0025-debug-mode.md) | A debug mode: `--debug` and `debug start` turn it on, profiles and a flight recorder go over the existing channel, and `debug bundle` writes one local archive with names replaced | Accepted |
| [0026](0026-end-and-memory-alerts.md) | The once-a-minute rule never holds back a session's end, and a warning near the memory cap has its own event, `memory` (replaces one rule of the noise control in 0019, and adds an event to it, warning trigger replaced by 0027) | Accepted |
| [0027](0027-memory-warning-growth.md) | The memory warning also comes when the memory still grows near the cap, and on its own at 95 % (replaces 0026's rule for what starts a warning) | Accepted |
| [0028](0028-redraw-the-alternate-screen.md) | Draw a session on the alternate screen again after a detach (replaces part of 0004) | Accepted |
| [0029](0029-a-sandbox-per-session.md) | Sessions in sandboxes on hosts the user owns, with every door closed until the user's policy opens it (widens roadmap item X6) | Proposed |
| [0030](0030-local-sockets-do-not-listen.md) | Only network ports count as listening on a host: Unix sockets and VM sockets do not | Accepted |
| [0031](0031-the-egress-process-is-not-a-relay.md) | A sandbox's egress process is not a relay, and neither a host's download of the runtime nor a judge that the user names is telemetry | Proposed |
| [0032](0032-secret-values-in-memory-only.md) | Secret values live only in the memory of the trusted side, never in a file | Proposed |
| [0033](0033-one-command-grammar.md) | One grammar for the command line: session verbs at the top, a noun and a verb for everything else, and every rule under `policy` | Accepted |
| [0034](0034-ssh-options-on-the-command-line.md) | The client puts the options that keep a host safe on the `ssh` command line and at the top of its generated config, where they win over the user's own config. It starts with forwarding off (adds to 0003) | Accepted |
| [0035](0035-pinned-host-keys.md) | Each saved host reached over SSH has a pinned host key in a file of its own, which options on the `ssh` command line check, so the user's config cannot undo it. OpenSSH decides which key the user's own settings trust, and only `host rekey` replaces a pin (applies the rule of 0034) | Replaced by 0054 |
| [0036](0036-host-tiers-and-the-grant-gate.md) | Host tiers and identity stay in a separate client file. The client checks each grant before it leaves the laptop | Proposed |
| [0040](0040-memory-of-microvm-guests.md) | A microvm session's unit cap is its guest RAM plus a host reserve (extends 0024) | Proposed |
| [0041](0041-a-signed-tag-release-gate.md) | A release builds only from a tag that a maintainer signed with a hardware key, checked by `main`'s gate against the list of keys on `main`, with the release key in an environment that only the gated job reads (extends 0017) | Proposed |
| [0044](0044-mac-host-loopback-port.md) | A joined Mac permits only its fixed Lima loopback SSH port behind the existing narrow SSH path | Proposed |
| [0045](0045-decoy-opt-outs-and-locks.md) | Check policy locks first, then keep every valid decoy opt out | Proposed |
| [0046](0046-public-network-answer-ceilings.md) | Public network answers retain an explicit restrictive address ceiling through rules and permits | Proposed |
| [0049](0049-nitro-slot-budget-undo.md) | Nitro slot donations remain until a separately authorised parent reboot | Proposed |
| [0050](0050-closed-purge-data-relay.md) | A closed data relay continues only the original live purge vector, with native operator checks and bounded public code comparison | Rejected by 0053 |
| [0051](0051-closed-manager-custody.md) | Retain original kernel custody in the user's manager between stopped runs | Rejected by 0053 |
| [0052](0052-exact-http2-legacy-build.md) | Qualify the original public HTTP/2 hooks with exact official Go 1.27.4 and the documented legacy build tag | Rejected by 0053 |
| [0053](0053-rebuild-with-open-source-parts.md) | Rebuild on the published base with maintained open-source parts, and build from scratch only when no tool meets the need | Accepted |
| [0054](0054-simple-pinned-host-keys.md) | Pin each SSH host's public keys in `known_hosts-<host>` with OpenSSH and `x/crypto/ssh`, with no log-verdict parser or epoch index (replaces 0035) | Proposed |
| [0055](0055-lean-release-signing.md) | Keep SSH checksum signatures, SBOMs and GitHub attestations; drop cosign keyless signing and the unmaintained SLSA generator (replaces 0017) | Proposed |
| [0056](0056-bubblewrap-process-wall.md) | Build the process wall with bubblewrap, `go-seccomp-bpf` and `go-landlock` instead of a custom namespace launcher (applies 0053) | Proposed |
| [0057](0057-model-gateway-route.md) | Route sandboxed model API calls through plain-HTTP `/gw/<route>` paths on the egress process with verified upstream TLS, no TLS interception and no direct `CONNECT` to model hosts | Proposed |
| [0058](0058-upstream-runsc-gvisor-wall.md) | Build the gVisor wall on pinned upstream `runsc` without patches, with single-entry OCI user namespaces and an Ubuntu AppArmor profile when required (applies 0053) | Proposed |

Write a new record from [the template](template.md).
