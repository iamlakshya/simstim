# The fidelity contract

While you are attached, your terminal shows the agent's own bytes. This page lists what that promise covers and the test behind each part. It is part of [the architecture](architecture.md).

The last column distinguishes automated checks, manual checks, documented limits and performance targets. A target is not a guarantee for every terminal or agent.

| Feature | How simstim keeps it | Test |
| --- | --- | --- |
| Keys: kitty protocol, modifyOtherKeys, Shift+Enter, Option/Alt | Raw input bytes. The filter matches only two keys and never re-encodes. A lone Esc goes out at once. Only the start of a key sequence that a read splits waits for its end, 15 ms at most. On reattach, the snapshot restores the kitty flags. | `simstim doctor keys`, run end to end |
| Truecolour, styled underlines | `TERM=xterm-ghostty` with its terminfo installed on the host. `COLORTERM=truecolor`. The client's `TERM_PROGRAM` is passed through. | Fixture replay |
| Native scrollback and Cmd+F | Output lands in the local terminal's normal screen. No RIS. Replay by offset avoids duplicates. | Manual check plus fixture |
| OSC 52 clipboard | Passed through live. Clipboard access depends on the local terminal's policy; simstim does not add a permission prompt or filter. | Manual |
| OSC 8 links, OSC 9 and 777 notifications, OSC 9;4 progress | Passed through live and may be repeated when historical output is replayed. | Fixture |
| Synchronized output (mode 2026) | Passed through, so no flicker | Fixture |
| Mouse, focus, bracketed paste | Passed through, and reset on detach | `x/vt` baseline after detach |
| Terminal queries (DA, XTVERSION, OSC 10/11, kitty query) | Live queries reach every attached terminal, and replies from all attached clients reach the PTY. Historical queries are removed during replay. With no client attached, the vt child answers supported queries from its own state and configured defaults; it does not cache client replies. | Fixture with no client |
| Resize | The newest attach or any attached client's input frame makes it leader. Only the leader's resize frames change the PTY size. Every attach forces SIGWINCH. Replies and mouse events also change the leader. | Manual, two devices |
| Kitty graphics, sixel | Passed through while attached. **Not** restored on reattach. This is a known limit. | Documented |
| Added delay per keystroke | Target: under 1 ms on the client | Performance overlay |

Byte fidelity is not a security filter. Attach only to hosts you trust to send output to your terminal. See the [security model](security.md) for the trust limits.
