---
status: accepted
date: 2026-10-17
---

# 0024 Draw a session on the alternate screen again after a detach

This record replaces one rule of [0004](0004-replay-by-offset.md): that a client which attaches again in the same terminal always continues from its byte offset. The rest of 0004 stays.

## Context

0004 replays only the bytes a terminal missed, because the terminal still shows the rest. That holds for the main screen, whose rows and scrollback stay after a detach. It does not hold for the alternate screen. Detach leaves it with `CSI ? 1049 l`, in the reset that puts the terminal back as it was ([architecture](../../explanation/architecture.md#34-detach)), and the terminal throws its cells away. On the next attach, `CSI ? 1049 h` enters it again, and xterm's control sequences say that mode clears the alternate screen first.

So a full-screen agent that redraws only the cells that changed, as Claude Code's fullscreen mode does, came back as a blank screen with a few pieces of its UI ([#36](https://github.com/iamlakshya/simstim/issues/36)). It happened on the second attach from the launcher, which keeps the attach state between attaches. Moving the mouse over the blank rows made the agent paint them, so its own picture of the screen was right.

The SIGWINCH that the holder sends on attach did not help: at an unchanged size, a program need not draw everything again.

## Options

1. Keep the replay, and make the agent repaint, for example by changing the PTY's size by one row and back
2. Ask for a snapshot when the session was on the alternate screen at the last detach
3. Stay on the alternate screen after a detach

## Decision

Option 2. The client sends no offset (`last_offset` −1) when its saved modes say the session was on the alternate screen. The holder then answers as for a new client: the shadow terminal's snapshot draws both screens, and the output from the snapshot's offset follows. A reconnect inside one attach never leaves the alternate screen, so it keeps its offset.

Option 1 depends on each agent's redraw, and a size change can make some agents print their screen again into the main screen's scrollback. It stays open as a follow-up if a snapshot is ever not enough. Option 3 would hide the user's own screen after a detach, and the terminal would not be as the client found it.

## Consequences

- Good, because a full-screen agent comes back whole, whatever it redraws.
- Good, because the change is in the client only: the protocol and the holder are unchanged.
- Bad, because such an attach costs a snapshot instead of a few bytes. A snapshot of 10,000 lines takes about 8 ms ([the shadow terminal](../../../vt/README.md)).
- Bad, because a build without the shadow terminal replays the whole ring instead, as it does for a new client.
- Bad, because notifications that the agent sent while away are not replayed for such a session, as for a new client.
