# Sandboxes and cloud hosts for `host create`

Researched on 15 October 2026.

This note asks how each provider could be a simstim host that `host create <provider>` makes, bootstraps and connects in one command, and that stops when idle and wakes on attach. It extends the [connectivity note](connectivity-and-providers.md), which already has the SSH recipes for plain VPS hosts, GCP IAP, AWS SSM and EICE, Azure, Cloudflare and Tailscale, and the VM list prices. Those are not repeated here.

The rules for the note:

- Facts come from public docs, API references, OpenAPI specs and public source code. Code was read from shallow clones made on 15 October 2026; links pin the commit.
- No account was made, no API key was used, and no live API call was made to any provider. Nothing was installed or started.
- **UNVERIFIED** marks a claim that I could not confirm from a primary source.
- Prices are list prices fetched on 15 October 2026, without VAT.

## Summary

1. **A provider does not need SSH.** The helper's protocol needs one two-way, binary-safe byte stream to one process (section 1.1). `ssh -T` is one. So are `docker exec -i`, `incus exec -T`, Apple's `container exec -i`, E2B's envd process API and Modal's exec API. A provider's own authenticated exec endpoint adds no listening port: it is the door, as sshd is today. Some channels fail the test: Fly's Machines `exec`, `utmctl exec`, the E2B CLI's `exec` (it decodes output as text) and Daytona's toolbox input (JSON strings).
2. **E2B fits, through envd, not SSH.** E2B has no SSH by default; its SSH recipe opens a new public port. Its in-VM daemon, envd, streams a process's stdout as bytes over Connect RPC, takes stdin in order, and keeps the process running when the request drops. simstim needs a small Go client for it. Pause keeps disk, memory and processes, costs nothing while paused, and lasts indefinitely. Against it: the timeout is a TTL, not an idle timer; a sandbox can run 1 hour (Hobby) or 24 hours (Pro) without a pause; network connections drop at pause; and CPU and memory are set per template, not per sandbox.
3. **Morph Cloud is the closest SSH-native sandbox.** The system `ssh` reaches a real sshd through `ssh.cloud.morph.so`, with the instance ID as the user and the user's own key. Pause keeps running processes, and an SSH connection can wake a paused VM. Against it: closed source, no free compute, and thin documented limits.
4. **Daytona and Modal fit less well.** Daytona has SSH, but the token is the user name, its gateway drops stderr, a stop kills every process in a container sandbox, and its server went closed source in June 2026. Modal has the best exec stream of all, with an official Go SDK, but no stop or resume: an idle sandbox is ended, and 24 hours is the cap.
5. **Coder works today as a recipe.** `coder ssh --stdio` is an OpenSSH ProxyCommand, and it starts a stopped workspace by itself. Codespaces has a hard 12-hour lifetime. Fly Machines need `flyctl` as the transport, reset the root filesystem unless told not to, and have no free tier.
6. **On VM clouds, idle stop belongs inside the guest.** On GCE and EC2 a guest `poweroff` stops the instance and its compute billing, with no credential on the host. Hetzner bills a stopped server in full and has no idle stop at all, so only snapshot, delete and recreate saves money there.
7. **Locally, Lima is the best first provider.** It is free, needs no account, and runs on macOS and Linux. It is a real VM with systemd and sshd, so `host create lima` runs the same code as a cloud VM. Its defaults mount the home folder and forward every guest port, so simstim must use `--plain`. `docker exec -i` is the cheapest live proof of a non-SSH transport, and it runs in CI.
8. **Sandboxes win only when they sleep.** A running 2 vCPU / 4 GiB sandbox costs about $0.17 an hour on E2B or Daytona, about $121 a month if never paused. A Hetzner CX23 costs €5.49 a month. Four hours a day for 22 days on E2B is about $15.
9. **Credentials stay the user's.** Each provider's token comes from the user's environment, the provider CLI's own login, or a command the user names. simstim never writes one down, and fetches per-host tokens again on each connect.
10. **Recommendation (section 14).** First: Lima over SSH, and E2B over envd, with Docker as the local proof of the exec transport. Next: Morph and GCE, then Hetzner (create and destroy only) and EC2. Document Coder as a recipe. Do not support Modal, Codespaces, UTM or Colima.

## 1. What simstim needs from a provider

### 1.1 A byte stream, not necessarily SSH

Today the client runs `ssh -T host simstim-host bridge` and speaks framed RPC over that process's stdin and stdout ([conn.go](../../internal/client/conn.go)). Bootstrap runs `uname -sm`, then pipes each binary into `sh -c 'cat > f; sha256sum -c; chmod; mv'` ([bootstrap.go](../../internal/client/bootstrap.go)). Nothing in the protocol depends on SSH. Any channel that can run a command and carry its stdio will do, if it is:

1. **Streaming both ways.** stdin arrives as it is written, not buffered to EOF.
2. **Binary-safe.** Frames are raw bytes. A channel that decodes output as UTF-8 text breaks them.
3. **Clean.** stdout carries only the process's bytes: no banner, no CR/LF translation, no TTY. stderr is separate or can be dropped.
4. **Long-lived.** It lasts hours, or it ends cleanly so the client can reconnect and replay by offset ([0004](../contributing/decisions/0004-replay-by-offset.md)).
5. **Closable.** When the client goes, the bridge gets EOF or a signal. If it does not, the bridge must exit by itself when the client's pings stop.

Two kinds of door must be told apart:

- **The provider's own control channel**: E2B's envd, Docker's socket, Incus's socket, Modal's exec router. It exists already and the provider authenticates it. simstim adds nothing that listens. It is like IAP or SSM, which the [connectivity note](connectivity-and-providers.md#4-provider-recipes) already accepts as the user's choice. It is not a simstim relay.
- **A port simstim would open**: sshd plus websocat in E2B, a tunnel in Modal, a public service on Fly. This is what the project rule forbids without a decision record and the owner's yes.

### 1.2 What the host must give

- Linux on amd64 or arm64, with `sh`, `cat`, `uname` and `sha256sum` ([connectivity note, 4.9](connectivity-and-providers.md#49-minimum-host-requirements)).
- **Processes that outlive the channel.** The helper starts each holder with `systemd-run --user` when a user manager runs, or else with `setsid` and its stdio on `/dev/null` ([launch.go](../../internal/host/launch.go)). A provider that kills the whole process tree, or the machine, when the channel ends is a poor fit.
- A disk that survives a stop, so session folders, agent logins and the helper stay.
- Outbound HTTPS for the agents. bubblewrap for Claude Code's and Codex's own sandboxes, which need user namespaces.

### 1.3 The lifecycle contract

`host create` and `host destroy` need five operations: **create**, **status**, **stop** (or pause), **start** (or resume) and **destroy**. Three already exist as the provider script contract: `start`, `stop` and `status`, with JSON on stdin ([examples/providers](../../examples/providers/README.md), [provider.go](../../internal/client/provider.go)). The client already wakes a host on attach: it runs `start`, then polls until the host answers, for up to 4 minutes.

Two designs for idle stop recur below:

- **Provider-side.** The provider stops or pauses the host on a timer: E2B's and Morph's TTL, Daytona's auto-stop, Codespaces' idle timeout, Coder's autostop.
- **Host-side.** The helper sees "no client connected and no session working for N minutes" and powers the guest off. It needs no credential on the host. It saves money only where a stopped machine is not billed for compute (GCE, EC2, Fly, local VMs). It must never run where a guest power-off destroys the machine (E2B, Modal).

### 1.4 Credentials

simstim stores no secrets ([security model](../explanation/security.md)). Each provider below takes a token from the user's own environment variable, from the provider CLI's own login file (read, never written), or from a command the user names, such as a keychain or password-manager read. Per-host tokens that a provider hands out (E2B's `envdAccessToken`, Daytona's SSH tokens) are fetched again on each connect and kept only in memory.

## 2. E2B

E2B runs each sandbox as a Firecracker microVM. The SDKs and CLI are Apache-2.0, in [e2b-dev/E2B](https://github.com/e2b-dev/E2B). The whole backend is Apache-2.0 too, in [e2b-dev/infra](https://github.com/e2b-dev/infra). That address now redirects to `e2b-dev/runtime`, and its README calls it "E2B Runtime" ([README](https://github.com/e2b-dev/infra/blob/ef1b99a/README.md)). Code in this section was read at E2B@62a17df (13 October 2026) and infra@ef1b99a (15 October 2026).

### 2.1 Access: no SSH, but an exec API

- **No SSH by default.** Inside each VM a daemon, envd, listens on port 49983. "`envd` exposes processes, PTYs, filesystem operations, file watchers, and port forwarding over Connect RPC and REST. It is what the SDKs talk to" ([README](https://github.com/e2b-dev/infra/blob/ef1b99a/README.md)).
- **The route.** Client → E2B's edge (client-proxy) → the node's orchestrator proxy → envd ([ARCHITECTURE.md](https://github.com/e2b-dev/infra/blob/ef1b99a/docs/ARCHITECTURE.md)). The address is `https://49983-<sandboxID>.e2b.app`. Newer SDKs use the stable host `https://sandbox.e2b.app` with the headers `E2b-Sandbox-Id: <id>` and `E2b-Sandbox-Port: 49983` ([connectionConfig.ts](https://github.com/e2b-dev/E2B/blob/62a17df/packages/js-sdk/src/connectionConfig.ts), [sandbox/index.ts](https://github.com/e2b-dev/E2B/blob/62a17df/packages/js-sdk/src/sandbox/index.ts)).
- **Auth to envd.** `X-Access-Token: <envdAccessToken>`, a per-sandbox token. A `Basic` header carries only a user name, which picks the Linux user ([authenticate.go](https://github.com/e2b-dev/infra/blob/ef1b99a/packages/envd/internal/permissions/authenticate.go), [rpc.ts](https://github.com/e2b-dev/E2B/blob/62a17df/packages/js-sdk/src/envd/rpc.ts)).
- **SSH is only a recipe.** The [SSH access guide](https://docs.e2b.dev/sandbox/ssh-access) builds a template with `openssh-server` and starts `websocat -b --exit-on-eof ws-l:0.0.0.0:8081 tcp:127.0.0.1:22`. The client uses `ProxyCommand=websocat --binary -B 65536 - wss://8081-%h.e2b.app`. Port 8081 is a new listener. Sandbox URLs are public unless `network.allowPublicTraffic` is false ([openapi.yml](https://github.com/e2b-dev/E2B/blob/62a17df/spec/openapi.yml)). Under simstim's rules this path would need its own decision record.

### 2.2 Carrying simstim's RPC over envd

envd's process service ([process.proto](https://github.com/e2b-dev/E2B/blob/62a17df/spec/envd/process/process.proto)) has what a bridge needs:

| RPC | Kind | Use for simstim |
| --- | --- | --- |
| `Start(StartRequest)` | server stream | Run `simstim-host bridge`. Events: `start{pid}`, `data{stdout \| stderr \| pty}` as **bytes**, `end{exit_code, status}`, `keepalive` |
| `StreamInput(stream)` | client stream | Send stdin. "Client input stream ensures ordering of messages" |
| `SendInput` | unary | Send one stdin chunk. The SDKs and CLI use this |
| `CloseStdin` | unary | EOF, for a process without a PTY |
| `SendSignal` | unary | `SIGTERM` or `SIGKILL` |
| `Connect(pid \| tag)` | server stream | Re-attach to a running process's output |

What envd's source says:

- **Binary-safe.** Output fields are protobuf `bytes`. With the Connect JSON codec they travel as base64; with the protobuf codec, raw.
- **The process outlives the request.** "We do not want the command to be killed if the request context is cancelled" ([start.go](https://github.com/e2b-dev/infra/blob/ef1b99a/packages/envd/internal/services/process/start.go)). So when the laptop sleeps, the old bridge keeps running with stdin open. simstim must start each bridge with a `tag` and kill the old one on reconnect, or make the bridge exit when the client's 5-second pings stop.
- **A deadline kills it.** A `Connect-Timeout-Ms` header becomes the process's lifetime (same file). The JS SDK sends 60 s by default ([commands/index.ts](https://github.com/e2b-dev/E2B/blob/62a17df/packages/js-sdk/src/sandbox/commands/index.ts)). simstim must send none.
- **Keepalives.** envd sends a keepalive event every `Keepalive-Ping-Interval` seconds: default 90, and the SDKs ask for 50 ([keepalive.go](https://github.com/e2b-dev/infra/blob/ef1b99a/packages/envd/internal/permissions/keepalive.go)). The edge proxy has no read or write deadline, and an idle timeout of 610 s ([client-proxy proxy.go](https://github.com/e2b-dev/infra/blob/ef1b99a/packages/client-proxy/internal/proxy/proxy.go), [shared proxy.go](https://github.com/e2b-dev/infra/blob/ef1b99a/packages/shared/pkg/proxy/proxy.go)).
- **Signals go to one PID.** `SendSignal` signals the process, not its group ([handler.go](https://github.com/e2b-dev/infra/blob/ef1b99a/packages/envd/internal/services/process/handler/handler.go)). A holder that calls `setsid` and closes its stdio survives the bridge, as it does over SSH. The handler closes its pipes on exit, so a grandchild that kept them open would hold the stream open; simstim's holders use `/dev/null`.
- **HTTP versions.** "Bidirectional streaming requires HTTP/2, but the other RPC types also support HTTP/1.1" ([Connect protocol](https://connectrpc.com/docs/protocol/)). A server stream plus a client stream needs no bidirectional RPC.

What does **not** work is the E2B CLI as a pipe. `e2b sandbox exec <id> cmd` streams piped stdin in 64 KiB `SendInput` chunks, but decodes stdout with `TextDecoder` ([exec.ts](https://github.com/e2b-dev/E2B/blob/62a17df/packages/cli/src/commands/sandbox/exec.ts), [commandHandle.ts](https://github.com/e2b-dev/E2B/blob/62a17df/packages/js-sdk/src/sandbox/commands/commandHandle.ts)). Invalid UTF-8 becomes U+FFFD, so binary frames would break. simstim needs its own small Connect client in Go.

That client is cheap. The proto is Apache-2.0, and the infra repo already holds generated Go stubs at [`packages/shared/pkg/grpc/envd/process/processconnect`](https://github.com/e2b-dev/infra/tree/ef1b99a/packages/shared/pkg/grpc/envd/process/processconnect). There is no official Go SDK; [e2b-go](https://pkg.go.dev/github.com/Atharva-Kanherkar/e2b-go) is a third-party one, not read.

The helper upload works as today: `sh -c 'cat > f; sha256sum -c; chmod; mv'` with the bytes on stdin.

**UNVERIFIED:** whether E2B's public edge carries a long `StreamInput` client stream. No official SDK uses it; only the generated stubs mention it. The safe fallback is ordered `SendInput` calls, one round trip each. That is fine for keys, and slow for a 16 MiB image paste: 256 calls of 64 KiB in a row.

### 2.3 Inside the VM

- **systemd is PID 1** on the Debian and Ubuntu base images. envd is a system unit running as root ([envd.service.tpl](https://github.com/e2b-dev/infra/blob/ef1b99a/packages/orchestrator/pkg/template/build/core/rootfs/files/envd.service.tpl)).
- **The default user is `user`**, made with `useradd -m -s /bin/bash user` ([configure.sh](https://github.com/e2b-dev/infra/blob/ef1b99a/packages/orchestrator/pkg/template/build/phases/finalize/configure.sh)). Template users get `NOPASSWD` sudo ([user.go](https://github.com/e2b-dev/infra/blob/ef1b99a/packages/orchestrator/pkg/template/build/commands/user.go)).
- **No login session.** envd forks processes with the user's credentials and does not go through PAM. So there is no `systemd --user` manager unless linger is on (`sudo loginctl enable-linger user`). The helper already falls back to `setsid`. Whether linger works in an E2B VM: **UNVERIFIED**.
- It is a full kernel, so bubblewrap should work. Whether user namespaces are on in E2B's guest kernel: **UNVERIFIED**.

### 2.4 Persistence

From [persistence](https://docs.e2b.dev/sandbox/persistence) and [billing](https://docs.e2b.dev/billing):

- **Pause** keeps the disk and, by default, memory: "When you pause a sandbox, both the sandbox's filesystem and memory state will be saved." Running holders and agents are frozen, not killed.
- A filesystem-only pause (`memory: false`) keeps the disk and reboots on resume.
- **Network connections do not survive** a pause: "If you resume the sandbox, the service will be accessible again but you need to connect clients again." An agent's open request to its model API fails on resume. How each agent copes: **UNVERIFIED**.
- **Paused sandboxes are kept for ever:** "A paused sandbox is kept indefinitely." Billing stops when a sandbox is paused, killed or times out (billing doc). The docs name no storage charge for a paused sandbox.
- "Pausing a sandbox takes approximately 4 seconds per 1 GiB of RAM". "Resuming a sandbox takes approximately 1 second".
- **Snapshots** (`POST /sandboxes/{id}/snapshots`) outlive the sandbox and can seed new ones. **Fork** clones a running sandbox. **Volumes** outlive sandboxes.

### 2.5 Limits and price

Fetched on 15 October 2026 from [pricing](https://e2b.dev/pricing) and [billing](https://docs.e2b.dev/billing):

| | Hobby | Pro |
| --- | --- | --- |
| Monthly fee | $0, with $100 of one-time credits | $150, plus usage |
| Longest run without a pause | 1 hour | 24 hours |
| Concurrent sandboxes | 20 | 100 (add-ons up to 1,100) |
| vCPU / RAM | up to 8 / 8 GiB | 8+ / 8+ GiB |
| Disk | 10 GiB | 20+ GiB |
| Creates per second | 1 | 5 |

- **Usage:** $0.000014 per vCPU-second and $0.0000045 per GiB-second. A 2 vCPU / 4 GiB sandbox is about $0.166 an hour, or about $121 for 730 hours (my arithmetic).
- **"After a sandbox is paused and resumed, the continuous runtime limit is reset"** ([persistence](https://docs.e2b.dev/sandbox/persistence)). So the 1-hour cap ends one run, not the sandbox.
- **CPU and memory belong to the template.** `POST /v2/sandboxes` has no CPU or memory field. They are set when a template is built (`cpuCount`, `memoryMB` in `POST /v3/templates`) ([openapi.yml](https://github.com/e2b-dev/E2B/blob/62a17df/spec/openapi.yml)). The defaults conflict: the pricing page says 2 vCPU and 4 GiB; the billing doc says 2 vCPU and 512 MiB. Agents want 4 GB or more, so `host create e2b` must build or name a template with enough memory. Builds can take "up to 1 hour" ([template quickstart](https://docs.e2b.dev/template/quickstart)).
- **The timeout is a TTL, not an idle timer.** A sandbox expires `timeout` seconds after create, or after the last `POST /timeout`. "Running commands, reading or writing files, and requests to the sandbox's URL do not reset the timeout of a running sandbox" ([auto-resume](https://docs.e2b.dev/sandbox/auto-resume)). The v2 API default is 300 s.

### 2.6 Self-hosting

E2B Embed runs the whole stack on one Linux host with KVM, through Docker Compose, Terraform (GCP, AWS, Azure) or Kubernetes. "No E2B account and no license key needed" ([embed/README.md](https://github.com/e2b-dev/infra/blob/ef1b99a/embed/README.md)). It asks for 12 GiB of RAM, 20 GiB of disk and `/dev/kvm`. On a Mac it needs an M3 or newer with macOS 15, inside a Lima VM with nested virtualisation ([compose/README.md](https://github.com/e2b-dev/infra/blob/ef1b99a/embed/compose/README.md)). Embed opens ports on its node (the API and the edge proxy, among others), so it suits a homelab or a server, not a laptop. The same simstim adapter would work against it: only the API and sandbox domains change.

### 2.7 Security model

One Firecracker microVM per sandbox, in its own cgroup and network namespace, with a per-sandbox nftables egress firewall and domain allow and deny lists ([README](https://github.com/e2b-dev/infra/blob/ef1b99a/README.md)). Internet access is on by default (`allow_internet_access`).

Every port a process opens gets a URL, `https://<port>-<id>.e2b.app` ([README](https://github.com/e2b-dev/infra/blob/ef1b99a/README.md)). An agent's dev server would be public by default. simstim should create sandboxes with `network.allowPublicTraffic: false`, so those URLs need the sandbox's traffic token.

### 2.8 The API, for fake tests

Base URL `https://api.e2b.app`, auth `X-API-Key: <key>` ([openapi.yml](https://github.com/e2b-dev/E2B/blob/62a17df/spec/openapi.yml)). The SDKs read the key from `E2B_API_KEY`; the CLI keeps its login in `~/.e2b/config.json` ([api.ts](https://github.com/e2b-dev/E2B/blob/62a17df/packages/cli/src/api.ts), [user.ts](https://github.com/e2b-dev/E2B/blob/62a17df/packages/cli/src/user.ts)).

| Action | Request | Response |
| --- | --- | --- |
| Create | `POST /v2/sandboxes` `{"templateID", "timeout": 300, "autoPause": bool, "autoPauseMemory": true, "autoResume": {"enabled": bool}, "allow_internet_access", "network": {"allowPublicTraffic": false}, "metadata": {}, "envVars": {}, "volumeMounts"}` | `201` `{"sandboxID", "templateID", "alias", "clientID", "envdVersion", "envdAccessToken", "trafficAccessToken", "domain"}` |
| Status | `GET /sandboxes/{id}` | `200` `{"sandboxID", "templateID", "startedAt", "endAt", "cpuCount", "memoryMB", "diskSizeMB", "state": "running" \| "paused", "envdVersion", "envdAccessToken", "metadata", "lifecycle": {"autoResume", "onTimeout": "kill" \| "pause"}}`; `404` when gone |
| List | `GET /v2/sandboxes?state=running,paused&metadata=k%3Dv` | `200` array; `X-Next-Token` header |
| Stop | `POST /sandboxes/{id}/pause` `{"memory": true}` | `204`; `409` already paused; `503` busy, retry |
| Start | `POST /v2/sandboxes/{id}/connect` `{"timeout": 300, "memory": true}` | `200` was running, `201` resumed; body as for create |
| Keep alive | `POST /sandboxes/{id}/timeout` `{"timeout": <seconds>}` | `204` |
| Destroy | `DELETE /sandboxes/{id}` | `204` |
| Snapshot | `POST /sandboxes/{id}/snapshots` `{"name", "memory"}` | `201` `{"snapshotID", "names": []}` |
| Template | `POST /v3/templates` `{"name", "cpuCount", "memoryMB"}`, then `POST /v2/templates/{templateID}/builds/{buildID}` `{"fromImage" \| "fromTemplate", "steps", "startCmd", "readyCmd"}`, then `GET /templates/{templateID}/builds/{buildID}/status` | `{"templateID", "buildID"}` |
| Errors | any | `{"code", "error_code", "message"}`; `error_code` values include `sandbox_capacity_unavailable` |

`GET /sandboxes/{id}` returns `envdAccessToken`, so simstim need not store it.

For a fake envd, the Connect path is `/process.Process/Start`. A streaming body is a run of envelopes: one flags byte, a 4-byte big-endian length, then the message. The last envelope has flag `0x02` and carries the end-of-stream JSON ([Connect protocol](https://connectrpc.com/docs/protocol/)).

### 2.9 Idle stop and wake on attach

- **Idle stop comes from the TTL.** Create with `autoPause: true` (the SDKs call it `lifecycle.onTimeout: "pause"`). While any client is connected, it sets the timeout again every minute: a lease. When the laptop sleeps, nobody renews the lease, and the sandbox pauses.
- **The catch.** An agent that is still working when the lid closes is frozen when the lease ends. So the lease should be long (say 60 minutes) while any session is working, and short (say 15 minutes) when all are idle or waiting for input. On Hobby, every run also ends at 1 hour.
- **Wake on attach.** `POST /v2/sandboxes/{id}/connect` resumes a paused sandbox and returns it. With `autoResume`, any envd call or URL request also resumes it; the edge holds the request for up to 90 s ([ARCHITECTURE.md](https://github.com/e2b-dev/infra/blob/ef1b99a/docs/ARCHITECTURE.md)). An explicit connect is clearer, and keeping `autoResume` off means stray traffic to a sandbox URL cannot wake it and run up a bill.
- **The guest cannot stop itself.** Pausing needs the E2B API key inside the VM, which would put a broad credential on the host. A guest `poweroff` would kill the VM. So idle stop is the lease, driven from the laptop.

### 2.10 Fit

**4 of 5.** No SSH and no new port: envd's exec stream carries the protocol, through a small Go client. Pause with memory is the best "stop" of any provider here, it costs nothing while paused, and the whole stack is open source and self-hostable. Against it: a TTL lease instead of an idle timer, the 1-hour run on the free plan, dropped connections at pause, resources fixed per template, and a second transport to build and test.

## 3. Morph Cloud

Read at morph-labs/morph-python-sdk@d37f0a8 (Apache-2.0) and the public [OpenAPI spec](https://cloud.morph.so/api/openapi.json). The server is not published.

- **Access: system OpenSSH, unchanged.** `ssh -o IdentitiesOnly=yes -i <key> <instance-id>@ssh.cloud.morph.so`: the gateway is on port 22 and the user name is the instance ID ([SSH](https://cloud.morph.so/docs/documentation/instances/ssh)). Keys are the user's own public keys, registered with `POST /api/user/ssh-keys` ([SSH keys](https://cloud.morph.so/docs/documentation/setup/ssh-keys)), or per-instance credentials from `GET /api/instance/{id}/ssh/key` ([instance keys](https://cloud.morph.so/docs/documentation/instances/ssh-keys)).
- **Avoid the SDK's login.** The Python SDK logs in as `<instance_id>:<api_key>` with a throw-away key, which puts the API key in the user name ([api.py](https://github.com/morph-labs/morph-python-sdk/blob/d37f0a8/morphcloud/api.py)). This is not in the docs.
- **A real sshd inside.** The SDK edits `/etc/ssh/sshd_config` and runs `systemctl restart sshd` (api.py). So exec channels, stderr and half-close should behave as normal OpenSSH (inference). The HTTP `exec` and `exec/sse` endpoints take no stdin and cannot carry a bridge ([command execution](https://cloud.morph.so/docs/documentation/instances/command-execution)).
- **Inside.** A full VM with systemd (the SDK runs `systemctl`). The default user appears to be root (**UNVERIFIED**).
- **Persistence.** Pause "Preserves memory state and running processes" and is charged "only for storage, not compute" ([FAQ](https://cloud.morph.so/docs/faq), [pause and resume](https://cloud.morph.so/docs/documentation/instances/pause-resume)). Stop is delete: `DELETE /api/instance/{id}` "Stop and delete an instance". Snapshots and branches ("Infinibranch") seed new instances. How long a paused instance is kept: **UNVERIFIED**.
- **Limits.** A new base snapshot defaults to 1 vCPU, 128 MB and 700 MB of disk (`CreateSnapshotRequest`). Plan caps run from 64 vCPU / 256 GB (Developer) to 1,024 vCPU (Scale); per instance or per organisation is **UNVERIFIED** ([pricing](https://cloud.morph.so/web/pricing)). No maximum lifetime is documented.
- **Price** (fetched 15 October 2026): one Morph Compute Unit (MCU) is "1 vCPU-hour + 4 GB RAM-hours + 16 GB disk-hours OR 5 TB snapshot-hours", at "$0.05". A running devbox uses `max(vCPUs, ceil(RAM/4GiB), ceil(disk/16GiB))` MCUs an hour, so 2 vCPU / 4 GB / 16 GB is about $0.10 an hour. The Developer plan is $0 with no starting credit; Team is $40 a month with 1,000 MCUs ([pricing](https://cloud.morph.so/web/pricing)).
- **Isolation.** "Virtual machines"; the hypervisor is not named (**UNVERIFIED**).
- **API.** `https://cloud.morph.so/api`, `Authorization: Bearer <key>`; the SDK reads `MORPH_API_KEY` (api.py).

  | Action | Request | Response |
  | --- | --- | --- |
  | Base snapshot | `POST /api/snapshot` `{"image_id": "morphvm-minimal", "vcpus", "memory" (MB), "disk_size" (MB), "metadata"}` | SnapshotModel `{"id", "status": "pending" \| "ready" \| "failed" \| "deleting" \| "deleted"}` |
  | Create | `POST /api/snapshot/{snapshot_id}/boot` `{"vcpus", "memory", "disk_size", "metadata", "ttl_seconds", "ttl_action": "stop" \| "pause"}` | InstanceModel `{"id", "status": "pending" \| "ready" \| "paused" \| "saving" \| "error", "spec": {"vcpus", "memory", "disk_size"}, "ttl": {"ttl_seconds", "ttl_expire_at", "ttl_action"}, "wake_on": {"wake_on_ssh", "wake_on_http"}, "metadata"}` |
  | Status | `GET /api/instance/{id}` | InstanceModel |
  | Stop | `POST /api/instance/{id}/pause` | InstanceModel |
  | Start | `POST /api/instance/{id}/resume` | InstanceModel |
  | Keep alive | `POST /api/instance/{id}/ttl` `{"ttl_seconds", "ttl_action"}` | InstanceModel |
  | Wake on SSH | `POST /api/instance/{id}/wake-on` `{"wake_on_ssh": true}` | InstanceModel |
  | Destroy | `DELETE /api/instance/{id}` | |

- **Idle stop and wake.** No idle detection; the TTL is a countdown "after which an instance will automatically shut down or pause" ([TTL](https://cloud.morph.so/docs/documentation/instances/ttl)). Morph's own scale-to-zero example uses `ttl_action: "pause"` and a TTL that the app renews ([example](https://cloud.morph.so/docs/examples/auto-scale-agent-workspaces)): the same lease as E2B. With `wake_on_ssh`, "direct SSH connections" resume a paused instance ([wake-on](https://cloud.morph.so/docs/documentation/instances/wake-on)). An explicit `resume` first is safer; the SDK does that itself. Whether an SSH connection waits for the wake or fails: **UNVERIFIED**.
- **Fit: 4 of 5.** The SSH path needs no new code, and pause keeps processes. Against it: closed source, no free compute, and many limits undocumented.

## 4. Daytona

The hosted service now runs private code: the main branch says "As of June 2026, Daytona's core development has moved to a private codebase" ([README](https://github.com/daytonaio/daytona/blob/ec4c21b/README.md)). The last public server is tag v0.190.0 (AGPL-3.0, 23 June 2026). The SDKs are Apache-2.0 in [daytona/clients](https://github.com/daytona/clients/tree/c46c4af). Gateway details below come from v0.190.0 and may differ live.

- **Access: SSH with a token as the user name.** `ssh <token>@ssh.app.daytona.io`. The token comes from `POST /api/sandbox/{id}/ssh-access?expiresInMinutes=60` and lasts 60 minutes by default ([SSH access](https://www.daytona.io/docs/en/ssh-access/)). The gateway accepts no keys: the token is a bearer secret, checked only at the handshake ([ssh-gateway main.go](https://github.com/daytonaio/daytona/blob/01c502b/apps/ssh-gateway/main.go)). It would appear in simstim's `ssh` command line and process list.
- **The gateway refuses a stopped sandbox** ("Please start the sandbox before attempting to connect"), so there is no wake on SSH (same file).
- **Stream.** An exec channel runs `/bin/sh -c <command>` with piped stdin, closed on EOF ([daemon ssh server.go](https://github.com/daytonaio/daytona/blob/01c502b/apps/daemon/pkg/ssh/server.go)). It is binary-safe. But **both gateway hops drop stderr**: they copy only the main data stream ([main.go](https://github.com/daytonaio/daytona/blob/01c502b/apps/ssh-gateway/main.go), [runner sshgateway](https://github.com/daytonaio/daytona/blob/01c502b/apps/runner/pkg/sshgateway/service.go)). In Go's SSH library, unread stderr still uses up the channel window ([channel.go](https://github.com/golang/crypto/blob/cf169cd/ssh/channel.go)), so about 2 MiB of stderr could stall stdout too (inference, **UNVERIFIED**). The helper must send stderr to a file. The toolbox API is worse: its command input is a JSON string ([toolbox spec](https://www.daytona.io/docs/toolbox-openapi.json)).
- **Inside.** In container sandboxes the Daytona daemon is PID 1 and reaps orphans; there is no systemd ([childreap.go](https://github.com/daytonaio/daytona/blob/01c502b/apps/daemon/pkg/childreap/childreap.go)). A `setsid` holder should survive the exec (**UNVERIFIED**). The old image ran as `daytona` with sudo; the live docs say "Code runs as root inside the sandbox" ([isolation](https://www.daytona.io/docs/en/isolation/)).
- **Persistence** ([sandboxes](https://www.daytona.io/docs/en/sandboxes/)). Stopping a container "terminates the running container. The filesystem is preserved, but memory state is not." "Pause is not supported for container sandboxes"; the newer Linux VM class can pause with memory. Archive moves a stopped disk to object storage; auto-archive after 7 days by default.
- **Limits.** Containers default to 1 vCPU, 1 GiB and 3 GiB of disk, with a maximum of 4 vCPU, 8 GiB and 10 GiB per sandbox. Outbound network is restricted on Tiers 1 and 2 ([limits](https://www.daytona.io/docs/en/limits/), [isolation](https://www.daytona.io/docs/en/isolation/)). Whether agents' model APIs are reachable there: **UNVERIFIED**.
- **Price** ([pricing](https://www.daytona.io/pricing), fetched 15 October 2026): $0.0504 per vCPU-hour, $0.0162 per GiB-hour, storage $0.000108 per GiB-hour after 5 GiB free, billed per second. "$200 in free compute included". Stopped bills disk only; archived bills nothing ([billing](https://www.daytona.io/docs/en/billing/)).
- **API.** `https://app.daytona.io/api`, `Authorization: Bearer <key>` ([openapi.json](https://www.daytona.io/docs/openapi.json)).

  | Action | Request | Response |
  | --- | --- | --- |
  | Create | `POST /sandbox` `{"name", "snapshot", "user", "env", "labels", "cpu", "memory" (GB), "disk" (GB), "autoStopInterval", "autoArchiveInterval", "autoDeleteInterval", "networkAllowList"}` | Sandbox `{"id", "name", "state", "desiredState", "cpu", "memory", "disk", "autoStopInterval", "lastActivityAt", "sandboxClass": "container" \| "linux-vm" \| "windows", "toolboxProxyUrl"}` |
  | Status | `GET /sandbox/{idOrName}` | Sandbox; `state` includes `creating`, `started`, `stopping`, `stopped`, `starting`, `archived`, `paused`, `error`, `destroyed` |
  | Stop | `POST /sandbox/{id}/stop` | |
  | Start | `POST /sandbox/{id}/start` (also resumes and restores from archive) | |
  | SSH token | `POST /sandbox/{id}/ssh-access?expiresInMinutes=N` | `{"token", "expiresAt", "sshCommand"}` |
  | Destroy | `DELETE /sandbox/{id}` | |
  | Rate limit | any | `429` `{"statusCode": 429, "message": "Rate limit exceeded"}` |

- **Idle stop and wake.** Auto-stop after 15 minutes by default. "Active SSH connections" reset the timer; background work does not: "the sandbox may auto-stop mid-process" ([sandboxes](https://www.daytona.io/docs/en/sandboxes/)). A stop kills every holder in a container sandbox. Wake: `POST /start`, poll for `started`, mint a token, then `ssh`.
- **Fit: 3 of 5.** It works over plain SSH today. But the token sits in the user name, stderr is dropped, a container stop kills every session, the disk tops out at 10 GiB, and the server is no longer open.

## 5. Modal Sandboxes

Read at modal-labs/modal-client@56fe5fa (Apache-2.0: Python and Go SDKs, and the protos). The server is not published.

- **Access: no SSH.** Control is gRPC to `api.modal.com:443` with `x-modal-token-id` and `x-modal-token-secret` ([client.go](https://github.com/modal-labs/modal-client/blob/56fe5fa/go/client.go)), read from `MODAL_TOKEN_ID` and `MODAL_TOKEN_SECRET` or `~/.modal.toml` ([config.go](https://github.com/modal-labs/modal-client/blob/56fe5fa/go/config.go)). Tunnels need a server listening inside the sandbox ([networking](https://modal.com/docs/guide/sandbox-networking)), so they are out.
- **Stream: the best of any provider.** Exec goes through a per-worker gRPC `TaskCommandRouter`: `TaskExecStart`, `TaskExecStdinWrite{offset, data bytes, eof}`, and `TaskExecStdioRead{offset, file_descriptor}` as a server stream of bytes ([task_command_router.proto](https://github.com/modal-labs/modal-client/blob/56fe5fa/modal_proto/task_command_router.proto)). It is binary-safe, stdout and stderr are separate, EOF works, and offsets let a client resume. The official Go SDK's `sb.Exec` returns a `ContainerProcess` with `Stdin io.WriteCloser`, `Stdout` and `Stderr io.ReadCloser` ([sandbox.go](https://github.com/modal-labs/modal-client/blob/56fe5fa/go/sandbox.go)). This proves the byte-stream idea with no port.
- **Inside.** gVisor by default, or `runtime="vm"` with its own kernel ([sandboxes](https://modal.com/docs/guide/sandboxes)). No systemd is documented. Exec'd processes keep running when the client detaches.
- **Persistence: none in place.** There is no stop, start, pause or resume. A sandbox ends at its `timeout`, its `idle_timeout`, `terminate()` or an out-of-memory kill, for good. Filesystem snapshots become images, kept 30 days by default. Memory snapshots are alpha, expire after 7 days, and "Snapshotting a Sandbox will currently cause it to terminate" ([snapshots](https://modal.com/docs/guide/sandbox-snapshots)).
- **Limits.** `timeout` defaults to 5 minutes, maximum 24 hours ([sandboxes](https://modal.com/docs/guide/sandboxes)).
- **Price** ([pricing](https://modal.com/pricing), fetched 15 October 2026): sandbox CPU $0.00003942 per physical core per second (1 core is 2 vCPU) and memory $0.00000667 per GiB per second: about $0.24 an hour for 1 core and 4 GiB. Starter is $0 with "$30 / month free credits".
- **API (gRPC only)**, service `modal.client.ModalClient` ([api.proto](https://github.com/modal-labs/modal-client/blob/56fe5fa/modal_proto/api.proto)): `SandboxCreate{app_id, definition, tags}` → `{sandbox_id}`; `SandboxWait{sandbox_id, timeout}` → `{result}`; `SandboxTerminate{sandbox_id}`; `SandboxSnapshotFs` → `{image_id}`; `SandboxGetCommandRouterAccess` → `{jwt, url}`. The `Sandbox` definition has `image_id`, `resources{milli_cpu, memory_mb}`, `timeout_secs`, `idle_timeout_secs`, `runtime`.
- **Idle stop and wake.** `idle_timeout` terminates. Wake would mean a new sandbox from a filesystem snapshot, with every session lost, and every host ends after 24 hours anyway.
- **Fit: 2 of 5.** A model stream with the wrong lifecycle: fine for throwaway hosts, not for persistent sessions.

## 6. Fly.io Machines

Read at superfly/fly-go@d072ed8 and superfly/flyctl@184af3a, with the [Machines OpenAPI](https://docs.machines.dev/openapi.json).

- **Access.** `fly ssh console` reaches port 22 on the Machine's private 6PN address, through a WireGuard tunnel that runs in user space inside flyctl ([ssh_terminal.go](https://github.com/superfly/flyctl/blob/184af3a/internal/command/ssh/ssh_terminal.go), [tunnel.go](https://github.com/superfly/flyctl/blob/184af3a/wg/tunnel.go)). Where Fly's SSH server lives, and whether every image works without its own sshd: **UNVERIFIED**. Auth is a single-use certificate, valid 1 hour ([connect.go](https://github.com/superfly/flyctl/blob/184af3a/internal/command/ssh/connect.go)); `fly ssh issue` makes ones of 1–72 hours ([docs](https://docs.fly.io/flyctl/cmd/fly_ssh_issue.md)).
- **System OpenSSH.** flyctl has no `--stdio` ProxyCommand mode. The documented routes are a system WireGuard tunnel plus `fly ssh issue` ([opensshd blueprint](https://docs.fly.io/blueprints/opensshd.md)), or `fly proxy 10022:22 <6pn-address>`, which listens on the laptop's loopback ([proxy.go](https://github.com/superfly/flyctl/blob/184af3a/internal/command/proxy/proxy.go)).
- **Stream.** The Machines API `exec` is one-shot: `stdin` is one JSON string, and `stdout` and `stderr` come back as strings ([flaps_machines.go](https://github.com/superfly/fly-go/blob/d072ed8/flaps/flaps_machines.go)). It cannot carry a bridge. But `fly ssh console --machine <id> -q -C "simstim-host bridge"` without `--pty` gets no PTY and copies raw stdin and stdout both ways ([console.go](https://github.com/superfly/flyctl/blob/184af3a/internal/command/ssh/console.go), [io.go](https://github.com/superfly/flyctl/blob/184af3a/ssh/io.go)). So flyctl can be the transport, with no new port on the Machine.
- **Inside.** No systemd. "When your entrypoint program exits, our `init` kills the Machine" ([multiple processes](https://docs.fly.io/app-guides/multiple-processes.md)). A stock image needs a long-lived entrypoint such as `sleep inf`. Whether a `setsid` process outlives the SSH session: **UNVERIFIED**.
- **Persistence.** The root filesystem is "a blank slate on every startup" unless `config.rootfs.persist` is `restart` or `always` ([overview](https://docs.fly.io/machines/overview.md), [configuration](https://docs.fly.io/reference/configuration.md)). Volumes survive stop and suspend. **Suspend** snapshots CPU and memory and resumes in a few hundred ms, but only up to 2 GB of RAM, and a deploy, migration or maintenance discards the snapshot ([suspend-resume](https://docs.fly.io/reference/suspend-resume.md)).
- **Limits.** Shared 1–8 vCPU with up to 2 GB per vCPU; performance 1–16 vCPU with 2–8 GB per vCPU ([sizing](https://docs.fly.io/machines/guides-examples/machine-sizing.md)). No documented lifetime. One API request per second per action per Machine ([working with the API](https://docs.fly.io/machines/api/working-with-machines-api.md)).
- **Price** ([pricing](https://docs.fly.io/about/pricing.md), fetched 15 October 2026, region `iad`): shared-cpu-2x with 4 GB about $25.40 for 30 days; performance-2x with 4 GB $66.00. A stopped or suspended Machine costs $0.15 per GB-month of root filesystem; volumes the same. **No free tier**; the trial is 2 Machine-hours or 7 days ([free trial](https://docs.fly.io/about/free-trial.md)).
- **Self-hosting:** no. **Isolation:** Firecracker microVMs ([architecture](https://docs.fly.io/reference/architecture.md)).
- **API.** `https://api.machines.dev`, `Authorization: Bearer <token>`.

  | Action | Request | Response |
  | --- | --- | --- |
  | Create | `POST /v1/apps/{app}/machines` `{"name", "region", "config": {"image", "guest": {"cpu_kind", "cpus", "memory_mb"}, "init": {"exec": [...]}, "auto_destroy": false, "restart": {"policy": "no"}, "rootfs": {"persist": "always", "size_gb"}, "mounts", "env", "metadata"}}` | `200` Machine `{"id", "name", "state": "created", "region", "private_ip", "instance_id", "config", "created_at"}` |
  | Status | `GET /v1/apps/{app}/machines/{id}` | Machine |
  | Wait | `GET …/{id}/wait?state=started&timeout=60` | `{"ok", "state", "version", "event_id"}` |
  | Stop | `POST …/{id}/stop` `{"signal", "timeout"}`, or `POST …/{id}/suspend` | `200` |
  | Start | `POST …/{id}/start` | `200` `{"message", "status", "previous_state"}` |
  | Destroy | `DELETE …/{id}?force=true` | `200` |

  States: `created`, `started`, `stopped`, `suspended`, `failed`; transient `creating`, `starting`, `stopping`, `suspending`, `destroying` and others; terminal `destroyed`, `replaced`, `migrated` ([machine states](https://docs.fly.io/machines/machine-states.md)).
- **Idle stop and wake.** Fly's auto-stop acts only on traffic through Fly Proxy services; SSH over 6PN does not pass through it (inference, **UNVERIFIED**; [autostop](https://docs.fly.io/launch/autostop-autostart.md)). A Machine can suspend itself with no token: `curl --unix-socket /.fly/api -X POST http://flaps/v1/apps/$FLY_APP_NAME/machines/$FLY_MACHINE_ID/suspend` ([suspend-resume](https://docs.fly.io/reference/suspend-resume.md)). The same for `/stop` is **UNVERIFIED**. Wake: `POST /start`, `GET /wait?state=started`, then connect.
- **Side note.** Fly's newer Sprites product has a WebSocket exec API with binary frames, where "Commands continue running after disconnect" ([execute command](https://docs.fly.io/sprites/api/websockets/execute-command.md), [lifecycle](https://docs.fly.io/sprites/concepts/lifecycle.md)). It was not researched further.
- **Fit: 3 of 5.** A real microVM, per-second billing, a clean API and a self-suspend socket. Against it: flyctl as the transport, no systemd, a root filesystem that resets unless told not to, best-effort suspend, and no free tier.

## 7. Coder

Read at coder/coder@c85ebc1. Coder is self-hosted: AGPL-3.0, with an `enterprise/` folder under a commercial licence ([LICENSE](https://github.com/coder/coder/blob/c85ebc1/LICENSE)). Community is free; Premium is priced per user per year on request ([pricing](https://coder.com/pricing), fetched 15 October 2026). Compute is the user's own.

- **Access: system OpenSSH.** `coder config-ssh` writes host blocks with `ProxyCommand coder ssh --stdio … %h` ([configssh.go](https://github.com/coder/coder/blob/c85ebc1/cli/configssh.go)). `--stdio` copies a raw SSH stream ([ssh.go](https://github.com/coder/coder/blob/c85ebc1/cli/ssh.go)). This is simstim's existing transport with a ProxyCommand, like IAP.
- **No new port.** The agent's SSH server listens only inside its own in-process tailnet, and the agent dials out to the Coder server ([agent.go](https://github.com/coder/coder/blob/c85ebc1/agent/agent.go), [tailnet/conn.go](https://github.com/coder/coder/blob/c85ebc1/tailnet/conn.go)). Traffic goes peer to peer, or through the deployment's own DERP relay ([networking](https://coder.com/docs/admin/networking)). That relay belongs to the user's Coder, not to simstim.
- **Inside.** Whatever the template makes: a Docker container with a persistent home volume, or a VM ([docker template](https://github.com/coder/coder/blob/c85ebc1/examples/templates/docker/main.tf), [aws-linux template](https://github.com/coder/coder/blob/c85ebc1/examples/templates/aws-linux/main.tf)). Commands without a PTY run with `Setsid: true` and are not torn down when the session ends, "to match OpenSSH" ([agentssh.go](https://github.com/coder/coder/blob/c85ebc1/agent/agentssh/agentssh.go)). A `systemd --user` manager is unlikely, as sessions skip PAM (**UNVERIFIED**). A stop ends every process.
- **Idle stop and wake, built in.** Autostop at a deadline; an open SSH session bumps it by the template's activity bump, 1 hour by default ([templates.go](https://github.com/coder/coder/blob/c85ebc1/coderd/templates.go), [scheduling](https://coder.com/docs/user-guides/workspace-scheduling)). **`coder ssh` starts a stopped workspace by itself**, so a ProxyCommand attach wakes it ([ssh.go](https://github.com/coder/coder/blob/c85ebc1/cli/ssh.go)). The gap: background work does not count as activity, so a detached agent is stopped at the deadline. simstim would set a long `ttl_ms`, or call `PUT /api/v2/workspaces/{id}/extend` while agents work.
- **API.** The user's Coder URL, `Coder-Session-Token: <token>` ([swagger.json](https://github.com/coder/coder/blob/c85ebc1/coderd/apidoc/swagger.json)).

  | Action | Request | Response |
  | --- | --- | --- |
  | Create | `POST /api/v2/organizations/{org}/members/me/workspaces` `{"name", "template_id", "ttl_ms", "rich_parameter_values"}` | `201` Workspace `{"id", "name", "ttl_ms", "last_used_at", "latest_build"}` |
  | Start, stop, destroy | `POST /api/v2/workspaces/{id}/builds` `{"transition": "start" \| "stop" \| "delete", "reason": "cli"}` | `201` WorkspaceBuild `{"id", "transition", "status", "job": {"status"}, "deadline"}` |
  | Status | `GET /api/v2/workspaces/{id}` | Workspace; `latest_build.status` is `pending`, `starting`, `running`, `stopping`, `stopped`, `failed`, `canceling`, `canceled`, `deleting` or `deleted` |
  | Keep alive | `PUT /api/v2/workspaces/{id}/extend` `{"deadline"}` | |

- **Fit: 4 of 5 for someone who already runs Coder; about 2 otherwise.** The transport and the wake are free. But `host create coder` needs a deployment and a template, and the template decides persistence and the process model.

## 8. GitHub Codespaces

Read at cli/cli@819f972 and the [REST description](https://github.com/github/rest-api-description/blob/507b88e/descriptions/api.github.com/api.github.com.json).

- **Access.** `gh codespace ssh --config` prints host blocks with `ProxyCommand gh cs ssh -c <name> --stdio -- -i <key>` ([ssh.go](https://github.com/cli/cli/blob/819f972/pkg/cmd/codespace/ssh.go)). It rides Microsoft Dev Tunnels to an sshd inside the codespace, which is firewalled from the internet ([security](https://docs.github.com/en/codespaces/reference/security-in-github-codespaces)). `--stdio` is a **hidden** flag, and gh uses an undocumented `?internal=true` endpoint for tunnel tokens ([api.go](https://github.com/cli/cli/blob/819f972/internal/codespaces/api/api.go)). There is no public exec API.
- **Inside.** A Docker dev container on its own VM. Processes keep running across disconnects ([lifecycle](https://docs.github.com/en/codespaces/about-codespaces/understanding-the-codespace-lifecycle)). systemd in the default image: **UNVERIFIED**.
- **Persistence.** `/workspaces` survives stop and rebuild; other folders survive stop. A stopped codespace is deleted after its retention period, 30 days by default ([automatic deletion](https://docs.github.com/en/codespaces/setting-your-user-preferences/configuring-automatic-deletion-of-your-codespaces)).
- **Limits.** A "maximum lifetime" of 12 hours, "regardless of your idle timeout policy or settings. This limit applies even while you are actively using the codespace". The idle timeout is 5–240 minutes, 30 by default; activity is typing, or terminal input or output ([timeout](https://docs.github.com/en/codespaces/setting-your-user-preferences/setting-your-timeout-period-for-github-codespaces)). Each codespace needs a repository.
- **Price** ([billing](https://docs.github.com/en/billing/concepts/product-billing/github-codespaces), fetched 15 October 2026): $0.18 an hour for 2 cores, $0.36 for 4, $0.72 for 8; storage $0.07 per GB-month. Personal accounts get 120 core-hours and 15 GB-month a month free (GitHub Free), or 180 and 20 (Pro).
- **API.** `https://api.github.com`, `Authorization: Bearer <token>` with the `codespace` scope. `POST /user/codespaces` `{"repository_id", "ref", "machine", "idle_timeout_minutes", "retention_period_minutes"}` → `201` or `202`; `POST /user/codespaces/{name}/start` and `/stop`; `DELETE /user/codespaces/{name}` → `202`. `state` values include `Available`, `Shutdown`, `Starting`, `ShuttingDown`, `Provisioning` and `Failed`.
- **Wake.** gh starts a `Shutdown` codespace on connect, and polls for up to 5 minutes ([codespaces.go](https://github.com/cli/cli/blob/819f972/internal/codespaces/codespaces.go)).
- **Fit: 2 of 5.** Good access and a free quota, but the 12-hour cap and the idle timeout end long agent runs, and the stdio path is hidden and internal.

## 9. Hetzner Cloud

The SSH recipe and server prices are in the [connectivity note](connectivity-and-providers.md#42-plain-ssh-hetzner-digitalocean-akamai-linode-vultr). This section covers `host create`. Read at hetznercloud/hcloud-go@7052653 and the public [OpenAPI spec](https://docs.hetzner.cloud/cloud.spec.json).

- **API.** `https://api.hetzner.cloud/v1`, `Authorization: Bearer <token>`. 3,600 requests an hour per project.

  | Action | Request | Response |
  | --- | --- | --- |
  | Find key | `GET /ssh_keys?fingerprint=<md5>` | `{"ssh_keys": [{"id", "name", "fingerprint", "public_key"}]}` |
  | Add key | `POST /ssh_keys` `{"name", "public_key", "labels"}` | `201` `{"ssh_key": {…}}` |
  | Create | `POST /servers` `{"name", "server_type", "image": "ubuntu-24.04", "location", "ssh_keys": [id], "user_data", "labels", "start_after_create": true, "public_net": {"enable_ipv4", "enable_ipv6"}}` | `201` `{"server": {"id", "status": "initializing", "public_net": {"ipv4": {"ip", "id"}, "ipv6": {"ip"}}}, "action": {"id", "command": "create_server", "status": "running"}, "next_actions": [...], "root_password": null}` |
  | Status | `GET /servers/{id}` | `server.status`: `running`, `initializing`, `starting`, `stopping`, `off`, `deleting`, `migrating`, `rebuilding`, `unknown` |
  | Stop | `POST /servers/{id}/actions/shutdown` (ACPI) or `/poweroff` | `201` `{"action"}` |
  | Start | `POST /servers/{id}/actions/poweron` | `201` `{"action"}` |
  | Wait | `GET /actions?id=<id>` | `action.status`: `running`, `success`, `error` |
  | Destroy | `DELETE /servers/{id}` | `200` `{"action"}` |
  | Snapshot | `POST /servers/{id}/actions/create_image` `{"type": "snapshot"}` | `201` `{"image", "action"}` |

- **Always send `ssh_keys`.** Without one, Hetzner makes a root password, returns it in `root_password` and emails it ([server FAQ](https://docs.hetzner.com/cloud/servers/faq/)). With one, simstim never sees a secret.
- **A stopped server costs the full price.** Billing is "regardless of whether it is turned on or not" ([billing FAQ](https://docs.hetzner.com/cloud/billing/faq/)). Hetzner may even power a server back on after a live migration (server FAQ). So stop saves nothing. The only saving is snapshot, delete, and recreate on attach. Snapshots cost €0.0143 per GB-month of compressed size, from the price data behind hetzner.com (VAT status **UNVERIFIED**). Snapshot and restore times: **UNVERIFIED**.
- **No idle stop** of any kind in the API or the docs. A guest power-off leaves the server off, and billed.
- **IP.** The primary IP stays across stop and start.
- **Credentials.** `HCLOUD_TOKEN`, or the active context in the hcloud CLI's `~/.config/hcloud/cli.toml` ([config_unix.go](https://github.com/hetznercloud/cli/blob/7a7bba7/internal/state/config/config_unix.go)). hcloud-go reads no environment variable itself. A small `net/http` client is enough.
- **Fit: 3 of 5.** The simplest create and destroy of any cloud, with a public spec for fakes. But idle stop saves no money without a slow snapshot-and-recreate flow.

## 10. Google Compute Engine

The IAP recipe is in the [connectivity note](connectivity-and-providers.md#44-gcp-iap-tcp-forwarding-and-os-login). Read from the public [discovery document](https://compute.googleapis.com/$discovery/rest?version=v1), revision 20261007.

- **API.** `https://compute.googleapis.com/compute/v1/`, `Authorization: Bearer <OAuth2 token>` from Application Default Credentials.

  | Action | Request | Response |
  | --- | --- | --- |
  | Create | `POST projects/{p}/zones/{z}/instances?requestId=<uuid>` `{"name", "machineType": "zones/{z}/machineTypes/e2-standard-2", "disks": [{"boot": true, "autoDelete": true, "initializeParams": {"sourceImage", "diskSizeGb"}}], "networkInterfaces": [{"network": "global/networks/default", "accessConfigs": [{"type": "ONE_TO_ONE_NAT"}]}], "metadata": {"items": [{"key": "ssh-keys", "value": "user:ssh-ed25519 …"}, {"key": "enable-oslogin", "value": "FALSE"}]}, "labels", "scheduling": {"maxRunDuration": {"seconds"}, "instanceTerminationAction": "STOP"}}` | Operation `{"name", "status": "PENDING" \| "RUNNING" \| "DONE", "error": {"errors": []}, "targetId"}` |
  | Status | `GET …/instances/{name}` | Instance `{"status", "networkInterfaces": [{"accessConfigs": [{"natIP"}]}]}`; status includes `PROVISIONING`, `STAGING`, `RUNNING`, `STOPPING`, `STOPPED`, `SUSPENDING`, `SUSPENDED`, `TERMINATED` |
  | Stop, start | `POST …/instances/{name}/stop`, `/start` | Operation |
  | Suspend, resume | `POST …/instances/{name}/suspend`, `/resume` | Operation |
  | Destroy | `DELETE …/instances/{name}` | Operation |
  | Wait | `POST projects/{p}/zones/{z}/operations/{op}/wait` (at most 2 minutes; loop until `DONE`) | Operation |

- **Keys.** Metadata `ssh-keys` as `USER:KEY`, with OS Login off on the instance ([add SSH keys](https://docs.cloud.google.com/compute/docs/connect/add-ssh-keys)). An organisation policy that enforces OS Login would block this (**UNVERIFIED**).
- **Stopped costs disks only.** "Stopped instances do not incur VM usage charges" (discovery, `instances.stop`). A 40 GiB balanced disk is about $4 a month ([disk pricing](https://cloud.google.com/compute/disks-image-pricing), us-central1 assumed).
- **Suspend keeps memory** for up to 60 days, then becomes `TERMINATED`. It cannot be started from inside the guest: "Compute Engine ignores the in-guest signal" ([suspend](https://docs.cloud.google.com/compute/docs/instances/suspend-resume-instance)). The rate for stored memory: **UNVERIFIED**.
- **Idle stop from the guest needs no credential.** `sudo poweroff` in the guest stops the instance ([stop and start](https://docs.cloud.google.com/compute/docs/instances/stop-start-instance)). `scheduling.maxRunDuration` with `STOP` is a hard cap per run, from 30 s to 120 days: a good safety net ([limit runtime](https://docs.cloud.google.com/compute/docs/instances/limit-vm-runtime)).
- **The IP changes** on every stop and start unless it is promoted to a static address ([IP addresses](https://docs.cloud.google.com/vpc/docs/ip-addresses)). simstim must look the IP up on each attach and pin the host key with `HostKeyAlias`. With `enable-guest-attributes=TRUE`, host keys can be read out of band through `instances.getGuestAttributes` ([guest attributes](https://docs.cloud.google.com/compute/docs/metadata/manage-guest-attributes); the `hostkeys/` path is **UNVERIFIED**).
- **Credentials.** ADC: `GOOGLE_APPLICATION_CREDENTIALS`, then `~/.config/gcloud/application_default_credentials.json`, then the metadata server ([ADC](https://docs.cloud.google.com/docs/authentication/application-default-credentials)). In Go, `golang.org/x/oauth2/google.FindDefaultCredentials` is a light dependency ([default.go](https://github.com/golang/oauth2/blob/c624b89/google/default.go)). The project ID is not in user credentials, so simstim must ask for it.
- **Fit: 4 of 5.** Clean REST with a public schema, a guest power-off that stops billing with no credential on the host, and a hard-cap safety net. Against it: project and zone set-up, a new IP on every start, and OS Login policies.

## 11. AWS EC2

The SSM and EICE recipes are in the [connectivity note](connectivity-and-providers.md#45-aws-ssm-session-manager-and-ec2-instance-connect-endpoint).

- **API.** The Query API at `https://ec2.<region>.amazonaws.com/`, form-encoded `Action=…&Version=2016-11-15`, signed with SigV4, with XML replies ([query requests](https://docs.aws.amazon.com/ec2/latest/devguide/Query-Requests.html)).

  | Action | Parameters | Reply |
  | --- | --- | --- |
  | Add key | `ImportKeyPair` `KeyName`, `PublicKeyMaterial` (base64) | `keyFingerprint`, `keyName`, `keyPairId` |
  | Create | `RunInstances` `ImageId`, `InstanceType`, `MinCount=1`, `MaxCount=1`, `KeyName`, `UserData` (base64), `TagSpecification.1.ResourceType=instance`, `InstanceInitiatedShutdownBehavior=stop`, `HibernationOptions.Configured`, `SecurityGroupId.1`, `ClientToken` | `instancesSet/item/{instanceId, instanceState{code, name}}` |
  | Status | `DescribeInstances` `InstanceId.1` | `…/instancesSet/item/{instanceState, ipAddress, stateReason}` |
  | Stop | `StopInstances` `InstanceId.1`, `Hibernate` | `{currentState, previousState}` |
  | Start | `StartInstances` | the same |
  | Destroy | `TerminateInstances` | the same |

  State codes: `0 pending`, `16 running`, `32 shutting-down`, `48 terminated`, `64 stopping`, `80 stopped` ([InstanceState](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_InstanceState.html)). `stateReason` `Client.InstanceInitiatedShutdown` tells simstim the host stopped itself ([StateReason](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_StateReason.html)). There is no operation object: poll `DescribeInstances`.
- **Keys.** `ImportKeyPair` takes only RSA and ED25519 ([key pairs](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/create-key-pairs.html)); ECDSA and FIDO keys need cloud-init `UserData`.
- **Port 22.** The default security group admits only its own members, so simstim must create a group with TCP 22 from the user's address, or use the SSM or EICE recipes ([default security group](https://docs.aws.amazon.com/vpc/latest/userguide/default-security-group.html)).
- **Stopped costs volumes only**, with a one-minute minimum on every start ([stop and start](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/how-ec2-instance-stop-start-works.html)). gp3 is $0.08 per GB-month in us-east-1 (public price data, published 12 October 2026).
- **Idle stop from the guest needs no credential.** "When you initiate a shutdown from an Amazon EBS backed instance (using a command such as shutdown or poweroff), the instance stops." Never use `halt`, which leaves it running ([shutdown behaviour](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/Using_ChangingInstanceInitiatedShutdownBehavior.html)).
- **Hibernate** keeps RAM on an encrypted EBS root, for up to 60 days. It must be enabled at launch, and Ubuntu 24.04 and Debian are not on the supported list ([prerequisites](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/hibernating-prerequisites.html), [overview](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/instance-hibernate-overview.html)). The prerequisites page implies that a guest suspend-to-disk also hibernates; whether that is reliable: **UNVERIFIED**.
- **The IP changes** on every start unless an Elastic IP is attached, which is billed while stopped.
- **Nitro Sandboxes (added 10 November 2026).** Since 9 November 2026, an instance type that supports Nitro Enclaves, launched with the enclave option, can run hardware-isolated child VMs beside its own workload, at no extra charge. Firecracker 1.18 drives them through `/dev/nitro_enclaves`, with no `/dev/kvm`, on Intel, AMD and Graviton ([EC2](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/amazon-ec2-nitro-sandboxes.html)). So an EC2 host can run microvm sandboxes without nested virtualisation ([isolated sessions](isolation.md#101-aws-nitro-sandboxes-on-ec2)).
- **Credentials.** The SDK's default chain: environment, web identity, `~/.aws/config` and `credentials` by `AWS_PROFILE`, SSO, roles ([Go SDK config](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/configure-gosdk.html)). SSO and SigV4 by hand are not worth it. Use `aws-sdk-go-v2/config` for credentials only, plus its SigV4 signer and hand-written XML for about 8 actions; or shell out to `aws configure export-credentials` ([export-credentials](https://docs.aws.amazon.com/cli/latest/reference/configure/export-credentials.html)).
- **Fit: 3 of 5.** Guest power-off, and even hibernation, work with no credential on the host. But `host create` has the most parts: SigV4, an AMI per region, a security group, limited key types, and a new IP on each start.

## 12. Local VMs and containers

Read at lima-vm/lima@f6158fe, abiosoft/colima@49dfe87, apple/container@22029b0, apple/containerization@fcc92dd, canonical/multipass@ffe5ba7, utmapp/UTM@7928d11, lxc/incus@574960c, docker/cli@dfc520d, moby/moby@ca90aff and containers/podman@f15bf2b. OrbStack is closed source; only its docs were read.

### 12.1 Summary

| Tool | Runs on | Licence | Isolation | systemd | Stream without SSH | SSH | Fit |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Lima | macOS (arm64, Intel), Linux | Apache-2.0 | Full VM (vz on macOS; QEMU/KVM on Linux) | Yes | `limactl shell` is ssh underneath | Generated `ssh.config`, loopback port | **5** |
| Colima | macOS, Linux | MIT | A Lima VM | Yes | `colima ssh --` is `limactl shell` | Through Lima; edits `~/.ssh/config` by default | 2 |
| OrbStack | macOS 14+ | Proprietary; free for personal use, $8 per user per month for work | One shared VM, shared kernel | Yes (machines) | `orb -m <m> cmd` (binary safety **UNVERIFIED**) | Built-in server on the Mac's loopback | 3 |
| Multipass | macOS 14+, Linux (snap) | GPL-3.0 | Full VM (QEMU) | Yes | `multipass exec` (libssh, no PTY when piped) | sshd in the VM | 3 |
| UTM | macOS | Apache-2.0 | Full VM | Depends | **No**: `utmctl exec` reads stdin to EOF first | Manual | 1 |
| Apple `container` | macOS 26, Apple silicon | Apache-2.0 | Small VM per container | Yes in `container machine` | `container exec -i`, `container machine run -i` | None | 3 |
| Docker | Linux; macOS and Windows through a VM product | Engine Apache-2.0; Desktop paid for larger firms | Shared-kernel container | No by default | `docker exec -i` | None | 3 |
| Podman | Linux; macOS through `podman machine` | Apache-2.0 | Shared-kernel container | With `--systemd` | `podman exec -i` | `podman machine ssh` (the VM) | 3 |
| Incus | Linux server; client elsewhere | Apache-2.0 | System container or VM | Yes | `incus exec -T` | Not needed | 3 (4 on Linux) |

### 12.2 Lima

- **Access.** Lima writes `~/.lima/<name>/ssh.config`; `ssh -F <that file> lima-<name>` works ([ssh docs](https://github.com/lima-vm/lima/blob/f6158fe/website/content/en/docs/usage/ssh.md)). sshd listens on port 22 inside the guest, on its private user-mode network or on vsock. Lima's host agent forwards it from a port on the laptop's `127.0.0.1` only ([default.yaml](https://github.com/lima-vm/lima/blob/f6158fe/templates/default.yaml), [vm_darwin.go](https://github.com/lima-vm/lima/blob/f6158fe/pkg/driver/vz/vm_darwin.go)). Nothing is reachable from the LAN. In simstim's terms the VM is the host, and its sshd is the same door a cloud VM has.
- **`limactl shell` is ssh** with `$SHELL -l -c` ([shell.go](https://github.com/lima-vm/lima/blob/f6158fe/cmd/limactl/shell.go)). The login shell can print to stdout, so call `ssh -F` directly. That is simstim's current path.
- **Inside.** An Ubuntu cloud image with cloud-init and systemd. The guest user has the host user's name and passwordless sudo. **Lima turns linger on** at boot ([20-rootless-base.sh](https://github.com/lima-vm/lima/blob/f6158fe/pkg/cidata/cidata.TEMPLATE.d/boot.Linux/20-rootless-base.sh)), except in plain mode.
- **Defaults to change.** Lima mounts the host home folder read-only, and forwards every guest port to the laptop's loopback ([mounts.yaml](https://github.com/lima-vm/lima/blob/f6158fe/templates/_default/mounts.yaml), [port docs](https://github.com/lima-vm/lima/blob/f6158fe/website/content/en/docs/config/port.md)). An agent could then read `~/.ssh` on the laptop. `--plain` turns off mounts, port forwards, the guest agent and SSH agent forwarding ([plain mode](https://github.com/lima-vm/lima/blob/f6158fe/website/content/en/docs/config/plain.md)); simstim then turns linger on itself, as on any VM.
- **Persistence.** The disk survives stop. There is no suspend, and snapshots are not implemented on vz ([vz_driver_darwin.go](https://github.com/lima-vm/lima/blob/f6158fe/pkg/driver/vz/vz_driver_darwin.go)). `limactl autostart enable` (Lima 2.2+) starts it at login ([autostart](https://github.com/lima-vm/lima/blob/f6158fe/website/content/en/docs/usage/autostart.md)).
- **Resources.** Defaults are 4 CPUs, 4 GiB (or half the host's memory if less) and 100 GiB of sparse disk. Lima does not seem to give guest memory back to macOS (**UNVERIFIED**), so stopping an idle VM is the real way to get RAM back.
- **Lifecycle** (shell out; there is no API):

  | Action | Command | Output |
  | --- | --- | --- |
  | Create | `limactl create --tty=false --name=<n> --plain --cpus=2 --memory=4 --disk=40 template:ubuntu-lts`, then `limactl start --tty=false <n>` | text |
  | Status | `limactl list --json <n>` | NDJSON, one object per line: `name`, `status` (`Running`, `Stopped`, `Broken`, `Installing`, `Uninitialized`, `""`), `sshConfigFile`, `sshLocalPort`, `cpus`, `memory`, `disk`, `vmType`, `arch` ([instance.go](https://github.com/lima-vm/lima/blob/f6158fe/pkg/store/instance.go), [lima_instance.go](https://github.com/lima-vm/lima/blob/f6158fe/pkg/limatype/lima_instance.go)) |
  | Stop, start | `limactl stop <n>`, `limactl start <n>` (start is a no-op when running) | |
  | Destroy | `limactl delete --force <n>` | |

- Whether a guest `poweroff` leaves the instance cleanly `Stopped`: **UNVERIFIED**.
- **Fit: 5 of 5.** Free, no account, macOS and Linux, and a real VM with systemd and sshd. `host create lima` runs the same code as a cloud VM: OpenSSH, upload, `uname -sm`, `systemd-run --user`, linger, and power-off for idle stop.

### 12.3 Docker and Podman

- **Stream.** `docker exec -i <c> cmd` without `-t`. The API stream is multiplexed with 8-byte headers, which the CLI or the Go client strips; stdin goes raw over the hijacked connection. It is binary-safe, with no CR/LF changes ([swagger.yaml](https://github.com/moby/moby/blob/ca90aff/api/swagger.yaml), [exec docs](https://github.com/docker/cli/blob/dfc520d/docs/reference/commandline/container_exec.md)). Podman's `exec -i` is the same ([interactive](https://github.com/containers/podman/blob/f15bf2b/docs/source/markdown/options/interactive.md)).
- **On disconnect** the daemon sends SIGKILL to the exec process only ([exec.go](https://github.com/moby/moby/blob/ca90aff/daemon/exec.go)). A `setsid` holder is re-parented to PID 1 and lives while the container runs (kernel behaviour; **UNVERIFIED** live). Run the container with `--init` and `sleep infinity`.
- **Inside.** No systemd by default, so this exercises the helper's `setsid` path. Podman's `--systemd=always` can run systemd as PID 1 ([systemd option](https://github.com/containers/podman/blob/f15bf2b/docs/source/markdown/options/systemd.md)).
- **bubblewrap is blocked** by Docker's default seccomp profile, which allows `unshare`, `setns` and `mount` only with `CAP_SYS_ADMIN` ([default.json](https://github.com/moby/moby/blob/ca90aff/vendor/github.com/moby/profiles/seccomp/default.json)). Agents' own sandboxes will not work without a custom profile.
- **Persistence.** The writable layer survives stop and start, and is lost on `rm`. `docker inspect` states: `created`, `running`, `paused`, `restarting`, `removing`, `exited`, `dead`.
- **Licence.** Docker Engine is Apache-2.0. Docker Desktop is free for small businesses (under 250 staff and $10 million revenue), personal use and education; larger firms pay ([licence](https://docs.docker.com/subscription/desktop-license/)).
- **Fit: 3 of 5** as a host, and the best test bed for an exec transport: one adapter, no account, and it runs in CI.

### 12.4 The others

- **Incus** (a Linux server only). `incus exec -T` gives one websocket per stream with no network path into the instance; system containers run systemd; there is a REST API on a local socket and a Go client ([instance-exec](https://github.com/lxc/incus/blob/574960c/doc/instance-exec.md)). On a Mac it needs Colima's `--runtime incus`.
- **Apple `container`** (macOS 26 on Apple silicon). Each container is its own small VM. `container machine` gives a persistent environment with its own init, and `container machine run` "boots it first if needed": wake on attach. Its defaults mount the home folder read-write, forward the SSH agent and grant all capabilities, so simstim would need `--home-mount none` ([container-machine.md](https://github.com/apple/container/blob/22029b0/docs/container-machine.md), [command reference](https://github.com/apple/container/blob/22029b0/docs/command-reference.md)).
- **OrbStack.** The best Mac experience: `ssh <machine>@orb` through a built-in server on loopback, with no sshd in the guest; systemd in machines; and "isolated machines" meant for agents. But it is macOS only, closed source, paid for work, and every machine shares one kernel ([machines](https://docs.orbstack.dev/machines/), [ssh](https://docs.orbstack.dev/machines/ssh), [isolated](https://docs.orbstack.dev/machines/isolated), [pricing](https://orbstack.dev/pricing)).
- **Multipass.** Real Ubuntu with systemd and JSON output, but Linux needs snap, the daemon runs as root and holds the SSH key, and the defaults are 1 CPU and 1 GiB ([constants.h](https://github.com/canonical/multipass/blob/ffe5ba7/include/multipass/constants.h)).
- **Colima** wraps Lima for container runtimes and adds an `Include` to `~/.ssh/config` by default ([app.go](https://github.com/abiosoft/colima/blob/49dfe87/app/app.go)). It adds nothing over Lima for simstim.
- **UTM.** `utmctl` drives the GUI app, has no create command, and its `exec` reads stdin to EOF before it runs anything ([UTMCtl.swift](https://github.com/utmapp/UTM/blob/7928d11/utmctl/UTMCtl.swift)). Not a fit.

## 13. Comparison

Prices are on-demand list prices fetched on 15 October 2026. "New port" means a port simstim would add on the host.

| Provider | Kind | How simstim connects | New port | What a stop keeps | Idle stop | Wake on attach | Hard limit | Running price, example | Free | Self-host | Fit |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| **E2B** | Firecracker microVM | envd exec stream (new Go client) | No | Disk, memory, processes | TTL lease; pause at timeout | `POST /v2/sandboxes/{id}/connect` | 1 h (Hobby) or 24 h (Pro) per run | 2 vCPU / 4 GiB ≈ $0.17/h | $100 once | Yes (Embed) | 4 |
| **Morph** | VM | System `ssh` via gateway | No | Disk, memory, processes | TTL lease; pause | `POST /resume`, or wake on SSH | None documented | 2 vCPU / 4 GB ≈ $0.10/h | No | No | 4 |
| Daytona | Container or VM | System `ssh` via gateway, token as user | No | Disk only (containers) | Built in, 15 min; SSH counts | `POST /start`, new token | 10 GiB disk (containers) | 2 vCPU / 4 GiB ≈ $0.17/h | $200 once | Old code only | 3 |
| Modal | gVisor or VM | gRPC exec (official Go SDK) | No | Nothing: no stop | `idle_timeout` terminates | New sandbox; sessions lost | 24 h | 1 core / 4 GiB ≈ $0.24/h | $30 a month | No | 2 |
| Fly Machines | Firecracker microVM | `flyctl ssh console` | No | Volume; root fs if set; memory on suspend (≤ 2 GB, best effort) | Self-suspend socket | `POST /start` + wait | None | shared-2x 4 GB ≈ $0.035/h | No | No | 3 |
| Coder | Template decides | System `ssh` + `coder ssh --stdio` | No | Template decides | Built in; SSH counts | Free: `coder ssh` starts it | Template decides | Your own infrastructure | Community | Yes (AGPL) | 4 with Coder, 2 without |
| Codespaces | Container in a VM | System `ssh` + `gh cs ssh --stdio` (hidden) | No | `/workspaces` and most folders | Built in, 5–240 min | Free: gh starts it | 12 h | 2 cores $0.18/h | 120 core-h a month | No | 2 |
| Hetzner | VM | System `ssh` | No | Everything, billed in full | None | `poweron` | None | CX23 €5.49 a month, always billed | No | No | 3 |
| GCE | VM | System `ssh` (IAP optional) | No | Disk; memory on suspend (API only) | Guest power-off; `maxRunDuration` cap | `start`, new IP | Suspend 60 days | e2-medium ≈ $24.46 a month + disk | not researched | No | 4 |
| EC2 | VM | System `ssh` (SSM or EICE optional) | No | EBS; memory on hibernate | Guest power-off | `StartInstances`, new IP | Hibernate 60 days | t4g.medium ≈ $24.53 a month + disk | not researched | No | 3 |
| **Lima** | Full VM | System `ssh` with Lima's config | No | Disk | Guest power-off or `limactl stop` | `limactl start` | None | Free | Free | Local | 5 |
| Docker | Container | `docker exec -i` | No | Writable layer | `docker stop` from the laptop | `docker start` | None | Free (Desktop licence limits) | Free | Local | 3 |
| Incus | System container or VM | `incus exec -T` | No | Disk; freeze | `incus stop` or `pause` | `incus start` | Linux only | Free | Free | Local | 3 |
| Apple `container` | VM per container | `container exec -i` | No | Machine storage | `machine stop` | `machine run` boots it | macOS 26, Apple silicon | Free | Free | Local | 3 |
| OrbStack | Shared-kernel VM | `ssh machine@orb` or `orb` | No | Disk | `orb stop` | `orb start` | macOS only | $8 per user per month for work | Personal use | Local | 3 |
| Multipass | VM | `multipass exec` or `ssh` | No | Disk; suspend | `multipass stop` | `multipass start` | None | Free | Free | Local | 3 |
| Colima, UTM | Lima VM; VM | Through Lima; no stream | No | Disk | — | — | — | Free | Free | Local | 2, 1 |

## 14. Recommendation

### 14.1 First

1. **Lima, over SSH.** It is the local provider that proves the whole of `host create` on a Mac or a Linux box with no account: create, bootstrap, connect, idle power-off, wake, destroy. It runs the cloud-VM code path unchanged (OpenSSH, upload, `uname -sm`, `systemd-run --user`, linger), so every later VM provider reuses it. Use `--plain` and a 40 GiB disk.
2. **E2B, over envd.** It is the first cloud. It needs no SSH and no new port; pause keeps running sessions and costs nothing while paused; the server is Apache-2.0 and can run on the user's own hardware through Embed; and the $100 Hobby credit pays for a live proof. It also forces the transport seam that Docker, Incus, Apple's `container` and Modal would use later. Known costs: a lease instead of an idle timer, the 1-hour run on Hobby, dropped connections at pause, a template build for memory, and a second transport to test.
3. **Docker, over `docker exec -i`, as the exec transport's local test bed.** It proves a non-SSH transport live, in CI and on any Mac with a Docker engine, before and without an E2B account. It also exercises the helper's `setsid` path where there is no systemd. Ship it marked experimental.

### 14.2 Next

4. **Morph.** SSH needs no new code, and pause keeps processes. It needs a lifecycle adapter and the same lease as E2B. Check first how long paused instances are kept, the default user, and whether SSH to a paused instance waits for the wake.
5. **GCE.** The best VM cloud for idle stop: a guest power-off with no credential, and `maxRunDuration` as a safety net. ADC is light in Go.
6. **Hetzner**, for create and destroy only. It is the cheapest always-on host. Idle stop as snapshot, delete and recreate can come later, if the restore time is acceptable.
7. **EC2**, after GCE. The same guest power-off design, with more parts.
8. **Coder, as a documented recipe**, not a provider. `coder config-ssh` plus the existing SSH path already gives connect and wake.

### 14.3 Later, or not

- **Daytona**: revisit its VM class, which can pause with memory, once SSH there is confirmed and the gateway forwards stderr.
- **Fly**: look at Sprites, not Machines.
- **Incus** (Linux), **Apple `container machine`** (once macOS 26 is common) and **OrbStack** (as an opt-in, because of its licence) are good second local providers.
- **Not recommended:** Modal (no stop, 24-hour cap), Codespaces (12-hour cap), UTM (no stream) and Colima (nothing over Lima).

## 15. Shapes recorded for fake-API tests

| Provider | Kind | Where |
| --- | --- | --- |
| E2B | REST JSON, plus a Connect stream for envd | 2.8 |
| Morph | REST JSON | 3 |
| Daytona | REST JSON | 4 |
| Modal | gRPC method and message names | 5 |
| Fly Machines | REST JSON | 6 |
| Coder | REST JSON | 7 |
| Codespaces | REST JSON | 8 |
| Hetzner | REST JSON (full OpenAPI 3.1 spec with examples) | 9 |
| GCE | REST JSON (discovery document with every enum) | 10 |
| EC2 | Query API with XML replies | 11 |
| Lima | `limactl list --json` NDJSON | 12.2 |
| Docker | `docker inspect` states; Engine API exec | 12.3 |

Other local JSON shapes, kept here for later: `multipass list --format json` gives `{"list": [{"name", "state", "ipv4", "release"}]}` ([json_formatter.cpp](https://github.com/canonical/multipass/blob/ffe5ba7/src/client/cli/formatter/json_formatter.cpp)); `incus list --format json` gives `status` and `status_code` (Running 103, Stopped 102, Frozen 110) ([status_code.go](https://github.com/lxc/incus/blob/574960c/shared/api/status_code.go)); Apple's `container machine inspect` gives `status` (`unknown`, `stopped`, `running`, `stopping`), `ipAddress` and `diskSize` ([MachineSnapshot.swift](https://github.com/apple/container/blob/22029b0/Sources/Services/MachineAPIService/Client/MachineSnapshot.swift)).

## 16. Open questions and UNVERIFIED items

E2B:

- Whether the public edge carries a long `StreamInput` client stream, and the real latency of one `SendInput` per chunk.
- Whether linger and a `systemd --user` manager work in an E2B VM, and whether bubblewrap works in its kernel.
- How agents cope when their open model request is cut by a pause.
- Which default resources the base template really has (the pricing page and the billing doc disagree).

Morph and Daytona:

- Morph: how long paused instances are kept; the default user; whether SSH to a paused instance waits; the hypervisor; whether plan caps are per instance.
- Daytona: whether the live gateway still drops stderr and checks the token only at the handshake; the user SSH lands as; SSH on VM sandboxes; whether Tier 1 and 2 outbound rules allow model APIs.

Others:

- Modal: whether a `setsid` process outlives its exec; the default user.
- Fly: where its SSH server lives; whether a `setsid` process outlives `fly ssh`; whether `/.fly/api` allows `/stop`.
- Coder and Codespaces: `systemd --user` in workspaces; whether output from a detached process counts as Codespaces activity.
- Hetzner: the status after a guest power-off; accepted key types; snapshot and restore times; whether host keys survive a restore.
- GCE: the stored-memory rate when suspended; the `hostkeys/` guest-attribute path; whether an OS Login policy overrides `enable-oslogin=FALSE`.
- EC2: whether `ImportKeyPair` rejects ECDSA and FIDO keys; whether a guest `systemctl hibernate` is reliable; the AMI lookup per region.
- Lima: boot time; whether a guest power-off leaves the instance `Stopped`; whether memory goes back to macOS.
- Docker: a `setsid` holder's survival after the exec client goes, tested live.
- All: boot-to-SSH times after a start or resume.

## Sources and method

Five research passes ran on 15 October 2026: one for E2B, and four in parallel for the other providers. They read docs, API references, pricing pages and public OpenAPI or discovery documents, and these repositories as shallow clones:

- e2b-dev/E2B@62a17df, e2b-dev/infra (now e2b-dev/runtime)@ef1b99a
- morph-labs/morph-python-sdk@d37f0a8
- daytonaio/daytona@ec4c21b and tag v0.190.0 (01c502b), daytona/clients@c46c4af
- modal-labs/modal-client@56fe5fa
- superfly/fly-go@d072ed8, superfly/flyctl@184af3a
- coder/coder@c85ebc1
- cli/cli@819f972, github/rest-api-description@507b88e
- hetznercloud/hcloud-go@7052653, hetznercloud/cli@7a7bba7
- golang/oauth2@c624b89, and one file of golang/crypto@cf169cd
- lima-vm/lima@f6158fe, abiosoft/colima@49dfe87, apple/container@22029b0, apple/containerization@fcc92dd, canonical/multipass@ffe5ba7, utmapp/UTM@7928d11, lxc/incus@574960c, docker/cli@dfc520d, moby/moby@ca90aff, containers/podman@f15bf2b

No account was made, no API key was used, no live or authenticated API call was made, and no VM or container was created. Cloned code was read, never built or run. Line numbers can drift, so most links point to a file at a pinned commit.
