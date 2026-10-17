---
status: accepted
date: 2026-10-16
---

# 0025 A debug mode: one switch, files on each side, and one local bundle

**Status:** Accepted. The owner asked on 16 October 2026 for the rest of [#44](https://github.com/iamlakshya/simstim/issues/44) to be built now, in the plan for #40 to #44, and accepted this record on 17 October 2026. It adds the debug mode to the Now list of [0018](0018-feature-set.md), as an item that the owner asked for. The rest of 0018 stays.

Part 1 of #44, a trace of typing lag, is built ([#50](https://github.com/iamlakshya/simstim/pull/50), [tracing typing lag](../debug-trace.md)). This record settles the rest: how a user turns debug on, what each side records, the files and their limits, and the bundle. Two pull requests build it, and each points here:

1. **The core:** the flag and its categories, `debug start` and `debug stop`, debug lines, and the limits, `holder.log`'s too. Also the snapshot at an out-of-memory kill, and the requests `debug.start`, `debug.stop`, `debug.profile` and `debug.save`, with the profiles and the flight recorder.
2. **The bundle and the launcher:** `debug bundle` with the requests `debug.collect` and `debug.read`, and its replacements. Also the launcher's commands, and the mark in its header.

Three smaller items go with the bugs they serve: the memory pressure in `host doctor` with #40, and the pings of `host test` and a Perfetto view of the records with #43.

## Context

When a session goes wrong, little records why. Two open bugs need data that the client and the host helper do not keep. One is an agent stopped at its memory cap ([#40](https://github.com/iamlakshya/simstim/issues/40)). The other is typing lag while attached ([#43](https://github.com/iamlakshya/simstim/issues/43)).

Part 1 records the path of each key, and the session's memory. With `SIMSTIM_DEBUG_FILE` set, the client and the holder write records of sizes and times to JSON lines files. Much is still missing:

- **No switch in a running session.** Only an environment variable turns debug on, and only for a new attach.
- **No debug lines.** The client writes no log, and the bridge has no logger. The holder logs at INFO only, and `holder.log` has no size limit.
- **No profiles.** No code uses `runtime/pprof` or `runtime/trace`.
- **No record of a kill.** At an out-of-memory kill, the holder sees only the `oom_kill` count. Nothing records what else held memory in the unit.
- **No way to collect.** A user copies the host's file by hand with `scp`. The client reaches a host made with `host create docker` or `e2b` through the provider's API, not SSH ([security](../../explanation/security.md), [0013](0013-host-providers.md)). So `scp` cannot reach it.

The project's rules bind the design:

- **No key while attached.** Only the detach and image-paste keys may be intercepted (AGENTS.md rule 2).
- **Nothing listens, nothing phones home.** [0003](0003-system-openssh.md) keeps it so: "nothing new listens on the host". This rules out Go's `net/http/pprof`, which serves profiles over HTTP. Nothing may be uploaded either.
- **Private details stay private.** A user may attach a bundle to a public issue. It must not carry their host names, addresses or user names.

Two tools show a good shape. Claude Code's `--debug` takes an optional filter, such as `--debug='mcp,startup'`, and "The filter binds only in the = form". Its `--debug-file <path>` "Implicitly enables debug mode" ([CLI reference](https://code.claude.com/docs/en/cli-reference)). Its `/debug` "starts capturing logs from that point forward" ([commands](https://code.claude.com/docs/en/commands)). HashiCorp's `consul debug` writes one archive "to the current directory", without tokens or keys. It recommends that the user "validate the contents of the archive" before sharing it ([consul debug](https://developer.hashicorp.com/consul/commands/debug)).

**The score.** The roadmap scores the rest of #44 at 0.7, as row O1 ([roadmap](../../roadmap.md#asked-for-by-the-owner)):

- **Demand 1.** No developer source in the sample asks for a debug mode, and the sample has no need code for memory or for lag ([developer needs](../../research/developer-needs.md#needs-ranked)). The scale gives a need without sources no demand. L1 and L2 get 1 because other tools sell them. Claude Code and Consul ship a debug mode, so it gets 1 too.
- **Impact 2.** A debug mode keeps no promise by itself. For a user who hits a bug, it turns a report from a guess into data. That is a clear gain, not the core promise.
- **Effort L.** Six new requests, debug lines in three processes, profiles, a flight recorder, the bundle and its replacements, the launcher's commands, and their tests. All this comes on top of part 1.

The score is 1 × 2 ÷ 3 = 0.7, so by its score the rest of #44 belongs in Later. Even if the needs behind the bugs counted, reliable-reconnect (14 sources) would give a demand of 3, and a score of 2.0. The rest of #44 is in Now only because the owner asked for it.

## Options

1. **Keep part 1 only.** No more code. But nothing starts debug in a running session, nothing makes a profile, and files leave the host only by hand.
2. **An HTTP endpoint for profiles** (`net/http/pprof`) on a local port or a Unix socket, reached through an SSH forward. It is Go's standard tool. But it listens, which 0003 and AGENTS.md rule 4 rule out.
3. **Signals.** `SIGUSR1` makes a holder write its profiles to a file. Nothing new listens. But a signal carries no arguments and gets no answer, and the client must find the process first.
4. **Requests over the existing channel,** behind a `debug` feature, with files on each side and one local bundle that the client builds.
5. **Always on.** Every session records all the time, so the data is there when a bug happens. But every user then pays in CPU and disk for the few who report a bug.

## Decision

Option 4. The bridge and the holder already answer requests, over the SSH channel and the holder's socket. So the debug mode needs no new door.

### Turning it on

- **`--debug[=<categories>]`** works on every command, and on the launcher. With no value, it turns on every category. As in Claude Code, a list binds only in the `=` form. So `--debug attach my-vps/app` turns on every category, then attaches.
- **`--debug-file <path>`** names the client's debug file. On its own, it turns on every category, as Claude Code's flag does.
- **`SIMSTIM_DEBUG_FILE`**, part 1's variable, stays as the same switch as `--debug-file`. When both are set, the flag wins. No other variable is added.
- **In a running session,** from any terminal: `debug start <host>/<session> [--for <duration>] [--only <categories>]`, and `debug stop <host>/<session>`. The session does not restart.
  - The holder records every host category (`attach`, `memory` and `runtime`), or those that `--only` names.
  - It records the `attach` category for every attached client. A client that did not ask for a trace gets an attach ID from the holder.
  - `debug stop` ends the session's debug at once, the host side of a traced attach too.
- **The client in the first terminal** still records nothing on the laptop, because its process did not start with debug. To trace its keys end to end, the user detaches and attaches again with `--debug`. Replay by offset makes this quick ([0004](0004-replay-by-offset.md)).
- **In the launcher:** four commands in the palette, with no default key: `debug: start`, `debug: stop`, `debug: save` and `debug: bundle`. A user can bind a key to each under `[keymap]`.
  - With a session selected, `debug: start` turns on the launcher's own debug and the session's, as `debug start` does. Each later attach from the launcher is then traced.
  - With a host selected, it turns on the launcher's own debug, and the debug of its connection to that host.
  - `debug: stop` ends both.
  - `debug: save` saves the flight recorder's window of the launcher, and of the selected session.
  - `debug: bundle` writes the bundle of the selected session or host, in the folder where the launcher started.
- **A mark in the header.** While the launcher's own debug is on, its header shows `debug` and the minutes left, such as `debug 24m`.
- **No key while attached.** To start debug in an attached session, detach first, or use a second terminal.

**A time limit.** Debug that outlives the command that started it stops by itself:

- `debug start` and the launcher's `debug: start` stop after 30 minutes. `--for` sets another time, up to 24 hours. That is long enough to wait for an out-of-memory kill during a day's work. A second `debug start` sets a new end.
- At the end, the holder stops by itself, with or without a client. `ls --json` shows the end as `debug_until`. After a reboot, a holder that starts again starts with debug off.
- Debug that a flag or the variable turns on lasts as long as its command. The host side of a traced attach lasts as long as the attach, as in part 1.
- A command other than the launcher then prints one line to stderr before it starts, such as `debug on: ~/.local/state/simstim/debug/client-20261016-185712-4242.jsonl`. It prints before an attach takes the terminal, so the attached bytes do not change. A variable left in a shell profile cannot go unseen.

### Categories

| Category | What it records | Where |
| --- | --- | --- |
| `attach` | Part 1: keys, echoes, the holder's lock and its screen model | Client, holder |
| `net` | Part 1's timed pings. Also each drop and reconnect, the state of the shared connection, and each request a bridge serves | Client, bridge |
| `memory` | Part 1's sample of the unit each second. Also every process of the unit at an out-of-memory kill | Holder |
| `launcher` | Each frame of the launcher: how long `Update` and `View` take | Client |
| `runtime` | Go's own view of each process: runtime metrics each second, the flight recorder, and CPU and heap profiles | Client, bridge, holder |

`runtime` is new. It costs the most, so a user can leave it out: `--debug=attach,net,memory`.

### What each side records

**Debug lines.** The client, the bridge and the holder log with `log/slog`. While debug is on, each line at DEBUG and above goes into the process's debug file as a record. The record's `ev` is `log`, with the line's `level`, and `text`: the line as slog's text handler writes it.

- A line belongs to a category, and the filter applies to it. Lines about debug itself, such as its start, its stop or a failed write, always go in.
- A line is about an event, such as an attach or a resize. Anything that happens for each key or each read of output is a record of its own, as in part 1.
- With debug off, the client and the bridge discard their lines (`slog.DiscardHandler`). The holder keeps its INFO log, `holder.log`, as now.

**The client:**

- `net`: each drop and reconnect, with its reason, the wait and the bytes replayed. This is also the data that [#10](https://github.com/iamlakshya/simstim/issues/10) wants to show.
- `net`: whether the master of the shared connection runs, from `ssh -O check` ([ssh](https://man.openbsd.org/ssh)). The client asks when debug starts, at each drop and at each reconnect. It records only yes or no, and the master's process ID.
- `net`: all of the bridge's stderr, where the client keeps only the first 8 KiB today. It holds ssh's own errors.
- `launcher`: a `frame` record for each frame, with the time in `Update` and in `View`, and the Go type of the message. Never the message's contents.

**The bridge.** A client whose debug is on sends `debug.start` on each of its connections. The bridge's debug then lasts as long as the connection, or until `debug.stop`. It records the name of each request and how long it took, each attach and detach, and the size of each upload.

**The holder.** At an out-of-memory kill, the holder reads every process in the unit's `cgroup.procs`:

- Its name, from `/proc/<pid>/comm`, and never its command line.
- Its RSS and PSS, from `/proc/<pid>/smaps_rollup`. The kernel's docs call that file "faster and more convenient" than `smaps` ([proc](https://docs.kernel.org/filesystems/proc.html)).
- The killed process is gone by then. The kernel's own report names it.

The holder writes the ten largest processes by PSS to `holder.log`, with debug on or off. With `memory` on, it also writes the whole list to `debug.jsonl`, as an `oom` record. The snapshot costs one read of a small file for each process, once per kill. Nobody can foresee a kill, so it does not wait for debug. It answers a question of #40: what else held memory in the unit.

**`runtime`, in every process:** each second, three of Go's runtime metrics ([runtime/metrics](https://pkg.go.dev/runtime/metrics)). They are the bytes of heap objects, and the pauses for garbage collection. The third is "the time goroutines have spent in the scheduler in a runnable state", which shows a holder that waits for a CPU.

**The kernel's report** of a kill goes only in a bundle. The bundle reads it only when the user can read the system journal. `journalctl -k` shows kernel messages. But "only root and users who are members of a few special groups" can read the system journal: `systemd-journal`, `adm` and `wheel` ([journalctl](https://www.freedesktop.org/software/systemd/man/latest/journalctl.html)). The bundle does not use `dmesg`, which needs `CAP_SYSLOG` when `dmesg_restrict` is 1 ([sysctl](https://docs.kernel.org/admin-guide/sysctl/kernel.html)). It keeps only the lines of a report that names the session's unit. Without that access, the unit's `memory.events` and its systemd result, `oom-kill`, must do.

**`ssh -v` goes only in a bundle, and only on request:** `debug bundle --ssh`. Its log holds names that the bundle cannot always find, such as a key's comment. The bundle then opens one new connection with `ssh -v -E <file>` and `-o ControlPath=none`, so it does not use or disturb the shared connection. With `BatchMode=yes`, ssh never asks for a password, and the bundle stops it after 15 s. The shared connection's master never runs with `-v`. It outlives debug, and `-E` appends "debug logs to log_file" with no size limit. A host reached through a provider's API has no SSH log.

### Profiles and the flight recorder over the existing channel

The bridge's hello lists a new feature, `debug`, as it lists `mkdir` (`moreFeatures` in `internal/host/bridge_ops.go`). `debug` implies part 1's feature, `trace`: a helper that lists `debug` lists `trace` too. `trace` keeps its meaning. An attach may carry an ID in its `trace` field, and the bridge then sends a `trace` frame before each key. `debug` adds the requests below, and the categories.

| Request | Served by | What it does |
| --- | --- | --- |
| `debug.start` | The bridge, or a holder | Turns on the debug of this bridge, or of a session, with its categories and its end |
| `debug.stop` | The bridge, or a holder | Turns it off |
| `debug.profile` | A holder | Writes a profile: `cpu` for a time (30 s by default, 5 minutes at most), `heap` or `goroutine` |
| `debug.save` | A holder | Writes the window of its flight recorder |
| `debug.collect` | The bridge | Gathers the host's part of a bundle, and lists the debug files it may send |
| `debug.read` | The bridge | Sends up to 1 MiB of one listed file, from an offset |

- The bridge passes a session's requests to its holder over the holder's socket, as it does `peek`.
- `debug.read` takes only a name from the host's own list, never a path. 1 MiB in base64 fits in one frame, which may be 4 MiB.
- A traced attach lists its host categories in a new field of the `attach` frame, `categories`. A holder from part 1 ignores the field, and records `attach` and `memory` as before.

**Long requests.** The bridge gives a holder 3 s to answer `peek`. A CPU profile takes up to 5 minutes, so for `debug.profile` the bridge waits for the profile's time plus 10 s. Each request runs in its own goroutine, as every request does now, so the wait holds up nothing else. The client shows the time left while it waits. If the client stops waiting, the holder still writes the file. A second profile or save in the same holder, while one runs, gets a `busy` error. Its hint says to wait for the first one.

**An older helper.** The client checks the feature before it sends a request. Without the feature, or on `unknown_op`, the client says that the helper is too old. Its hint is the usual one: update the helper with `host add`. A session that started before the update keeps its old holder, which answers `unknown_op` too. The client then says that debug works once the session starts again.

**CPU and heap profiles** come from `runtime/pprof`, as gzipped protocol buffers ([runtime/pprof](https://pkg.go.dev/runtime/pprof)):

- The holder writes one on request: `debug profile <host>/<session> cpu|heap|goroutine [--for <duration>]`.
- A bridge with `runtime` on profiles its CPU for its whole debug run. It writes a heap profile when the run ends.
- The client does the same with its own CPU and heap.

**The flight recorder** (`runtime/trace.FlightRecorder`, since Go 1.25) keeps a moving window of the newest execution trace ([runtime/trace](https://pkg.go.dev/runtime/trace)):

- **When it runs:** in the holder while the session's `runtime` category is on, and in the client while the client's is on. It does not run in the bridge. A bridge only relays, and part 1 already times its hop.
- **Its window:** `MinAge` 10 s, and `MaxBytes` 8 MiB. The Go team suggests a `MinAge` of "around 2x the time window of the event", and a lag lasts a few seconds ([blog](https://go.dev/blog/flight-recorder)). `MaxBytes` "takes precedence over MinAge", so a busy holder may keep less than 10 s.
- **Its size:** `MaxBytes` is "a hint", with no guarantee on the size that `WriteTo` writes. So a window is about 8 MiB, possibly more. The first pull request measures it on a busy session, and the limits below allow for it.
- **Its cost:** since Go 1.21, tracing costs 1 to 2 % of CPU for many programs, down from 10 to 20 % ([blog](https://go.dev/blog/execution-traces-2024)). The holder runs in the session's unit, so its buffers count toward the session's memory cap.
- **To save it by hand** just after a lag, run `debug save <host>/<session>` from a second terminal. Or detach, and use `debug: save` in the launcher. A detach takes well under 10 s.
- **It saves itself** when a key waits more than 100 ms for the holder's lock, `h.mu`. It also saves itself when the holder's status tick comes more than 500 ms late. A late tick means that the holder itself stalled, for example under memory pressure.
- **The client** saves its own window when an echo takes 250 ms longer than the median of the attach's pings. A slow link alone does not trigger it.
- Each process saves by itself at most once a minute. Each save is a file whose name ends in `flight-<time>.trace`, for `go tool trace`.
- Go allows "only one flight recorder" in a program at a time. Each process runs one at most.

**Nothing listens.** Profiles and saved windows are files, and the bridge reads them on request. There is no HTTP server and no new socket.

### The bundle

`debug bundle [<host> | <host>/<session>] [--ssh]` writes one local archive. Without an argument, it takes the laptop's side only. With a host, it adds every session on that host with debug files. With a session, it adds that session only.

- **Where:** the current folder, as `simstim-debug-<time>.tar.gz`, with mode 0600. Its name holds no host name. Each entry in it has mode 0600 too, so the files stay private when unpacked.
- **How it gets the host's files:** `debug.collect`, then `debug.read` for each file. This works over every transport, sshd or a provider's API.
- **A file outside the debug folder:** `--debug-file` and the variable can name a file anywhere, as part 1's own example does. Each time it opens such a file, the client adds its path to `outside.txt` in the debug folder. The bundle takes each listed file that still exists, with its `.1` file.
- **What it takes fresh:** a heap and a goroutine profile of each holder it covers. Also the output of `doctor` and `host doctor`, the unit's state from `systemctl --user show`, and the kernel's report. With `--ssh`, also one `ssh -v` log.
- **What it prints:** its path and size, each file with its size, and the counts from `replaced.txt`. Then: "Nothing was uploaded. Read the files before you attach them to an issue."
- **Its size:** GitHub takes a `.tgz` or `.gz` file of up to 25 MB ([attaching files](https://docs.github.com/en/get-started/writing-on-github/working-with-advanced-formatting/attaching-files)). Above that, the bundle says so, and names its largest files.
- **Nothing is uploaded.** The user decides where the archive goes.

| Path in the archive | What |
| --- | --- |
| `README.txt` | What each file is, and the tool that reads it |
| `replaced.txt` | Each kind of value that was replaced, and how many times. Never the values. |
| `laptop/` | The laptop's debug folder, the files in `outside.txt`, and the output of `doctor` |
| `host/doctor.json` | The output of `host doctor` |
| `host/ssh.log` | With `--ssh`: `ssh -v` of one new connection |
| `host/bridge/` | The bridges' debug files |
| `host/sessions/<id>/` | `debug.jsonl`, `holder.log`, the unit's state, the kernel's report, the profiles and the saved windows |

**What it replaces.** Before a file goes into the archive, the client replaces these values with placeholders. One value gets the same placeholder in every file.

| Value | Found from | Placeholder |
| --- | --- | --- |
| Host names | Each host's name and SSH target in `hosts.toml`. The host's own name, from `host doctor`. Any `*.ts.net` name. With `--ssh`, the `hostname` and `proxyjump` hosts of `ssh -G` | `<host-1>` |
| The laptop's name | `os.Hostname` | `<laptop>` |
| Addresses | Every IPv4 and IPv6 address that Go's `net.ParseIP` accepts, but not loopback | `<addr-1>` |
| User names | The user on the laptop, and on the host, from `host doctor`. With `--ssh`, the `user` of `ssh -G` | `<user>` |
| Home folders | `$HOME` on each side, and any `/Users/<name>` or `/home/<name>` | `~` |
| Session names and folders | `ls` | `<session-1>`, `~/<folder-1>` |
| Email addresses | The pattern in `scripts/check-private.sh` | `<email>` |
| SSH keys | `SHA256:` and 43 base64 characters, as `ssh -v` prints a fingerprint. With `--ssh`, the `identityfile` names of `ssh -G`, and the text before the key type where `ssh -v` names a key that it offers | `<fingerprint>`, `<key-1>` |
| Alert tokens, URLs and header values | `alerts.toml` on the host | `(hidden)`, as `alerts.Redact` shows them |

`alerts.Redact` works on the parsed alerts file, not on text. So the host reads the values from `alerts.toml`, and replaces them in each file before `debug.read` sends it. They never leave the host. The client replaces the rest.

Session names, folders, the laptop's name and keys go beyond the list in #44. A folder such as `~/src/<customer>` can name a customer. A key's fingerprint or comment can name its owner.

**How it matches.** A plain search and replace would break the bundle. E2B's user is `user`, cloud hosts use `ubuntu` or `root`, and a session called `api` would change `internal/api` in every stack. So:

- **Whole paths:** a home folder only at the start of a path, followed by `/` or the end of the path. A session's folder only as its whole path.
- **Whole tokens:** any other name only where a space, a quote, `@`, `:`, `=`, `,` or a bracket stands on each side. So the session `api` matches in `session=api`, but not in `internal/api`.
- **Generic names stay:** the users that `scripts/check-private.sh` allows (such as `user`, `ubuntu`, `admin` and `dev`), `root`, and any name under 3 characters. They name nobody.
- **Profiles:** the bundle parses each one, and rewrites only two kinds of strings: the file name of each mapping, and the file name of each function. Both are paths, and in both it replaces only the home folder. Function names, such as `internal/api.Secret`, stay as they are.
- **The parser** is `github.com/google/pprof/profile`, the pprof project's own (Apache-2.0). That is a new dependency of the client only, because the client builds the bundle. To avoid it, a small reader of the profile's `string_table` and those two fields would do ([profile.proto](https://github.com/google/pprof/blob/main/proto/profile.proto)).
- **Saved windows** stay as they are. A release build uses `-trimpath`, so its windows hold no home folder. A client built without it, as `go install` builds it, writes absolute source paths into its windows.
- **A last check:** after the replacements, the bundle checks each file again, with the same rules. It gunzips each profile first. In a saved window, which it does not parse, it looks for the home folders as bytes. It leaves out a file that still matches, and says so.

The replacement is a best effort. A log line can hold a name that the client does not know, such as a repository outside the session's folder. So the bundle asks the user to read it first, as `consul debug` does.

### Privacy

The debug mode never records these (#44, section 5):

- Key bytes, output bytes, screen snapshots or peek data.
- Secret values, the manifest's `env`, or the environment.
- Clipboard images, or the files in `paste/`.
- Alert tokens and URLs, or the agent's questions.

This record adds two:

- A process's command line, the agent's arguments too. A record names a process by its `/proc/<pid>/comm` only.
- The contents of memory: no heap dump, and no core file.

**Debug lines** go through slog's text handler. For a value with no text form of its own, the handler writes "the result of fmt.Sprint" ([log/slog](https://pkg.go.dev/log/slog)). So an `api.Secret` prints as `[redacted]`. A JSON handler would marshal the value and print it (`internal/api/secret.go`). The text in a `log` record comes from the text handler, so this holds inside a JSON lines file too.

**Profiles and traces hold no secrets.** They hold stacks, function and file names, counts, sizes and times. They do not hold the contents of memory:

- A heap profile is "a sampling of memory allocations of live objects", counted by the stack that made each one ([runtime/pprof](https://pkg.go.dev/runtime/pprof)). A CPU profile holds stacks and how often each ran.
- The execution tracer collects "Scheduling, syscall, garbage collections, heap size, and other events" ([diagnostics](https://go.dev/doc/diagnostics)).
- A heap dump is different. `runtime/debug.WriteHeapDump` writes "a description of the heap and the objects in it" ([runtime/debug](https://pkg.go.dev/runtime/debug)). Claude Code's `/heapdump` snapshot "contains your full conversation and credentials" ([commands](https://code.claude.com/docs/en/commands)). The debug mode never calls `WriteHeapDump`.
- Profile labels never carry user data, such as a session's name.

A test on 16 October 2026 checked this with Go 1.26.9, the version in `go.mod`. A program kept a marker string in its heap and in its environment. It wrote a heap, a CPU and a goroutine profile, and a flight recorder window. It ran once built as a release is (`-trimpath`), and once built as `go install` builds:

- No file held the marker in either build.
- In the release build, each profile held one absolute path: the binary's. The window held none.
- In the other build, the profiles and the window also held the absolute path of each source file, the home folder too.

**A test with a canary.** Each of the two pull requests adds a test that turns on every category. A marker sits in a secret, the environment, the keys and the output. No debug file and no bundle may hold it. Part 1's host test already checks that a trace holds no key bytes.

**Files are private.** Every debug file has mode 0600, and every debug folder has mode 0700.

### Limits

| File | Side | Limit |
| --- | --- | --- |
| The client's debug file | Laptop | 8 MiB, then one old file with `.1` added (part 1) |
| A session's `debug.jsonl` | Host | 8 MiB, then `.1` (part 1) |
| A bridge's debug file | Host | 8 MiB, then `.1` |
| `holder.log` | Host | 1 MiB, then `.1`, as the alert delivery log (`internal/alerts/store.go`) |
| A session's `debug/` folder | Host | 32 MiB in all |
| The debug folder | Laptop and host | 64 MiB in all |

- **Turnover of folders.** When a folder passes its limit, its oldest files go first, at each start and each save. A file in use is never removed. A session's files go with its folder when `kill` removes the session.
- **The holder's lock.** Debug code adds no work inside `h.mu` beyond part 1's clock reads. Debug lines, the snapshot at a kill, profiles, saves and metrics all run outside the lock. Records still go through part 1's queue, which drops a record rather than wait.
- **Byte-exact.** The attach tests, in Go and end to end, run again with `--debug` and every category on. The bytes on the terminal must match a run without debug. The line on stderr and the mark in the header show only outside an attach. [Testing](../testing.md) asks for this test for any change to the attach path.
- **With debug off,** each hook costs a nil check, and each debug line a call to a handler that discards it. The only other work is the snapshot at a kill.

### Folders and names

On the laptop, in the debug folder, `~/.local/state/simstim/debug/` (under `$XDG_STATE_HOME` when it is set):

| File | What |
| --- | --- |
| `client-<time>-<pid>.jsonl` | Records and debug lines, unless `--debug-file` names another file |
| `client-<time>-<pid>-cpu.pprof`, `client-<time>-<pid>-heap.pprof` | The client's profiles |
| `client-<time>-<pid>-flight-<time>.trace` | The client's saved windows |
| `outside.txt` | The paths of debug files outside this folder |

On the host, in `~/.local/state/simstim/`:

| File | What |
| --- | --- |
| `sessions/<id>/debug.jsonl` | The holder's records and debug lines (part 1's name) |
| `sessions/<id>/holder.log` | As now, with a limit and the snapshot at a kill |
| `sessions/<id>/debug/` | `cpu-<time>.pprof`, `heap-<time>.pprof`, `goroutine-<time>.pprof`, `flight-<time>.trace` |
| `debug/` | The debug folder, for the bridges: `bridge-<time>-<pid>.jsonl`, with `-cpu.pprof` and `-heap.pprof` |

`<time>` is the local time, such as `20261016-185712`.

The names in one place:

| Name | Kind |
| --- | --- |
| `--debug[=<categories>]`, `--debug-file <path>` | Flags of every command |
| `SIMSTIM_DEBUG_FILE` | Part 1's variable, kept |
| `attach`, `net`, `memory`, `launcher`, `runtime` | Categories |
| `debug start`, `debug stop`, `debug save`, `debug profile`, `debug bundle` | Commands. Each takes `<host>/<session>`, and `debug bundle` also a host. |
| `--for <duration>` | A flag of `debug start` and `debug profile` |
| `--only <categories>` | A flag of `debug start` |
| `--ssh` | A flag of `debug bundle` |
| `debug: start`, `debug: stop`, `debug: save`, `debug: bundle` | Launcher commands |
| `trace` (part 1), `debug` | The bridge's features. `debug` implies `trace`. |
| `trace` (part 1), `categories` | Fields of the `attach` frame: the attach's ID, and its host categories |
| `trace` (part 1) | The frame that the bridge sends before each key of a traced attach |
| `debug.start`, `debug.stop`, `debug.profile`, `debug.save`, `debug.collect`, `debug.read` | Requests |
| `debug_until` | A field of a session in `ls --json` |
| `log`, `frame`, `reconnect`, `master`, `request`, `oom`, `metrics`, `save`, `stop` | New values of a record's `ev` |

### Reading the files

- `test/keytrace` from part 1 reads the records, `go tool pprof` the profiles, and `go tool trace` the saved windows.
- A person or an agent can read a JSON lines file as it is, as Claude Code's `/debug` reads its own log.
- With #43, `keytrace` can gain a flag, `-trace-events <file>`, that writes the Chrome trace event format. Each key would be a flow from the client to the host and back. Perfetto shows flow events "as connecting arrows between slices" ([formats](https://perfetto.dev/docs/getting-started/other-formats)). Its UI "runs fully locally, in-browser" ([Perfetto](https://perfetto.dev/docs/getting-started/system-tracing)).

### `host doctor` without debug

The issue, #44, asks whether `host doctor` should show memory pressure and the spread of the round trip without debug on. This record answers, and the bugs build the answers:

- **Memory pressure: yes, with #40.** For each running session, `host doctor` shows `avg60` of the `some` and `full` lines in the unit's `memory.pressure` ([PSI](https://docs.kernel.org/accounting/psi.html)). The kernel keeps these averages anyway, so showing them records nothing and needs no switch. It warns when `full avg60` is 1 % or more, a first threshold to tune with data from the debug mode.
- **The round trip: not in `host doctor`,** which checks a host's setup. With #43, `host test` takes 20 timed pings, 100 ms apart, in place of one round trip. It shows the median and the largest. The launcher's round trip is part of #43 too.

### Not in this record

- The fixes for #40 and #43. With debug off, the debug mode changes two things on the host: the snapshot at a kill, and the size limit of `holder.log`. It changes nothing else that a session does.
- A command that removes debug files. The limits bound them, and `kill` removes a session's files.

## Consequences

- Good, because a user can turn debug on in a running session, with no restart and no key while attached.
- Good, because one bundle replaces the outputs that a user copies by hand. It works over every transport, the providers' APIs too.
- Good, because nothing new listens and nothing is uploaded. Profiles and files travel over the channel that already exists.
- Good, because the flight recorder keeps the seconds before a lag, and saves them by itself.
- Good, because the snapshot at a kill shows what held the memory, even with debug off (#40).
- Bad, because the protocol grows: a feature, six requests to the bridge, four to the holder, and a field in `attach`. Each must work with older and newer helpers, and each needs tests.
- Bad, because there is much more code to keep, in two pull requests on top of part 1's 2,500 lines. That is a real cost for a feature that scores 0.7.
- Bad, because the client gets a new dependency to parse profiles, unless it uses a small reader of its own.
- Bad, because the replacements are a best effort. They catch the values that the client knows, in whole tokens and paths. The user must still read the bundle.
- Bad, because a bundle has no `ssh -v` log unless the user asks for it with `--ssh`.
- Bad, because a client built without `-trimpath` writes the home folder into its saved windows, and the bundle then leaves them out.
- Bad, because `runtime` costs 1 to 2 % of CPU while it runs. The flight recorder's buffers, about 8 MiB or more, count toward the session's memory cap.
- Bad, because the holder does a little more with debug off. At each out-of-memory kill, it reads each process's `smaps_rollup` once.
- Bad, because the kernel's report needs a group that most users are not in. Without it, only the unit's own counters show the kill.
- Bad, because a session that started before the helper's update has no debug mode until it starts again.
- Bad, because a list after `--debug` binds only in the `=` form, and `--debug net` reads `net` as an argument. Claude Code has the same rule, and the help says so.
- Bad, because 30 minutes can end debug before a slow out-of-memory kill. `--for` raises the limit to 24 hours.
