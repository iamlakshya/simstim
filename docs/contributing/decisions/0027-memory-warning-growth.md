---
status: accepted
date: 2026-10-16
---

# 0027 The memory warning also comes when the memory still grows, and on its own at 95 %

**Status:** Accepted. The owner left the alert choices to the maintainers on 16 October 2026 ("pick what is right for alert"), as for [0026](0026-end-and-memory-alerts.md). This record replaces 0026's rule for what starts a warning: "An episode starts when the memory that the kernel cannot reclaim reaches 90 % of the cap. The kernel must also work to stay under the cap at that point." The rest of 0026 stays.

## Context

Under 0026, the holder warns when two things hold. The memory that the kernel cannot reclaim reaches 90 % of the session's cap. And the kernel works to stay under the cap. Either the `max` count in `memory.events` rose, or the session's tasks stalled on memory for 10 % of the last 10 seconds. The second condition keeps a large session that holds steady from a warning.

A run on a real host showed a gap. The host was a Debian 13 VM with 4 GB of memory, and the session had a cap of 256 MiB. A program's anonymous memory grew steadily, with no page cache in the session's unit. That memory stayed at 90 % of the cap or more for 12 s before the kill. But the kernel had no cache to reclaim, so the `max` count stayed at 0 until the very end, and nothing stalled. The cap stopped the program with no warning.

With page cache in the unit, the same rule warned 11 s before the kill, as designed. So the rule fails exactly in the case that [#40](https://github.com/iamlakshya/simstim/issues/40) is about: a steady leak.

## Options

1. **Keep 0026's rule.** A leak with no page cache in the unit gets no warning.
2. **Warn at 90 % with no other sign**, as [0008](0008-memory-max-only.md) first suggested. This is simple. But a large session that holds steady at 91 % then needs the user until its memory falls below 80 %.
3. **Warn at 90 % when the memory still grows, as well as on the kernel's signs, and at 95 % with no other sign.**
4. **Predict the time until the cap** from the rate of growth, and warn when it falls below a time such as a minute. This is more exact, but it needs a longer window to measure a rate, and it is harder to explain and to test.

## Decision

Option 3, chosen by the maintainers on 16 October 2026.

- **Growth.** At 90 % of the cap, the warning also comes when that memory still grows. It must have risen by 1 % of the cap over the last 5 seconds (5 ticks of 1 s). The leak on the test host grew by about 0.8 % of the cap each second, so 4 % in 5 s. 1 % in 5 s catches a leak four times slower than that one. At that rate, the last 10 % of the cap lasts 50 s.
- **95 % alone.** At 95 % of the cap, the warning comes with no other sign. At most 5 % of the cap is left then, which one tool call can take.
- **The kernel's signs stay.** At 90 %, a rise of the `max` count or a stall of 10 % still starts a warning, as in 0026.

The rest of 0026's `memory` event stays. The episode ends when that memory falls below 80 % of the cap. The holder sends one `memory` alert for each episode, and the mark clears as 0026 says. The holder keeps the memory of the last 5 ticks from what it already reads, so the warning reads no new file. It still reads `memory.stat` only from 80 % of the cap.

## Consequences

- Good, because a steady leak with no page cache now warns. In the measured case, the warning comes at 90 %, about 12 s before the kill.
- Good, because a session at 95 % of its cap or more now warns, even when it does not grow. One allocation can still stop it.
- Good, because the warning reads no new file, and costs what it did.
- Bad, because a session that grows slowly and on purpose near its cap now warns. An example is a build that uses its last 10 %. It then needs the user until its memory falls below 80 %, or until someone attaches and types.
- Bad, because a large session that holds steady between 95 % and its cap now warns, though it may never grow.
- Bad, because a leak slower than 1 % of the cap in 5 s still warns only at 95 %. Under a cap of 13.3 GiB (a 16 GiB host), 1 % is 136 MiB. So a leak of 10 MiB a second warns at 95 %, with about a minute left.
- Bad, because the numbers rest on one measured case. A run on more hosts and agents may move them.
- Bad, because below 80 % of the cap the holder reads `memory.current`, not the memory that the kernel cannot reclaim. A growth that starts there counts for less than it is, never for more.
