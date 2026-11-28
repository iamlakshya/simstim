---
status: proposed
date: 2026-11-27
---

# 0054 Pin host keys with OpenSSH and x/crypto/ssh

This record replaces [0035](0035-pinned-host-keys.md), following [0053](0053-rebuild-with-open-source-parts.md) and [the reuse research](../../research/reuse.md#4-ssh-transport-and-host-keys-h2-h3-spec-1471-1485-decisions-0003-0034-0035). It applies the command-line rule of [0034](0034-ssh-options-on-the-command-line.md) to host keys.

## Context

Record 0035 proposed pinning host keys with a debug-log (`-E`) verdict parser, staged pin files and a 128-bit ControlMaster epoch index. That machinery grew to over 5,000 lines of custom code.

OpenSSH itself already enforces a per-host pin when the client passes `HostKeyAlias`, a private `UserKnownHostsFile` and `StrictHostKeyChecking=yes` on the command line. `ssh-keyscan`, `ssh` and `golang.org/x/crypto/ssh` already fetch, parse, fingerprint and format `known_hosts` lines.

## Options

1. Build the log-verdict parser, staging and epoch index of 0035.
2. Pin each host's public keys in a private `known_hosts-<host>` file using OpenSSH (`ssh`, `ssh-keyscan`) and `golang.org/x/crypto/ssh` (`ssh.ParseKnownHosts`, `ssh.FingerprintSHA256`, `knownhosts.Line`), with no log parsing.

## Decision

Option 2.

- **One private pin file per host.** `~/.config/simstim/ssh/known_hosts-<host>` (0600) stores the host's public keys under the alias `simstim-<host>`, formatted with `knownhosts.Line`.
- **Command-line enforcement.** Whenever a host's pin file exists, every `ssh` command for that host passes `-o HostKeyAlias=simstim-<host> -o UserKnownHostsFile=<file> -o StrictHostKeyChecking=yes -o UpdateHostKeys=no -o GlobalKnownHostsFile=none -o VerifyHostKeyDNS=no -o NoHostAuthenticationForLocalhost=no -o CheckHostIP=no -o FingerprintHash=sha256 -o LogLevel=ERROR`, plus `-o KnownHostsCommand=none` and `-o NoHostAuthenticationForProxyCommand=no` when the local `ssh` supports them. These options come before any config file, so a `StrictHostKeyChecking no` or `UserKnownHostsFile /dev/null` in `~/.ssh/config` or Lima's SSH config cannot undo them.
- **Obtaining the first key.**
  1. With `--host-key SHA256:…`, the client fetches the host's public keys without logging in (`ssh-keyscan` for direct hosts, or `ssh` with `StrictHostKeyChecking=accept-new` into a temporary file and `PreferredAuthentications=none` for hosts behind a jump host or proxy), checks for revocation in the user's SSH settings, matches the fingerprint with `ssh.FingerprintSHA256`, and writes the pin.
  2. Otherwise, the client runs one `ssh` connection with the user's own OpenSSH settings and `StrictHostKeyChecking=yes`. If OpenSSH trusts the key, the client pins the host's public keys from that verified session (falling back to a no-login key fetch if needed). If the user's settings mark the key changed or revoked, `host add` refuses with exit code 5. If the host key is unknown, `host add` lets OpenSSH ask at a terminal (`StrictHostKeyChecking=ask` into a temporary file with no login), pins on first use with `--yes` or `host create lima`, or fails with exit code 4 when input is off.
- **No log parsing or epoch index.** There is no `-E` debug-log parser, no staging protocol and no ControlMaster epoch index. ControlMaster sockets stay in the existing private `nf-%C` folder, and `PinHostKey`, `host rekey` and `ForgetHost` stop the host's master with `ssh -O exit`.
- **Changed keys and rekey.** A changed host key makes `ssh` exit with 255 and refuses the connection with exit code 5, naming the pinned and shown SHA256 fingerprints and `host rekey`. `host add` never replaces an existing pin. Only `host rekey <host>` replaces a pin, after showing old and new fingerprints and asking for the typed host name (or `--confirm <host>`).
- **Existing hosts.** A host saved before this change gets its pin on its next connection before the bridge starts.

## Consequences

- Good, because no line in `~/.ssh/config`, an `Include` file or Lima's generated config can make the client accept a different host key.
- Good, because the implementation is a few hundred lines on OpenSSH and `golang.org/x/crypto/ssh`, with no custom log parser or key-format code.
- Good, because `known_hosts-<host>` is a plain OpenSSH file that `ssh-keygen -lf` reads directly.
- Bad, because a reinstalled host needs `host rekey` before connecting again.
- Bad, because a host trusted only via `@cert-authority` gets its current public key pinned, so rotating that key needs `host rekey`.
