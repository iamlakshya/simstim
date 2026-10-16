---
status: accepted
date: 2026-10-16
---

# 0026 The once-a-minute rule never holds back a session's end, and a warning near the memory cap has its own event

**Status:** Accepted. The owner left this choice to the maintainers on 16 October 2026 ("pick what is right for alert"). This record replaces one rule of [0019](0019-alerts-and-hooks.md)'s noise control for a session's end: "Each session sends one event of a kind at most once a minute." The rule stays for every other event. The record also extends 0019's events with `memory`. The rest of 0019 stays.

## Context

0019 set the noise control for alerts. Each session sends one event of a kind at most once a minute, and a host sends at most 20 a minute. The minute keeps a session that flaps from filling the user's phone. [#40](https://github.com/iamlakshya/simstim/issues/40) found two cases where the minute drops an alert that matters.

- **A child's stop, then the agent's.** The memory cap stops a process that the agent started, such as a build. The holder sends `oom`, and the agent runs on. Within a minute, the cap stops the agent itself. That is the session's end, and the holder sends a second `oom`. The minute drops it. The first alert said that a process stopped, so nothing tells the user that the agent stopped too. The [alerts guide](../../how-to/alerts.md) documents this gap today.
- **A warning near the cap.** Step 3 of the plan in #40 warns when a session comes close to its memory cap. Its first draft, still in review, sent the warning as `needs_input`, with the reason in the summary. So the warning shared the minute with the agent's own questions. A question in the minute before hid the warning, and the warning hid a question in the minute after. The user needs to act on each one.

Both cases come from one rule. The warning is not merged yet, so its kind can still change before any config names it.

## Options

1. **Keep 0019, and document the gaps.** No code changes. With the laptop shut, the user learns of the agent's stop only at the next look at the launcher. The warning and the questions keep hiding each other.
2. **Exempt end events only.** The minute never holds back a session's end. This fixes the first case. The warning still shares `needs_input` with the questions, so the second case stays.
3. **Exempt end events, and give the warning its own kind.** This fixes both cases. It adds an event kind, which users and destinations see.
4. **Key the minute by kind and cause.** The holder gives each event a cause, such as a child's stop, the agent's stop or the warning. The minute then applies to each kind and cause. This fixes both cases without a new kind. But nobody can see the cause: a user cannot name it in `events`, and a destination cannot route on it. Each new cause also needs its own rule.
5. **Split `oom` into two kinds**, one for a child's stop and one for the agent's. Each kind then has its own minute. This fixes only the first case. A config that lists `events` would stop getting the agent's stop until the user adds the new kind. A destination that routes on `oom` would miss it too.

## Decision

Option 3, chosen by the maintainers on 16 October 2026.

### A session's end

A session's end event is `exited`, `failed`, or `oom` when the cap stopped the agent itself. The once-a-minute rule for its kind never holds it back. Each run ends once. Restore and resume start a new run in a new holder, so each end is new. An end also does not start the minute for a later event of its kind.

The host's limit of 20 a minute still counts an end, and can still drop it. That limit keeps many sessions together from flooding a destination. `exited` still goes out only where the user lists it in `events`. The dispatcher knows an end by its exit code. Only an end carries `exit_code`, as the event format already says.

### The `memory` event

A new event kind, `memory`, means that a session is close to its memory cap. It is in the default events, with `needs_input`, `turn_done`, `failed` and `oom`. Its summary reads, for example, "api-tests on my-vps is close to its memory cap (2.7 of 2.9 GiB)". ntfy gives it high priority, as it does `needs_input`, `failed` and `oom`.

The holder sends it once for each episode. An episode starts when the memory that the kernel cannot reclaim reaches 90 % of the cap. The kernel must also work to stay under the cap at that point. The episode ends when that memory falls below 80 % of the cap. A kill at the cap also starts an episode, without a `memory` alert, because the `oom` alert says more.

`memory` does not wait for `delay`. Neither typing nor a change of state holds it back, as they hold back `needs_input`. The once-a-minute rule applies to it, with a minute of its own.

The session's state becomes `needs_input`, so the launcher and `ls` show that the session needs the user. That change sends only the `memory` alert, not `needs_input` as well. The mark clears when the episode ends, or at a kill at the cap. It also clears when someone attaches after the warning showed, and then presses a key. A person who was attached all along never saw the warning, because the screen is the agent's own. So their keys do not clear it.

The agent's own questions still send `needs_input`, and the two kinds no longer share a minute. The agent may ask while the session already needs the user for the warning. Its state is then `needs_input` before and after, so no change of state raises an alert. The holder must still raise `needs_input` for the question when the reason passes from the warning to the agent.

The end rule ships first. The `memory` kind ships with the warning near the cap.

## Consequences

- Good, because a child's stop no longer hides the agent's stop that follows it.
- Good, because a warning near the cap and a question from the agent no longer hide each other.
- Good, because a user can route `memory` on its own, for example to one destination only, or leave it out of `events`.
- Bad, because one stop at the cap can now send two `oom` alerts within a minute: one for the child, and one for the agent. Only the second carries `exit_code` and `signal`.
- Bad, because the host's limit can still drop an end, when the host's sessions sent 20 alerts in the last minute. That limit guards the destinations, and a host seldom reaches it.
- Bad, because a user whose config lists `events` gets `memory` only after adding it there. So does a destination with its own `events`.
- Bad, because a command or webhook that knows only 0019's kinds now gets one more. The format's version stays 1, because a new kind changes no field's meaning.
