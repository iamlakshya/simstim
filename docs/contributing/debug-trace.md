# Tracing typing lag

The debug trace measures where a typed key waits on its way to the agent and back. It records times and sizes on the laptop and on the host, so that you can find the cause of a lag such as [#43](https://github.com/iamlakshya/simstim/issues/43). It is the first part of the debug mode in [#44](https://github.com/iamlakshya/simstim/issues/44). It is off by default.

## Turn it on

1. Set `SIMSTIM_DEBUG_FILE` to a file on the laptop, and attach:

   ```sh
   SIMSTIM_DEBUG_FILE=$HOME/key-trace.jsonl simstim attach my-vps/auth-fix
   ```

   The launcher reads the variable too: start `simstim` with it set, and each attach from the launcher writes to the file.
2. Type in the session while the agent works, until the lag shows.
3. Detach.

While the trace is on:

- The client writes its records to the file.
- The client asks the host to trace its side of this attach. The host helper writes its records to the session's folder.
- The client pings the host every second, not every 5 s, to show the spread of the round trip.
- The host samples the session's memory every second.

Both sides stop when you detach. There is no flag yet. The variable's name follows the `--debug-file` flag that #44 proposes.

## Where the files are

| Side | File |
| --- | --- |
| Laptop | The file that `SIMSTIM_DEBUG_FILE` names |
| Host | `~/.local/state/simstim/sessions/<id>/debug.jsonl` |

`simstim ls --json` shows each session's `id`. Each file turns over at 8 MiB to the same name with `.1` added. So the two files together keep the newest 8 to 16 MiB. A busy agent fills 8 MiB in a few minutes.

## Copy the host's file

Copy the file with the SSH target that you gave to `host add`. Copy the old file too, if it exists:

```sh
scp 'my-vps:.local/state/simstim/sessions/<id>/debug.jsonl*' .
```

## Read the results

`test/keytrace` joins the two files by key and prints percentiles for each step. Give it the client's file first, then the host's files, oldest first:

```sh
go run ./test/keytrace ~/key-trace.jsonl debug.jsonl.1 debug.jsonl
```

It prints one line for each step, with the count, p50, p90, p99 and the largest value, in milliseconds:

| Step | What it measures |
| --- | --- |
| client: stdin read to key sent | The client's own delay: the input filter, a held Esc, or an upload that blocks the attach loop |
| client: key sent to its echo arrived | From the key's write to the SSH channel to the first output after it |
| client: echo arrived to written | The write to the local terminal |
| total: stdin read to echo written | The whole echo, as you see it |
| host: bridge read to holder read | The hop from the bridge to the holder's socket |
| host: wait for the lock in input() | The holder's lock, before the key's PTY write |
| host: holder read to PTY write end | The lock wait and the PTY write together |
| host: PTY write end to next PTY read | The agent's reply: it reads the key, draws, and writes |
| network: round trip of a key, without the host | The key's round trip minus the time on the host. It needs no clock offset. |
| network: key sent to bridge read (clock offset) | The way up |
| network: next PTY read to echo arrived (clock offset) | The way down, with the holder's output path |
| host: output() wait for the lock, holds the lock | The holder's lock for each read of the agent's output |
| host: screen model feed | The Go screen model, inside the lock |
| host: tick() wait for the lock, holds the lock | The holder's status tick, once a second |
| net: ping round trip | The timed pings on the attach channel |

The last lines show the clock offset of each attach. Then they show the session's memory: the largest use, and the worst second of memory stalls and major faults.

### How to read them

These rules come from #43:

- **The lock wait is large** (`wait for the lock in input()`, or `output() holds the lock`): the cause is the holder's lock, `h.mu`.
- **The time from the PTY write to the next read is large, and the memory stalls are high:** the cause is memory. The kernel frees the agent's code near the cap, and reads it back from the disk.
- **The same lag shows over plain `ssh -t`** to the same agent: the cause is the agent or the host.
- **The key's network round trip is much longer than the ping's:** look at the network, the DSCP class, and the shared connection.
- **The client's own delay is large:** look at the input filter and the attach loop.

To compare with `ssh -t`, type in the same agent in a plain SSH session while you watch the echo.

## The clock offset

The two machines have different monotonic clocks. Go's monotonic time has no meaning outside its process, so each side uses the system's `CLOCK_MONOTONIC` instead. The bridge and the holder on one host share it.

The client sends a timed ping with an ID. The bridge answers with the ID and its own clock. The client then calculates the offset as NTP does ([RFC 5905](https://www.rfc-editor.org/rfc/rfc5905)): the host's time minus the midpoint of the send and the answer. The error is at most half the round trip. An older host helper echoes the ID alone, so the steps that need the offset stay empty.

Each attach has its own offset, because the launcher can attach to several hosts, and a host can restart. Two clocks also drift apart over a long attach. So for each key, `keytrace` looks at the 8 pings of its attach nearest the key. As NTP's clock filter does, it uses the one with the shortest round trip.

## The records

Each line is one JSON object. Every record has `t`, a monotonic time in ns, `cat`, the category (`attach`, `net` or `memory`), and `ev`, the event. The other fields hold sizes in bytes, times in ns, and durations in ns. A field that does not apply is left out.

| Side | `ev` | When | Fields |
| --- | --- | --- | --- |
| Both | `start` | The trace starts | `side`, `session`, `version`, `wall` (Unix time in ns), and `trace` on the host |
| Client | `conn` | A connection attaches | `trace`, and `host_trace` when the host helper can write its own trace. A session that started before the helper's upgrade keeps its old holder, which writes nothing. |
| Client | `stdin` | A read from the terminal ends | `read`, `n` |
| Client | `key` | A `TInput` frame is written | `trace`, `key`, `read`, `n`, `write` |
| Client | `echo` | The first output after a key is written to the terminal | `trace`, `key`, `recv` (when the frame arrived), `n` |
| Client | `ping` | A pong arrives | `trace`, `ping`, `rtt`, `host`, `offset` |
| Host | `key` | The PTY write of a key ends | `trace`, `key`, `n`, `bridge` and `recv` (when the bridge and the holder read it), `wait` |
| Host | `read` | A read of the agent's output returns, before the holder asks for its lock | `n`, `wait`, `hold`, `feed`, and `trace` and `key` on the first read after a key |
| Host | `tick` | The status tick asks for the lock | `wait`, `hold` |
| Host | `sample` | Once a second | `mem`: `memory.current`, `memory.peak`, four `memory.stat` counters, `memory.events`, and the `total` of each line of `memory.pressure` and `cpu.pressure`, in ns |
| Both | `dropped` | Records were lost | `dropped` |

`trace` names one connection of an attach, and `key` counts its `TInput` frames from 1. Both sides count the same frames, so `trace` and `key` join a key's records. `read` counts the client's reads from the terminal in each attach.

## Limits

- Records hold sizes and times only. They never hold key bytes, output bytes, screen text, secrets or the environment.
- Each file has mode 0600, and turns over at 8 MiB.
- The trace adds only clock reads to the time the holder holds its lock. It reads the clock once when `input()`, `output()` or `tick()` gets the lock, and twice around the screen model in `output()`. It builds each record after the unlock, and sends it to a writer goroutine.
- Records go to the writer through a queue. When the queue is full, the trace drops records, and a `dropped` record says how many.
- With the trace off, each hook costs one nil check. `just bench` checks the attach pump.
- Give each client process its own file. Two processes that write one file turn it over at different times.
- The host samples memory only when the session runs in its own systemd unit. In a container without systemd, the host's file has no samples.
- With an older host helper, only the client writes records, and its `conn` record has no `host_trace`.
- After an upgrade of the host helper, a session that started before it keeps its old holder. That holder writes no records, but the new bridge still sets `host_trace`. Only a session that started after the upgrade has a host file.
- When the client cannot write all its records, for example on a full disk, it prints a warning after the detach.
