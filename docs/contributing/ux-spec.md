# simstim UX

**Status:** accepted, 15 October 2026. This is the third design of the client's screens:

- the first was one home screen with panes;
- the second was a light launcher that followed Amp's CLI ([decision 0009](decisions/0009-light-launcher.md));
- this one keeps that launcher's flow, with a look and motion of our own ([decision 0012](decisions/0012-own-look.md)).

simstim has two kinds of screen, with opposite rules:

| | The launcher | An attached session |
| --- | --- | --- |
| What | Hosts, sessions, search, the quick start, sheets, the add-host wizard, the guided setup | The agent's real TUI |
| Who draws it | simstim, in the alternate screen | The agent, byte for byte |
| Job | Start or join a session in a few keys, then get out of the way | Everything else |
| Look | Our own: a signal. One column, a cyan accent, live traces, sheets that rise from the bottom | Untouched. Every row and column is the agent's. |
| Keys simstim reads | All of them | Only the detach key, and Ctrl+V when the clipboard holds an image |

[Amp's CLI](../research/amp-tui.md) was the baseline. We keep its good habits: colours from the terminal's palette, faint secondary text, a command palette, one list at a time. The rest is our own.

## 1. Principles

1. **One job per screen.** Pick a host, then pick or start a session. Each screen is one list in one column. There are no sidebars, split panes, permanent peek panes or dashboards.
2. **The session owns the terminal.** Once you attach, every row and column belongs to the agent. The client shows only two things of its own: the reconnect notice while the link is down, and system notifications (OSC 777), which draw nothing on the screen.
3. **A signal, not a box.** No frame round the list. The look is a cyan accent, braille traces of each session's output, rules instead of boxes, and sheets that rise from the bottom. It reads like a console tuned to your sessions.
4. **The terminal's own colours.** Every colour is a palette slot or the terminal's default, so simstim matches your theme. Fixed RGB is kept for the brand gradient.
5. **Motion with a purpose.** Things move when they change, are busy or need you, smoothly and in your theme's colours. Motion never holds a key back for more than a moment, and one switch turns it off.
6. **Fast paths.** The launcher draws at once and opens where you left off, so `simstim` then Enter takes you back in. A number opens a row, one line starts a session, and `/` searches every host.
7. **Plain words.** Short labels in lower case for keys, sentence case for text, no exclamation marks. Every error says what to try next.

## 2. Screens

The mockups are 80 columns wide and come from the golden tests in `internal/tui/testdata`. The column grows with the terminal, up to 116 columns, centred.

### 2.1 First run

With no hosts, the screen is a centred welcome.

```text
                            ⣀⡠⠤⢄⡀   ⣀⣀⣀    ⢀⠤⠒⠊⠉⠉⠉⠑⠢⠤⡀
                        ⢀⡠⠒⠉    ⠈⢑⢔⠉   ⠑⢄⡠⠒⠁       ⣀⢤⣚⣓⣦⣤⣀
                  ⠶⠶⠶⢶⡒⠪⢅⡀     ⢀⠔⠁ ⠑⢄  ⡠⠊⠑⡄     ⢀⠔⡩⠊⠁   ⠈⠉⠛⠓⠶⠶
                      ⠈⠑⠢⢌⡑⠢⠤⠤⠒⠁    ⢈⠦⡊   ⠈⠢⢄ ⢀⣔⠕⠊
                          ⠈⠒⠢⢄⣀⣀⣀⡠⠤⠒⠁ ⠈⠢⢄⡀ ⣀⡠⠝⠉

                                 s i m s t i m
                              jack in to any host

             enter set up   a add a host   ctrl+o commands   ? keys
```

The welcome plays as a short sequence:

1. **The mark** opens out from its middle. It is three strands of a signal woven into a ribbon that pinches at both ends, drawn in braille, in the brand gradient. Then it flows, a pulse runs along it, and the strands are brighter where they cross.
2. **The name** decodes out of braille noise, letter by letter.
3. **The tagline**, then **the keys**, come in under it. Enter opens the guided setup (2.12). `a` opens the add-host wizard, and says how many hosts your SSH config already has.

### 2.2 The launcher's layout

Every list screen has the same parts, top to bottom:

```text
  ⢄⡠⠊ simstim  › atlas-dev                      9ms · 4 sessions · 1 needs you   ← header
  ────────────────────────────────────────────────────────────────────────────   ← rule

  ▍ 1 ● auth-refact…  claude    ~/src/app        needs input      2m  ⣀⣀⣠⣴⣶⣦⣄⣀   ← the list
    2 ⢼ api-tests     codex     ~/src/api        working         14s  ⣀⣴⣶⣦⣴⣶⣶⣼
    3 ○ notes         shell     ~                idle             1h  ⣀⣀⣀⣀⣀⣀⣀⣀
    4 ✓ docs-pass     opencode  ~/src/docs       exited 0         3h  ⣀⣀⣀⣀⣀⣀⣀⣀

    ● Do you want to proceed?              ~/src/app · 6.1 of 8 GiB · s4          ← the line under the list

    ✓ Copied: simstim attach atlas-dev/s4.                                       ← notices
  ────────────────────────────────────────────────────────────────────────────   ← bottom rule
    ❯ / search every host   n start a session                                     ← the prompt line
    enter attach   space peek   r rename   esc hosts   ? keys                      ← the keys
```

- **The header** has three parts on the left: the logo (a three-cell braille wave), the name in the brand gradient, and where you are (`hosts`, `› atlas-dev`, `› search`). The facts sit on the right. A host's name decodes in when you open it.
- **The debug mark:** while the launcher's own debug is on, `debug` and the minutes left follow where you are, in cyan. An example is `debug 24m` (decision 0025). With `--debug`, the debug lasts as long as the launcher, and the mark shows no minutes. While a bundle is being written, the mark says `bundling…`. When the facts do not fit, the mark loses its minutes first.
- **The rule** draws itself from the left when the launcher starts. While something is busy, such as a host connecting, a cyan band sweeps along it.
- **Each row** has these columns:
  - the marker: a cyan `▍` and a soft cyan tint on the selected row;
  - its number, faint (1–9 open it);
  - the state glyph and the name in bold;
  - details, faint;
  - at the right, the **trace**.
- **The trace** is eight braille cells of the session's output over the last half minute. A busy agent draws a wave, a quiet one a flat line. It scrolls as time passes. It is coloured like the state: cyan for working, yellow for needs input, faint otherwise.
- **The line under the list** is about the selected row.
- **Notices** rise above the bottom rule.
- **The prompt line** is where search and the quick start happen. When idle, it says how to use them.
- **The keys** are in lower case, the key in cyan and its words faint, with wide gaps between them.

### 2.3 Hosts

```text
  ⢄⡠⠊ simstim  hosts                                     3 hosts · 1 needs you
  ────────────────────────────────────────────────────────────────────────────

  ▍ 1 ● atlas-dev   4 sessions · 1 needs you                     9ms  ⣀⣀⣀⣀⣀⣀⣀⣼
    2 ◌ aws-gpu     offline                                           ⣀⣀⣀⣀⣀⣀⣀⣀
    3 ● gcp-dev     2 sessions                                  41ms  ⣀⣀⣀⣀⣀⣀⣀⣼

    ssh atlas-dev · Debian 13 · 4 CPUs · claude, codex, opencode, shell
```

- **Each row:**
  - The state: `●` green online, the spinner while connecting, `◌` faint offline, `✕` red not ready.
  - The name, then a faint summary, in which the count that needs you is yellow. When the summary does not fit, the session count goes, so that the count that needs you stays whole.
  - The round trip, then the trace of all the host's sessions together. The round trip's column is as wide as its widest value, and at least six cells.
- **The line under the list:** how the host is reached, and what it runs.
- **Keys:** Enter (or `l`, `→`, or its number) opens the host's sessions. `n` starts a session there, `a` adds a host and `i` shows its details. With exactly one host, the launcher opens on its sessions.

**The round trip** shows in the list, in the header of a host's sessions, and in the host's details. It tells you whether the network is why keys feel slow. Here the link to atlas-dev is uneven, and the one to gcp-dev is steady:

```text
  ⢄⡠⠊ simstim  › atlas-dev                 31/100ms · 4 sessions · 1 needs you

  ▍ 1 ● atlas-dev  4 sessions · 1 needs you                 31/100ms  ⣿⣿⣿⣶⣶⣿⣿⣿
    3 ● gcp-dev    2 sessions                                42/47ms  ⣿⣿⣿⣶⣶⣿⣿⣿
```

- **What it times:** a ping on the launcher's own connection to the host. The host's helper answers it at once, so the time is the network and the helper, without the host's work. A helper that does not answer pings is timed with a list of its sessions, as before.
- **How often:** one ping to each host with each refresh of the lists, every 2 s. The launcher keeps the round trips of the last minute. It sends no ping while you are attached, and none to a host that it no longer shows.
- **What it shows:** the median, then the 90th percentile after a slash, with one unit: `31/100ms`. With two units, each number keeps its own: `310ms/1.2s`. Until there are ten round trips, or when the two numbers are the same, the median shows alone: `9ms`. With fewer than ten, the 90th percentile would be the slowest ping.
- **An uneven link:** when the 90th percentile is 50 ms or more above the median, it is yellow. One ping in ten then takes at least 50 ms longer than the median, which a typist can feel. Without colour, it still stands out from the faint text round it. The 90th percentile decides, not the slowest ping, so one late ping does not mark a link.
- **What to do about it:** an uneven link points to the network, such as Wi-Fi or a VPN. [Tracing typing lag](debug-trace.md) shows where a key waits.
- **When it changes:** the numbers change at most once every 10 s, as the single ping did before. So a ping never repaints the screen by itself. The first round trip of a connection shows at once.
- **Narrow terminals:** when a host's facts do not fit in the header or the details, the session count goes first, then the 90th percentile.

### 2.4 Sessions

```text
  ⢄⡠⠊ simstim  › atlas-dev                      9ms · 4 sessions · 1 needs you
  ────────────────────────────────────────────────────────────────────────────

    1 ● auth-refact…  claude    ~/src/app        needs input      2m  ⣀⣀⣀⣀⣀⣀⣀⣀
  ▍ 2 ⢼ api-tests     codex     ~/src/api        working         14s  ⣀⣀⣀⣀⣀⣀⣀⣼
    3 ○ notes         shell     ~                idle             1h  ⣀⣀⣀⣀⣀⣀⣀⣀
    4 ✓ docs-pass     opencode  ~/src/docs       exited 0         3h  ⣀⣀⣀⣀⣀⣀⣀⣀

    ▸ ∴ Working (14s · esc to interrupt)                      ~/src/api · s3
```

- **Each row:** the number, the state glyph, the name, the agent and folder (faint), the state in its colour, the time in that state, and the trace.
  - A working session's glyph is a tiny Game of Life in one braille cell.
  - The dot of a session that needs you breathes.
  - The state column fits the longest label, `out of memory`.
- **Order:** needs input, working, idle, then ended.
- **The live activity line** says what the selected session is doing now:
  - If it waits for you, its question, in yellow.
  - Near its memory cap, the host's warning, such as "close to its memory cap (2.7 of 2.9 GiB)". The agent's question comes first.
  - Otherwise, the most telling line of its screen, after a cyan `▸`. That line is read from the session about every 1.5 s, and borders, prompts, tips and the hints an agent always shows are skipped. A tool's result under `⎿` stays, because it says what the agent did. The line decodes in when it changes.
  - Its folder, memory and ID sit at the right.
  - If the memory cap stopped another process in the session, "a process hit the cap" follows the memory, in yellow. The agent runs on, and the row keeps its live state. The note shows only when the left still fits whole, so it never cuts a question or an activity. When there is no room, the note goes before the folder is cut.
- **Keys:**
  - Enter, or the row's number, attaches. On an empty list, Enter opens the quick start.
  - `n` opens the quick start, `space` peeks at the screen, and `r` renames in place.
  - `u` resumes an agent that has ended (2.4.1).
  - `d` deletes after a confirm, `y` copies the attach command, and `alt+e` (`opt+e` on a Mac) hides or shows ended sessions.
  - Esc (or `h`, `←`) goes back to the hosts, on the same host.
- **An agent that has ended:** a session's holder outlives its agent, so the launcher reads the agent's state, not only whether the holder answers.

  ```text
      1 ⠶ bench         gemini    /srv/bench       working          5m  ⣀⣀⣀⣀⣀⣀⣀⣼
    ▍ 2 ✕ importer      claude    /srv/app         out of memory    1h  ⣀⣀⣀⣀⣀⣀⣀⣀
      3 ✕ fuzz          shell     /srv/fuzz        killed          20m  ⣀⣀⣀⣀⣀⣀⣀⣀
      4 ◌ pairing       codex     /srv/pair        lost             2h  ⣀⣀⣀⣀⣀⣀⣀⣀

      ✕ stopped at its memory cap of 1.5 GiB   u resume            /srv/app · g3
  ```

  - The state says how the agent ended:
    - `out of memory`: the memory cap stopped it. The out-of-memory mark counts only with SIGKILL, the signal that the kernel's out-of-memory killer sends. An older holder sets the mark again after its agent exits.
    - `killed`, `terminated`, `interrupted`, `hung up` or `crashed`: a signal stopped it. `stopped` stands for any other signal.
    - `exited` and the code: it exited.
    - `lost`: its holder is gone, for example after a host restart.
  - The state is red, except `exited 0` and `lost`, which are faint.
  - The line under the list says why the agent ended, in the state's colour, such as "stopped by signal 9 (killed)" or "exited with code 2". Then it shows the next steps.
  - The next steps come in order, and each one shows only when it fits whole. A step that does not fit is left out, and a later one may still show. A key takes room from the folder and the ID, which keep a few cells. A command may take all of their room, because the line is the one place that shows it with the reason. The steps do not show in search, where a key types a letter.
  - The first step is `u resume`. It starts the agent again in the same session, with its resume command (2.4.1).
  - After a clean exit, `exited 0`, the user most likely quit the agent, so `d delete` comes first and `u resume` second.
  - For a lost agent, `u resume` comes first, then `→ simstim host restore <host> --all`, then `d delete`. The resume acts on this session at once, with one key. It also fetches the session's secrets again, which a restore cannot, and it works after restore has used its tries. The restore command brings back every session that a host restart stopped, so it follows. A delete would remove the ID that both need, so it comes last.
  - If the memory cap stopped another process before the agent ended, "a process hit the cap" follows the folder, in yellow, as for a running session. The keys among the next steps leave it room, because they also show at the bottom, and the note does not.
  - The line shows no memory. What the session still holds is not the agent's: it is the holder, and any process that the agent left.
  - Enter and the row's number go forward to the agent's last screen, in the peek, while the holder answers. Without a holder, a warning says why the agent ended, with the step to try (2.8).
  - That warning reads like the notice at the end of an attach, such as "importer on gcp-dev ended (out of memory).". After the memory cap, it has the same step, with the command for a bigger cap: "u resumes it, or run simstim resume --memory-max." For a lost agent, the step is the restore command, whole. After any other end, it is "Press u to resume it, or d to delete it."
  - The keys follow the next steps: `u resume`, then `enter how to restore` for a lost agent in the place of its command, then `d delete`. Then come `enter peek` while the holder answers, `esc hosts`, `ctrl+o commands` and `? keys`. `r` still renames, but it is rare on an agent that has ended, so it leaves the keys to `ctrl+o`. The palette offers neither attach nor the attach command, and offers the peek only while the holder answers.
  - The delete sheet says only what it removes, because no work can be lost.
- **Before the host answers:**
  - The launcher shows the sessions it remembers from the last run at once, quiet (faint), while the rule's scan says it is connecting.
  - Enter attaches straight away.
  - The line under the list says "last seen before this run" until the live list arrives ([decision 0011](decisions/0011-instant-launcher.md)).
- **Offline:** a red `✕` and the message, then a cyan `→` and the step to try:

  ```text
    ✕ Cannot reach aws-gpu: ssh: connect to host 198.51.100.4 port 22: Operation
      timed out.
    → ssh aws-gpu   (check that this works first)
    Retrying in the background.
  ```

- **Every host:** "All sessions" in the palette lists every host's sessions in one list, under each host's name. `simstim flip` opens the same list, and warns: it is an old name ([CLI spec, section 3](cli-spec.md#3-old-names-and-warnings-c5-c6-c7)). `simstim attach` with no session opens the picker instead, an inline list.

#### 2.4.1 Resume

`u` on a session whose agent has ended starts the agent again, in the same session. "Resume session" in the palette does the same.

- **The key** is a letter of the word "resume". `r` renames and `s` opens the guided setup. `e` would sit too close to `alt+e`, which shows ended sessions. So the resume takes `u`.
- **What the host does.** It stops the old holder, and its unit with any process that the agent left there. Then it starts a new holder with the agent's resume command, and the session's resume hook first. The session keeps its ID, name, folder and worktree. The cap is the one the session asked for, or the host's default.
- **Secrets.** The launcher fetches the session's secrets again, as for a start, without a prompt. The host's record names them, so it fetches only those that `hosts.toml` gives the host. When the host asks for another, `u` fetches nothing, and an error names the secret, with `simstim resume <host>/<session> --secret <name>` as the step.
- **While it starts:** a notice says "Resuming importer on gcp-dev…". After the memory cap, it also says which cap the agent gets again: "Resuming importer on gcp-dev, with its cap of 1.5 GiB…". While it runs, `u` on the same session does nothing.
- **One at a time.** The host holds a lock on the session while a resume, a restore or a delete of it runs. Another one says that the session is busy, with a hint to wait.
- **Then** the launcher attaches, as after a start, while the session is still the one selected and no sheet or prompt is open. Otherwise a notice says "Resumed importer on gcp-dev.". An error shows with its step, such as a helper that is too old.
- **On an agent that runs,** `u` says "Not available: the agent still runs.", and the palette says the same.
- **A bigger cap** needs the command line: `simstim resume <host>/<session> --memory-max 4G`.

### 2.5 Search every host

`/` from either list opens the prompt line in search mode. The list shows the sessions of every host that match. A result can match its name, agent, folder or ID, or its host's name. Matched letters are cyan and underlined.

```text
  ⢄⡠⠊ simstim  › search                                             every host
  ────────────────────────────────────────────────────────────────────────────

  ▍ 1 ● auth-refact…  claude    atlas-dev:~/sr…  needs input      2m  ⣀⣀⣀⣀⣀⣀⣀⣀
    2 ⢼ api-tests     codex     atlas-dev:~/sr…  working         14s  ⣀⣀⣀⣀⣀⣀⣀⣼
  …
    search ❯ te
    enter attach   ↑↓ move   esc close
```

Enter attaches the selected result, and Esc closes the search. On a result whose agent has ended, Enter opens its last screen while the holder answers, as in 2.4.

### 2.6 Quick start

`n` first resolves the effective default host in every launcher view. It opens the full new-session form for that host. The environment override wins over the saved default, then the only eligible non-shared host. A restored last-session view does not override this choice. A configured default that is invalid, shared, stale or unreadable shows its error and hint; it never selects another host.

When there is no default, `n` keeps the quick start for the current usable host. That host must still be saved and online. The whole session is one line: `claude ~/src/app auth`.

```text
    claude  codex  opencode  shell   ~/src/api/  ~/src/app/
  ────────────────────────────────────────────────────────────────────────────
    atlas-dev ❯ claude ~/src/api/
    enter start   tab complete   ctrl+enter detached   ctrl+f form   esc cancel
```

- **The line:** an agent the host has, a folder (any word with a `/` or `~`), and the rest as the name. `+wt` asks for a new git worktree. Each part is optional: the agent defaults to the one used last on the host, and the folder to `~`.
- **Completion:** a faint ghost after the cursor shows what Tab would add, either an agent's name or a folder on the host. The line above the rule lists the host's agents, with the one the line names lit, then the folders that match.
- **Keys:**
  - Enter starts the session and attaches, and `ctrl+enter` starts it detached.
  - `ctrl+f` opens the full form (2.7) with the line filled in.
  - While the session starts, the line shimmers "Starting claude on atlas-dev…". An error appears above the rule.
  - Esc while it starts stops the wait, here and in the form. The session can still start. A toast then says so, and the launcher neither attaches to it nor asks about it.

### 2.7 Sheets

Every dialog is a sheet. It rises from the bottom of the screen over the dimmed list, full width, with no side borders. Its title is in cyan, set into a rule across its top, its body is indented, and its keys are on its last line. A sheet's facts sit at the right of its rule.

```text
  ── New session on atlas-dev ───────────────────────────────────────────────

    Agent      claude  codex  opencode  shell  other…   not installed: anti…
    Folder     ~/src/app
    Name       auth
    Worktree   ● this folder   ○ new worktree

    enter start   tab next   ctrl+enter start detached   esc cancel
```

| Sheet | Opens on | What it holds |
| --- | --- | --- |
| New session | `n` with an effective default, `ctrl+f` in the quick start, or the palette | Agent (the chosen one lit while the row has focus), folder with completion from the host, name, worktree, wall and host. The Host row changes only this start; left and right choose a saved host. "other…" runs any command you type. |
| Delete | `d` | Red rule and title: "Delete api-tests?", what stops and what stays. For an agent that has ended, only what it removes. `simstim rm` asks with the same lines (`client.DeleteLines`). |
| Peek | `space` | The session's last screen, refreshed while the agent runs. ↑↓ move to the next session, and Enter attaches. For an agent that has ended, it shows no memory, and Enter keeps it open. |
| Commands | `ctrl+o` | The palette: a filter, then every command as plain words ("New session", "Peek at the screen") with its key in cyan. A command that cannot run says why, faint. |
| Keys | `?` | Every key that works here, in two columns, built from the live registry. It leaves out `?` itself. |
| Host details | `i` | The lines of `host show`, from one function in the client. First come the saved route, labels, machine, idle, provider script and secrets. Then come the system, the helper, the agents with their versions, and the limits. A list, such as the agents, wraps under its label when it does not fit. `t` test, `s` shell, `r` reconnect, `x` remove |
| Add a host | `a` | The wizard (2.10) |
| Set up | Enter on the first-run screen, `s` | The guided setup (2.12) |

The new-session form marks an automatic host choice as `default`. Selecting another Host row makes that an explicit choice for this start and leaves the saved default unchanged. The form checks its default or explicit saved entry again before starting. If that choice changed, it asks the user to choose again. Escape cancels a pending local host selection; a late result cannot open a form or start a session.

A click outside the palette, the peek, the host details or a confirm closes it.

### 2.8 Notices and quitting

- **Notices** are short lines that slide in from the left above the bottom rule, and fade:
  - Success is a green `✓` and the text.
  - Information is a cyan `•`.
  - A warning is a yellow `●`. When there is a step to try, as after an agent ran out of memory, a cyan `→` and the step follow.
  - An error is a red `✕`, then a cyan `→` and the step to try.

  They last 4 s, or 8 s for an error or a step to try, and two lines show at once.
- **Quitting:** `ctrl+c` turns the keys line into "ctrl+c again to quit, sessions keep running   esc stay". A second `ctrl+c` quits, and any other key stays. `q` quits at once. Quitting never stops a session.

### 2.9 Attached

The agent owns the whole terminal: every row and every column. There is no border, status bar, sidebar or session list.

- **Jack in:** with motion on, an attach starts with a 0.17 s sweep. The other rows fade, and a cyan line runs down from the chosen row. Then the terminal is the agent's.
- **Size:** the agent's PTY is the terminal's size, and follows every resize.
- **Window title:** the agent's own. simstim saves the title before attaching and puts it back on detach.
- **Detach:** Ctrl+\ goes back to the launcher, on the same session, and the rows fade back in. Press it twice quickly to send it to the agent.
- **Handoff:** the launcher cancels and joins its background requests before attaching. On return, it applies completed host removals before refreshing the lists and clears a cancelled rename. A failed reader restart after a shell, an editor or key creation exits the launcher and restores the terminal. While the launcher prepares a handoff, queued keys, clicks, scrolling and pastes cannot replace its target. Interrupted setup checks can be tried again after the launcher returns. Completed host creations stay saved.
- **Lost connection** is the only thing the client draws on the screen:
  - At once, the title shows `⧗ reconnecting · atlas-dev/s4`, and an OSC 9;4 progress mark appears.
  - After 3 s, one faint line is drawn on the bottom row: `simstim · connection lost · reconnecting · keys are not sent · ctrl+\ to leave`.
  - On reconnect, a replay or a snapshot redraws the agent's screen over the line, and the title comes back. A notice follows in the launcher.
- **System notifications** (OSC 777) tell you when another session needs you or ends, and when an image upload fails. They draw nothing on the screen.

### 2.10 Add a host

`a`, from any screen: a sheet with one step per screen, the step count at the right of its rule. Esc goes back one step. A reply to a closed setup step cannot advance a later setup. A check that times out shows its error and can be tried again.

1. **Where.** A target from `~/.ssh/config` (↑↓ pick, and typing filters), a name, and an optional extra SSH config file.
2. **Test.** Each check shows the spinner, then `✓` or `✕` with the time it took. A failure shows the error and its next step. The first check is the host key, which the wizard pins as `host add` does, before it connects (isolation spec 14.7.1). When your settings do not name the key, the step shows its fingerprint and the command that prints it on the host, with `enter pin this key` and `esc back`.
3. **Tier.** Who else has root on the host (`Only me`, `Someone else`, `A machine for one task`), setting `own`, `shared` or `throwaway`.
4. **Install the helper.** This is the consent screen when the helper is not installed yet. It says what is installed and where, what else changes (linger, terminfo), and that nothing listens on the network.
5. **Sandboxes.** After the host is saved with its pin, separate saved-host requests probe the sandbox drivers and verify the pinned runtime and images. When no optional root step is needed, the wizard runs the short sandbox test boot at once and advances on success. When optional root setup can unlock a stronger wall, the sheet pauses (`enter root step   s skip   esc close`). If the test boot fails, the floor stays unchanged (`enter try again   s continue   esc close`).
6. **Root step.** Shows each privileged step, its command and the script's SHA-256 before anything runs. On an `own` or `throwaway` host, Enter runs with `sudo` in the terminal; on a `shared` host, Enter prints the script for the owner, and `r` asks for the exact host name before running `sudo`. `p` prints, `s` skips, and Esc goes back to Sandboxes.
7. **Ready.** Shows `connect`, `linger`, `terminfo`, the agents found, the system, the memory for sessions, and the sandbox `Floor`. If no sandbox wall was verified yet, the headline says the host is saved and its sandbox wall is not ready yet, rather than claiming it is ready. When a first-class or supported agent is missing or not logged in, Enter goes on to the agents. Otherwise it starts a session.
8. **Agents.** The checklist: every agent with an installer, first-class and supported ones first. An installed agent has a green `✓`, a ticked one a cyan `●`, the rest a faint `○`, each with its state. The best-effort agents wait under a fold, `▸ 5 more agents, best effort`, which Enter or Space opens. Space ticks, and Enter installs what is ticked.

   ```text
     ── Agents on pi ──────────────────────────────────────────── step 8 of 10 ──

       Tick the agents to install on pi. Each one installs with its vendor's
       own installer, in a session you watch.

         ✓ Claude Code       installed, logged in
       ▍ ● Codex             not installed
         ○ opencode          not installed
         ○ Antigravity CLI   not installed
         ▸ 5 more agents, best effort

       space tick   enter install 1 agent   esc back
   ```

9. **Install, then log in.** One sheet for each install (step 9 of 10), then one for each agent that is installed but not logged in (step 10 of 10). Each shows the command, where it comes from, and the session it runs in (`install-codex`, `login-codex`). Enter starts the session and attaches; the user answers the installer or the login there. When they detach, or the session ends, the sheet checks the host again, and the next sheet shows how it went. `s` skips one, and Esc stops the rest.
10. **Done.** Each agent with its mark (`✓` ready, `!` not logged in, `?` login unknown, `✕` missing) and the step to try, then the command for the first session. Enter opens the new-session form with that agent chosen.

### 2.11 Fast paths

- **Where you left off.** The launcher opens on the sessions of the host you last attached to, with that session selected. `simstim` then Enter takes you back in.
- **At once.** It draws the sessions it remembers before any host answers.
- **Numbers.** 1–9 open the row with that number.
- **Search and new sessions.** `/` finds a session on any host. `n` opens the form for the effective default, or keeps the single-line quick start when there is no default and the current host is usable.
- **The mouse.** Click a row to select it, and click the selected row to open it. The wheel moves the selection, and a click on a palette row runs it.
- **The tab title** counts the sessions that need you: `● 1 · simstim · homelab`.
- **When nobody is looking.** If a session starts to need you while the terminal is in the background, the launcher sends a desktop notification.
- **From the shell.** `simstim attach -` attaches to the last session. `simstim attach auth-refactor` finds the host of a session whose name is unique.

### 2.12 Guided setup

Enter on the first-run screen, or `s` from any screen: a sheet that asks where agents will run, then hands the host to the add-host wizard (2.10), which ends with the agents and their logins. The engine behind it is shared with `simstim setup` ([decision 0020](decisions/0020-guided-setup.md)).

1. **Where.** The places, numbered. A green `●` marks one this computer can use now, a faint `○` one it cannot, with what is missing. The selected place that is not ready shows its steps under the list, with each command in cyan; Enter checks again.

   ```text
     ── Set up ──────────────────────────────────────────────────────────────────

       Where will agents run?

         1 ● A host in your SSH config           3 found: atlas-dev, gcp-dev, …
       ▍ 2 ○ A local VM, with Lima               limactl is not on your PATH
         3 ● A container, for testing (Docker)   no systemd, so no memory caps
         4 ○ A cloud sandbox, with E2B           no E2B API key found
         5 ● A server I will rent                step by step, with checks

       To make it ready:
       On macOS, with Homebrew:
         brew install lima
       or with MacPorts:
         sudo port install lima
       Other ways, such as the release archive, are in Lima's guide:
       https://lima-vm.io/docs/installation/

       enter check again   ↑↓ move   esc close
   ```

2. **A host in the SSH config** opens the wizard's first step, with the hosts listed. Esc there comes back.
3. **Lima, Docker or E2B** asks for a name, then makes the machine. The spinner shows the provider's last line of progress. Esc stops it, and a half-made Lima VM or container is deleted. The wizard then installs the helper without its consent screen, since making the host was the user's yes, and turns on idle stop for a VM.
   If creation completed before the stop, the host stays saved and appears in the list without further preparation. The notice gives the command to continue setup.

4. **A server I will rent** has one screen for each step, with the step count at the right of the rule:
   1. **An SSH key.** The key it found, or how to make one. `k` runs `ssh-keygen` in the terminal.
   2. **A server.** What to rent, and the command that prints the key to paste. Then its address.
   3. **A user for your agents.** The commands to run as root, with the address and user filled in. Enter checks that `ssh user@address` works, and the error shows when it does not.
   4. **A name for the server.** Then the lines for `~/.ssh/config`, in full. `y` adds them, `n` leaves them to the user; Enter checks that `ssh <name>` works. Then the wizard tests the host.

The sheets keep the field and its error in view on a short screen; long steps give way first, ending in `…`.

## 3. Keymap

These keys work in the launcher. You can rebind any key in `config.toml`, and `simstim config keymap` prints the effective map. On screen, keys are written in lower case, as you type them: `enter`, `esc`, `ctrl+o`, `space`, `opt+e` on a Mac.

| Key | Where | Action |
| --- | --- | --- |
| `↑` `↓` or `j` `k` | Lists | Move |
| `1`–`9` | Lists | Open the row with that number: a host's sessions, or attach |
| Click, wheel | Lists, palette | Select, open the selected row, move; run a palette command |
| `enter`, `l`, `→` | Hosts | Open the host's sessions |
| `esc`, `h`, `←` | Sessions | Back to the hosts |
| `enter` | Sessions | Attach, or open the quick start on an empty list. On a session whose agent has ended, open its last screen. |
| `n` | Anywhere | New-session form on the effective default; quick start on the current usable host only when no default exists |
| `/` | Anywhere | Search every host |
| `space` | Sessions | Peek at the screen |
| `r`, `f2` | Sessions | Rename |
| `d` | Sessions | Delete, with a confirm |
| `u` | Sessions | Resume an agent that has ended, in the same session (2.4.1) |
| `y` | Sessions | Copy the attach command |
| `w` | Anywhere | Go to the next session that needs you, on any host; press again for the one after |
| `alt+e` (`opt+e`) | Sessions | Hide or show ended sessions |
| `a` | Anywhere | Add a host |
| `s`, or `enter` on the first-run screen | Anywhere | The guided setup |
| `i` | Anywhere | Host details |
| `tab`, `ctrl+enter`, `ctrl+f` | Quick start | Complete; start detached; open the form |
| `ctrl+o` | Anywhere | Commands |
| `?` | Anywhere | Keys |
| `esc` | Sheets, prompt | Back or cancel |
| `q`, or `ctrl+c` `ctrl+c` | Anywhere | Quit. Sessions keep running. |
| `ctrl+\` | Attached | Detach. Press it twice to send it to the agent. |
| `ctrl+v` | Attached | Paste an image if the clipboard holds one; otherwise the key passes through |

The palette's debug commands, `debug: start`, `debug: stop`, `debug: save` and `debug: bundle`, have no default key. A user can bind one under `[keymap]`. No key turns debug on while attached.

## 4. Visual language

### 4.1 Palette

Every token is a terminal palette slot, the terminal's default colour, or the faint attribute. So the launcher looks the same in 256- and 16-colour terminals as in truecolour: your theme decides the colours. The truecolour column gives the colours in the reference theme, the owner's Ghostty theme, which the README images use.

| Token | Sent as | Truecolour (reference theme) | 256 colours | 16 colours | No colour | Used for |
| --- | --- | --- | --- | --- | --- | --- |
| text | default foreground | `#BFBDB6` | default | default | default | Names, body text |
| background | default background | `#0B0E14` | default | default | default | Never painted, except the tint |
| faint | SGR 2 on the text | `#656565` (half way to the background) | SGR 2 | SGR 2 | SGR 2 | Details, words in the keys, rules, the dimmed backdrop |
| accent | slot 6 | `#90E1C6` | 6 | cyan | default | Keys, sheet titles, the marker, working, the prompt, links, the scan, the debug mark |
| tint | the accent mixed 13 % into the background | `#1C292B` | none | none | none | The selected row (truecolour only: elsewhere the marker shows it) |
| success | slot 2 | `#7FD962` | 2 | green | default | `✓`, an online host |
| warning | slot 3 | `#F9AF4F` | 3 | yellow | default | Needs input, its question, its trace, an uneven round trip |
| danger | slot 1 | `#EA6C73` | 1 | red | default | `✕`, failed states, the delete sheet |
| cursor | reverse video | `#BFBDB6` block | reverse | reverse | reverse | Text fields |

**No colour.** With `NO_COLOR` set, or `TERM=dumb`, the terminal gets no colour at all, only bold, faint, underline and reverse, and motion is off. Nothing depends on colour alone: the marker `▍` shows the selection, and each state has its own glyph (`●`, `⠶`, `○`, `✓`, `✕`, `◌`).

**Tested.** `internal/tui/theme_test.go` renders the screens through each colour profile (truecolour, 256, 16, no colour, no terminal) and checks what each one sends. It also checks that every fixed colour keeps a contrast of 3:1 or more on dark and light backgrounds. The golden screens cover 60×20, 80×24 and 120×36.

### 4.2 The brand

One fixed gradient, from deep teal to electric cyan, colours the welcome mark, the name in the header and on the welcome screen, and the logo. On a light background the teal is deeper, so it keeps its contrast.

| | Dark background | Light background |
| --- | --- | --- |
| Truecolour | `#0E7C7B` to `#00E5FF` | `#045A5A` to `#0098B8` |
| 256 colours (nearest index) | 30 (`#008787`) to 45 (`#00D7FF`) | 23 (`#005F5F`) to 31 (`#0087AF`) |
| 16 colours | cyan (slot 6), no gradient | cyan (slot 6), no gradient |

### 4.3 Type and layout

- **Bold:** names, the selected command, the brand. **Faint:** everything secondary. **Underline:** matched letters in search.
- **No boxes.** Rules (`──`) divide the screen, and a sheet's title sits in its rule. The list has no frame.
- **Spacing:**
  - A two-cell gutter holds the marker.
  - The number and the glyph each take one cell, with one space after each.
  - Columns are two spaces apart, and hints three.
- **Narrow terminals:** the column keeps a two-cell margin down to 60 cells. Below 70 cells the folder column of a session list goes; the line under the list still shows the folder, and in search the host too.
- **Truncation:** `…`. Paths keep whole folders, `/home/dev/…/auth`; names are cut at the end.
- **Sheets** rise over the list, which is dimmed with the faint attribute, so the effect works with any theme. One sheet shows at a time.

### 4.4 Glyphs

| Glyph | Meaning |
| --- | --- |
| `▍` cyan | The selected row |
| `⢄⡠⠊` | The logo: a small braille wave |
| `●` green / yellow | An online host / a session that needs input (it breathes) |
| `⢼` braille, cyan | A working session: a tiny Game of Life on the cell's eight dots |
| `⠁⠂⠄⡀⢀⠠⠐⠈` | The spinner: one dot running round a braille cell |
| `⣀⣀⣴⣶⣦⣀` | A trace: output over the last half minute |
| `○` | Idle |
| `✓` green | Done, exited 0, a check passed |
| `✕` red | Failed, an error, a host that is not ready |
| `→` cyan | The step to try next |
| `▸` cyan | What the selected session is doing now |
| `◌` | Offline, lost |
| `❯` | The prompt |

### 4.5 Motion and sequences

Motion is there to show change, work in progress and things that need you. Keys act at once, whatever is moving. The one exception is the jack-in, which holds them for 0.17 s.

| Sequence | When | What happens |
| --- | --- | --- |
| Boot | The launcher opens | The header rule draws from the left (0.26 s); the rows fade in one after another (22 ms apart, 140 ms each), then the line under them |
| Slide | Into a host's sessions, or back | The list slides in, from the right going deeper and from the left coming back (0.18 s), and fades in; the host's name decodes in the header |
| Glide | The selection moves | The marker moves at once; the row's tint fades over from the old row (0.11 s) |
| Decode | A host's name, the welcome's name, a new activity line | Each letter settles out of braille noise (0.42 s) |
| Rise | A sheet opens | It slides up from the bottom edge (0.16 s) over the dimming list |
| Scan | A host connecting, a session starting, the wizard at work | A cyan band sweeps along the header rule, once every 1.6 s |
| Jack in | An attach | The other rows fade and a cyan line runs down from the chosen row (0.17 s), then the agent takes over |
| Notices | When they come and go | Slide in from the left over 150 ms and fade; fade out over the last 300 ms |
| Shimmer | "Connecting…", "Starting…" | A brighter band sweeps across the faint text |
| Breath | A session needs you | Its dot breathes, once every 1.6 s |
| Life | A session works | Its braille glyph runs a tiny Game of Life |
| Trace | Output | Each row's trace scrolls with time |
| Logo | While anything else moves | The wave in the header moves one step with each frame. When nothing else moves, it stops where it is, and later goes on from there |
| Welcome | The first-run screen | The ribbon opens out, flows and pulses (30 fps); the name decodes; the tagline and keys arrive |

**Colour.** To blend smoothly, the launcher asks the terminal for its real colours: the default foreground and background (OSC 10 and 11) and the 16 palette slots (OSC 4). Fades, the tint and the scan then mix those colours in truecolour, so they match your theme. Without truecolour, or before the terminal answers, the same motion uses steps of faint, normal and bold, and the marker alone shows the selection.

**Cost.** One frame tick runs only while something moves: 30 fps for sequences, the scan and the welcome, and 10 fps for spinners, breathing dots, working glyphs and scrolling traces. When nothing moves, nothing repaints. A ping to a host repaints nothing by itself, because the round trip's numbers change at most once every 10 s (2.3). The logo never starts a frame tick. It moves only with one, so a refresh on a still list draws it where it was. The debug mark repaints only when its minutes change, once a minute.

**One switch.** `ui.motion = auto | on | off`. `auto` turns motion off inside SSH sessions, in Emacs and JetBrains terminals, and when `NO_COLOR` or `TERM=dumb` is set. With motion off, every sequence is its last frame. Traces still show what happened, but they move only when the list refreshes.

### 4.6 Capability probe

`simstim doctor terminal` sends one batch of queries, with DA1 as the end marker. It covers:

- the kitty keyboard protocol, synchronized output and OSC 52;
- XTVERSION;
- the OSC 10, 11 and 12 colours;
- modes 2027 and 2031.

It shows what the agent will get through the pass-through.

## 5. Feedback

**Notices** (2.8):

- "Detached. auth-refactor keeps running on atlas-dev."
- "Reconnected to atlas-dev."
- "Copied: simstim attach atlas-dev/s4." The command names the session by its host and its ID.

A repeat extends the notice instead of adding a line.

**Errors:**

- Every error has a message and one next step.
- In the launcher and in sheets, an error is a red `✕` and the message, then a cyan `→` and the step.
- On the command line it prints as a bold red `Error:` and the message, a blank line, then the step:

  ```text
  Error: cannot reach atlas-dev through the jump host bastion.

  Try: simstim host test atlas-dev --verbose
  ```

**Empty states:** a faint statement, a blank row, then a hint whose key is cyan. "No sessions on gcp-dev yet." / "n start one from a single line, like: claude ~/src/app". "Nothing matches 'xyz'."

**Copy rules:**

- Use sentence case and plain words, with no exclamation marks.
- Say what keeps running: "Quit. Sessions keep running."
- Name the host and the session in every message.

## 6. CLI

The launcher is the home, and every action also has a command, for scripts and terminal keybindings.

The root help groups the commands as this table does: Sessions, Hosts, Agents, and Setup and help ([CLI spec, 8.1](cli-spec.md#81-the-root-help)). Every command has an example in its help, and `simstim help concepts` explains the words that the tool uses.

An address names a session: `<host>/<session>`, or `<session>` alone when no other host has a session of that name, or `-` for the last session attached to ([CLI spec, section 9.2](cli-spec.md#92-addresses)).

A new session without a host uses `SIMSTIM_DEFAULT_HOST`, then the saved choice of `host default`, then the only eligible non-shared host. `new -` uses the still-saved host of the last attached session. A shared host cannot be the default. The launcher uses the same local resolver, and its last-session cache grants no permission to start work. Changing the Host row changes only that form's target. Every start still uses its actual accepted host connection and current checks.

`host default <host>` sets the saved choice, `host default` prints it, and `host default --clear` clears it. Local host selection reads no room estimate and starts or wakes no machine. The chosen host still admits or refuses the actual session.

| Command | Does |
| --- | --- |
| `simstim [flags]` | Run any coding agent on any host, and use it as if it ran here. [Reference](../reference/cli/_index.md) |
| `simstim agents [flags]` | List the agents, install one on a host, and log in to it. [Reference](../reference/cli/agents.md) |
| `simstim agents install <agent> [<host>] [flags]` | Install an agent on a host with its official installer. [Reference](../reference/cli/agents_install.md) |
| `simstim agents login <agent> [<host>] [flags]` | Start an agent's own login on a host, in a session you can answer. [Reference](../reference/cli/agents_login.md) |
| `simstim agents ls [flags]` | List the agents in the agent table, with how each one installs. [Reference](../reference/cli/agents_ls.md) |
| `simstim attach [<host>/<session>\|-] [flags]` | Attach to a session. [Reference](../reference/cli/attach.md) |
| `simstim completion [flags]` | Print the shell completion script for bash, zsh, fish or PowerShell. [Reference](../reference/cli/completion.md) |
| `simstim completion bash [flags]` | Print the completion script for bash. [Reference](../reference/cli/completion_bash.md) |
| `simstim completion fish [flags]` | Print the completion script for fish. [Reference](../reference/cli/completion_fish.md) |
| `simstim completion powershell [flags]` | Print the completion script for PowerShell. [Reference](../reference/cli/completion_powershell.md) |
| `simstim completion zsh [flags]` | Print the completion script for zsh. [Reference](../reference/cli/completion_zsh.md) |
| `simstim config [flags]` | Show the configuration. [Reference](../reference/cli/config.md) |
| `simstim config check [flags]` | Find mistakes in the config files, each with its file and line. [Reference](../reference/cli/config_check.md) |
| `simstim config keymap [flags]` | Print the launcher's commands and their keys. [Reference](../reference/cli/config_keymap.md) |
| `simstim config path [flags]` | Print where the configuration lives. [Reference](../reference/cli/config_path.md) |
| `simstim cp <source> <destination> [flags]` | Copy files between this computer and a running sandbox. [Reference](../reference/cli/cp.md) |
| `simstim debug [flags]` | Record why a session goes wrong: debug files, profiles and traces. [Reference](../reference/cli/debug.md) |
| `simstim debug bundle [<host>\|<host>/<session>] [flags]` | Write one archive of the debug files, with private values replaced. [Reference](../reference/cli/debug_bundle.md) |
| `simstim debug profile <host>/<session> cpu\|heap\|goroutine [flags]` | Write a profile of the session's process, for go tool pprof. [Reference](../reference/cli/debug_profile.md) |
| `simstim debug save <host>/<session> [flags]` | Save the session's recent debug record, just after a lag. [Reference](../reference/cli/debug_save.md) |
| `simstim debug start <host>/<session> [flags]` | Turn on the debug of a running session, for 30 minutes or `--for`. [Reference](../reference/cli/debug_start.md) |
| `simstim debug stop <host>/<session> [flags]` | Turn off the debug of a session at once. [Reference](../reference/cli/debug_stop.md) |
| `simstim demo [flags]` | Try the launcher with sample data (nothing connects anywhere). [Reference](../reference/cli/demo.md) |
| `simstim doctor [flags]` | Check this machine and its terminal. [Reference](../reference/cli/doctor.md) |
| `simstim doctor keys [flags]` | Show each key as the terminal sends it, and the client's own keys. [Reference](../reference/cli/doctor_keys.md) |
| `simstim doctor terminal [flags]` | Ask this terminal what it supports: keys, colours and more. [Reference](../reference/cli/doctor_terminal.md) |
| `simstim exec <host>/<session> [--timeout <duration>] -- <command>… [flags]` | Run one command inside a session's sandbox. [Reference](../reference/cli/exec.md) |
| `simstim events [<host>\|<host>/<session>] [flags]` | Read recent session events, or follow them as they happen. [Reference](../reference/cli/events.md) |
| `simstim fork <host>/<session> [flags]` | Copy a sandbox into independent sessions. [Reference](../reference/cli/fork.md) |
| `simstim forward <host>/<session> [<port>…] [flags]` | Forward chosen session ports, or one laptop port in reverse. [Reference](../reference/cli/forward.md) |
| `simstim help [<command>] [flags]` | Show the help of a command or a topic. [Reference](../reference/cli/help.md) |
| `simstim host [flags]` | Add, create, list, test and remove hosts. [Reference](../reference/cli/host.md) |
| `simstim host add <name> [<ssh-target>] [flags]` | Connect a host: check it, install the helper, prepare it. [Reference](../reference/cli/host_add.md) |
| `simstim host alerts <host> [flags]` | Show where a host sends alerts, and the last deliveries. [Reference](../reference/cli/host_alerts.md) |
| `simstim host alerts log <host> [flags]` | Show the host's last alert deliveries, and why any were not sent. [Reference](../reference/cli/host_alerts_log.md) |
| `simstim host alerts off <host> [flags]` | Turn alerts off on a host: remove its alerts file. [Reference](../reference/cli/host_alerts_off.md) |
| `simstim host alerts set <host> [flags]` | Choose where a host sends alerts, and which ones. [Reference](../reference/cli/host_alerts_set.md) |
| `simstim host alerts test <host> [flags]` | Send a test alert to every destination now, and say what each did. [Reference](../reference/cli/host_alerts_test.md) |
| `simstim host create <provider> [<name>] [flags]` | Make a host with lima, docker or e2b, and connect it. [Reference](../reference/cli/host_create.md) |
| `simstim host default [<host>] [flags]` | Show or choose the default host for new sessions. [Reference](../reference/cli/host_default.md) |
| `simstim host doctor <host> [flags]` | Check a host's setup, and with `--fix` fix what it can. [Reference](../reference/cli/host_doctor.md) |
| `simstim host invite <name> [flags]` | Make a public invite for a machine to prepare itself. [Reference](../reference/cli/host_invite.md) |
| `simstim host join [flags]` | Prepare this machine from a laptop invite on standard input. [Reference](../reference/cli/host_join.md) |
| `simstim host leave [flags]` | Undo this machine's original root-owned join ledger. [Reference](../reference/cli/host_leave.md) |
| `simstim host ls [flags]` | List hosts. [Reference](../reference/cli/host_ls.md) |
| `simstim host prepare <host> [flags]` | Probe a host, prepare its runtime and test its sandbox wall. [Reference](../reference/cli/host_prepare.md) |
| `simstim host rekey <host> [flags]` | Replace a host's pinned host key, after you confirm. [Reference](../reference/cli/host_rekey.md) |
| `simstim host restore <host> [flags]` | Start again the sessions that a host restart stopped. [Reference](../reference/cli/host_restore.md) |
| `simstim host rm <host> [flags]` | Forget a host, or delete a created machine. [Reference](../reference/cli/host_rm.md) |
| `simstim host set <host> <key>=<value>... [flags]` | Change a host's settings, such as idle or tier. [Reference](../reference/cli/host_set.md) |
| `simstim host show <host> [flags]` | Describe a host. [Reference](../reference/cli/host_show.md) |
| `simstim host start <host> [flags]` | Start the host through its provider or provider script. [Reference](../reference/cli/host_start.md) |
| `simstim host status <host> [flags]` | Ask the provider or provider script whether the host is running. [Reference](../reference/cli/host_status.md) |
| `simstim host stop <host> [flags]` | Stop the host through its provider or provider script. [Reference](../reference/cli/host_stop.md) |
| `simstim host test <host> [flags]` | Check that a host answers, and how fast. [Reference](../reference/cli/host_test.md) |
| `simstim host upgrade <host> [flags]` | Install this client's helper on a host. [Reference](../reference/cli/host_upgrade.md) |
| `simstim image [flags]` | List, pull, save and remove microvm root images. [Reference](../reference/cli/image.md) |
| `simstim image ls [<host>] [flags]` | List a host's prepared images. [Reference](../reference/cli/image_ls.md) |
| `simstim image pull <ref> <host> [flags]` | Pin an OCI image on this laptop and prepare it on a host. [Reference](../reference/cli/image_pull.md) |
| `simstim image rm <digest> <host> [flags]` | Remove an image that no session uses. [Reference](../reference/cli/image_rm.md) |
| `simstim image save <host>/<session> <ref> [flags]` | Save a running microvm's system changes as an image on its host. [Reference](../reference/cli/image_save.md) |
| `simstim ls [<host>] [flags]` | List sessions on every host, or on one. [Reference](../reference/cli/ls.md) |
| `simstim new [<host>\|-] [-- <command>…] [flags]` | Start a session on a host, and attach to it. [Reference](../reference/cli/new.md) |
| `simstim policy [flags]` | Read and change sandbox policy at any layer. [Reference](../reference/cli/policy.md) |
| `simstim policy allow [net] [<target>] [flags]` | Add an allow rule at one policy layer. [Reference](../reference/cli/policy_allow.md) |
| `simstim policy apply [flags]` | Preview and apply current policy files to running sandboxes. [Reference](../reference/cli/policy_apply.md) |
| `simstim policy check <target> [flags]` | Explain which rule and layer decides a destination. [Reference](../reference/cli/policy_check.md) |
| `simstim policy deny [net] <target> [flags]` | Add an deny rule at one policy layer. [Reference](../reference/cli/policy_deny.md) |
| `simstim policy edit [flags]` | Edit one policy file, check it and preview its live changes. [Reference](../reference/cli/policy_edit.md) |
| `simstim policy learn <host>/<session> [flags]` | Turn selected observed network destinations into policy rules. [Reference](../reference/cli/policy_learn.md) |
| `simstim policy lockdown [flags]` | Pause session allowances and refuse unknown network destinations. [Reference](../reference/cli/policy_lockdown.md) |
| `simstim policy log --in <host>/<session> [flags]` | Read one session's network decisions. [Reference](../reference/cli/policy_log.md) |
| `simstim policy ls [flags]` | Show the merged policy and each value's layer. [Reference](../reference/cli/policy_ls.md) |
| `simstim policy refuse <request-ID> [flags]` | Refuse one held network request by its opaque ID. [Reference](../reference/cli/policy_refuse.md) |
| `simstim policy rm <id or target> [flags]` | Remove a rule from one policy layer. [Reference](../reference/cli/policy_rm.md) |
| `simstim ports <host>/<session> [flags]` | List the TCP ports a session's processes listen on. [Reference](../reference/cli/ports.md) |
| `simstim prune [<host>] [flags]` | Remove stopped sessions with no unpulled commits. [Reference](../reference/cli/prune.md) |
| `simstim pull <host>/<session> [flags]` | Bring a session's branch into the git repository you are in. [Reference](../reference/cli/pull.md) |
| `simstim rename <host>/<session> <name> [flags]` | Rename a session. [Reference](../reference/cli/rename.md) |
| `simstim resume <host>/<session> [flags]` | Start a session's agent again after it ended, and attach to it. [Reference](../reference/cli/resume.md) |
| `simstim rewind <host>/<session> [<n>] [flags]` | List disk checkpoints or return to one. [Reference](../reference/cli/rewind.md) |
| `simstim rm [<host>/<session>\|-] [flags]` | Delete a session, after a question. [Reference](../reference/cli/rm.md) |
| `simstim send <host>/<session> <text> [flags]` | Type text into a session without attaching, then press Enter. [Reference](../reference/cli/send.md) |
| `simstim setup [flags]` | Set up step by step: a host, its agents, and their logins. [Reference](../reference/cli/setup.md) |
| `simstim shell [<host>/<session>\|-] [flags]` | Open a shell inside a session's sandbox. [Reference](../reference/cli/shell.md) |
| `simstim show [<host>/<session>\|-] [flags]` | Describe one session, or print its last screen with `--screen`. [Reference](../reference/cli/show.md) |
| `simstim ssh-config [flags]` | Prepare the local SSH configuration for sandbox tools. [Reference](../reference/cli/ssh-config.md) |
| `simstim stop <host>/<session> [flags]` | Stop a sandbox and keep its work and conversation. [Reference](../reference/cli/stop.md) |
| `simstim version [flags]` | Print the version. [Reference](../reference/cli/version.md) |

The table has one row for each current command. `--no-input`, `--no-color`,
`--debug` and `--debug-file` apply throughout the tree. `SIMSTIM_NO_INPUT` and
`NO_COLOR` turn questions or colour off too: with input off, a question that
needs an answer fails with exit code 4, and names the flag that answers it
([CLI spec, section 6](cli-spec.md#6-prompts-pickers-and-input-c4)). The
command reference lists their flags and the kept aliases. Hidden old forms warn
and are not offered by completion.

**Flags** mean one thing everywhere ([the CLI spec, section 10](cli-spec.md#10-flags-c6)). A list takes the flag again or commas, and a size takes a unit. An old form still works for two releases or 12 months, whichever is later, and prints one warning on stderr.

**Exit codes** follow [the CLI spec, section 5](cli-spec.md#5-exit-codes-c2). `simstim help exit-codes` prints them:

| Code | Meaning |
| --- | --- |
| 0 | Done |
| 1 | Failed: the command could not do its job, for example because a host did not answer |
| 2 | Wrong usage: an unknown command, flag or key, a missing argument, or a bad value |
| 3 | Not found: no host, session or agent of that name |
| 4 | Needs an answer: a prompt with input off, such as `rm` without `-y` in a script |
| 5 | Refused by a policy or a tier |
| 130 | Cancelled |

`exec` returns the exit code of the command that it ran.

## 7. Config

```toml
# ~/.config/simstim/config.toml
[ui]
motion = "auto"          # auto | on | off

[attach]
detach_key = "ctrl+\\"
image_paste_key = "ctrl+v"
scrollback = "keep"      # keep | clear: what to do with local scrollback on a fresh attach

[sessions]
memory_max = "auto"      # auto = the host's memory minus a reserve (0024), or a size such as "4G"

[keymap]
"session: delete" = "d"
"session: rename" = ["r", "f2"]
```

```toml
# ~/.config/simstim/hosts.toml
[[host]]
name = "atlas-dev"
ssh = "atlas-dev"           # a Host from ~/.ssh/config, used as is
labels = { provider = "homelab" }
# provider = { script = "~/.config/simstim/providers/gcp.sh" }
```

`simstim config check` finds the mistakes in both files ([CLI spec, section 13](cli-spec.md#13-config-check-c9)). A `config.toml` that does not decode, or cannot be read, stops no command: each one prints one warning with the line, and uses the defaults. The launcher shows the warning as a notice. After an edit in its settings editor that breaks the file, the launcher keeps the settings it had. A `hosts.toml` that does not decode stops each command that reads it, with exit code 2.

## 8. Not built yet

These parts of the spec are planned, and are not in the current build:

- using the capability probe for the launcher's own drawing (the probe itself is `doctor terminal`);
- live light and dark refresh through mode 2031 (the launcher reads OSC 11 once, at start);
- the ⧗ mark for hosts with a time limit (GCP IAP, AWS EICE);
- the disk check in the add-host wizard's test step;
- editing a host from host details.

Parts of the exit codes are not built yet either. A prompt with input off, such as `host destroy` in a script, reads no answer as no and exits with 130, not 4. Task C4 of [the CLI plan](cli-plan.md) builds that. Until that plan builds policies and tiers, no command exits with 5.

### Held network requests

The policy sheet reads held network requests on its original accepted session
connection. It keeps the complete bounded snapshot and draws held rows before
ordinary rules, in yellow, with their opaque IDs. Matching masked labels never
merge rows. Selection follows an unchanged ID across a refreshed row order.
A zero-waiter request keeps its original deadline and says it waits for a retry.

On a held row, `x` refuses the selected opaque request and `r` refreshes the
complete snapshot. Delete cannot remove an ordinary rule through a held row.
A failed or stale refresh retains visible stale state and blocks actions.
Closing the sheet or changing its connection retires queued work; a late reply
cannot replace another sheet's state. Older helpers supply no invented list.
