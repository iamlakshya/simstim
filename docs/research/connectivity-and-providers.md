# Connectivity and providers

Research note for simstim. Researched on 14 and 15 October 2026.

This note covers how the simstim client reaches a host, and how it keeps that link alive. The rules for the note:

- Facts come from primary sources: docs, man pages, release notes, source code and issues. Links are inline.
- Code was read from shallow clones made on 14 and 15 October 2026. Line numbers can drift, so most links point to a file, not a line.
- **UNVERIFIED** marks a claim that I could not confirm from a primary source.
- Prices are on-demand list prices, fetched on 15 October 2026, without VAT.

## Summary

1. **Use the system OpenSSH client.** Do not embed an SSH library. Only OpenSSH honours all of these:
   - the whole `~/.ssh/config`;
   - every agent, including 1Password and Secretive;
   - FIDO keys and certificates;
   - post-quantum key exchange.

   Zed, VS Code Remote-SSH, Herdr, Mutagen and Warp all shell out to `ssh`.
2. **Use one long-lived channel per host.** Run `ssh -T` with a small simstim host helper at the far end. The helper speaks framed RPC over stdio. Attach streams, lists, events and image paste all go over it. An app-level heartbeat detects a dead link in seconds. Zed and Herdr work this way.
3. **Add a ControlMaster that simstim owns, on macOS and Linux only.**
   - Use it for bootstrap, quick CLI calls and, in v2, port forwards.
   - Never trust `ssh -O check` as a health test.
   - Windows OpenSSH has no ControlMaster, so nothing may depend on it.
4. **Install the helper by upload from the client.** Ship static builds. The flow:
   1. Run the helper first, and install only if that fails.
   2. Verify a SHA-256.
   3. Install into a versioned path.
   4. Do a handshake with magic bytes, a protocol version and capabilities.

   Ask before the first install. Never install during a background reconnect.
5. **Support providers through ssh_config alone.** Section 4 has recipes for:
   - GCP IAP;
   - AWS SSM and EC2 Instance Connect Endpoint;
   - Azure Bastion;
   - plain VPS providers;
   - Tailscale;
   - Cloudflare Access;
   - a homelab VM behind ProxyJump.

   Each has its own limit. For example: IAP drops a session after 1 hour idle, EICE ends every connection at 1 hour, and Bastion has no ProxyCommand mode.
6. **Expect the connection to die on every sleep or network change.**
   - Detect it fast: a 5 s heartbeat with a deadline of about 15 s.
   - Probe at once on wake and on network change.
   - Reconnect with backoff, then reattach.
   - Never replay keystrokes typed while disconnected.
7. **Put replay in the host's session daemon, not in the transport.** Keep an output ring per session, with byte offsets. On reattach:
   - if the client's last offset is still in the ring, replay from it;
   - if not, redraw the screen.

   This keeps native scrollback and missed notifications across short gaps. Eternal Terminal v7, tsshd, Zed and VS Code all keep an acknowledged buffer of this kind.
8. **Keep these defaults:**
   - no listening ports on hosts;
   - no relay;
   - no telemetry;
   - no agent forwarding.

   Agent credentials live on the host, scoped to that host.

The closest prior art is [Herdr](https://github.com/herdrdev/herdr). It is written in Rust under the Apache-2.0 licence, and had about 44,000 stars on 15 October 2026. It already uses this shape:

- system `ssh` with a private ControlMaster;
- a stdio bridge to a Unix socket on the host;
- client-side upload checked by SHA-256;
- image paste by staging a file on the host.

simstim should study it closely, both to reuse its lessons and to find a real difference.

## 1. System OpenSSH or an embedded library

### 1.1 What simstim needs from an SSH client

- `~/.ssh/config` with `Include` and every `Match` form.
  - `Match` covers `canonical`, `final`, `exec`, `localnetwork`, `host`, `originalhost`, `tagged`, `command`, `user`, `localuser`, `version` and `sessiontype` in OpenSSH 10.0 (`man ssh_config` on this Mac).
  - It also needs `ProxyJump` (multi-hop), `ProxyCommand`, `CanonicalizeHostname`, `IdentityAgent` and `CertificateFile`.
- Every agent:
  - the system `ssh-agent`;
  - 1Password, whose socket is `~/Library/Group Containers/2BUA8C4S2C.com.1password/t/agent.sock` ([1Password compatibility](https://www.1password.dev/ssh/agent/compatibility/));
  - Secretive, which uses Secure Enclave keys, and these "only support 256-bit EC keys", so `ecdsa-sha2-nistp256` ([Secretive FAQ](https://github.com/maxgoedjen/secretive/blob/main/FAQ.md)).
- `known_hosts`, including:
  - hashed hosts;
  - `@cert-authority` and `@revoked`;
  - `UpdateHostKeys`;
  - `StrictHostKeyChecking accept-new`.
- FIDO keys (`sk-ssh-ed25519@openssh.com`, `sk-ecdsa-sha2-nistp256@openssh.com`) and OpenSSH user and host certificates.
- Post-quantum key exchange.
  - OpenSSH 10.0 (2025-04-09) made `mlkem768x25519-sha256` the default.
  - OpenSSH 10.1 (2025-10-06) warns when a connection uses a key exchange that is not post-quantum ([release notes](https://www.openssh.org/releasenotes.html)).

### 1.2 Comparison

| Need | System `ssh` | Go `x/crypto/ssh` + `kevinburke/ssh_config` | Rust `russh` 0.64 + `russh-config` | Rust `ssh2` (libssh2) | libssh 0.12 (C) / `libssh-rs` | Rust `openssh` crate |
| --- | --- | --- | --- | --- | --- | --- |
| Config parser | Full | Parser only. `Match all` and `Match host` only, and `Match exec` is an error ([parser.go](https://github.com/kevinburke/ssh_config/blob/master/parser.go)) | About 10 keywords. No `Match`, `Include`, `IdentityAgent` or `CertificateFile` ([lib.rs](https://github.com/Eugeny/russh/blob/main/russh-config/src/lib.rs)) | None | `Include`, `IdentityAgent`, `CertificateFile` and `Match exec` work. `Match final` and `Match canonical` are skipped ([config.c](https://gitlab.com/libssh/libssh-mirror/-/blob/master/src/config.c)) | Uses system `ssh` |
| ProxyJump / ProxyCommand | Yes | You build it yourself | ProxyJump is parsed but not dialled | You build it yourself | Native ProxyJump since 0.11 | Yes |
| Agent and agent forwarding | Yes | Yes ([agent/forward.go](https://github.com/golang/crypto/blob/master/ssh/agent/forward.go)) | Yes | UNVERIFIED | UNVERIFIED | Yes |
| known_hosts | Full | Hashed, `@cert-authority` and `@revoked`. No `UpdateHostKeys`. Open bug [golang/go#29286](https://github.com/golang/go/issues/29286) | Hashed and exact hosts only. No CA, revoked or wildcards ([known_hosts.rs](https://github.com/Eugeny/russh/blob/main/russh/src/keys/known_hosts.rs)) | Hashed. No CA or revoked | UNVERIFIED | Full |
| FIDO keys | Yes (see the Apple note below) | Verify only. Signing must go through an agent | Listed. Signing UNVERIFIED | Caller supplies a callback | Yes, via libfido2 | Yes |
| Certificates | Yes | Yes | Yes | UNVERIFIED | Yes | Yes |
| Post-quantum KEX | ML-KEM and sntrup761 | ML-KEM (Go 1.24+). No sntrup761 | ML-KEM. No sntrup761 | None in the released crate. libssh2 1.11.1 has none; master has ML-KEM but is unreleased | libssh 0.12 has both, but `libssh-rs-sys` 0.2.8 bundles libssh 0.11.4, which has none | Yes |
| macOS Keychain (`UseKeychain`) | Apple's build only | No | No | No | No | Yes |
| Windows client | Yes, without ControlMaster | Yes | Yes | Yes | Yes | No. It fails to compile on non-Unix ([lib.rs](https://github.com/openssh-rust/openssh/blob/master/src/lib.rs)) |

**FIDO on a Mac.** Apple's `ssh` has no built-in FIDO support. Its `/usr/libexec/ssh-sk-helper` does not link libfido2 and prints "internal security key support not enabled" (checked with `otool -L` and `strings` on macOS 26.2). FIDO keys on a Mac therefore usually mean Homebrew OpenSSH or a `SecurityKeyProvider`. A tool that runs the first `ssh` in `PATH` picks up whichever setup the user has.

**How other tools chose.**

- System `ssh`:
  - Zed ([transport/ssh.rs](https://github.com/zed-industries/zed/blob/main/crates/remote/src/transport/ssh.rs));
  - VS Code Remote-SSH;
  - Mutagen (`exec.LookPath("ssh")`, [ssh_posix.go](https://github.com/mutagen-io/mutagen/blob/master/pkg/ssh/ssh_posix.go));
  - Herdr ([attach.rs](https://github.com/herdrdev/herdr/blob/master/src/remote/attach.rs));
  - Warp ([docs](https://docs.warp.dev/terminal/warpify/ssh)).
- OpenSSH config that runs their own binary as a `ProxyCommand`: Coder and DevPod.
- Embedded libraries:
  - Teleport uses `x/crypto/ssh`.
  - WezTerm uses libssh or libssh2, with its own parser. That parser only logs "not implemented" for `Match exec` and does not support ProxyJump directly ([wezterm-ssh config.rs](https://github.com/wezterm/wezterm/blob/main/wezterm-ssh/src/config.rs)).

### 1.3 The costs of shelling out, and how to handle them

- **You inherit the user's `ssh`, bugs included.**
  - OpenSSH 10.1 had a bug that "rendered ssh(1) unusable when ControlPersist was enabled" for terminal sessions. 10.2 fixed it (bz3872, [release notes](https://www.openssh.org/releasenotes.html)).
  - Read `ssh -V` at start, and keep a list of known bad versions.
  - This Mac (macOS 26.2) ships `OpenSSH_10.0p2`. The latest release is 10.6 (2026-10-06).
- **Gate new features by version.**
  - Set a floor of OpenSSH 8.0 on the client.
  - Use `-P tag` and `Match tagged` only on 9.4 or later.
  - Use `-O conninfo` and `-O channels` only on 10.3 or later.
- **Prompts.** `ssh` may ask for a passphrase, a password, a host-key decision or a 2FA code, or print a browser link (IAP, Cloudflare Access, Tailscale check mode). There are two ways to handle this:
  - For the first connection, give `ssh` the real terminal in the foreground.
  - In the TUI, set `SSH_ASKPASS` to a simstim helper, with `SSH_ASKPASS_REQUIRE=force` (added in OpenSSH 8.4, 2020-09-27). Zed uses askpass.

  Background reconnects should run with `BatchMode=yes`. If that fails for an auth reason, show "press Enter to reconnect" rather than loop. Agent prompts such as Touch ID still appear in `BatchMode`, because the agent shows them, not `ssh`.
- **Errors arrive as exit code 255 plus stderr text.** Keep the stderr of each `ssh` process in a per-host log, and show its last lines when a connect fails.
- **The remote command runs through the user's login shell,** which may be fish or nu, not POSIX `sh`.
  - Send scripts on stdin to `sh -s` (`ssh host 'sh -s' < script`), as VS Code does with bash.
  - Frame the output, because rc files print noise:
    - Zed uses NUL delimiters;
    - VS Code uses UUID start and end markers;
    - Mutagen uses magic bytes.

### 1.4 How to use system `ssh` well

- **Never parse `~/.ssh/config` yourself.** Run `ssh -G <alias>` to get the resolved values. It evaluates every `Host` and `Match` block. Zed and Herdr both do this to find a user's ControlPath.
- **Do not edit `~/.ssh/config`.** Copy Herdr: generate a wrapper file and pass it with `-F` ([attach.rs](https://github.com/herdrdev/herdr/blob/master/src/remote/attach.rs)):

  ```sshconfig
  # ~/.config/simstim/ssh_config, generated, passed with -F
  Include ~/.ssh/config
  Include ~/.config/simstim/hosts.conf   # recipes that simstim's wizards wrote
  Include /etc/ssh/ssh_config
  Host *
    ServerAliveInterval 10
    ServerAliveCountMax 3
    ConnectTimeout 15
  ```

  - OpenSSH uses the first value it finds for each option. So the user's own settings win over simstim's defaults, and `-o` on the command line wins over both.
  - Print an `Include` line that the user can add to `~/.ssh/config` if they want plain `ssh` to see simstim's hosts.
  - When `-F` is given, `ssh` skips the default user and system files. That is why the wrapper includes them explicitly.
- **Tag simstim's connections.** On OpenSSH 9.4 or later, pass `-P simstim`. Users can then write `Match tagged simstim` blocks for settings that apply only to simstim. 9.4 added `Tag`, `Match tagged` and `Match localnetwork` ([release notes](https://www.openssh.org/releasenotes.html)).
- **Keep compression off** on shared connections. OpenSSH 10.6 disabled the LZ77 dictionary coder "to mitigate the side-channel leaks" across multiplexed channels ([release notes](https://www.openssh.org/releasenotes.html)).
- **Handle the terminal identity yourself.** This matters for fidelity on Ghostty.
  - Ghostty sets `TERM=xterm-ghostty`. Most hosts lack that terminfo entry, and tools then fail with "missing or unsuitable terminal" ([Ghostty terminfo](https://ghostty.org/docs/help/terminfo)).
  - Ghostty's `ssh-terminfo` and `ssh-env` shell integration only wrap an interactive `ssh` typed in the shell. They also depend on `sshd` accepting the variables ([Ghostty reference](https://ghostty.org/docs/config/reference)).
  - `sshd` accepts `TERM` only when a PTY is requested. It drops `COLORTERM` unless `AcceptEnv` allows it (`man sshd_config`).
  - So simstim must:
    - install the client's terminfo entry on the host during bootstrap (see section 3);
    - send `TERM`, `COLORTERM`, `TERM_PROGRAM` and `TERM_PROGRAM_VERSION` inside the attach request, not through `SendEnv`.

## 2. Multiplexing

### 2.1 ControlMaster: what it gives

- `ControlMaster`, `ControlPath` and `ControlPersist` let many `ssh` commands share one TCP and SSH connection. A command through a live master skips TCP, key exchange and auth.
- Speed: one anecdotal measurement shows about 0.66 s for a fresh `ssh server true`, against 0.03 s through a master ([Wikibooks](https://en.wikibooks.org/wiki/OpenSSH/Cookbook/Multiplexing)).
- Through IAP or SSM the saving is far larger. Each new connection must start the `gcloud` or `aws` CLI, fetch a token and open a WebSocket. The local `gcloud` start-up alone measured 0.4–1.2 s. Reports of 20–30 s for SSM plus Instance Connect are anecdotal ([HN](https://news.ycombinator.com/item?id=43007982)). Rigorous per-connection numbers: UNVERIFIED.
- Control commands: `ssh -O check|conninfo|channels|forward|cancel|proxy|exit|stop`. `conninfo` and `channels` arrived in 10.3.
- `%C` is a SHA-1 hex hash of `%l%h%p%r%j`. It includes the jump host, so each ProxyJump chain gets its own socket.

### 2.2 ControlMaster: the pitfalls

- **`-O check` does not test the network.** The master replies "alive" with its PID without touching the server ([mux.c](https://github.com/openssh/openssh-portable/blob/master/mux.c)). After sleep or roaming, a wedged master passes the check, and every new client hangs on the dead TCP connection until `ServerAlive` or the kernel gives up.
  - [zest.releaser#150](https://github.com/zestsoftware/zest.releaser/issues/150): after suspend, git push "will hang for 5 minutes".
  - [lima#5420](https://github.com/lima-vm/lima/issues/5420) (open, 2026): "Host sleep is the reliable trigger", and `-O forward` forwards die silently.
- **Unix socket path limit.** macOS limits `sun_path` to 104 bytes. The master first binds `<ControlPath>.<16 random chars>`. So a ControlPath must be at most about 86 bytes on macOS, or the master cannot start. Herdr uses a literal `/tmp` path to save bytes.
- **Races with `ControlMaster auto`.** When two clients start at once, the loser logs "already exists, disabling multiplexing" and makes its own connection. [OpenSSH bug 2707](https://lists.mindrot.org/pipermail/openssh-bugs/2017-April/017629.html) (still open) reports a broken pipe when a client reuses a master just as `ControlPersist` expires.
- **Pipe inheritance.** With `auto` and `ControlPersist`, the forked master keeps the caller's stderr open, so a caller that waits for EOF hangs ([golang/go#13453](https://github.com/golang/go/issues/13453), [ansible#66535](https://github.com/ansible/ansible/issues/66535)). The fix is to start the master yourself, with stdio set to null. The `openssh` crate does this ([builder.rs](https://github.com/openssh-rust/openssh/blob/master/src/builder.rs)).
- **Session limit.** `sshd` has `MaxSessions` 10 by default. That is the number of shell, exec and subsystem sessions per network connection (`man sshd_config`). Five attached agents, each on its own `ssh -t` through one master, plus RPC, uploads and `sftp`, come close to it. Port forwards are channels, not sessions, so they do not count.
- **Windows.** Win32-OpenSSH lists client ControlMaster as out of scope ([Project Scope](https://github.com/PowerShell/Win32-OpenSSH/wiki/Project-Scope)). Issue [#405](https://github.com/PowerShell/Win32-OpenSSH/issues/405) has been open since 2016. On Windows every `ssh` call is a new TCP connection with a full handshake.
- **Jump hosts.** Options passed with `-o` "generally apply to the destination host and not any specified jump hosts" (`man ssh_config`). The jump hop gets only what the user's config gives it. Dead-link detection still works end to end, because the destination's `ServerAlive` probes travel through the jump.

### 2.3 How others keep one long-lived channel

- **Zed** ([transport/ssh.rs](https://github.com/zed-industries/zed/blob/main/crates/remote/src/transport/ssh.rs), [remote_client.rs](https://github.com/zed-industries/zed/blob/main/crates/remote/src/remote_client.rs), [server.rs](https://github.com/zed-industries/zed/blob/main/crates/remote_server/src/server.rs)):
  - It starts its own master: `ssh -N -o ControlMaster=yes -o ControlPersist=no -o ControlPath=<tempdir>/ssh.sock`, with askpass.
  - Every other command adds `-o ControlMaster=no`.
  - Since [zed#45271](https://github.com/zed-industries/zed/pull/45271) it reuses a user's master if `ssh -G` and `-O check` find one.
  - The channel runs `remote_server proxy --identifier <id> [--reconnect]`, which joins a daemon on the host through Unix sockets. Messages are protobuf envelopes with a u32 length prefix.
  - Heartbeat: every 5 s. After 5 misses it reconnects, with at most 3 attempts.
  - Unacknowledged messages stay in a buffer and are replayed after reconnect.
  - The daemon exits after 10 minutes with no client.
- **VS Code Remote-SSH** (VSIX 0.129, plus [ipc.net.ts](https://github.com/microsoft/vscode/blob/main/src/vs/base/parts/ipc/common/ipc.net.ts)):
  - It runs one `ssh` with a dynamic SOCKS forward (`-D`) and a "local server" that shares the connection between windows.
  - Its `PersistentProtocol` gives every message an id and an ack, and replays unacknowledged messages on reconnect.
  - Keepalive is sent every 5 s, with a 20 s timeout.
  - The server keeps a disconnected client for 3 hours (`--reconnection-grace-time`, default 10800 s).
  - ControlMaster appears only as advice in its help text, for macOS and Linux.
- **Herdr** ([attach.rs](https://github.com/herdrdev/herdr/blob/master/src/remote/attach.rs), [remote_bridge.rs](https://github.com/herdrdev/herdr/blob/master/src/platform/remote_bridge.rs)):
  - It uses system `ssh` with a private `-S <socket>`, `ControlMaster=auto` and `ControlPersist=600` on Unix. It sets `multiplexing: false` on Windows.
  - The transport is `ssh -T host herdr remote-client-bridge`, which pipes stdio to the server's Unix socket.
  - Background calls add `BatchMode=yes`, `StrictHostKeyChecking=yes` and `ConnectTimeout=10`.
  - **Stale-master handling.** It probes the master with `-o ProxyCommand=false`, so a dead master fails fast instead of silently opening a new connection. If the probe fails and a fresh connection works, it runs `-O stop` on the stale master.
  - The bridge has a 60 s idle watchdog, on a clock that counts time asleep.
- **Mutagen** runs its agent over stdio with `ServerAliveInterval=10`, `ServerAliveCountMax=1` and `ConnectTimeout=5` ([transport.go](https://github.com/mutagen-io/mutagen/blob/master/pkg/agent/transport/ssh/transport.go)).

### 2.4 Comparison

| | One long-lived `ssh -T` plus host helper (framed RPC) | ControlMaster plus one `ssh` per call |
| --- | --- | --- |
| Latency of a list or metadata call | One round trip. No process to start | Start a local `ssh`, open a channel, exec: tens of milliseconds |
| Dead-link detection | App heartbeat, independent of TCP state | Master's `ServerAlive` only. `-O check` lies, so each call needs its own timeout |
| Windows client | Works the same | Not available. Every call is a full connection |
| Sessions against `MaxSessions` | 1 for all attached sessions | 1 per attached session plus extras |
| ProxyJump, IAP, SSM | Setup cost paid once per connect | Paid once per master. Paid per call without one |
| Push events (port opened, agent waiting) | Natural | Needs polling |
| Port forwards | Needs a master, or TCP tunnelling inside the RPC | `ssh -O forward` |
| Cost | You must build, install and version a host binary | None |

### 2.5 Recommendation

Use both, in layers. The RPC channel is the product. The master is an optimisation that simstim owns.

```sh
# 1. Master (macOS and Linux only). simstim starts and supervises it.
#    stdio goes to /dev/null. -E sends logs to a file.
ssh -F ~/.config/simstim/ssh_config -P simstim \
    -M -N -f -E ~/.local/state/simstim/log/<host>.ssh.log \
    -o ControlPath=/tmp/simstim-501/%C -o ControlPersist=10m \
    -o ServerAliveInterval=10 -o ServerAliveCountMax=3 \
    <alias>

# 2. RPC channel through the master. One per host, long-lived.
ssh -F ~/.config/simstim/ssh_config -P simstim \
    -S /tmp/simstim-501/%C -o ControlMaster=no \
    -T -o ClearAllForwardings=yes -o ForwardAgent=no \
    <alias> '~/.local/share/simstim/bin/0.1.0/simstim-host bridge'

# 3. Health probe of the master: fails fast instead of opening a new connection.
ssh -S /tmp/simstim-501/%C -o ControlMaster=no -o ProxyCommand=false <alias> true
```

1. **One RPC channel per connected host.** It carries:
   - list and metadata calls;
   - session events;
   - the byte streams of attached sessions;
   - image uploads, in chunks of 64 KiB or less, with priority for terminal frames;
   - heartbeats.

   Five agents on one host then cost one SSH session, not five.
2. **The helper's `bridge` command is thin.** It connects to the long-running session daemon's Unix socket under `${XDG_RUNTIME_DIR:-/tmp}/simstim-$UID/`, mode 0700. All state lives in the daemon, so a dropped `ssh` loses nothing on the host. This is the Zed and Herdr pattern.
3. **The master is private to simstim.**
   - Use a short ControlPath. `/tmp/simstim-$UID/%C` is about 60 bytes, under the 86-byte limit.
   - Start it explicitly with `-M -N -f` and stdio set to null. Do not use `auto`.
   - Use it for bootstrap commands, for CLI calls such as `simstim ls` while the TUI is closed, and in v2 for `ssh -O forward`.
   - Health is decided by the RPC heartbeat, never by `-O check`. When the heartbeat fails, run `ssh -O exit` (or kill the PID), then start a new master.
4. **Without a master** (Windows, or a user opt-out), the RPC channel is the only connection. Bootstrap takes a few extra connections once per helper version.
5. **In v1, attach goes over RPC, not `ssh -t`.**
   - The client sets the local terminal to raw mode and forwards input bytes untouched, except for the detach key. It forwards output bytes untouched, and sends resize events as frames.
   - This is as faithful as `ssh -t`, because both are plain byte relays. It also avoids `MaxSessions`, keeps one heartbeat, and lets replay work (section 5).
   - It also sidesteps `ObscureKeystrokeTiming`, which sends keystrokes on a 20 ms grid with fake packets "for interactive sessions" (`man ssh_config`; on by default since 9.5). The client applies it only while a TTY channel is open (`channel_tty_open()` in `obfuscate_keystroke_timing()`, [clientloop.c](https://github.com/openssh/openssh-portable/blob/master/clientloop.c)), so an `ssh -T` channel never pays the delay of up to 20 ms. The cost is that the privacy defence against keystroke-timing analysis is lost too. simstim could add its own padding later if users want it.
6. **Keep a plain fallback:** `ssh -t -e none host simstim-host attach <id>`. It works from any SSH client, including a phone, and helps when debugging. `-e none` stops `~.` in an agent prompt from killing the link.

## 3. Installing the host helper over SSH

### 3.1 How others do it

| Tool | OS and arch detection | Who fetches the binary | Version handshake | Install path and clean-up | Checksum |
| --- | --- | --- | --- | --- | --- |
| Zed ([ssh.rs](https://github.com/zed-industries/zed/blob/main/crates/remote/src/transport/ssh.rs)) | `uname -sm` with NUL-framed output. PowerShell and `cmd /c ver` on Windows. Linux and macOS, x86_64 and aarch64 only | Host runs `curl` or `wget`. Fallback: client downloads, then uploads with `sftp` or `scp` | Exact version, in the file name | `~/.zed_server/zed-remote-server-<channel>-<version>`. The server deletes old versions that are not in use | None (HTTPS only) |
| VS Code Remote-SSH | Script fed to bash on stdin. `uname -s`, `uname -m`, `getconf LONG_BIT`. The CLI checks glibc ≥ 2.28 or musl ([prereqs.rs](https://github.com/microsoft/vscode/blob/main/cli/src/util/prereqs.rs)) | Host runs `wget` or `curl`. `remote.SSH.localServerDownload` makes the client use `scp`. `manual-bootstrap` mode for offline hosts | Exact commit, plus a connection token | `~/.vscode-server/…`. Keeps 5. File locks, with `lockfilesInTmp` for NFS homes ([download_cache.rs](https://github.com/microsoft/vscode/blob/main/cli/src/download_cache.rs)) | None |
| Herdr ([attach.rs](https://github.com/herdrdev/herdr/blob/master/src/remote/attach.rs)) | `uname -s; uname -m` through `/bin/sh`. Probes for Windows first | Client. It reuses its own binary when the platforms match. Otherwise it downloads, checks against a manifest, and pipes the bytes into `ssh … tee <tmp>`, then runs `chmod` and `mv` | Protocol generation plus capabilities. "Compatible client and server versions do not have to match" | `~/.local/bin/herdr`. Asks before install. Background reconnects never install | SHA-256 on the client ([checksum.rs](https://github.com/herdrdev/herdr/blob/master/src/checksum.rs)) |
| Mutagen ([pkg/agent](https://github.com/mutagen-io/mutagen/tree/master/pkg/agent)) | Runs the agent first. On exit 126 or 127, probes `uname -s -m`, then `cmd.exe /c set` | Client, with `scp`, from a bundle of agents for every platform | Magic bytes `05 27 87`, then exact major, minor and patch | `~/.mutagen/agents/<version>/`. Deletes versions unused for 30 days. Bug: `chmod +x` runs only from a Windows client, and SFTP-based `scp` can drop the mode bits ([#564](https://github.com/mutagen-io/mutagen/pull/564)) | None |
| DevPod ([inject.sh](https://github.com/loft-sh/devpod/blob/main/pkg/inject/inject.sh)) | Pattern match on `uname -a`. Linux only | Client streams into `cat >`, or host runs `curl`/`wget` | ping/pong, then the exact `version` string | `/tmp/devpod`, with a check for noexec mounts. No push since November 2025 | None (size only) |
| Coder | Not done over SSH. The workspace's start-up script downloads the agent from the Coder server ([bootstrap_linux.sh](https://github.com/coder/coder/blob/main/provisionersdk/scripts/bootstrap_linux.sh)) | Host | API major.minor: same major, server minor ≥ client minor ([apiversion.go](https://github.com/coder/coder/blob/main/apiversion/apiversion.go)) | `mktemp -d` at each boot | None (checks only that `--version` contains "Coder") |

The common patterns:

- Every tool uses `uname -s` and `uname -m`. Windows detection is a fallback.
- Static or musl builds remove the glibc and musl question.
- Output is framed against rc-file noise.
- Install is atomic: temp file, then `chmod`, then `mv`.
- Versioned paths make concurrent installs safe without a lock.
- Only Herdr checks a SHA-256.

### 3.2 Design for simstim

1. **Build static helpers.** Use Rust `*-unknown-linux-musl` or Go with `CGO_ENABLED=0`. Targets:
   - `linux-x86_64` and `linux-aarch64` in v1;
   - `darwin-aarch64` and `darwin-x86_64` for Mac hosts;
   - later perhaps `freebsd-x86_64` and `linux-armv7`.
2. **The client owns the binaries.**
   - It downloads the helper for a platform on first need, from the release that matches its own version. The release manifest lists SHA-256 values and is signed (minisign or Sigstore).
   - It caches helpers under `~/Library/Caches/simstim/helpers/<version>/`.
   - A `simstim helper fetch --all` command prepares an offline laptop.
   - Hosts never need outbound internet for simstim. An optional host-download mode can come later.
3. **Run first, install on failure,** like Mutagen. Exec the versioned path. Exit code 126 or 127, or a failed handshake, leads to install.
4. **Probe with one script,** sent on stdin to `sh -s`, with NUL-framed output. It collects:
   - `uname -s`, `uname -m` and `getconf LONG_BIT`;
   - `$HOME` and `$XDG_RUNTIME_DIR`;
   - whether `sha256sum`, `shasum` or `openssl` exist;
   - whether `tic` and `infocmp` exist;
   - whether the install directory allows exec.
5. **Upload over an exec channel, not SFTP.** `sftp` may be disabled on the host. Since OpenSSH 9.0, `scp` uses the SFTP protocol by default ([release 9.0](https://www.openssh.org/txt/release-9.0)). The steps:
   1. `umask 077; mkdir -p "$D"; cat > "$D/.simstim-host.$$"`
   2. Check the SHA-256 on the host if a tool exists. Otherwise check the size.
   3. Always run `chmod 755`.
   4. `mv -f` into place.
   5. Run `--version` as a self-test. This also catches a noexec mount.
6. **Paths.**
   - Binary: `${XDG_DATA_HOME:-$HOME/.local/share}/simstim/bin/<version>/simstim-host`.
   - State: `~/.local/state/simstim/`.
   - Sockets: `${XDG_RUNTIME_DIR:-/tmp}/simstim-$UID/`, mode 0700. Linux's socket path limit is 108 bytes.
   - Provide an override for a noexec or NFS home.
7. **Handshake.**
   - The helper first writes a magic prefix and one line, for example `\0SIMSTIM proto=1.2 ver=0.4.2 caps=attach,files,ports\n`.
   - The client skips bytes until it sees the magic.
   - Compatibility rule: same protocol major, and the helper's minor at least the client's minor. Then check capabilities. Do not require exact versions.
8. **On mismatch,** install the new version beside the old one. Never kill a running session daemon without asking. The daemon owns live agent processes, so an upgrade must hand over or wait (Herdr asks first).
9. **Clean-up.** At helper start, delete versions that are older than the newest three and not in use.
10. **Terminfo.** The client runs `infocmp -x "$TERM"` locally and sends the result. The helper compiles it into `~/.terminfo` with `tic -x`, or writes the compiled entry itself when the host has no `tic`.
    - A recent `ncurses` is needed locally. Apple's `infocmp` before Sonoma is too old ([Ghostty terminfo](https://ghostty.org/docs/help/terminfo)).
    - The helper doing the write itself is a simstim design idea, not prior art.
11. **Consent.** Ask before the first install on a host. Show the path and the size. Background reconnects never install.

## 4. Provider recipes

simstim v1 uses `ssh_config` only, with no provider SDKs. A wizard may print or write these blocks into simstim's own `hosts.conf` (section 1.4). Each recipe expects simstim's defaults on top: a heartbeat in the RPC channel, plus `ServerAliveInterval 10` and `ServerAliveCountMax 3`.

### 4.1 Summary

| Recipe | Extra hop | Survives a network change? | Hard limits |
| --- | --- | --- | --- |
| Plain SSH (Hetzner, DigitalOcean, Linode, Vultr) | None | No. The TCP connection dies when the client's IP changes | None |
| Tailscale | WireGuard, direct or through a DERP relay | Often. The 100.x address stays the same, so TCP can live through a short roam (UNVERIFIED as a guarantee) | Node key expires after 180 days by default |
| Tailscale SSH | As above | As above | A policy change or a `tailscaled` restart ends sessions. Check mode asks you to log in again |
| GCP IAP | HTTPS WebSocket via Google | Partly. `gcloud` resumes the tunnel for up to 15 minutes | Drops after 1 hour idle |
| AWS SSM | HTTPS WebSocket via AWS | Poorly. Resume often fails | Idle timeout 1–60 min (default 20) may apply. About 0.6 MB/s |
| AWS EICE | HTTPS WebSocket via AWS | No reconnect | Every connection ends at 1 hour. 20 connections per endpoint |
| Azure Bastion | Local tunnel via Bastion | No | No ProxyCommand mode. Standard SKU costs about $212 a month |
| Cloudflare Access | HTTPS WebSocket via Cloudflare | No ([cloudflared#1301](https://github.com/cloudflare/cloudflared/issues/1301)) | Edge restarts close WebSockets |
| ProxyJump (homelab) | One more SSH hop | Depends on the first hop (Tailscale here) | `MaxSessions` on the jump host |

### 4.2 Plain SSH: Hetzner, DigitalOcean, Akamai Linode, Vultr

```sshconfig
Host hz-dev
  HostName 203.0.113.10            # or the ::1 address of the server's IPv6 /64
  User dev                         # create a user; root is the default login
  IdentityFile ~/.ssh/id_ed25519_hz
  IdentitiesOnly yes
```

- **Default login.** `root` on all four. Vultr uses `linuxuser` with "Limited User Login" ([Hetzner](https://docs.hetzner.com/cloud/servers/getting-started/connecting-to-the-server/), [DigitalOcean](https://docs.digitalocean.com/products/droplets/how-to/connect-with-ssh/), [Linode](https://techdocs.akamai.com/cloud-computing/docs/set-up-and-secure-a-compute-instance), [Vultr](https://docs.vultr.com/limited-user-login)).
- **Firewalls.** All four offer a free cloud firewall that denies inbound traffic by default. Allow TCP 22 only from known addresses, or close 22 to the public and use Tailscale (section 4.3).
  - [Hetzner firewall](https://docs.hetzner.com/cloud/firewalls/overview/)
  - [DigitalOcean limits](https://docs.digitalocean.com/products/networking/firewalls/details/limits/)
  - [Linode firewall](https://techdocs.akamai.com/cloud-computing/docs/cloud-firewall)
  - [Vultr firewall](https://docs.vultr.com/firewall-quickstart-for-vultr-cloud-servers)
- **IPv4 and IPv6.**
  - Hetzner charges for a primary IPv4 (€0.50 a month). IPv6 is free ([Primary IPs](https://docs.hetzner.com/cloud/servers/primary-ips/overview/)). An IPv6-only host works when the laptop's network has IPv6. Many hotel and phone networks do not, so Tailscale is the safer path there.
- **Latency and roaming.**
  - Latency is the network round trip, with nothing added.
  - The connection dies when the laptop's IP changes.
  - A short sleep on the same network can survive if no keepalive fired during it. That is ordinary TCP behaviour, not a provider promise.
- **Gotcha.** DigitalOcean resets connections during a live migration.

### 4.3 Tailscale, and Tailscale SSH

```sshconfig
Host ts-dev
  HostName dev.example-tailnet.ts.net   # MagicDNS name; prefer the full name
  User dev
```

- **Addresses.** Tailscale 100.x addresses "stay the same, no matter where nodes move to" ([IP and DNS](https://tailscale.com/kb/1033/ip-and-dns-addresses)). The TCP 4-tuple does not change when the laptop changes Wi-Fi. A connection can therefore live through a short roam or sleep, if the tunnel returns before the timeouts fire. That follows from the design; no Tailscale doc promises it for SSH (UNVERIFIED).
- **MagicDNS.** On by default for tailnets created after 2022-10-20. Some macOS command-line tools bypass the system resolver, so use the full `*.ts.net` name ([MagicDNS](https://tailscale.com/kb/1081/magicdns)).
- **Paths.** A direct path uses UDP 41641. Otherwise traffic goes through a DERP relay over HTTPS on 443, with "lower maximum throughput" ([connection types](https://tailscale.com/docs/reference/connection-types)). After a network change a connection can fall back to a relay for a while. `tailscale ping` shows the path. DERP latency figures: UNVERIFIED.
- **Key expiry.** Node keys expire after 180 days by default, and connections stop. Tagged devices have expiry off by default ([key expiry](https://tailscale.com/kb/1028/key-expiry)). Tag server hosts.
- **Tailscale SSH** (`tailscale set --ssh`, [docs](https://tailscale.com/kb/1193/tailscale-ssh)):
  - It intercepts port 22 on the tailnet address only. Any client works, including system OpenSSH. SFTP and `scp` work.
  - It needs no keys on the host, and it reuses the host keys in `/etc/ssh` when it can read them.
  - A policy change that removes access, or a `tailscaled` restart, ends sessions. Turning it on hangs open SSH sessions to that address.
  - In `check` mode, the user must log in again in a browser every `checkPeriod` (default 12 h; a custom period needs a paid plan, [policy syntax](https://tailscale.com/kb/1337/policy-syntax)). simstim must show `ssh`'s stderr during connect so that the link is visible. Whether check mode ends open sessions: UNVERIFIED.
  - Policy flags for agent forwarding and port forwarding exist ([tailcfg.go](https://github.com/tailscale/tailscale/blob/main/tailcfg/tailcfg.go)). Their defaults on the hosted control plane: UNVERIFIED.
- **Recommendation.** Tailscale is the best default for VPS and homelab hosts. It closes port 22 to the internet and has the best roaming behaviour. Plain OpenSSH over the tailnet keeps the user's normal keys and agent. Tailscale SSH is a fine option, but simstim should not require it.

### 4.4 GCP: IAP TCP forwarding and OS Login

```sshconfig
Host gcp-dev
  HostName dev-vm-1                 # the instance name, not an IP
  User alice_example_com            # OS Login user name
  IdentityFile ~/.ssh/google_compute_engine
  ProxyCommand gcloud compute start-iap-tunnel %h %p --listen-on-stdin --project=my-project --zone=europe-west2-b --verbosity=warning
```

This follows Google's doc, [connect using IAP](https://docs.cloud.google.com/compute/docs/connect/ssh-using-iap).

- **First run.** Run `gcloud compute ssh dev-vm-1 --tunnel-through-iap` once, to create and register the key.
- **Hidden flag.** `--listen-on-stdin` is missing from the [reference page](https://docs.cloud.google.com/sdk/gcloud/reference/compute/start-iap-tunnel), but it is in the `gcloud` source.
- **Firewall.** Allow TCP 22 from `35.235.240.0/20` (IPv6: `2600:2d00:1:7::/64`). The VM needs no external IP ([using TCP forwarding](https://docs.cloud.google.com/iap/docs/using-tcp-forwarding)).
- **Roles.** `roles/iap.tunnelResourceAccessor`. With OS Login, also `roles/compute.osLogin` or `roles/compute.osAdminLogin`, and `roles/iam.serviceAccountUser` if the VM has a service account. To turn OS Login on, set the metadata `enable-oslogin=TRUE` ([set up OS Login](https://docs.cloud.google.com/compute/docs/oslogin/set-up-oslogin)).
- **OS Login user names** look like `alice_example_com`. Add your own key with `gcloud compute os-login ssh-keys add --key-file=… [--ttl=…]` ([add SSH keys](https://docs.cloud.google.com/compute/docs/connect/add-ssh-keys)). This works with Secretive and 1Password public keys.
- **`gcloud compute config-ssh` does not help.** It writes entries only for VMs with an external IP, and adds no ProxyCommand ([reference](https://docs.cloud.google.com/sdk/gcloud/reference/compute/config-ssh)).
- **OS Login certificates** (`enable-oslogin-certificates=TRUE`) reject unsigned keys. They "have an expiry of five minutes", and each connection needs an API call to sign ([certificates](https://docs.cloud.google.com/compute/docs/oslogin/certificates)). That does not fit a static `ssh_config`. Leave this mode out of v1.
- **Latency.** Traffic is wrapped in a WebSocket to `tunnel.cloudproxy.app`. Every byte takes a detour through Google's front end. Added latency figures: UNVERIFIED.
- **Roaming.** `gcloud` "attempts to reestablish the tunnel if it becomes disconnected" (docs). The source reconnects to `/v4/reconnect` with the session id and the count of acknowledged bytes, and keeps trying for up to 15 minutes. A short network change can therefore resume without breaking the SSH TCP stream. How long Google's side keeps the session: UNVERIFIED.
- **Gotchas.**
  - "IAP automatically disconnects sessions after one hour of inactivity" (docs). Keepalives count as traffic, so simstim's heartbeat keeps the session open (inference).
  - IAP "isn't intended for bulk transfer of data", and Google "reserves the right to rate-limit". No numbers are published.
  - Upload speed improves with NumPy installed beside `gcloud` (docs).
  - Each new connection starts `gcloud` (0.4–1.2 s measured locally) and fetches a token. This is a strong reason for the master and the long-lived channel.

### 4.5 AWS: SSM Session Manager, and EC2 Instance Connect Endpoint

**SSM** ([enable SSH connections](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-getting-started-enable-ssh-connections.html)):

```sshconfig
Host aws-dev
  HostName i-0123456789abcdef0
  User ec2-user                     # ubuntu on Ubuntu AMIs
  ProxyCommand sh -c "aws ssm start-session --target %h --document-name AWS-StartSSHSession --parameters 'portNumber=%p' --region eu-west-2"
```

- **Prerequisites.**
  - SSM Agent 2.3.672.0 or later on the instance, with an instance profile that has `AmazonSSMManagedInstanceCore` (or Default Host Management Configuration).
  - The Session Manager plugin 1.1.23.0 or later on the laptop ([macOS install](https://docs.aws.amazon.com/systems-manager/latest/userguide/install-plugin-macos-overview.html)).
  - The IAM permission `ssm:StartSession` on the instance and on `AWS-StartSSHSession`.
  - No inbound port.
- **Idle timeout.** Default 20 minutes, range 1–60 ([idle timeout](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-preferences-timeout.html)). The docs say "client-side inputs" reset the timer. Two things are UNVERIFIED:
  - whether the timeout applies to `AWS-StartSSHSession` port sessions at all;
  - whether SSH keepalive bytes reset it.

  Users report idle port sessions closing ([aws-sdk-go#4691](https://github.com/aws/aws-sdk-go/issues/4691)). Set the account's idle timeout to 60 minutes.
- **Maximum duration.** None by default. It can be set from 1 to 1,440 minutes ([max duration](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-preferences-max-timeout.html)).
- **Roaming.** The plugin tries to resume its WebSocket, but resume often fails ([session-manager-plugin#135](https://github.com/aws/session-manager-plugin/issues/135), [#136](https://github.com/aws/session-manager-plugin/issues/136): "Keepalive settings on SSH do not prevent the disconnect"). Treat SSM as "reconnect after every sleep".
- **Throughput.** About 0.6 MB/s whatever the instance size, because the service is capped at one message per millisecond ([amazon-ssm-agent#664](https://github.com/aws/amazon-ssm-agent/issues/664)). Fine for a terminal. Slow for large file pastes.
- **Logging.** SSH sessions through SSM get no session logging. That is good for secrets, but auditors may object.

**EC2 Instance Connect Endpoint** ([connect using EICE](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/connect-using-eice.html)):

```sshconfig
Host aws-eice
  HostName i-0123456789abcdef0
  User ec2-user
  ProxyCommand aws ec2-instance-connect open-tunnel --instance-id %h --region eu-west-2
```

- **Hard limit.** "The maximum duration for an established TCP connection is 1 hour (3,600 seconds)" ([EICE](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/connect-with-ec2-instance-connect-endpoint.html)). Every simstim connection will be cut each hour. The reconnect path must be smooth, and replay matters more here.
- **Quotas, none adjustable** ([quotas](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/eice-quotas.html)):
  - 5 endpoints per account per Region;
  - 1 per VPC;
  - 20 concurrent connections per endpoint.

  No extra charge, except data across availability zones.
- **No reconnect.** The CLI's WebSocket code has no reconnect logic (source check).
- **Key push** (`send-ssh-public-key`). "The key remains for 60 seconds" ([API](https://docs.aws.amazon.com/ec2-instance-connect/latest/APIReference/API_SendSSHPublicKey.html)). Each new connection needs a push, which does not fit plain `ssh_config`. Use a normal key in `authorized_keys` instead.

### 4.6 Azure: Bastion native client, and Entra ID login

- **Bastion native client** needs the Standard or Premium SKU with "Native Client Support". The Developer and Basic SKUs do not support it ([native client](https://learn.microsoft.com/en-us/azure/bastion/native-client), [SKU comparison](https://learn.microsoft.com/en-us/azure/bastion/bastion-sku-comparison)).
- **There is no stdio (ProxyCommand) mode.** The feature request has been open since January 2023 ([azure-cli-extensions#5807](https://github.com/Azure/azure-cli-extensions/issues/5807)). The workable recipe is a long-running local tunnel plus a host block:

  ```sh
  az network bastion tunnel --name my-bastion --resource-group rg \
     --target-resource-id <vm-resource-id> --resource-port 22 --port 50022
  ```

  ```sshconfig
  Host az-dev
    HostName 127.0.0.1
    Port 50022
    User azureuser
    HostKeyAlias az-dev       # keeps this host key apart from other localhost tunnels
  ```

  simstim cannot start the tunnel through `ssh_config` alone. The user, or a launchd job, must keep it running. A wrapper script that starts the tunnel and then runs `nc` might work as a ProxyCommand (UNVERIFIED).
- **Limits.**
  - The tunnel mode table lists "Concurrent VM sessions: No".
  - Sessions drop during Bastion maintenance ([FAQ](https://learn.microsoft.com/en-us/azure/bastion/bastion-faq)).
  - The idle timeout is UNVERIFIED (one Q&A answer says 15 minutes).
- **Price** (East US, Retail Prices API, hourly × 730):
  - Basic $139 a month;
  - Standard $212;
  - Premium $329;
  - Developer free, but no native client.

  Bastion Standard costs more than the VM it guards.
- **Entra ID login** (`az ssh config -g rg -n vm --file f`). It writes a host entry with a short-lived certificate. The VM needs the AADSSHLoginForLinux extension and a role such as "Virtual Machine User Login" ([Entra login](https://learn.microsoft.com/en-us/entra/identity/devices/howto-vm-sign-in-azure-ad-linux)).
  - Certificates last about one hour and cannot be changed ([azure-cli-extensions#3565](https://github.com/Azure/azure-cli-extensions/issues/3565)). A live session survives, because OpenSSH checks the certificate only at login. Each reconnect after an hour needs `az ssh config` again.
  - It has no Bastion option.
- **Recommendation.** For Azure, prefer Tailscale on the VM. Otherwise use a public IP with the network security group limited to known addresses. Treat Bastion as a documented but awkward path.

### 4.7 Cloudflare Access

```sshconfig
Host cf-dev
  HostName dev.example.com
  User dev
  ProxyCommand /opt/homebrew/bin/cloudflared access ssh --hostname %h
```

This follows [Cloudflare's SSH guide](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/use-cases/ssh/ssh-cloudflared-authentication/).

- **Host side.** The host runs a `cloudflared` tunnel that publishes `ssh://localhost:22`. That tunnel is an outbound connection, so the host still has no listening public port. But it is a relay that a third party runs. That is fine as a user's choice, but not a simstim default.
- **First connection.** It opens a browser for the Access login, and the token is cached.
  - The Access session lasts 24 h by default.
  - For SSH through `cloudflared`, "Cloudflare does not control the length of an active session" ([session management](https://developers.cloudflare.com/cloudflare-one/access-controls/access-settings/session-management/)).
- **Short-lived certificates** are now marked legacy ([docs](https://developers.cloudflare.com/cloudflare-one/applications/non-http/short-lived-certificates-legacy)). The replacement, "Access for Infrastructure", needs the WARP client. It does not support port forwarding, ProxyJump or agent forwarding ([docs](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/use-cases/ssh/ssh-infrastructure-access/)). simstim's v2 port forwarding would then fail.
- **Gotchas.**
  - Cloudflare edge releases restart servers, "which terminates WebSockets connections" ([WebSockets](https://developers.cloudflare.com/network/websockets/)). The idle WebSocket timeout is not published; reports say about 100 s (UNVERIFIED). Keep the heartbeat well under that.
  - No reconnect after a network change ([cloudflared#1301](https://github.com/cloudflare/cloudflared/issues/1301)).
  - Bot Fight Mode breaks the handshake ([cloudflared#885](https://github.com/cloudflare/cloudflared/issues/885)).
- **Price.** Zero Trust is free for up to 50 users.

### 4.8 A homelab VM behind a ProxyJump host

This is the shape of the owner's lab: guest VMs are reachable only through the hypervisor, which is on the tailnet.

```sshconfig
Host lab-jump
  HostName hypervisor.example-tailnet.ts.net
  User jump
  ServerAliveInterval 15
  ServerAliveCountMax 3

Host lab-dev
  HostName 192.0.2.20               # guest address, reachable only from the jump host
  User dev
  ProxyJump lab-jump
```

- **How it works.** OpenSSH turns `ProxyJump` into a ProxyCommand: `ssh -W '[%h]:%p' lab-jump` ([ssh.c](https://github.com/openssh/openssh-portable/blob/master/ssh.c)).
  - The inner `ssh` reads the jump host's own block, not simstim's `-o` options. Keepalives for the hop must be in the user's config, as above.
  - `ProxyJump` and `ProxyCommand` compete, and the first one found wins (`man ssh_config`).
- **simstim's master.** One master for `lab-dev` holds the whole chain: it is one ProxyCommand process. `%C` includes `%j`, so the socket is distinct from a direct connection.
- **Latency.** One extra hop. On a home network behind Tailscale it is small, but the first hop's roaming behaviour decides everything. Here that is Tailscale, which is good.
- **Gotchas.**
  - Every session through one master counts against the destination's `MaxSessions` (10). Sessions do not count on the jump host, because the jump is a single `direct-tcpip` channel.
  - The jump host must allow TCP forwarding.
  - Agent forwarding is not needed for the hop. ProxyJump authenticates to the guest from the laptop, end to end.

### 4.9 Minimum host requirements

- **OS and architecture.** Linux on x86_64 or arm64, with any libc, because the helper is static. macOS hosts later. Windows hosts are out of scope for v1.
- **sshd.** Must allow exec channels. Not accepted:
  - a `ForceCommand`;
  - an SFTP-only account;
  - a restricted shell.

  The user's login shell must be able to run `sh -s`. Any maintained OpenSSH server works.
- **Tools.** `sh`, `uname`, `mkdir`, `chmod`, `mv` and `cat`. Optional: one of `sha256sum`, `shasum` or `openssl`, and `tic`.
- **Paths.** A home directory that allows exec, or a configured install path. A writable runtime directory for sockets.
- **Sessions that outlive the login.** systemd-logind's `KillUserProcesses=` "Defaults to "yes"" upstream, and that "will break tools like screen(1) and tmux(1), unless they are moved out of the session scope" ([logind.conf(5)](https://man7.org/linux/man-pages/man5/logind.conf.5.html)). Many distributions build with `no` (UNVERIFIED per distribution).
  - The helper should start the session daemon outside the login scope, for example with `systemd-run --user` and lingering on.
  - It should also check `loginctl show-user` and warn.
- **No root, no listening ports, no outbound access for simstim.** The agents themselves need outbound HTTPS to their APIs.
- **Agent sandboxes.** Claude Code's Linux sandbox needs bubblewrap and socat ([sandboxing](https://code.claude.com/docs/en/sandboxing)). Codex also prefers bubblewrap. Prefer a full VM to an LXC container, where nested bubblewrap often fails.
- **Resources for about 5 concurrent agents.**
  - Claude Code asks for "4 GB+ RAM" ([setup](https://code.claude.com/docs/en/setup)).
  - Codex asks for "4-GB minimum (8-GB recommended)" ([install.md](https://github.com/openai/codex/blob/main/docs/install.md)).
  - Gemini CLI says "Casual: 4GB+", "Power: 16GB+" ([installation](https://geminicli.com/docs/get-started/installation/)).

  Most of the memory goes to builds and tests, not to the agent TUIs. Per-process figures: UNVERIFIED.

  | Use | vCPU | RAM | Disk |
  | --- | --- | --- | --- |
  | Minimum (builds one at a time, 4 GB swap or zram) | 2 | 4 GB | 40 GB |
  | Comfortable | 4 | 8 GB | 80 GB |
  | Heavy Rust builds or parallel test suites | 4–8 | 16 GB | 100 GB+ |

  Agent logs can grow large; one Codex report found about 755 GiB of session logs ([codex#34061](https://github.com/openai/codex/issues/34061)). The helper should report free disk in host details.

### 4.10 Cheap VM sizes, October 2026

Monthly price = hourly × 730, on demand, without VAT, fetched on 15 October 2026. Prices for GCP, AWS and Azure exclude the disk; add about $3–8 for 40–80 GB.

| Provider | Plan | vCPU / RAM | Price a month | Region | Public IPv4 |
| --- | --- | --- | --- | --- | --- |
| Hetzner | CX23 (cost-optimised) | 2 / 4 GB | €5.49 | Germany, Finland | +€0.50 |
| Hetzner | CX33 (cost-optimised) | 4 / 8 GB | €8.49 | Germany, Finland | +€0.50 |
| Hetzner | CAX21 (Arm) | 4 / 8 GB | €10.49 | Germany, Finland | +€0.50 |
| Hetzner | CPX22 | 2 / 4 GB | €19.49 | Germany, Finland | +€0.50 |
| Hetzner | CPX32 | 4 / 8 GB | €35.49 | Germany, Finland | +€0.50 |
| Hetzner | CPX31 | 4 / 8 GB | $73.49 | USA | extra |
| Vultr | Regular Cloud Compute | 2 / 4 GB; 4 / 8 GB | $20; $40 | New Jersey | included |
| DigitalOcean | Basic Regular | 2 / 4 GB; 4 / 8 GB | $24; $48 | any | included |
| Akamai Linode | Shared 4 GB; 8 GB | 2 / 4 GB; 4 / 8 GB | $24; $48 | any | included |
| GCP | e2-medium (shared core) | 2 / 4 GB | $24.46 | us-central1 | +$3.65 |
| GCP | e2-standard-2 | 2 / 8 GB | $48.92 | us-central1 | +$3.65 |
| AWS | t4g.medium (burstable) | 2 / 4 GB | $24.53 | us-east-1 | +$3.65 |
| AWS | t4g.large (burstable) | 2 / 8 GB | $49.06 | us-east-1 | +$3.65 |
| AWS | c7g.xlarge | 4 / 8 GB | $105.85 | us-east-1 | +$3.65 |
| Azure | B2als v2 (burstable) | 2 / 4 GB | $27.45 | East US | +$3.65 |
| Azure | B4als v2 (burstable) | 4 / 8 GB | $97.09 (looks wrong; UNVERIFIED) | East US | +$3.65 |

Notes:

- **Hetzner repriced on 15 June 2026,** for new orders and rescales ([price adjustment](https://docs.hetzner.com/general/infrastructure-and-availability/price-adjustment/), checked directly). Examples:
  - CPX22 rose from €7.99 to €19.49;
  - CPX32 rose from €13.99 to €35.49;
  - CX33 rose from €6.49 to €8.49;
  - US CPX31 rose from $24.99 to $73.49.

  The cost-optimised CX line showed as unavailable on its product page on 15 October. How often it is in stock: UNVERIFIED.
- **Other sources:**
  - Vultr prices come from its official plans API, because the pricing page refused the fetch.
  - DigitalOcean: [droplet pricing](https://www.digitalocean.com/pricing/droplets).
  - Linode: [pricing](https://www.akamai.com/cloud/pricing/cpu).
  - GCP: [general-purpose pricing](https://cloud.google.com/products/compute/pricing/general-purpose).
  - AWS: the JSON behind [on-demand pricing](https://aws.amazon.com/ec2/pricing/on-demand/).
  - Azure: the Retail Prices API.
- **Azure B4als v2.** The API returned $0.133 an hour, which is 3.5 times the B2als v2 price. Check it in the Azure calculator.
- **Burstable types** (t4g, B-series, e2 shared core) run on CPU credits and slow down under sustained builds. Five busy agents can drain the credits.
- **Cheapest for the comfortable 4 / 8 GB size:**
  - Hetzner CX33 or CAX21 in the EU, when in stock;
  - then Vultr at $40;
  - then Hetzner CPX32, DigitalOcean or Linode at about $42–48.

  The hyperscalers cost about $90–110 for 4 / 8 GB, before the disk. Their reason to exist in simstim is existing accounts and private networks, not price.

## 5. Reconnect and resume

### 5.1 Detecting a dead connection

There are four layers. From slowest to fastest:

1. **TCP keepalive** (`TCPKeepAlive`). The kernel default waits 2 hours before the first probe: Linux `tcp_keepalive_time` 7200 s ([ip-sysctl](https://docs.kernel.org/networking/ip-sysctl.html)), and `net.inet.tcp.keepidle` 7200000 ms on this Mac. The man page also calls it "spoofable". It is useless for a fast decision.
   - If data is in flight, Linux keeps retransmitting for at least 924.6 s (`tcp_retries2` = 15). An agent TUI with a spinner nearly always has data in flight.
2. **SSH keepalive** (`ServerAliveInterval`, `ServerAliveCountMax`). These probes go inside the encrypted channel. With 15 and 3, `ssh` exits after about 45–60 s ([ssh_config(5)](https://man.openbsd.org/ssh_config.5); `server_alive_check()` in [clientloop.c](https://github.com/openssh/openssh-portable/blob/master/clientloop.c)). simstim should pass 10 and 3 as a backstop.
   - On the server side, `ClientAliveInterval` is off by default. With 15 × 3 the server drops a silent client after about 45 s. That is too strict for laptops that sleep, but it does not matter if the session daemon owns the state.
3. **App heartbeat in the RPC channel.** Ping every 5 s and declare the link dead after about 15 s without any frame. Zed uses 5 s × 5 misses; VS Code sends keepalives every 5 s with a 20 s timeout. This layer decides.
4. **OS events.**
   - On wake from sleep, or when the network path changes, send a ping at once with a 2–3 s deadline. Do not wait out the timers.
   - On macOS the sources are the workspace wake notification and `NWPathMonitor` from the Network framework (API choice UNVERIFIED in a prototype).
   - A cheap cross-platform signal: compare a clock that counts sleep with one that does not. Herdr's bridge watchdog uses a clock that counts suspend.

**Does a TCP connection survive a short sleep?** Only if all of these hold:

- the IP did not change;
- no keepalive fired while the laptop slept;
- the server did not give up retransmitting;
- NAT or Wi-Fi state survived.

This is reasoning from TCP behaviour, not a documented guarantee (UNVERIFIED). Design for "every sleep is a disconnect", and treat survival as a bonus. Tailscale and the IAP tunnel improve the odds. Nothing guarantees it.

### 5.2 The reattach UX

1. **When the heartbeat fails,** keep the last frame on screen. Draw a one-line banner in reverse video, such as "lab-dev: reconnecting (attempt 2) · press the detach key to leave". Set the window title to match.
2. **Drop input while disconnected, and say so.** Do not buffer it. A buffered `y` or Enter, typed at a stale screen, could approve an agent action minutes later.
3. **Kill the dead `ssh` processes** (RPC channel and master). Run `ssh -O exit` if the master socket still answers. Reconnect at once, then back off: 1, 2, 4, 8, 15, 30 s, with a cap. A network-up event resets the backoff. VS Code uses a similar ladder of 0, 5, 5, 10 … 30 s.
4. **Reconnect with `BatchMode=yes`.**
   - Agent prompts, such as Touch ID for Secretive or 1Password, still appear.
   - If auth needs a human (a passphrase, a password, a browser login for IAP, Cloudflare or Tailscale check mode), stop and show "press Enter to reconnect".
5. **Mind `PerSourcePenalties`.** Avoid probes that connect and then close before auth. Since OpenSSH 9.8, `sshd` penalises sources: "noauth" for 1 s, "authfail" for 5 s, enforced after 15 s has accrued, up to 10 minutes (`man sshd_config`). A reconnect storm of failed logins can lock the laptop out for minutes. Through IAP or a jump host, all users share one source address.
6. **After reconnect, reattach to the same session.** The daemon replays from the last offset or redraws (section 5.4). Remove the banner only after the first frame arrives.
7. **On EICE and SSM, expect a cut every hour.** A good reconnect makes this a one-second flicker.

### 5.3 How Eternal Terminal v7 and tsshd resume

**Eternal Terminal** ([MisterTea/EternalTerminal](https://github.com/MisterTea/EternalTerminal)). The latest release is v7.0.0 (2026-07-07). The protocol version is still 6.

- **Setup.** `et` runs `etterminal` over SSH with a random id and passkey. `etterminal` registers with the root `etserver` daemon, which listens on **TCP 2022**. The client then connects straight to port 2022 ([protocol.md](https://github.com/MisterTea/EternalTerminal/blob/master/docs/protocol.md)).
- **Encryption.** libsodium `crypto_secretbox`, keyed by the passkey that travelled over SSH.
- **Buffers.** Each side keeps a `BackedWriter` with up to 64 MiB of sent packets, numbered in sequence ([BackedWriter.hpp](https://github.com/MisterTea/EternalTerminal/blob/master/src/base/BackedWriter.hpp)).
  - On reconnect, both sides exchange the last sequence number they received (`SequenceHeader`). Each then sends the packets the other missed (`CatchupBuffer`).
  - v7 keeps sessions alive while the client is away (#762).
  - Past 64 MiB it stops reading from the program, rather than dropping data (#731).
- **Other v7 changes:** ProxyJump parsing, Unix-socket forwarding and Windows fixes ([v7.0.0 notes](https://github.com/MisterTea/EternalTerminal/releases/tag/et-v7.0.0)). Unreleased work on master adds named sessions, a ControlMaster mux and tmux `-CC` support.
- **Two conflicts with simstim's rules.**
  - It needs an open TCP port on the host.
  - Its client sends error logs to Datadog by default; turn this off with `--telemetry=false` or `ET_NO_TELEMETRY=1` (README).
- **Open TUI issues** include heavy scrolling freezes (#178) and a line starting with `%` being misread as tmux `-CC` (#899, opened 2026-10-14).

**tssh and tsshd** ([trzsz-ssh](https://github.com/trzsz/trzsz-ssh), [tsshd](https://github.com/trzsz/tsshd)). tssh is at v0.1.26 (2026-07-26) and tsshd at v0.1.10 (2026-10-03). Both are pre-1.0.

- **tssh is not OpenSSH.** It is a Go client on `x/crypto/ssh`, so it has the parser gaps of section 1.2.
- **Setup.** tssh opens an SSH session and runs `tsshd`. tsshd daemonises, listens on a **random UDP port from 61001 to 61999**, and prints JSON with the port, keys and certificates. The SSH connection then closes ([udp.go](https://github.com/trzsz/trzsz-ssh/blob/main/tssh/udp.go)).
- **Transport.** QUIC (quic-go, TLS 1.3) by default, or KCP.
- **Roaming.** A small proxy sits under QUIC. After 3 s without a heartbeat, the client reconnects from its new address with an authenticated packet. The QUIC connection itself "is never interrupted", so QUIC's own retransmission does the replay.
- **Long gaps.** tsshd caches up to 1,000 lines (or two screens). On overflow it forces a redraw by resizing the window by one column and back ([session.go](https://github.com/trzsz/tsshd/blob/main/tsshd/session.go)).
- **Conflict with simstim's rules:** it needs an open UDP port on the host.

**mosh** syncs screen state, not a byte stream: "only the visible state" ([mosh.org](https://mosh.org/)). This is why it has no scrollback and passes few escape sequences. Its last release is 1.4.0 (2022).

### 5.4 Should simstim run its own resumable stream?

**What the session layer already gives.** A session daemon that keeps a terminal model redraws the screen on attach. Examples: tmux, zellij, shpool's default `"screen"` mode, and zmx, which feeds output to libghostty-vt. The visible screen comes back without any replay.

**What is lost without replay:**

1. **Native scrollback.** Lines that scrolled off the screen during the gap never reach the laptop's scrollback. This matters for Claude Code. Its default renderer writes the transcript into the terminal's own scrollback; only its fullscreen mode scrolls inside the app ([terminal config](https://code.claude.com/docs/en/terminal-config)). The owner's goal of "exactly as if native" depends on native scrollback, so this loss is real.
2. **One-shot escape sequences sent during the gap:**
   - desktop notifications (OSC 9, 777, 99);
   - the bell;
   - clipboard writes (OSC 52);
   - status reports.

   Claude Code sends its notification "when you appear to be away", and "The desktop notification reaches your local machine over SSH". It also supports an OSC 7501 status protocol (same page). These are exactly the events that fire while the laptop sleeps.
3. **The bytes in flight at the moment of the drop.** Without offsets, the client cannot tell what it has already drawn.

**Where replay must live.** Transport replay, as in ET or QUIC, helps only if a host-side process stays attached to the session during the outage. In simstim that process is the session daemon, which outlives every `ssh`. So the replay buffer belongs in the daemon. It needs no new transport.

**The judgement:**

- **Keep, per session in the daemon, a ring of raw PTY output** (2–8 MiB) with a monotonic byte offset.
- **The client sends its last offset in the attach request:**
  - If the offset is still in the ring, the daemon sends the bytes after it. The result is exact: native scrollback, notifications and clipboard events all arrive in order.
  - If not, or for a different client or terminal size, the daemon sends a fresh screen from its terminal model, and the gap is shown as such.
- **Cap event replay.** After a long gap, collapse missed notifications into one: "3 notifications while away". Do not fire them all at once. Never replay OSC 52 clipboard writes that are older than a few seconds.
- **No input replay.** Keystrokes are never resent after a reconnect (section 5.2).
- **The RPC channel needs its own sequence and ack only for requests** that must not run twice, such as creating a session. Idempotency keys are enough. A full ET-style acked stream for every frame is not needed, because the daemon holds the authoritative output.

This is cheap, because the daemon already reads every byte of PTY output. It gives most of ET's benefit without ET's open port. Zed and VS Code use the same idea, an acked replay buffer held by the server, for their RPC.

## 6. File transfer and port forwarding

### 6.1 Image and file paste (v1)

- **Prior art.** Claude Code has no official image-paste path over SSH.
  - A proposal to use OSC 52 or kitty's OSC 5522 was closed as "not planned" ([claude-code#42712](https://github.com/anthropics/claude-code/issues/42712)).
  - Users copy the file with `scp` and paste the path ([#5277](https://github.com/anthropics/claude-code/issues/5277)).
  - Zed's terminal sends a raw Ctrl+V when the clipboard holds an image, and uploads nothing ([terminal_view.rs](https://github.com/zed-industries/zed/blob/main/crates/terminal_view/src/terminal_view.rs)).
  - VS Code: no upload-on-paste found (UNVERIFIED as absent).
- **Herdr's design is the model** ([clipboard_image.rs](https://github.com/herdrdev/herdr/blob/master/src/server/clipboard_image.rs)):
  1. The client reads the clipboard image, capped at 16 MiB.
  2. It sends the bytes over its own protocol.
  3. The server writes them to `$TMPDIR/herdr-clipboard-images-<uid>/…` (directory 0700, file 0600, `create_new`) and deletes files after 24 h.
  4. It pastes the path as text.
- **simstim should do the same:**
  - Bind a key in the client (say Ctrl+V when the clipboard holds an image, or a simstim chord).
  - Read the image with the macOS pasteboard API.
  - Upload it over the RPC channel in chunks, or as a separate exec channel when a master exists, so a large file does not stall the terminal.
  - Store it in the session's own directory on the host.
  - Inject the path as a bracketed paste.
  - Through SSM (about 0.6 MB/s), show progress for large files.
- **Do not depend on `sftp`.** It may be disabled. Herdr and DevPod both write through an exec channel.

### 6.2 Detecting ports that agents' dev servers open (v2)

- **VS Code** ([extHostTunnelService.ts](https://github.com/microsoft/vscode/blob/main/src/vs/workbench/api/node/extHostTunnelService.ts), [remoteExplorer.ts](https://github.com/microsoft/vscode/blob/main/src/vs/workbench/contrib/remote/browser/remoteExplorer.ts)):
  - It reads `/proc/net/tcp` and `/proc/net/tcp6`, keeps sockets in state `0A` (LISTEN), and maps socket inodes to processes through `/proc/<pid>/fd`.
  - It polls every max(20 × the last scan time, 2 s).
  - It ignores ports that were open before the connection.
  - `remote.autoForwardPortsSource` is `process`, `output` (regexes for localhost URLs in terminal output, [urlFinder.ts](https://github.com/microsoft/vscode/blob/main/src/vs/workbench/contrib/remote/browser/urlFinder.ts)) or `hybrid`. After 20 ports it falls back from `process` to `hybrid`.
- **Coder** ([ports_supported.go](https://github.com/coder/coder/blob/main/agent/ports_supported.go)):
  - It uses `cakturk/go-netstat` and caches results for 1 s. The UI polls every 5 s.
  - It ignores ports below 9 and a list of well-known ports (22, 25, 53, 3306, 5432, 27017 and others).
- **GitHub Codespaces.** The docs describe only output-based detection ([docs](https://docs.github.com/en/codespaces/developing-in-a-codespace/forwarding-ports-in-your-codespace)). Whether it reuses VS Code's process scan: UNVERIFIED.
- **Gitpod** polls `/proc/net/tcp{,6}` every 2 s.
- **Tailscale's `portlist`** ([portlist_linux.go](https://github.com/tailscale/tailscale/blob/main/portlist/portlist_linux.go)):
  - On Linux it uses netlink `sock_diag`, falls back to `/proc`, and polls every 1 s.
  - On macOS it uses `netstat -na`, with `lsof` only for process names, because "netstat ~100x faster than lsof".
- **eBPF.** I found no developer tool that uses it. The `sock:inet_sock_set_state` tracepoint would give instant events. It needs root or CAP_BPF and a recent kernel, and does not work on macOS. That breaks the no-root rule.
- **simstim's helper** should:
  - use `sock_diag`, then `/proc`, on Linux, and `netstat` on macOS;
  - poll every 1–2 s, only while a client is attached;
  - map ports to processes, and report only ports owned by a session's process tree;
  - ignore ports open before the session started.

  Showing the owning session ("vite in session api-fix opened :5173") is better than VS Code's global list.

### 6.3 Forwarding mechanics

- **With a master:**
  - `ssh -S <ctl> -O forward -L 127.0.0.1:5173:127.0.0.1:5173 <alias>` adds a forward on the live connection.
  - `-O cancel` with the same specification removes it (cancel arrived in OpenSSH 6.0).
  - SSH gives each forward its own channel and flow-control window, so a busy dev server does not stall the terminal.
- **Limitations:**
  - Forwards belong to the master, and die with it. After every reconnect, simstim must add them again.
  - A local port clash is the client's problem. Pick the same port if free, else the next free one, and show the mapping.
  - The server may refuse (`AllowTcpForwarding`, `PermitOpen`, `DisableForwarding`). Cloudflare Access for Infrastructure does not support port forwarding.
  - Some `ExitOnForwardFailure` cases are not handled in the mux code ([mux.c](https://github.com/openssh/openssh-portable/blob/master/mux.c)).
  - The `~C` command line is off by default since OpenSSH 9.2 (`EnableEscapeCommandline`), so it is not usable for automation.
- **Without a master (Windows):** tunnel TCP inside the RPC protocol, with per-stream windows. This is more code, so do it only if Windows clients matter.
- **Always bind to `127.0.0.1` on the laptop,** never to all interfaces.

## 7. Security model

### 7.1 Design rules and what they rule out

| Rule | Effect |
| --- | --- |
| No listening ports on hosts | Rules out ET (TCP 2022), tsshd (UDP 61001–61999) and mosh (UDP 60000–61000). The session daemon listens only on a Unix socket, mode 0700 |
| No relay | simstim runs no service in the middle. Users may still choose IAP, SSM, Cloudflare or Tailscale DERP in their own config |
| No telemetry by default | Unlike ET, whose client sends error logs to Datadog by default. Crash reports stay local unless the user sends them |
| Agent secrets stay on hosts | The client never holds API keys or GitHub tokens for agents. Hosts hold only their own scoped credentials |
| simstim holds no secrets of its own | Auth is `ssh`'s job, with the user's keys and agent. simstim stores host names and settings only |

### 7.2 SSH agent forwarding

- **The risk.** `ssh_config(5)` says: "Users with the ability to bypass file permissions on the remote host can access the local agent… An attacker cannot obtain key material from the agent". Any process that runs as the same user can use a forwarded agent while the link is up. That includes:
  - a prompt-injected agent;
  - a malicious `postinstall` script.

  They can then log in to other servers as you. A coding agent host is exactly the place where untrusted code runs.
- **Destination constraints** (`ssh-add -h`, OpenSSH 8.9+, [agent restriction](https://www.openssh.org/agent-restrict.html)) limit which hosts a forwarded key may reach.
  - They need new enough `ssh-agent`, `ssh` and `sshd` on each hop, and the host keys must already be known.
  - The page still says an attacker "will still be able to steal use of a forwarded agent on that host" for the allowed destinations.
  - "Restrictions only work for user authentication", so a constrained key cannot sign git commits.
  - OpenSSH 10.5 fixed a bypass where a locked agent ignored the binding.
- **Confirmation.**
  - `ssh-add -c` asks before each use, but "confirmation is somewhat easy to phish".
  - 1Password asks once per application and terminal session, after which "any new connection made within the remote environment using the same key is also authorized" ([1Password forwarding](https://www.1password.dev/ssh/agent/forwarding/)).
  - Secretive notifies on every use, but does not implement the session binding that destination constraints need (its `Agent.swift`).
- **simstim's rule:**
  - `ForwardAgent no` on every connection it makes.
  - Use ProxyJump for hops; it never needs forwarding.
  - If a user turns forwarding on for a host, show a warning, and suggest per-host credentials instead.
  - If simstim ever relays an agent over its own protocol, it must replay `session-bind@openssh.com`, as ET's new code does, or destination constraints break.

### 7.3 Per-host credentials for agents

- **GitHub fine-grained personal access tokens** ([docs](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens)):
  - Each is limited to one user or organisation, narrowed to chosen repositories, with fine-grained permissions.
  - Default expiry is 30 days. An organisation may require approval or a maximum lifetime.

  Give each host its own token, limited to the repositories that host works on, and pass it as `GH_TOKEN`.
- **`gh auth login` on a headless host** falls back to a plain-text file, and requests broad scopes (`repo`, which covers all private repositories). Prefer `GH_TOKEN` with a fine-grained token.
- **Deploy keys** grant "access to a single repository" and are "read-only by default" ([deploy keys](https://docs.github.com/en/authentication/connecting-to-github-with-ssh/managing-deploy-keys)). They do not expire. They are good for a host that only needs to pull and push one repository.
- **GitHub App installation tokens** "expire after 1 hour". Good for automation. Heavy for one person.
- **Commit signing.** Use a host-local SSH signing key, registered as a signing key on GitHub. Never use a forwarded key.
- **Agent logins live on the host:**
  - Claude Code on Linux stores credentials in `~/.claude/.credentials.json`, mode 0600. `claude setup-token` makes a one-year token that "can only make model requests" ([IAM](https://code.claude.com/docs/en/iam)).
  - Codex uses `~/.codex/auth.json`, mode 0600 ([storage.rs](https://github.com/openai/codex/blob/main/codex-rs/login/src/auth/storage.rs)).

  These are bearer tokens: anyone who can read the file can use it.

### 7.4 Threat model

**A compromised host** (through a malicious dependency, a prompt injection or a stolen host key) can:

- **Read all credentials on the host.** Mitigate with per-host, per-repository tokens, short expiry, and nothing that reaches other hosts.
- **Use a forwarded agent.** Mitigate by not forwarding (section 7.2).
- **Send escape sequences to the laptop's terminal.** simstim relays output bytes untouched, for fidelity, so the terminal is the line of defence. The history:
  - title-report injection: CVE-2003-0063 in xterm, and CVE-2024-38396 in iTerm2 with tmux integration, CVSS 9.8 ([oss-security](https://openwall.com/lists/oss-security/2024/06/17/1));
  - CVE-2024-56803 in Ghostty, fixed in 1.0.1;
  - surveys at [dgl.cx/term](https://dgl.cx/term) and [G-Research](https://www.gresearch.com/news/g-research-the-terminal-escapes/).

  Risks that remain: OSC 52 clipboard writes, which can put a hostile command on the clipboard for a later paste, and OSC 8 links. simstim should offer a per-host "untrusted" mode. It would drop OSC 52 writes, drop OSC 8 links with schemes other than http and https, and drop title and colour report queries. The default stays faithful.
- **Attack the laptop through forwarded ports.** A forwarded dev server is served on `localhost` in the user's browser. Cookies do not isolate by port, so a hostile page on one localhost port can read and set cookies for other localhost apps (RFC 6265 behaviour). Mitigations:
  - make auto-forward opt-in per host, or show a clear notice;
  - always bind to `127.0.0.1`;
  - never use remote forwards (`-R`) that let the host reach the laptop.
- **Lie to the client through the helper protocol.** The client must treat every RPC reply as untrusted input:
  - Bound every size.
  - Never run a command, open a URL or write a local file because the host asked. Show the URL; the user clicks it.
  - Image paste only flows from the laptop to the host.

**A compromised client** holds the SSH keys and the host list, so it reaches every host. Mitigations:

- hardware-backed keys: Secure Enclave through Secretive, or FIDO `-sk` keys with touch required;
- one key per host or per provider, with `IdentitiesOnly yes`;
- short-lived user certificates (`ssh-keygen -s ca -V +8h`) for users who run a CA;
- the agent's per-use approval (1Password, Secretive).

simstim itself should hold no tokens, so a stolen simstim config reveals host names only.

**A compromised simstim release** would run its helper on every host. Mitigations:

- sign releases and the helper manifest;
- pin helper hashes inside the client binary for its own version;
- aim for reproducible builds;
- never auto-update the client silently.

**A network attacker** sees only SSH. Two points:

- Keep clients current. Terrapin (CVE-2023-48795) let a man in the middle break "the integrity of the early encrypted SSH transport". OpenSSH 9.6 fixed it with strict key exchange.
- Post-quantum key exchange protects recorded traffic against future decryption. A host with an old `sshd` makes OpenSSH 10.1+ print a warning. simstim should show it once in host details, not hide it.

### 7.5 Options simstim passes on its connections

```text
-o ForwardAgent=no -o ForwardX11=no -o ClearAllForwardings=yes   (RPC and side channels)
-o Compression=no
-o ServerAliveInterval=10 -o ServerAliveCountMax=3 -o ConnectTimeout=15
-o BatchMode=yes                       (background reconnects only)
-o StrictHostKeyChecking=yes           (background; first connect uses the user's setting)
-o ControlMaster=no -S <private path>  (every call except the master itself)
-e none                                (the plain `ssh -t` fallback)
-P simstim                             (OpenSSH 9.4 and later)
```

`PermitLocalCommand` is off by default; leave it so.

Task H2 of [the isolation plan](../contributing/isolation-plan.md) takes only the forwarding options from this list ([the isolation spec, 14.8.5](../contributing/isolation-spec.md#1485-where-the-client-checks)). Every `ssh` command that the client runs has `ForwardAgent=no`, `ForwardX11=no`, `PermitLocalCommand=no` and `GSSAPIDelegateCredentials=no` on its command line, where they win over the user's config ([decision 0034](../contributing/decisions/0034-ssh-options-on-the-command-line.md)). The generated config starts with the same settings and `ClearAllForwardings yes`, for a `ProxyJump` hop. Every connection but `forward` also has `ClearAllForwardings=yes`. `forward` has a connection of its own and `ClearAllForwardings=no`, and refuses a `RemoteForward`, a `Tunnel` or `ForkAfterAuthentication yes` from the config. A user's config can no longer turn on agent or X11 forwarding, so the warning that section 7.2 suggests is not needed. The multiplexing options stay as today (`ControlMaster=auto`), so the client does not use the `ControlMaster=no -S <private path>` line above.

## 8. Recommendations

### v1

1. **Transport.** Spawn the system `ssh` from `PATH`, with a setting to choose another binary.
   - Floor: OpenSSH 8.0 on the client.
   - Read `ssh -V`. Gate features by version, and warn about known bad versions such as 10.1 with ControlPersist.
   - No embedded SSH library.
2. **Config.** Never edit `~/.ssh/config`.
   - Use a generated wrapper passed with `-F`. It includes the user's config, simstim's own `hosts.conf` and the system config, then simstim's defaults.
   - Resolve hosts with `ssh -G`.
   - Tag connections with `-P simstim` where supported.
3. **Connection shape:**
   - One long-lived `ssh -T … simstim-host bridge` per connected host, carrying framed, length-prefixed RPC.
   - The bridge connects to a session daemon on a 0700 Unix socket.
   - On macOS and Linux, a private master is started with `-M -N -f`, stdio set to null, a ControlPath under 86 bytes and `ControlPersist 10m`.
   - The RPC heartbeat decides health. Never `-O check`.
4. **Attach** over the RPC channel:
   - The client handles raw mode, resize and a detach key.
   - Send `TERM`, `COLORTERM` and `TERM_PROGRAM` in the attach request.
   - Install the client's terminfo on the host.
   - Keep `ssh -t -e none host simstim-host attach <id>` as a fallback for any SSH client.
5. **Bootstrap:**
   - static helpers;
   - client-side upload over an exec channel;
   - run first, install on failure;
   - SHA-256 from a signed manifest;
   - versioned install paths;
   - a magic plus protocol-version plus capabilities handshake;
   - consent before the first install, and no install during a background reconnect.
6. **Reconnect:**
   - 5 s heartbeat with a 15 s deadline, and an immediate probe on wake or network change;
   - backoff from 1 s up to 30 s;
   - `BatchMode` for background attempts, and "press Enter" when a human is needed;
   - a banner while disconnected, and input dropped while disconnected.
7. **Replay** in the session daemon: a per-session output ring with byte offsets. Replay when the client's offset is still in the ring; otherwise redraw. Collapse missed notifications. Never replay input.
8. **Image paste.** Upload to a 0700 per-session directory on the host, with files deleted after 24 h. Paste the path as a bracketed paste. Cap the size at 16 MiB.
9. **Providers.** Document the recipes in section 4. Make Tailscale (plain OpenSSH over the tailnet) the recommended default for VPS and homelab hosts. Support IAP, SSM and EICE as ProxyCommand recipes, and warn about their limits in the host details view.
10. **Security defaults:**
    - no agent forwarding;
    - no listening ports;
    - no relay;
    - no telemetry;
    - no secrets in simstim;
    - forwarded ports bound to localhost only;
    - an "untrusted host" filter mode.

### v2

1. **Port forwarding.** Helper-side detection with `sock_diag` or `/proc`, scoped to the session's process tree. Add forwards with `ssh -O forward`, and add them again after each reconnect. Use RPC tunnelling only where no master exists.
2. **Optional host download** of the helper, with the same signed checksums.
3. **Windows client.** No master, so either RPC tunnelling or one `ssh` per forward.
4. **Provider wizards** that write `hosts.conf` blocks, perhaps reading `gcloud` or `aws` output, without linking provider SDKs.
5. **A respawn path after a host reboot,** which depends on the session daemon starting under systemd user units with lingering.

### Not recommended

- An embedded SSH library (section 1.2).
- mosh, Eternal Terminal or tsshd as the transport. Each needs an open port on the host. mosh also fails on fidelity.
- Relying on `ssh -O check`, on `ControlMaster auto` with a shared user socket, or on TCP keepalive.
- Agent forwarding as a default way to give agents GitHub access.

## 9. Open questions and unverified items

- Whether SSM's idle timeout applies to `AWS-StartSSHSession`, and whether SSH keepalives reset it.
- The real per-connection set-up cost of IAP, SSM and EICE (seconds, measured).
- How long IAP keeps a session open for `gcloud` to resume it.
- The Azure Bastion idle timeout, and the Azure B4als v2 price.
- Cloudflare's idle WebSocket timeout.
- Whether Tailscale check mode ends live sessions, and the default forwarding flags in Tailscale SSH.
- Which distributions set `KillUserProcesses=yes`.
- Per-process memory use of each agent CLI, measured on a host with five sessions.
- Whether Hetzner's cost-optimised CX line is reliably in stock.

## Sources and method

Four research passes ran in parallel on 14 and 15 October 2026. They read:

- docs, man pages and release notes;
- these repositories, as shallow clones:
  - zed-industries/zed@3e14506
  - microsoft/vscode@a4d6dc1
  - herdrdev/herdr@916fcf8
  - coder/coder@1d57682
  - mutagen-io/mutagen@6ccfeaa
  - loft-sh/devpod@5a0efcb
  - openssh-rust/openssh@17afe72
  - Eugeny/russh@e5d80a0
  - wezterm/wezterm@e2a98e1
  - kevinburke/ssh_config@bd15f5d
  - golang/crypto@cf169cd
  - libssh2/libssh2@83284a0
  - openssh/openssh-portable (master)
  - tailscale/tailscale@b83f28e
- the VS Code Remote-SSH VSIX 0.129.2026100915, read but not run;
- the official pricing pages and price APIs named in section 4.10.

These were checked directly against the primary source while writing:

- the OpenSSH release history for 8.4–10.6, and when `clientloop.c` applies keystroke obfuscation;
- the Hetzner price table;
- the IAP inactivity limit;
- Claude Code's notification and scrollback behaviour;
- the logind default;
- the existence of Herdr.

Other facts come from the research passes, with links as given.
