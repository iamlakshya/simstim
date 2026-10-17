# Security model

What the tool does to keep your hosts and your laptop safe, and what it leaves to you. It is part of [the architecture](architecture.md); to report a problem, see [SECURITY.md](../../SECURITY.md).

**Rules:**

- **Nothing new listens on a host.** sshd is the only door, or, for a host made with `host create docker` or `e2b`, the provider's own authenticated exec API (Docker's socket, E2B's envd). There is no relay and no telemetry.
- **Provider credentials are read, never kept.** An E2B API key comes from your environment, the E2B CLI's own login, or a command you name. It is kept in memory, and never written, logged or sent to a host. Lima and Docker need none.
- **simstim stores no secrets.** Agent logins live on the host, scoped to that host. Fine-grained GitHub tokens should be per host. A session can get [secrets](../how-to/secrets.md) that a command fetches on the laptop at its start; they live only in that session's environment, never in a file, a log or a command line.
- **A repository's setup script runs only after you trust it,** once for each repository on each host. The client asks, and keeps the answer on the laptop ([setup hooks](../how-to/setup-hooks.md)).
- **SSH agent forwarding is off by default.** If a user turns it on, the stable `SSH_AUTH_SOCK` link makes it work across reconnects. The risk is written in the docs.
- **Helpers are released signed,** and checked by SHA-256 before install.
- **Sockets and staged files are private:** 0600 and 0700, owner checked.
- **Ports forwarded by simstim bind to `127.0.0.1` only.** This does not change where applications on the host listen.
- **Live terminal output is not filtered.** OSC 52 clipboard operations, OSC 8 links and terminal queries pass through to your terminal. There is no untrusted-host mode. The replay filter removes historical terminal queries to avoid answering old questions; it is not a security filter.

**Threat model, in short:**

- **A compromised host** can see everything its agents can see. It can also send terminal escape sequences to the laptop. Their effects depend on your terminal's features, permissions and bugs. Trust a host before attaching, keep your terminal up to date, and use its clipboard and link controls to limit what remote output can do. simstim does not make hostile terminal output safe.
- **Several attached clients** all receive live output, including queries, and all can send input. The size leader is not an access-control boundary. Do not treat another attached client as a read-only viewer.
- **A compromised laptop** can drive every host it has keys for, as it already can through `ssh`.
