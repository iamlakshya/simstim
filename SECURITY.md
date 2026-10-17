# Security policy

This tool runs AI agents with shell access on your hosts and carries your terminal traffic, so we take security reports seriously.

## Report a vulnerability

Do not open a public issue. Report it privately through GitHub: <https://github.com/iamlakshya/simstim/security/advisories/new> (**Security → Report a vulnerability** on this repository).

Include the version, the host and client systems, the steps to reproduce, and the impact you see.

## What happens next

1. We acknowledge the report within 7 days.
2. We confirm the issue, agree its severity with you, and keep you updated at least every 14 days.
3. We fix it in a private fork and release the fix. We aim to do this within 90 days of the report, and sooner for severe issues.
4. We publish a GitHub security advisory with a CVE when the fix is out, and credit you unless you ask us not to. Please keep the details private until then (coordinated disclosure).

If a report is not a vulnerability, we say why and close it.

## Supported versions

Only the latest minor release gets security fixes. The first release is 0.1.0.

## Design rules that limit risk

- The tool adds no network listener on a host. Connections use the host's SSH server, Docker exec through the Docker daemon, or E2B's envd process API. Access depends on the chosen transport's permissions and credentials.
- There is no relay, no cloud service and no telemetry.
- The tool stores no secrets. Agent logins stay on each host.
- SSH agent forwarding is off by default.
- Host helpers are checked by SHA-256 before they are installed.
- Sockets and uploaded files are private to the user (modes 0600 and 0700).
- Ports forwarded to the laptop bind to `127.0.0.1` only.

Live terminal output is passed through unchanged, not made safe for an untrusted host. Remote escape sequences can use features your local terminal permits, including clipboard operations and links. There is no untrusted-host output filter. Trust hosts before attaching and keep your terminal up to date.

[The security model](docs/explanation/security.md) has the full threat model.
