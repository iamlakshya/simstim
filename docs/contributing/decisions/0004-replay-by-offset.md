---
status: accepted (its rule that a client in the same terminal always continues from its offset replaced by 0028)
date: 2026-10-15
---

# 0004 Reattach by byte offset, with a full redraw as the fallback

## Context

After a disconnect, the user's terminal still shows the session's earlier output, in its native scrollback. Sending the whole history again would duplicate it. Sending nothing would lose the output printed while away.

## Options

1. Always redraw the whole screen
2. Replay from the client's last byte offset when it is still in the holder's ring; otherwise redraw
3. Clear the local scrollback on every attach

## Decision

Option 2. The holder keeps the last 8 MiB of output, with absolute offsets. A client that reattaches in the same terminal sends its offset and gets exactly the bytes it missed, so scrollback continues with no gap and no duplicate. A new client, or an offset that fell out of the ring, gets a full redraw. Replayed bytes have terminal queries removed, so the local terminal never answers a stale question. A setting can clear local scrollback on a full redraw.

## Consequences

- Good, because sleep and network changes cost nothing visible.
- Good, because notifications sent while away still arrive.
- Bad, because the client must remember terminal modes and restore them before replaying.
