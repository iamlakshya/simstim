# Create a host

`host create` makes a host and sets it up in one step. It starts a VM, a container or a sandbox, installs the helper, and connects. `host rm --destroy` deletes it again. The design and its reasons are in [decision 0013](../contributing/decisions/0013-host-providers.md).

| Provider | What you get | How simstim reaches it | Account |
| --- | --- | --- | --- |
| `lima` | An Ubuntu LTS VM on your Mac or Linux machine | The system `ssh`, through Lima's own SSH config | None |
| `docker` | A container, for testing (experimental) | `docker exec -i` | None |
| `e2b` | An E2B sandbox in the cloud | E2B's own process API | An E2B account and API key |

Creating a host counts as your consent to install the helper on it. `host create` still prints what it installs and where. Nothing new listens on the host: the door is sshd, or the provider's own exec API.

The helper's identity is checked during registration before sandbox preparation. A provider's successful creation reply alone does not mark the host as checked.

## A machine for one session

Use E2B when each session should have its own provider machine:

<!-- test: help (it makes a paid provider machine) -->
```sh
simstim new --machine e2b --name task --detach
```

The saved host is `m-task`, and the session is `m-task/task`. The helper, probe, runtime and sandbox run there. The client shows the exact root script and runs it through the provider with `sudo -n`; it never asks for a password. The machine has the `shared` tier. Its actual probe decides which wall can run. A wall below `gvisor` needs an explicit level and `--downgrade`.

Set the provider credentials and template in `config.toml`:

```toml
[machines.e2b]
token_command = "your-key-command"
template = ""
```

With no token command, the provider uses `E2B_API_KEY` or the E2B CLI login. An empty template uses the same default as `host create e2b`. Credentials stay on the laptop. The client copies no login file into the machine.

Session and classifier credentials must be marked `shared` and explicitly exposed. Named SSH keys use their separate host-only delivery and fresh shared-host question. They never enter the guest environment; their exposure to the host is recorded by name. A changed saved tier does not relax these requirements.

`--count` makes an independent machine for each session. A failed or cancelled start removes only its own machine. A machine that could not be removed stays marked `machine` in `host ls`; use `host rm <host> --destroy --confirm <host>` to retry. Removing its original session with `rm` destroys the machine and its disk. A stop or a detach keeps it. E2B may pause it with its memory, including any exposed secret, when its lease expires.

## With the guided setup

`simstim setup` offers each provider when it asks where agents will run. It says whether this computer can use each one now: `limactl` or `docker` on your `PATH`, or an E2B key. If not, it shows the steps to install it, from the provider's own guide, and checks again. Then it makes the host, connects it, and offers the agent checklist and the logins ([install agents](install-agents.md#pick-them-from-a-checklist)). To skip the questions:

<!-- test: help (it makes a VM) -->
```sh
simstim setup --create lima --name dev --agents claude --yes
```

Ctrl+C stops setup, and Esc stops the launcher's setup (`s`). If Lima or Docker creation may have begun, an interrupted or failed attempt can leave a machine behind. The error keeps the original resource and locator when they are known. Inspect that exact attempt with the provider's tools before retrying. After the host is saved, `simstim host rm dev --destroy` deletes it.

## A local VM with Lima

[Lima](https://lima-vm.io) runs Linux VMs on macOS and Linux. It is free and needs no account.

1. Install Lima 2.3.0 or later: see [its install guide](https://lima-vm.io/docs/installation/). `host create` checks the version with `limactl --version`, and refuses an older Lima.
2. Create the host:

   ```sh
   simstim host create lima dev
   ```

   It makes the VM `simstim-dev` with 2 CPUs, 4 GiB of memory and a 40 GiB disk that grows as it fills. Change them with `--cpus`, `--memory` and `--disk`. A size takes a unit, such as `--memory 8G` or `--disk 80G`. The VM is made with Lima's `--plain`, so it cannot see your home folder, and its ports are not forwarded to your machine.
3. It installs the helper, turns on linger, and sets up idle stop. The first time, with the image download, this took about a minute on an Apple silicon Mac.
4. Install your agents in the VM, from a shell there (`limactl shell simstim-dev`), then start a session: `simstim new dev --agent claude`.

simstim reaches the VM with the system `ssh` and Lima's generated `ssh.config`, which its own generated config includes. Your `~/.ssh/config` is never changed. Saved Lima lifecycle commands keep the original instance folder and `LIMA_HOME`. A different current `LIMA_HOME`, or a replacement instance at the same path, cannot select another VM.

### Your Mac's loopback services

Lima lets a VM reach the services on your Mac's loopback address, `127.0.0.1`, through the name `host.lima.internal`. Such a service can be a local database, a development server or a tool's local API. Without a change, an agent in the VM can connect to them too.

On a Mac, `host create lima` closes this path:

- It makes the VM with Apple's Virtualization framework (Lima's `vz`), and sets Lima's `networkOpts.hostLoopback` to false. The VM then cannot connect to your Mac's loopback services.
- Lima's `~/.lima/_config/override.yaml` wins over these settings. Before the VM first starts, `host create` checks them again. If the file keeps the path open, `host create` retains the new VM and tells you which settings to remove from the file. It does the same when `default.yaml` or `override.yaml` gives every VM a `user-v2` network, which Lima cannot run with the path closed, and it names that network.
- It also asks Lima for nested virtualisation, which later sandboxes need to run a VM inside the VM. macOS allows it on Apple M3 or later, with macOS 15 or later. On another Mac, Lima refuses it when the VM starts. `host create` then turns it off, starts the VM again, and says that the host gets no microvm sandboxes. If the VM still does not start, `host create` retains it and reports the original locator. If `override.yaml` sets `nestedVirtualization: true`, the change has no effect: `host create` retains the VM and tells you to remove that setting from the file.

On Linux, Lima runs the VM with QEMU, which cannot close the path. `host create` and `host doctor` warn you about this. Run untrusted work on a remote host instead.

`host doctor` checks the path of a Lima VM, on your computer, in its "laptop loopback" line. It warns for a VM that can still reach your loopback services, such as a VM made before this check. To close the path of such a VM, stop it, change the setting, and start it again:

<!-- test: skip (it changes a Lima VM) -->
```sh
limactl stop simstim-dev
limactl edit --set '.networkOpts.hostLoopback = false' simstim-dev
limactl start simstim-dev
```

This works only for a VM that uses `vz`. Lima refuses the setting for a VM on QEMU. On a Mac, replace a VM on QEMU with a new host, which uses `vz`:

<!-- test: skip (it deletes and makes a VM) -->
```sh
simstim host destroy dev
simstim host create lima dev
```

Two cases need another step, and `host doctor` names it in its Try line:

- If `~/.lima/_config/override.yaml` sets `networkOpts.hostLoopback: true`, that file wins over the VM's own setting, and the edit above does nothing. Remove the setting from the file, then stop and start the VM.
- A running VM keeps the network that it started with. If the VM's settings, or Lima's `default.yaml` or `override.yaml`, changed after the VM started, `host doctor` warns until you stop and start the VM.

### Idle stop and wake

- The VM powers itself off after 30 minutes with no client connected and no session working. A systemd user timer runs the check every few minutes; no daemon is added.
- It needs passwordless sudo, which Lima gives. Without it there is no idle stop, and `host doctor` says so.
- Change the time with `simstim host set dev idle=2h`, or turn it off with `simstim host set dev idle=never`.
- Attach to a session, or run `simstim host start dev`, and the VM boots again. Sessions come back after the restart, and each agent resumes its own conversation.
- The launcher does not start a stopped host when it opens. It shows the host as stopped instead.

## A container, for testing

The Docker provider is experimental. It proves the exec transport, and it runs in CI. Before starting a container, creation checks that its fixed original endpoint and configuration reach the same Docker engine under the later read-only inspection role. An endpoint that needs unavailable authentication or TLS settings stops before a container starts. Saved Docker actions use that same engine, pinned configuration and immutable container ID. Changing the current Docker context does not select a different machine. If the original container is gone, a later container with the same name stays untouched.

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

<!-- test: help (it deletes the VM) -->
```sh
simstim host rm dev
```

At a terminal, `host rm` says what the machine holds, such as `It holds 2 sessions.`, and asks you to type the host's name to delete the machine too. Press Enter to keep the machine: `host rm` then only forgets the host, and the machine stays for its provider's own tools. Any other text cancels, and nothing changes.

In a script, name the machine with `--destroy`, and confirm with the host's name:

<!-- test: help (it deletes the VM) -->
```sh
simstim host rm dev --destroy --confirm dev
```

It refuses while a session is working, unless you pass `--force`. It also refuses when the provider cannot tell the machine's state. It refuses, too, when the machine runs and it cannot list the sessions, because one of them may be working. After the typed name, it looks at the machine again, just before the delete. While `host rm` runs, its question included, no other command changes the host. A `host create`, `host set` or another `host rm` of that name fails at once, and says that the host is busy. A command that read the host before, such as `setup --host` or the launcher, then saves nothing, and says that the host changed. It deletes the VM, the container or the sandbox, with its disk, then removes the host from `hosts.toml` and its generated SSH config. If the delete or the forget fails, the host stays saved: run the command again. A machine that its provider no longer has counts as deleted.

`host destroy dev` still works until v0.4.0 and 11 November 2027, whichever comes later. It runs `host rm dev --destroy`, and warns. Its `-y` is now `--confirm dev`.

## The commands

| Command | Does |
| --- | --- |
| `host create <provider> [<name>]` | Makes, bootstraps and connects a new host |
| `host start <host>` | Starts or resumes it |
| `host stop <host>` | Stops a VM or container, or pauses a sandbox |
| `host status <host>` | Asks the provider whether it runs |
| `host show <host>` | Describes it: the machine, its idle time, and what the host says about itself |
| `host set <host> idle=<30m\|never>` | Sets the idle time |
| `host rm <host> --destroy` | Deletes it and forgets it |

Every flag is in [the command reference](../reference/cli/host_create.md).

## Microvm sandboxes in Lima

On an M3 or later Mac with macOS 15 or later, Lima's vz backend can expose
nested virtualisation. Host creation requests it and lets Lima perform the
actual hardware and system check. An older Mac can still create a Lima VM.
The client turns nesting off only after Lima reports that specific refusal.

The guest must still have Linux 6.8 or later, usable `/dev/kvm`, and the
verified runtime. Sandbox preparation performs the real boot check before a
floor is pinned. A Mac model or a requested setting is not proof of working
KVM. `host doctor` explains when an Apple silicon Lima guest has no KVM device.
