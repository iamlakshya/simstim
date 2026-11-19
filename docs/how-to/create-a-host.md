# Create a host

`host create` makes a host and sets it up in one step. It starts a VM, a container or a sandbox, installs the helper, and connects. `host destroy` deletes it again. The design and its reasons are in [decision 0013](../contributing/decisions/0013-host-providers.md).

| Provider | What you get | How simstim reaches it | Account |
| --- | --- | --- | --- |
| `lima` | An Ubuntu LTS VM on your Mac or Linux machine | The system `ssh`, through Lima's own SSH config | None |
| `docker` | A container, for testing (experimental) | `docker exec -i` | None |
| `e2b` | An E2B sandbox in the cloud | E2B's own process API | An E2B account and API key |

Creating a host counts as your consent to install the helper on it. `host create` still prints what it installs and where. Nothing new listens on the host: the door is sshd, or the provider's own exec API.

## With the guided setup

`simstim setup` offers each provider when it asks where agents will run. It says whether this computer can use each one now: `limactl` or `docker` on your `PATH`, or an E2B key. If not, it shows the steps to install it, from the provider's own guide, and checks again. Then it makes the host, connects it, and offers the agent checklist and the logins ([install agents](install-agents.md#pick-them-from-a-checklist)). To skip the questions:

<!-- test: help (it makes a VM) -->
```sh
simstim setup --create lima --name dev --agents claude --yes
```

Press Ctrl+C while Lima or Docker makes the machine, and the setup deletes the half-made VM or container, so nothing is left behind. After that, the host is saved: `simstim host destroy dev` deletes it. The launcher's setup (`s`) does the same, and Esc stops it.

## A local VM with Lima

[Lima](https://lima-vm.io) runs Linux VMs on macOS and Linux. It is free and needs no account.

1. Install Lima: see [its install guide](https://lima-vm.io/docs/installation/).
2. Create the host:

   ```sh
   simstim host create lima dev
   ```

   It makes the VM `simstim-dev` with 2 CPUs, 4 GiB of memory and a 40 GiB disk that grows as it fills. Change them with `--cpus`, `--memory` and `--disk`. The VM is made with Lima's `--plain`, so it cannot see your home folder, and its ports are not forwarded to your machine.
3. It installs the helper, turns on linger, and sets up idle stop. The first time, with the image download, this took about a minute on an Apple silicon Mac.
4. Install your agents in the VM, from a shell there (`limactl shell simstim-dev`), then start a session: `simstim new dev --agent claude`.

simstim reaches the VM with the system `ssh` and Lima's generated `ssh.config`, which its own generated config includes. Your `~/.ssh/config` is never changed.

### Idle stop and wake

- The VM powers itself off after 30 minutes with no client connected and no session working. A systemd user timer runs the check every few minutes; no daemon is added.
- It needs passwordless sudo, which Lima gives. Without it there is no idle stop, and `host doctor` says so.
- Change the time with `simstim host idle dev 2h`, or turn it off with `simstim host idle dev never`.
- Attach to a session, or run `simstim host start dev`, and the VM boots again. Sessions come back after the restart, and each agent resumes its own conversation.
- The launcher does not start a stopped host when it opens. It shows the host as stopped instead.

## A container, for testing

The Docker provider is experimental. It proves the exec transport, and it runs in CI.

```sh
simstim host create docker test
```

It starts a container from `debian:stable-slim` (change it with `--image`; the image needs `sh`, `bash` and coreutils) with `--init`, running `sleep infinity`, and adds a user called `user`. simstim reaches it with `docker exec -i`: no SSH and no port.

What does not work in a container:

- There is no systemd, so sessions run as detached processes, without memory caps, and they are not restored after a restart.
- Without a unit for each session, a program in one session can still attach to, type into or stop another through its socket. `host doctor` reports the session sockets as weak ([security model](../explanation/security.md)).
- Docker's default seccomp profile blocks bubblewrap, so agents' own sandboxes fail.
- `forward` needs SSH, so it does not work.
- There is no idle stop. Stop the container with `simstim host stop test`.

## An E2B sandbox

[E2B](https://e2b.dev) runs each sandbox as a small VM. A paused sandbox keeps its memory, so sessions are frozen, not killed, and it costs nothing while paused.

1. **Your API key.** simstim looks for it in this order, when it needs it:
   1. `E2B_API_KEY` in your environment;
   2. the E2B CLI's own login (`e2b auth login`), which it reads and never changes;
   3. a command that prints it, which you give with `--token-command`, for example a read from your keychain.

   simstim never writes the key down, logs it, or sends it to a host.
2. **A template, once.** CPUs and memory belong to the template, and agents want 2 vCPUs and 4 GiB or more. Write `FROM e2bdev/base` into `e2b.Dockerfile`, then run:

   ```sh
   e2b template create simstim --cpu-count 2 --memory-mb 4096
   ```

   If the template is missing, `host create` says so and prints this command. Use another name with `--template`.
3. **The host:**

   ```sh
   simstim host create e2b cloud
   ```

   The sandbox pauses when its lease runs out, does not wake for stray traffic, and keeps its URLs private.

### The lease

- While any client is connected, it renews the sandbox's lease every minute: 60 minutes while a session is working, 15 when none is. When your laptop sleeps or closes, nobody renews it, and the sandbox pauses.
- Change the times with `idle_stop` and `busy_lease` in `hosts.toml` (see [the configuration](../reference/config.md)).
- Attaching to a session resumes a paused sandbox.
- The lease is not an idle timer. An agent still working when the lease runs out is frozen, and its open request to its model fails on resume. On E2B's free plan, every run also stops after one hour.

### Limits

- Keys travel in one request each by default. Set `stdin = "stream"` in the host's `[host.machine]` table to send them in one long request instead; this is not yet proved on E2B's servers.
- `forward` needs SSH, so it does not work.
- The E2B provider is built from E2B's public API spec and tested against recorded answers. No live E2B account was used to build it.

## Destroy a host

```sh
simstim host destroy dev
```

It asks first. It refuses while a session is working, unless you pass `--force`. It deletes the VM, the container or the sandbox, with its disk, then removes the host from `hosts.toml` and its generated SSH config.

## The commands

| Command | Does |
| --- | --- |
| `host create <provider> [name]` | Makes, bootstraps and connects a new host |
| `host start <name>` | Starts or resumes it |
| `host stop <name>` | Stops a VM or container, or pauses a sandbox |
| `host status <name>` | Asks the provider whether it runs |
| `host idle <name> <30m\|never>` | Sets the idle time |
| `host destroy <name>` | Deletes it and forgets it |

Every flag is in [the command reference](../reference/cli/host_create.md).
