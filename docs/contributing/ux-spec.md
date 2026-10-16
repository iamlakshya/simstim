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

1. **One job per screen.** Pick a host, then pick or start a session. Each screen is one list in one column. There are no sidebars, split panes, permanent previews or dashboards.
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

    ✓ Copied: simstim attach atlas-dev/auth-refactor.                            ← notices
  ────────────────────────────────────────────────────────────────────────────   ← bottom rule
    ❯ / search every host   n start a session                                     ← the prompt line
    enter attach   space peek   r rename   esc hosts   ? keys                      ← the keys
```

- **The header** has three parts on the left: the logo (a three-cell braille wave), the name in the brand gradient, and where you are (`hosts`, `› atlas-dev`, `› search`). The facts sit on the right. A host's name decodes in when you open it.
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

- **Every host:** `simstim flip`, or "All sessions" in the palette, lists every host's sessions in one list, under each host's name.

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

`n` opens the prompt line for a new session on the host in play. The whole session is one line: `claude ~/src/app auth`.

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
| New session | `ctrl+f` in the quick start, or the palette | Agent (the chosen one lit while the row has focus), folder with completion from the host, name, and worktree. "other…" runs any command you type. |
| Delete | `d` | Red rule and title: "Delete api-tests?", what stops and what stays. For an agent that has ended, only what it removes. |
| Peek | `space` | The session's last screen, refreshed while the agent runs. ↑↓ move to the next session, and Enter attaches. For an agent that has ended, it shows no memory, and Enter keeps it open. |
| Commands | `ctrl+o` | The palette: a filter, then every command as plain words ("New session", "Peek at the screen") with its key in cyan. A command that cannot run says why, faint. |
| Keys | `?` | Every key that works here, in two columns, built from the live registry. It leaves out `?` itself. |
| Host details | `i` | Route, system, helper, agents, limits; `t` test, `s` shell, `r` reconnect, `x` remove |
| Add a host | `a` | The wizard (2.10) |
| Set up | Enter on the first-run screen, `s` | The guided setup (2.12) |

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
- **Lost connection** is the only thing the client draws on the screen:
  - At once, the title shows `⧗ reconnecting · atlas-dev/s4`, and an OSC 9;4 progress mark appears.
  - After 3 s, one faint line is drawn on the bottom row: `simstim · connection lost · reconnecting · keys are not sent · ctrl+\ to leave`.
  - On reconnect, a replay or a snapshot redraws the agent's screen over the line, and the title comes back. A notice follows in the launcher.
- **System notifications** (OSC 777) tell you when another session needs you or ends, and when an image upload fails. They draw nothing on the screen.

### 2.10 Add a host

`a`, from any screen: a sheet with one step per screen, the step count at the right of its rule. Esc goes back one step.

1. **Where.** A target from `~/.ssh/config` (↑↓ pick, and typing filters), a name, and an optional extra SSH config file.
2. **Test.** Each check shows the spinner, then `✓` or `✕` with the time it took. A failure shows the error and its next step.
3. **Install the helper.** This is the consent screen. It says what is installed and where, what else changes (linger, terminfo), and that nothing listens on the network.
4. **Ready.** The agents found, the system, and the memory for sessions. The host's sessions are now open behind the sheet. When a first-class or supported agent is missing or not logged in, Enter goes on to the agents. Otherwise it starts a session.
5. **Agents.** The checklist: every agent with an installer, first-class and supported ones first. An installed agent has a green `✓`, a ticked one a cyan `●`, the rest a faint `○`, each with its state. The best-effort agents wait under a fold, `▸ 5 more agents, best effort`, which Enter or Space opens. Space ticks, and Enter installs what is ticked.

   ```text
     ── Agents on pi ───────────────────────────────────────────── step 5 of 6 ──

       Tick the agents to install on pi. Each one installs with its vendor's
       own installer, in a session you watch.

         ✓ Claude Code       installed, logged in
       ▍ ● Codex             not installed
         ○ opencode          not installed
         ○ Antigravity CLI   not installed
         ▸ 5 more agents, best effort

       space tick   enter install 1 agent   esc back
   ```

6. **Install, then log in.** One sheet for each install, then one for each agent that is installed but not logged in. Each shows the command, where it comes from, and the session it runs in (`install-codex`, `login-codex`). Enter starts the session and attaches; the user answers the installer or the login there. When they detach, or the session ends, the sheet checks the host again, and the next sheet shows how it went. `s` skips one, and Esc stops the rest.
7. **Done.** Each agent with its mark (`✓` ready, `!` not logged in, `?` login unknown, `✕` missing) and the step to try, then the command for the first session. Enter opens the new-session form with that agent chosen.

### 2.11 Fast paths

- **Where you left off.** The launcher opens on the sessions of the host you last attached to, with that session selected. `simstim` then Enter takes you back in.
- **At once.** It draws the sessions it remembers before any host answers.
- **Numbers.** 1–9 open the row with that number.
- **Search and quick start.** `/` finds a session on any host; `n` starts one from a single line.
- **The mouse.** Click a row to select it, and click the selected row to open it. The wheel moves the selection, and a click on a palette row runs it.
- **The tab title** counts the sessions that need you: `● 1 · simstim · homelab`.
- **When nobody is looking.** If a session starts to need you while the terminal is in the background, the launcher sends a desktop notification.
- **From the shell.** `simstim attach -` attaches to the last session.

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
| `n` | Anywhere | Quick start on the host in play |
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

## 4. Visual language

### 4.1 Palette

Every token is a terminal palette slot, the terminal's default colour, or the faint attribute. So the launcher looks the same in 256- and 16-colour terminals as in truecolour: your theme decides the colours. The truecolour column gives the colours in the reference theme, the owner's Ghostty theme, which the README images use.

| Token | Sent as | Truecolour (reference theme) | 256 colours | 16 colours | No colour | Used for |
| --- | --- | --- | --- | --- | --- | --- |
| text | default foreground | `#BFBDB6` | default | default | default | Names, body text |
| background | default background | `#0B0E14` | default | default | default | Never painted, except the tint |
| faint | SGR 2 on the text | `#656565` (half way to the background) | SGR 2 | SGR 2 | SGR 2 | Details, words in the keys, rules, the dimmed backdrop |
| accent | slot 6 | `#90E1C6` | 6 | cyan | default | Keys, sheet titles, the marker, working, the prompt, links, the scan |
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

**Cost.** One frame tick runs only while something moves: 30 fps for sequences, the scan and the welcome, and 10 fps for spinners, breathing dots, working glyphs and scrolling traces. When nothing moves, nothing repaints. A ping to a host repaints nothing by itself, because the round trip's numbers change at most once every 10 s (2.3). The logo never starts a frame tick. It moves only with one, so a refresh on a still list draws it where it was.

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
- "Copied: simstim attach atlas-dev/auth-refactor."

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

| Command | Does |
| --- | --- |
| `simstim` | Opens the launcher |
| `simstim setup [--host h \| --create lima\|docker\|e2b] [--name n] [--agents a,b] [--yes] [--no-login]` | The guided setup: where agents run, the host, the agent checklist, the installs and the logins, then the first session's command |
| `simstim host add <name> [target] [--ssh-config f] [--provider-script s --wake] [--yes]` | Connects a host: probe, consent, install, setup, doctor; in a terminal, the agent checklist when an agent is missing |
| `simstim host ls`, `test`, `doctor`, `rm` | Host management |
| `simstim host restore <name> [--all]` | Starts again the sessions that a host restart stopped (or, with `--all`, a crash) |
| `simstim host start\|stop\|status <name>` | Runs the host's provider script |
| `simstim new <host> [--agent claude] [--dir ~/src/app [--mkdir]] [--name x] [--worktree [--base ref]] [--memory-max 4G] [--detach] [-- cmd…]` | Starts a session, and attaches unless `--detach` |
| `simstim ls [--host h] [--json]` | Lists sessions, with their state and where the state came from, and why an agent ended: `errored (out of memory)` |
| `simstim attach <host>/<session>`, `attach -` | Attaches directly; `-` is the last session. Good for a Ghostty keybinding. To a session whose agent has ended, it says why and what to try, as at the end of an attach. |
| `simstim resume <host>/<session> [--memory-max 4G] [--secret NAME] [--detach]` | Starts an ended agent again in its session, with its resume command, and attaches unless `--detach`. Without `--memory-max`, the cap is the one the session asked for. |
| `simstim flip [<host>/<session>]` | A quick switch: the sessions of every host in one list, then attach |
| `simstim send <host>/<session> <text> [--no-enter]` | Types text into a session without attaching |
| `simstim rename <host>/<session> <name>`, `kill <host>/<session>` | Renames; stops and removes (keeping a worktree that has work) |
| `simstim ports <host>/<session>`, `forward <host>/<session> [port…]` | Lists the ports a session listens on; forwards them to localhost |
| `simstim demo [--empty]` | The launcher on sample data; nothing connects anywhere |
| `simstim doctor keys` | Prints each key event as the terminal sends it, and marks the client's own keys |
| `simstim doctor terminal` | Asks the terminal what it supports: kitty keys, synchronized output, colours, and more |
| `simstim --debug[=<categories>] <command>` | Turns on the debug mode for one command, or the launcher: every category, or those listed (`attach`, `net`, `memory`, `launcher`, `runtime`). Prints the debug file's path on stderr first |
| `simstim --debug-file <file> <command>` | Names the client's debug file, and turns on every category. `SIMSTIM_DEBUG_FILE` does the same |
| `simstim debug start <host>/<session> [--for 30m] [--only <categories>]`, `debug stop <host>/<session>` | Turns the debug of a running session on, until `debug stop` or the time limit, or off at once |
| `simstim debug save <host>/<session>` | Saves the last seconds of the session's flight recorder on the host |
| `simstim debug profile <host>/<session> cpu\|heap\|goroutine [--for 30s]` | Writes a profile of the session's holder on the host. `--for` is for `cpu` only |
| `simstim version`, `completion zsh\|bash\|fish` | Housekeeping |

**Exit codes:** 0 for success, 1 for a user error, 2 for a host or network error, 3 for a session or host that is not found.

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

## 8. Not built yet

These parts of the spec are planned, and are not in the current build:

- using the capability probe for the launcher's own drawing (the probe itself is `doctor terminal`);
- live light and dark refresh through mode 2031 (the launcher reads OSC 11 once, at start);
- the ⧗ mark for hosts with a time limit (GCP IAP, AWS EICE);
- the disk check in the add-host wizard's test step;
- editing a host from host details.
