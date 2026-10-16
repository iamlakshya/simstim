---
status: accepted (its once-a-minute rule for a session's end replaced by 0026, which also extends its events with memory)
date: 2026-10-15
---

# 0019 Alerts and event hooks run on the host

## Context

The roadmap's N5 asks for alerts that reach the user when a session needs them, with the laptop shut: 13 developer sources ask for it, and Cursor, Devin, Codex and Conductor sell it for $20 to $50 a month ([roadmap](../../roadmap.md)). Today the client shows a desktop alert (OSC 777) only while it runs. The owner asked on 15 October 2026 for the whole backend for hooks and alerts now, and for the phone path (N4) to be scaffolded and launched later.

Project rules that bind the design: nothing new listens on a host, there is no relay and no telemetry, and simstim stores no secrets of its own.

## Options

1. **Alerts from the client.** Simple, but the client is not running when the laptop is shut, which is when alerts matter.
2. **A per-user daemon on the host** that watches every session. One more long-lived process type, and one more thing to keep alive and upgrade.
3. **Each session's holder fires its own events**, on the host, to the user's own destinations. The holder already decides the session's state; it gains a small dispatcher.
4. **A hosted push service of ours.** A relay, which the rules forbid.

## Decision

Option 3.

**Events.** The holder emits an event when a session changes state:

| Event | When |
| --- | --- |
| `needs_input` | The agent waits for the user (a hook or a screen rule), for longer than `delay` |
| `turn_done` | The agent finished a turn that ran longer than `min_turn` |
| `exited` | The program ended with code 0 |
| `failed` | The program ended with another code or a signal |
| `oom` | A process hit the memory cap and was stopped |
| `started` | The session started |
| `restored` | The session was started again after a host restart |
| `test` | `host alerts test` |

An event is JSON with a version: `{"v":1,"event":…,"host":…,"session":{"id","name","agent","dir"},"time":…,"exit_code":…,"detail":…}`. `detail` (the agent's question, from its hook or screen) is included only where the user turns it on, since it can hold code.

**Destinations**, in one file on the host, `alerts.toml` in the product's config folder (mode 0600, written by `host alerts set` or by hand):

- `command`: runs a program with the event as JSON on stdin and as environment variables. Event values are never put into a shell string.
- `ntfy`: an HTTP POST to the user's own ntfy topic (their server or ntfy.sh), with title, priority and tags.
- `webhook`: an HTTP POST of the event JSON, or of a Slack- or Discord-shaped message.

**There is no default destination.** Nothing leaves the host until the user names one.

**Noise control.** `needs_input` waits `delay` (10 s by default) and is dropped if the session moves on, or if a client attached to it typed during the wait. Each session sends one event of a kind at most once a minute. A host sends at most 20 a minute, counted in a file shared by the holders. Delivery is best effort: a 10 s timeout, one retry, and a line in a JSON log on the host (`host alerts log` shows it). Delivery never blocks the session.

**Secrets.** A token or webhook URL stays in `alerts.toml` on the host, mode 0600, which the user owns. `host alerts set` can take it from a command run on the laptop, such as a keychain read, so it never has to be typed or stored on the laptop.

**Repo setup hooks** (roadmap N6) use the same runner for `command`, but run inside the session's terminal before the agent starts, so their output shows. They are a separate change.

**The phone path** (N4) is scaffolded as a hidden, experimental subcommand of the host helper, so phones can later pick and attach to a session through any SSH app. It is not in the user docs or the help until the owner launches it.

## Consequences

- Good, because alerts work with the laptop shut, from where the session runs, with no relay and no listening port.
- Good, because the user picks every destination, and `command` reaches any service they like.
- Good, because holders already know the state, so no new process type is needed.
- Bad, because each holder parses the config and keeps its own timers. The config is small and read only when it changes.
- Bad, because a host with no outbound HTTPS cannot use `ntfy` or `webhook`; `command` still works.
- Bad, because screen-rule states can be wrong. The `delay` and the per-session limit keep false alerts rare, and hooks are preferred where the agent has them.
