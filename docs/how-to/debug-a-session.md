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

The launcher prints no such line. Its header shows `debug` instead, and a notice names the file.

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

## Debug from the launcher

The launcher's palette, `ctrl+o`, has four debug commands. They have no key, but you can bind one under `[keymap]` in `config.toml`:

| Command | What it does |
| --- | --- |
| `debug: start` | With a session selected, turns on that session's debug, as `debug start` does. With a host selected, turns on the debug of the launcher's connection to that host. Once that works, it turns on the launcher's own debug too, with every category. |
| `debug: stop` | Turns off the launcher's debug, and the debug that `debug: start` turned on. When `debug: start` turned nothing on, it turns off the selected session's debug, or that of the connection to the selected host. |
| `debug: save` | Saves the flight recorder's window of the launcher, and of the selected session |
| `debug: bundle` | Writes the bundle of the selected session or host, in the folder where the launcher started. One bundle is written at a time. |

While the launcher's own debug is on, its header shows `debug` and the minutes left, such as `debug 24m`. Each attach from the launcher is then traced. What `debug: start` turns on stops after 30 minutes, and a second `debug: start` sets a new end. With `--debug`, the launcher's own debug lasts as long as the launcher, and the mark shows no minutes. The debug that `debug: start` turns on for a session or a connection still stops after 30 minutes.

While a bundle is being written, the header says `bundling…`. A notice then gives the archive's path. If you quit the launcher first, the bundle stops, and leaves no file.

## Save the seconds before a lag

While the session's `runtime` category is on, its holder keeps the newest 10 s or so of Go's execution trace. This is the flight recorder. Just after a lag, run this from a second terminal:

```sh
simstim debug save my-vps/auth-fix
```

Or detach, and run `debug: save` in the launcher. A detach takes well under the 10 s that the window keeps.

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

`simstim ls --json` shows each session's `id`.

## Collect the files in a bundle

`debug bundle` collects the debug files into one archive in the current folder. It works over every kind of host, one made with `host create docker` or `e2b` too:

```sh
simstim debug bundle my-vps/auth-fix
```

- Without an argument, it takes the laptop's side only. With a host, it adds every session on that host that has debug files. With a session, it adds that session only.
- It also takes fresh heap and goroutine profiles of each session's holder, and the output of `doctor` and `host doctor`.
- For each session, it takes the state of its systemd unit and the kernel's report of an out-of-memory kill. The kernel's report needs a user in the `systemd-journal`, `adm` or `wheel` group. Without one, the unit's state still shows the kill.
- `--ssh` adds the `ssh -v` log of one new connection to the host. That connection does not use the shared one, and never asks for a password.
- It writes `simstim-debug-<time>.tar.gz` with mode 0600, and each file in it has mode 0600 too. The name holds no host name. Until the archive is complete, its name ends in `.part`. Ctrl+C stops the bundle, and it leaves no file.
- It prints the archive's path and size, each file and its size, and the counts of what it replaced. Above 25 MB, the most that GitHub takes for one file, it says so and names its largest files.

Nothing is uploaded. You decide where the archive goes.

### What the bundle replaces

Before a file goes into the archive, the bundle replaces the private values that it knows with placeholders. One value has the same placeholder in every file. `replaced.txt` in the archive counts each kind, and never holds the values.

| Value | Placeholder |
| --- | --- |
| Host names: each host's name and SSH target in `hosts.toml`, what `ssh -G` says of it, the host's own name, and any `*.ts.net` name | `<host-1>` |
| The laptop's name | `<laptop>` |
| Every IPv4 and IPv6 address, but loopback | `<addr-1>` |
| User names, on the laptop and on the host | `<user>` |
| Home folders on both sides, and any `/Users/<name>` or `/home/<name>` | `~` |
| Session names and folders | `<session-1>`, `~/<folder-1>` |
| Email addresses | `<email>` |
| SSH keys: fingerprints, identity files, and the names that `ssh -v` gives keys | `<fingerprint>`, `<key-1>` |
| The tokens, addresses, header values and commands of the host's alerts file, and the body of each HTTP error that an alert met | `(hidden)` |

The host replaces the values of its alerts file itself, so they never leave it. The client replaces the rest. In the `ssh -v` log, the bundle leaves out each line that can hold an environment variable's value or a proxy's command. It also replaces the path of each SSH config file and each `known_hosts` file, but the usual ones in `~/.ssh` and `/etc/ssh`, and the client's own. A file kept in a project's folder names the project.

The bundle matches whole names and whole paths. So a session called `api` changes `session=api`, but not `internal/api`. A host's, a user's or the laptop's name is replaced inside a longer name too, as in `prod-my-vps` or `my-vps.internal`. A session's folder, or a key's path, is replaced where it starts a longer name, as in `~/src/app-old` or `~/.ssh/work-cert`.

Some names stay, because they name nobody:

- these generic names: `me`, `you`, `u`, `dev`, `user`, `admin`, `root`, `runner`, `shared`, `ubuntu`, `debian`, `vscode`, `node`, `linuxbrew` and `localhost`
- the names of systems and processors, as Go's file names hold them: `linux`, `darwin`, `windows`, `freebsd`, `unix`, `amd64` and `arm64`
- any name under 3 characters
- the words of the records' own format, such as `net`, `attach`, `host` or `heap`
- the product's own name, the names of its programs, and the words of its commands
- OpenSSH's names that end in `@openssh.com`, and systemd's unit names, which hold an `@`

So if your user, a host or a session has one of these names, it stays in the bundle. Check for it before you share the bundle.

In a profile, the bundle replaces the home folder in file names only. Function names stay as they are. Saved windows stay as they are too.

After the replacements, the bundle searches each file again for each value that it knows, in any case. It leaves out a file that still holds one, and says why. `replaced.txt` is checked too. These values pass the check:

- a session's name next to a slash or a hyphen, as `api` in `internal/api`, since such names are often common words
- another name, a home folder, a session's folder or a key's path, only inside a longer word of letters and digits
- in a profile's file names, a host's name as a whole folder of the path, as a Go package's name can be. A user's or the laptop's name there keeps the profile out, unless letters or digits touch it.

So a profile of a program built without `-trimpath` can keep a host's name, when the host has the same name as a folder of the program's source, as in `/opt/<host>/x.go`. A release is built with `-trimpath`, so its profiles hold no such path. Otherwise a host called like a Go package, such as `api`, would keep every profile out of the bundle.

For example, a window that a client built without `-trimpath` saved holds your home folder, so the bundle leaves it out.

From a debug folder, the bundle takes only the files that the debug mode named, and never a link. A debug file outside the folder goes in as `laptop/outside/1.jsonl` and so on, since its own name can name a project. It goes in only if each whole line is a record of the debug mode: `--debug-file` adds records to any file, a file of your own too. What the bundle prints names things as you know them. The replacements are in the archive only.

The replacement is a best effort. A log line can hold a name that the bundle does not know, such as a repository outside the session's folder. Read the files before you attach them to an issue.

### Copy the files by hand

To copy a host's files without the bundle, use the target that you gave to `host add`:

```sh
scp -r 'my-vps:.local/state/simstim/sessions/<id>/debug*' .
```

`scp` cannot reach a host made with `host create docker` or `e2b`, because the client reaches those through the provider's API. For a Docker host, use `docker cp` with the machine's `id` in `~/.config/simstim/hosts.toml`. The sessions run as the user `user`, or as `root` when the container could not add that user:

```sh
docker cp <container>:/home/user/.local/state/simstim/sessions/<id>/debug.jsonl .
```

These copies replace nothing.

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

When the helper on a host predates the debug mode, `debug start` says that it is too old. `debug bundle` says the same of a helper without the bundle. Update it with `simstim host add <host>`. A session that started before the update keeps its old holder, which has no debug mode. Debug works once that session starts again.
