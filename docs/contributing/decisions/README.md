# Decision records

Each record explains one choice: the context, the options and the outcome. Records are never edited after they are accepted; a new record replaces an old one.

| # | Decision | Status |
| --- | --- | --- |
| [0001](0001-go-and-bubble-tea.md) | Go for the client and host helper, Bubble Tea v2 for the screens | Accepted |
| [0002](0002-own-holder-per-session.md) | Our own holder process per session, not zmx | Accepted |
| [0003](0003-system-openssh.md) | The system OpenSSH client as the only transport | Accepted |
| [0004](0004-replay-by-offset.md) | Reattach by byte offset, with a full redraw as the fallback | Accepted |
| [0005](0005-apache-2-and-dco.md) | Apache-2.0 with a DCO (DCO part replaced by 0010) | Accepted |
| [0006](0006-defaults.md) | Detach key, image key and memory cap defaults | Accepted |
| [0007](0007-name-in-one-place.md) | The product name lives in one package | Accepted |
| [0008](0008-memory-max-only.md) | Cap sessions with MemoryMax only, not MemoryHigh (replaces part of 0006) | Accepted |
| [0009](0009-light-launcher.md) | A light launcher, and a session that owns the whole terminal (replaces the first home-screen design; its look is replaced by 0012) | Accepted |
| [0010](0010-no-commit-trailers.md) | No sign-off or attribution lines in commits (replaces the DCO part of 0005) | Accepted |
| [0011](0011-instant-launcher.md) | The launcher remembers: it draws at once and opens where you left off | Accepted |
| [0012](0012-own-look.md) | Our own look: a signal, not a copy of Amp (replaces the look in 0009; the website no longer follows it, 0022) | Accepted |
| [0013](0013-host-providers.md) | Host providers: Lima and E2B first, over a byte-stream transport | Accepted |
| [0014](0014-code-layout.md) | Code layout: thin mains, an importable command tree, black-box tests under test/ | Accepted |
| [0015](0015-docs-toolchain.md) | Docs toolchain: Hugo with Hextra, a Diátaxis docs folder and generated reference (its toolchain replaced by 0022) | Accepted |
| [0016](0016-public-repo-layout.md) | Public repo layout: full community files at the root, GitHub config in .github/, private material out | Accepted |
| [0017](0017-release-signatures.md) | Release signatures that any machine with OpenSSH can check | Accepted |
| [0018](0018-feature-set.md) | The feature set: what we build now, next and later | Accepted |
| [0019](0019-alerts-and-hooks.md) | Alerts and event hooks run on the host | Accepted |
| [0020](0020-guided-setup.md) | A guided setup: one command and one sheet, over one engine | Accepted |
| [0021](0021-site-design.md) | The site: a landing page in the launcher's look, docs kept on Hextra, and a three.js field that loads last (its toolchain, landing sections, field, look and motion setting replaced by 0022) | Accepted |
| [0022](0022-site-rebuild.md) | The site: Starlight for the docs, three tabs, and a landing page that tells its story in a drawing (replaces the toolchain in 0015, parts of 0021, and 0012's hold on the website) | Accepted |
| [0023](0023-protect-main.md) | Changes reach main only through a pull request that passes CI | Accepted |
| [0024](0024-memory-cap.md) | A session's memory cap is the host's memory minus a reserve, with the same limit on all sessions together, and a setting to change it (replaces the cap in 0006, and part of 0008) | Proposed |

Write a new record from [the template](template.md).
