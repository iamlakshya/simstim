# Debug a session

The debug mode records why a session goes wrong, such as a lag while you type or an agent stopped at its memory cap. It writes files on the laptop and on the host. Nothing listens and nothing is uploaded. It is off by default. [Decision 0025](../contributing/decisions/0025-debug-mode.md) has its design.

## Turn it on for one command

Add `--debug` to any command, or to the launcher:

```sh
simstim --debug attach my-vps/auth-fix
```

`--debug` alone turns on every category. To turn on some only, list them in the `=` form:

```sh
simstim --debug=attach,net attach my-vps/auth-fix
```

A list binds only after `=`. So `--debug net` reads `net` as an argument, not as a category.

Before the command starts, the client prints the path of its debug file on stderr:

```text
debug on: ~/.local/state/simstim/debug/client-20261016-185712-4242.jsonl
```

`--debug-file <file>` names another file. On its own, it turns on every category. `SIMSTIM_DEBUG_FILE=<file>` does the same as `--debug-file`. When both are set, the flag wins. Debug that a flag turns on lasts as long as the command.

## The categories

| Category | What it records | Where |
| --- | --- | --- |
| `attach` | Each key, its echo, the holder's lock and its screen model | Laptop and host |
| `net` | Timed pings, each drop and reconnect, the shared SSH connection, ssh's own errors, and each request that the bridge serves | Laptop and host |
| `memory` | A sample of the session's memory each second. At an out-of-memory kill, the 20 largest processes by RSS, both at the memory warning before it and after it | Host |
| `launcher` | Each frame of the launcher: the time in `Update` and in `View` | Laptop |
| `runtime` | Go's runtime metrics each second, the flight recorder, and CPU and heap profiles | Laptop and host |

`runtime` costs the most. Leave it out when you do not need it: `--debug=attach,net,memory`.

## Turn it on in a running session

`debug start` turns on the debug of a session that already runs, from any terminal. The session does not restart:

```sh
simstim debug start my-vps/auth-fix
```

1. The holder records the host's categories: `attach`, `memory` and `runtime`. To record fewer, add `--only memory`, for example.
2. The holder records the keys of every attached client.
3. Debug stops by itself after 30 minutes. To keep it on longer, add `--for 8h`. The limit is 24 hours.
4. To stop it at once, run `simstim debug stop my-vps/auth-fix`.

A second `debug start` sets a new end. `simstim ls --json` shows the end as `debug_until`. After a reboot or a `resume`, the session's new holder starts with debug off. `resume` says so when debug was on: run `debug start` again to keep recording.

Debug uses some of the session's memory, which counts against its memory cap:

- `runtime` keeps the flight recorder's window, about 8 to 12 MiB on a busy session.
- The queue of records waiting for the disk can take about 2 MiB more.

So to chase a memory problem, such as [#40](https://github.com/iamlakshya/simstim/issues/40), leave `runtime` out: `simstim debug start my-vps/auth-fix --only memory`.

At each out-of-memory kill, the holder writes the largest processes left in the session to `holder.log`, with debug on or off. The process that the kernel stopped is gone by then. With `memory` on, the holder also notes the largest processes when the memory warning starts. At the kill, it writes that list too, which names the process stopped. Each line says whether it is from before the kill or after it.

The client in the first terminal records nothing on the laptop, because it did not start with `--debug`. To trace your keys end to end, detach, and attach again with `--debug`.

No key turns debug on while you are attached. Detach first, or use a second terminal.

## Save the seconds before a lag

While the session's `runtime` category is on, its holder keeps the newest 10 s or so of Go's execution trace. This is the flight recorder. Just after a lag, run this from a second terminal:

```sh
simstim debug save my-vps/auth-fix
```

The holder also saves the window by itself, at most once a minute:

- when a key waits more than 100 ms for the holder's lock
- when the holder's status tick comes more than 500 ms late

With `runtime` on, the client saves its own window when an echo takes 250 ms longer than the median of the pings.

A window saved by itself has `auto-` before `flight-` in its name. Each folder keeps the newest two of these. They never push out a file that you asked for.

## Write a profile

```sh
simstim debug profile my-vps/auth-fix cpu --for 1m
simstim debug profile my-vps/auth-fix heap
```

The holder writes a `cpu`, `heap` or `goroutine` profile, with debug on or off. A CPU profile runs for 30 s, or for the time that `--for` gives, up to 5 minutes. `--for` is for `cpu` only: the other two take a moment. The client shows the time left. If you stop the client, the holder still writes the file. While one profile or save runs, a second one in the same session fails, and says to wait.

## Where the files are

On the laptop, in `~/.local/state/simstim/debug/`, or in `$XDG_STATE_HOME/simstim/debug/` when that variable is set:

| File | What |
| --- | --- |
| `client-<time>-<pid>.jsonl` | Records and debug lines, unless `--debug-file` names another file |
| `client-<time>-<pid>-cpu.pprof`, `-heap.pprof` | The client's profiles, with `runtime` on |
| `client-<time>-<pid>-flight-<time>.trace` | The client's saved windows, and `-auto-flight-<time>.trace` for the ones it saved by itself |

On the host, in `~/.local/state/simstim/`, or under `$XDG_STATE_HOME` there:

| File | What |
| --- | --- |
| `sessions/<id>/debug.jsonl` | The holder's records and debug lines |
| `sessions/<id>/holder.log` | The holder's log, with the processes of the session at each out-of-memory kill |
| `sessions/<id>/debug/` | `cpu-<time>.pprof`, `heap-<time>.pprof`, `goroutine-<time>.pprof`, `flight-<time>.trace` and `auto-flight-<time>.trace` |
| `debug/bridge-<time>-<pid>.jsonl` | Each bridge's requests, with its profiles, while a client with debug on is connected |

`simstim ls --json` shows each session's `id`. Copy the files from a host that you reach with SSH, with the target that you gave to `host add`:

```sh
scp -r 'my-vps:.local/state/simstim/sessions/<id>/debug*' .
```

`scp` cannot reach a host made with `host create docker` or `e2b`, because the client reaches those through the provider's API. For a Docker host, copy the files out of its container with `docker cp`. The container's name is the machine's `id` in `~/.config/simstim/hosts.toml`. The sessions run as the user `user`, or as `root` when the container could not add that user:

```sh
docker cp <container>:/home/user/.local/state/simstim/sessions/<id>/debug.jsonl .
docker cp <container>:/home/user/.local/state/simstim/sessions/<id>/debug ./debug
```

A later release adds a command that collects the files over any kind of host.

## Read the files

- `go run ./test/keytrace` joins the records of the laptop and the host, and prints the time of each step of a key ([tracing typing lag](../contributing/debug-trace.md)).
- `go tool pprof` reads the profiles, and `go tool trace` the saved windows.
- A JSON lines file reads as it is, by a person or by an agent.

## Limits

- Each debug file turns over at 8 MiB to the same name with `.1` added. `holder.log` turns over at 1 MiB.
- A session's `debug/` folder keeps 32 MiB, and each debug folder 64 MiB. The oldest files go first, the windows saved by themselves before the rest. A file in use, or still being written, stays.
- A busy holder's saved window can be about 12 MiB, more than the 8 MiB that the recorder aims for.
- `kill` removes a session's files with its folder.
- With debug off, the client and the bridge discard their debug lines, and each hook costs one check.

## What it never records

- Key bytes, output bytes, screen text or peek data.
- Secret values, the session's environment, or yours.
- A process's command line: a record names a process by its short name only. `holder.log` names the agent's program and counts its arguments.
- The contents of memory: no heap dump and no core file.

Profiles and saved windows hold function and file names, counts, sizes and times. A client built without `-trimpath`, as `go install` builds it, writes your home folder's path into them. Every debug file has mode 0600, and every debug folder has mode 0700. Read the files before you attach them to an issue.

## An older host helper

When the helper on a host predates the debug mode, `debug start` says that it is too old. Update it with `simstim host add <host>`. A session that started before the update keeps its old holder, which has no debug mode. Debug works once that session starts again.
