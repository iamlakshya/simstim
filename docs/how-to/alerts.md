# Get alerts from your sessions

A host can tell you when a session needs you, finishes a long turn or fails. It sends the alert itself, so it works with your laptop shut. It sends only to places you name: an [ntfy](https://ntfy.sh) topic, a webhook, or a command on the host. Until you name one, nothing leaves the host.

[Decision 0019](../contributing/decisions/0019-alerts-and-hooks.md) explains the design.

## Start in one minute

Install the ntfy app on your phone and subscribe to a topic with a long, random name. The name is the only thing that keeps others out of a public topic, so treat it like a password. Then:

```sh
simstim host alerts set my-vps --ntfy https://ntfy.sh/<your-topic>
simstim host alerts test my-vps
```

Your phone shows "Test alert from my-vps". From now on, every session on `my-vps` sends an alert when it needs you, finishes a turn of more than a minute, or fails. It also sends one when it comes close to its memory cap, and when it runs out of memory. Running sessions pick up the change at their next event. A session started before your helper had alerts keeps its old helper, and sends nothing: start it again to get alerts from it.

`simstim host alerts my-vps` shows the settings and the last deliveries.

## Set up ntfy

**ntfy.sh, the public server.** Use a topic name nobody can guess, as above.

**Your own ntfy server, or a reserved topic.** These need an access token. Give `--token-cmd` a command that prints the token on your laptop, such as a keychain read. The client runs it, sends the token to the host, and never prints it:

```sh
# macOS keychain
simstim host alerts set my-vps --ntfy https://ntfy.example.com/agents \
  --token-cmd 'security find-generic-password -s ntfy-token -w'

# 1Password
simstim host alerts set my-vps --ntfy https://ntfy.example.com/agents \
  --token-cmd 'op read op://Private/ntfy/token'
```

The token is stored only on the host, in the alerts file, which only you can read. To keep it in a file of its own instead, edit the alerts file by hand and set `token_file` (see [the alerts file](#the-alerts-file)). The token file must be mode 0600 too.

The alert's title names the host, and its text is the summary, such as "auth-fix on my-vps needs you". `needs_input`, `failed`, `oom` and `memory` have high priority. The others have the default. Set `priority` in the file to change that.

## Set up a webhook

A webhook gets an HTTP POST of the event as JSON (see [what is sent](#what-is-sent)). For Slack or Discord, ask for a message they understand:

```sh
simstim host alerts set my-vps --webhook https://hooks.slack.com/services/… --format slack
simstim host alerts set my-vps --webhook https://discord.com/api/webhooks/… --format discord
```

A webhook address is a secret: anyone who has it can post to your channel. It stays on the host, and `host alerts` shows only its start.

If your service needs a header, such as `Authorization`, add it to the alerts file by hand, under `headers`.

## Set up a command

A command runs on the host with `sh -c`, in your home folder and your login environment. It gets the event as one line of JSON on stdin, and as environment variables:

| Variable | Value |
| --- | --- |
| `SIMSTIM_EVENT` | The event, such as `needs_input` |
| `SIMSTIM_SUMMARY` | The one-line summary |
| `SIMSTIM_HOST` | The host's name |
| `SIMSTIM_SESSION_ID`, `SIMSTIM_SESSION_NAME`, `SIMSTIM_SESSION_AGENT`, `SIMSTIM_SESSION_DIR` | The session |
| `SIMSTIM_TIME` | When it happened, in RFC 3339, UTC |
| `SIMSTIM_EXIT_CODE`, `SIMSTIM_SIGNAL` | How the program ended, for `exited`, `failed` and `oom` |
| `SIMSTIM_DETAIL` | The agent's question, only if you turn `detail` on |
| `SIMSTIM_EVENT_VERSION` | The version of the event's format, now `1` |

Some examples:

```sh
# Keep every event in a file.
simstim host alerts set my-vps --command 'cat >> ~/agent-events.jsonl'

# A desktop notification on a Linux workstation.
simstim host alerts set my-vps --add --command 'notify-send "$SIMSTIM_SUMMARY"'

# Telegram, with the bot token in a file only you can read.
simstim host alerts set my-vps --add --command \
  'curl -s -d chat_id=12345 --data-urlencode "text=$SIMSTIM_SUMMARY" "https://api.telegram.org/bot$(cat ~/.telegram-token)/sendMessage"'
```

Event values never go into the command line itself, so a session name such as `$(rm -rf ~)` is only text. Keep it that way in your own scripts: quote the variables (`"$SIMSTIM_SUMMARY"`), and never pass them to `eval` or `sh -c`.

A command has 10 seconds. After that it is stopped, with any children it started. Its output goes to the delivery log, cut short.

## The alerts file

`host alerts set` writes `~/.config/simstim/alerts.toml` on the host, mode 0600. You may also edit it by hand. A file that other users can read is not used: `chmod 600` it. An unknown key is an error, so a typo never turns alerts off without a word.

```toml
events = ["needs_input", "turn_done", "failed", "oom", "memory"]  # the default
delay = "10s"               # how long needs_input waits before it is sent
min_turn = "60s"            # the shortest turn that sends turn_done
detail = false              # add the agent's question to needs_input
quiet_when_typing = true    # drop needs_input if you typed during the delay

[[destination]]
type = "ntfy"
url = "https://ntfy.example.com/agents"
token_file = "~/.config/ntfy-token"    # or token = "…"; not both
priority = "high"                      # 1 to 5, or min, low, default, high, urgent

[[destination]]
type = "webhook"
url = "https://hooks.slack.com/services/…"
format = "slack"                       # json (the default), slack or discord
events = ["failed", "oom"]             # this destination only gets these
headers = { X-Team = "agents" }

[[destination]]
type = "command"
run = "cat >> ~/agent-events.jsonl"
```

Addresses must start with `https://`. Plain `http://` works only for `localhost` and `127.0.0.1`. A host may have up to 10 destinations.

These `host alerts set` flags write the same keys:

| Flag | Key |
| --- | --- |
| `--ntfy URL`, `--token-cmd CMD` | an ntfy destination, with `token` |
| `--webhook URL`, `--format F` | a webhook destination |
| `--command CMD` | a command destination |
| `--events a,b` | `events` |
| `--delay 10s`, `--min-turn 60s` | `delay`, `min_turn` |
| `--detail` | `detail = true` (`--detail=false` turns it off) |
| `--add` | keep the destinations the file has, and add this one |

A destination flag without `--add` replaces the whole file. With no destination flag, only the settings you give change.

## What is sent

| Event | When |
| --- | --- |
| `needs_input` | The agent waits for you, from its own hook or from what its screen shows, for longer than `delay` |
| `turn_done` | The agent finished a turn in which it worked for longer than `min_turn` |
| `exited` | The program ended with code 0 |
| `failed` | The program ended with another code, or a signal |
| `oom` | A process in the session hit its memory cap and was stopped. When that process was the program itself, `oom` is the session's end, in place of `failed`, and has `exit_code` and `signal`. A program that runs on does not also send `needs_input`. [Memory](memory.md) says how to raise the cap |
| `memory` | The session is close to its memory cap, before the cap stops a process |
| `started` | The session started, or `resume` started its agent again |
| `restored` | The session was started again after a host restart |
| `test` | `host alerts test` |

No event is sent when you kill a session, or when the host shuts down.

A session's end is `exited`, `failed`, or the `oom` that stopped the program itself. The limit of one event of a kind a minute never holds an end back ([keep the noise down](#keep-the-noise-down)). So if the cap stops a process that the program started, and then the program itself within a minute, both `oom` alerts go out. Only the second has `exit_code` and `signal`. For processes that the program started, a session sends one `oom` a minute at most.

A session that comes close to its memory cap sends one `memory` alert, such as "api-tests on my-vps is close to its memory cap (2.7 of 2.9 GiB)". The session then needs you, but sends no `needs_input` for that. The next `memory` alert comes only after the session's memory falls well below the cap. It goes out at once, also while you type, and its limit of one a minute is its own. If you list `events` in the file, add `memory` to get it. [Memory](memory.md#when-a-session-comes-close-to-its-cap) says what the warning means and what to do. [Decision 0026](../contributing/decisions/0026-end-and-memory-alerts.md) explains why it has its own kind.

An event looks like this:

```json
{
  "v": 1,
  "event": "failed",
  "host": "my-vps",
  "session": {"id": "q3k7mz2a", "name": "api-tests", "agent": "codex", "dir": "/home/me/src/api"},
  "time": "2026-10-15T10:02:11Z",
  "exit_code": 2,
  "summary": "api-tests on my-vps failed (exit 2)"
}
```

`exit_code` and `signal` appear only when the program ended. `detail` appears only when you turn it on. New fields may appear; `v` changes only if a field changes its meaning or goes away.

## Privacy

- **Nothing is sent until you name a destination.** There is no default, no relay and no service of ours in the middle.
- **The agent's question stays on the host** unless you turn on `detail`. It can hold code, file names or secrets from your screen. With `detail` on, `needs_input` carries the agent's own message, or the last lines of its screen.
- **Tokens and webhook addresses stay on the host**, in a file only you can read. `host alerts` shows them hidden, and the delivery log shows only the start of each address.
- **ntfy.sh is a public server.** Anyone who knows your topic can read it. Use a long random topic, or your own server with a token.

## Keep the noise down

- `needs_input` waits `delay` (10 seconds). If the session moves on, or you type into it, in that time, nothing is sent. So you get no alert while you are at the keyboard. Set `quiet_when_typing = false` to change that.
- Each session sends one event of a kind a minute at most. This limit never holds back the session's end: `exited`, `failed`, or the `oom` that stopped the program.
- A host sends 20 events a minute at most, across all its sessions. This limit counts a session's end too.
- `turn_done` comes only after turns longer than `min_turn` (a minute).
- List only the events you want in `events`, or give one destination its own `events`.

## When an alert does not arrive

Start with a test, which ignores the delay and the limits:

```sh
simstim host alerts test my-vps
simstim host alerts test my-vps --event needs_input
```

It says what each destination did. Then look at the delivery log, which also says why an event was not sent:

```sh
simstim host alerts log my-vps -n 50
```

If the log has nothing from a session at all, the session may run an older helper, from before alerts. Start it again.

| The log says | What to do |
| --- | --- |
| `not sent: the session moved on during the delay` | Nothing: you or the agent answered within `delay` |
| `not sent: someone typed into the session during the delay` | Nothing; or set `quiet_when_typing = false` |
| `not sent: this session sent one … in the last minute` | Nothing: this is the per-session limit |
| `config` with an error | Fix the alerts file; `host alerts` shows the error and what to try |
| `HTTP 401` or `HTTP 403` | The ntfy token or the webhook address is wrong |
| `cannot reach it` | The host has no way out to that address. Use a `command` that reaches it another way. |
| `the command failed: exit status 127` | The command was not found. Use its full path. |

The log is `~/.local/state/simstim/alerts.log` on the host, one JSON object a line. It keeps about 2 MiB.

## Turn alerts off

```sh
simstim host alerts off my-vps
```

This removes the alerts file. Running sessions stop sending at their next event.
