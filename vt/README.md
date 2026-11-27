# The shadow terminal

A small program that keeps a copy of one session's screen. The session holder starts it as a child process and feeds it every byte the agent writes. On request, it writes a **snapshot**: the bytes that redraw the screen in a blank terminal. A new client gets the snapshot instead of a replay of the output ring, which may start in the middle of the stream.

It is built on libghostty-vt, Ghostty's terminal core. It runs in its own process, so a crash here cannot take the agent down. The holder restarts it and feeds it the ring again (`internal/host/vt.go`).

## Versions

| What | Version |
| --- | --- |
| Ghostty | libghostty-vt 1.0.0: the tag `vt-1.0.0`, commit `0fa3d577a5614150ff8e2c152de5fb9d5c6d2dec` (6 November 2026), pinned in `build.zig.zon` with its hash |
| Zig | 0.17.0 exactly, as `scripts/tools.lock` and the workflows pin it. Ghostty's build accepts 0.17.x only, so Zig 0.16 fails. |

The pin holds the fixes of four Ghostty advisories that affect libghostty-vt:

| Advisory | What | Fix |
| --- | --- | --- |
| [GHSA-w9q4-5jvv-mcv4](https://github.com/ghostty-org/ghostty/security/advisories/GHSA-w9q4-5jvv-mcv4) | Terminal output alone could corrupt memory, when a clear by scrolling pruned the cursor's page. | `e257bb45` |
| [GHSA-jqr7-69mm-v35h](https://github.com/ghostty-org/ghostty/security/advisories/GHSA-jqr7-69mm-v35h) | A very wide terminal could crash when it was narrowed. | `0af5c9e55` |
| [GHSA-qjfx-r533-6vx3](https://github.com/ghostty-org/ghostty/security/advisories/GHSA-qjfx-r533-6vx3) | A scrollback limit could free the cursor's page on a terminal one row high. | `65ff54c6` |
| [GHSA-3r5j-c6rj-xq2v](https://github.com/ghostty-org/ghostty/security/advisories/GHSA-3r5j-c6rj-xq2v) | The kitty clipboard protocol could make memory grow. | `44c4c8f5` |

On 11 November 2026, GitHub's compare API answered "ahead" for each fix against the pin: `https://api.github.com/repos/ghostty-org/ghostty/compare/<fix>...0fa3d577a5614150ff8e2c152de5fb9d5c6d2dec`. "ahead" or "identical" means that the pin holds the fix.

GHSA-jqr7 names apps that take a terminal size from a remote client, and the session holder is one. So the holder and this program both cap a terminal at 4096 columns and 2048 rows. Ghostty itself allows 65535. At 4096 by 2048, with 10,000 lines of scrollback full and the alternate screen on, this program held 488 MiB on Linux. At 4096 by 4096 it held 629 MiB.

To move to another Ghostty commit, use a libghostty-vt tag (`vt-X.Y.Z`) where you can:

```sh
cd vt
zig fetch --save=ghostty https://github.com/ghostty-org/ghostty/archive/<commit>.tar.gz
```

Then check the four fixes against the new commit with the compare API, and run the tests (below). Read the tag's release notes, and expect to fix some calls.

## Build

```sh
just vt                 # both Linux binaries, into internal/client/helpers/vt-linux-{amd64,arm64}
cd vt && zig build      # this machine, Debug, into vt/zig-out/bin/shadow-vt
cd vt && zig build linux  # both Linux binaries, into vt/zig-out/linux
```

`just build` runs `just vt` when Zig 0.17 is installed. Otherwise it prints a note, removes any old build in `internal/client/helpers/`, and goes on without the shadow terminal, so the client never embeds an old pin. `just vt` alone fails with the same note. `just setup` puts the pinned Zig into `.tools/`, or get Zig from <https://ziglang.org/download/>. The first build fetches Ghostty and its dependencies into `vt/zig-pkg/`.

The Linux binaries are static (musl) and stripped: about 1.5 MB for amd64 and 1.2 MB for arm64.

They are built **ReleaseSafe**. It keeps Zig's safety checks and Ghostty's assertions, so a bug stops the process with a message instead of running on with a corrupt screen. The holder then restarts it. ReleaseFast parses about 1.6 times faster, but ReleaseSafe is already far faster than any agent writes: about 70 MB/s for truecolour log output and 350 MB/s for redrawing TUIs, and 8 ms for a snapshot with 10,000 lines of scrollback. `-Dlinux-optimize=ReleaseFast` changes it.

## Tests

The tests are in Go, in `internal/host/vt_test.go`:

```sh
go test -run TestVT ./internal/host/
```

They build this program with Zig 0.17 if it is installed, or use `VT_BINARY`, or skip. They also skip a binary whose `--version` names another Ghostty commit than the one in `build.zig.zon`. Each one feeds recorded output to the shadow terminal, takes a snapshot, feeds the snapshot to a fresh one, and compares the two cell by cell. The fixtures are in `internal/host/testdata/vt/`.

## Command line

```text
shadow-vt [--cols N] [--rows N] [--scrollback LINES] [--version]
```

The size defaults to 80 by 24. More than 4096 columns or 2048 rows, here or in an `R` or `S` frame, is cut to that. The terminal keeps 10,000 lines of scrollback by default. `--version` prints the version and the Ghostty commit.

## Protocol

Messages go over stdin and stdout as frames:

| Bytes | Field |
| --- | --- |
| 1 | type, an ASCII letter |
| 4 | payload length, unsigned, little-endian |
| n | payload |

Numbers in payloads are little-endian. A frame of an unknown type is ignored, so new types can be added.

### Holder to shadow terminal

| Type | Payload | Does |
| --- | --- | --- |
| `D` | output bytes | Feeds the session's output. |
| `R` | u16 cols, u16 rows, then optionally u16 width and u16 height in pixels | Resizes. The pixel size is the window's, as in `TIOCSWINSZ`. It is used for size reports. |
| `S` | u16 cols, u16 rows, u32 max scrollback lines | Resizes to that size, then replies `S` with a snapshot. An empty reply means it failed. |
| `Q` | 1 byte: 1 on, 0 off | Answer the agent's terminal queries (`W`), or stop. Off at start. |
| `P` | JSON profile | What to report while answering. See below. |
| `T` | optionally u32 max scrollback lines | Replies `T` with a JSON dump of the screen, for tests. |
| `Y` | any | Replies `Y` with the same payload once every earlier frame is handled. |

### Shadow terminal to holder

| Type | Payload | Means |
| --- | --- | --- |
| `S` | snapshot bytes | The reply to `S`. |
| `W` | bytes | Write these to the PTY: replies to queries. Only while `Q` is on. |
| `E` | JSON | An event from the output. See below. |
| `T`, `Y` | | The replies to `T` and `Y`. |

Replies come in the order of their requests.

### The profile

All fields are optional. An empty field keeps the default.

```json
{"da1": [62, 22, 52], "da2": [1, 10, 0], "xtversion": "ghostty 1.3.1",
 "fg": "#cdd6f4", "bg": "#1e1e2e", "cursor": "#f5e0dc", "color_scheme": "dark"}
```

- `da1`, `da2`: the DA1 and DA2 replies. The defaults are `CSI ? 62 ; 22 c` and Ghostty's `CSI > 1 ; 10 ; 0 c`.
- `xtversion`: the XTVERSION reply. The default is "libghostty".
- `fg`, `bg`, `cursor`: the colours for OSC 10, 11 and 12 queries. With none, those queries get no reply.
- `color_scheme`: `dark` or `light`, for `CSI ? 996 n`.

While `Q` is on, it also answers from its own state: the cursor position, DECRQM, DECRQSS, the kitty keyboard query, XTGETTCAP and size reports. It never answers `OSC 7501 ; ?`, because the holder always does.

### Events

```json
{"type": "bell"}
{"type": "notify", "title": "Agent", "body": "needs you"}
{"type": "title", "title": "my title"}
{"type": "pwd", "data": "file://host/path"}
{"type": "progress", "state": "set", "value": 42}
{"type": "status", "state": "blocked", "data": "state=blocked:kind=permission"}
```

`notify` comes from OSC 9 and OSC 777, `progress` from OSC 9;4 (a `value` of -1 means none), and `status` from OSC 7501. Its `data` is everything after `7501;`.

## The snapshot

Write it with the cursor at the start of an empty line, with only empty lines below. Text above the cursor scrolls into the terminal's scrollback; nothing is erased. It never contains RIS (`ESC c`) or a query.

In order, it holds:

1. Grapheme clustering (mode 2027), if it is on, as it decides how wide some text is.
2. The primary screen's scrollback, up to the limit, then one line feed per screen row. This moves those rows into the terminal's own scrollback, where search finds them.
3. The primary screen's visible rows.
4. If the alternate screen is active: the primary screen's saved cursor and kitty keyboard flags, then the mode that entered the alternate screen (1049, 1047 or 47), then the alternate screen's rows. Both screens come back.
5. The active screen's saved cursor (DECSC).
6. Every mode that differs from its default: DECCKM, keypad, bracketed paste, focus, autowrap, insert, reverse video, cursor visibility, the left and right margin mode, colour-scheme and size report subscriptions, and the rest. Then the mouse tracking and encoding modes, in the order the program set them.
7. The scroll region (DECSTBM, DECSLRM), then origin mode.
8. The cursor position, including a pending wrap, then the pen (SGR), an open hyperlink, character protection and the character sets.
9. The kitty keyboard flags as a stack: the base with `CSI = u`, then each pushed entry with `CSI > u`. A program that pops later gets back what it pushed over.
10. modifyOtherKeys, the cursor shape (DECSCUSR), palette changes (OSC 4), colour overrides (OSC 10, 11, 12), the title (OSC 2) and the working directory (OSC 7).

Rows are written cell by cell, with SGR styles (including truecolour, underline styles and colours) and OSC 8 hyperlinks. Soft-wrapped rows stay wrapped, so the terminal reflows them on a resize. Blank cells inside a row are skipped with cursor moves, so they stay empty.

Limits:

- Synchronised output (mode 2026) is left out on purpose: replaying it stalls the client.
- Not restored: kitty graphics and sixel images, tab stops (setting them moves the cursor), the title stack, shell-integration marks (OSC 133), per-cell protection (DECSCA), a half-received escape sequence, and the alternate screen's content when it is not active.
- A row that wraps from the scrollback onto the screen is split there.
- Text widths follow Ghostty's rules. A terminal that measures some characters differently may place the rest of that row differently.
- After a crash, the restarted terminal knows only what the ring holds. Modes set before the ring's start are lost until the agent sets them again.

## Licences

libghostty-vt is MIT-licensed. `src/snapshot.zig` adapts the restore approach of zmx's `serializeTerminalState` (MIT, Copyright (c) 2025 Eric Bower). The file carries the notice.
