# The command line: spec

**Status:** proposed, 10 November 2026. **Plan:** [cli-plan.md](cli-plan.md). **Decision:** [0033](decisions/0033-one-command-grammar.md), accepted. **Research:** [the CLI study](../research/cli-dx.md).

This spec says what to build so that the command line is easy to guess, hard to misuse and consistent. [The plan](cli-plan.md) says in what order. A coding agent builds it one pull request at a time, under [AGENTS.md](../../AGENTS.md) and [CONTRIBUTING.md](../../CONTRIBUTING.md). Task IDs such as C2 name tasks in the plan. Each section names the tasks that it serves, such as "(C2)". [Traceability](#18-traceability) lists each task with its sections and its research.

[The isolation spec](isolation-spec.md) adds the commands of isolated sessions in its section 9. Those commands follow this spec from the start. This spec gives the shared rules: the grammar, errors, exit codes, prompts, output, help, flags and completion. The isolation spec says what each of its own commands does. Where the two specs touch, this spec names the isolation task.

Facts about this repository come from its code on 10 November 2026. Facts about other tools link a primary source. A fact that no primary source confirmed carries the mark UNVERIFIED, and names the task step that checks it first.

**Words.**

- `<name>` is `brand.Name`, today `simstim`. Commands are written as users type them, `simstim …`. Code builds every name from `internal/brand` ([0007](decisions/0007-name-in-one-place.md)). The variable `SIMSTIM_NO_INPUT` is `brand.Env("NO_INPUT")` in code.
- **Input is on** when stdin, stdout and stderr are all terminals, and the user did not turn input off ([section 6](#6-prompts-pickers-and-input-c4)). Otherwise **input is off**.
- **The session cache** is the launcher's memory of [0011](decisions/0011-instant-launcher.md): each host's sessions when last seen, and the session last attached to. It is `launcher.json` in the state folder, with the last session in `last.json` beside it (`internal/client/seen.go`, since C5).
- **An address** names a session: `<host>/<session>`, a bare `<session>`, or `-` ([section 9.2](#92-addresses)).
- **The end** of an old name is the point after which it stops working ([section 3](#3-old-names-and-warnings-c5-c6-c7)).
- **A release** is a new minor or major version, such as 0.2.0 after 0.1.2. A patch version is not a release in this spec.

## 1. Scope

In scope:

- The shared parts that every command uses: errors, suggestions, exit codes, prompts, pickers, machine output, help, flags and completion.
- The changes to today's commands that [the CLI study](../research/cli-dx.md#9-the-command-map) proposes, with warned aliases for the old names.
- `config check`, `agents login`, `host set`, `host show`, `host upgrade` and `show`.
- Docs for coding agents: `llms.txt`, a Markdown copy of each page, and a snippet for a project's `AGENTS.md`.

Not in scope:

- What the commands of the isolation spec do. Section 9 of that spec owns them.
- The launcher's look. [The UX spec](ux-spec.md) owns it.
- A server, a port or telemetry. Nothing in this spec listens or sends data anywhere ([AGENTS.md](../../AGENTS.md), rule 4).

## 2. The grammar (C1)

Record [0033](decisions/0033-one-command-grammar.md) states the grammar, and [the CLI study, section 8](../research/cli-dx.md#8-the-grammar-we-propose) gives its 14 rules, G1 to G14, with their sources. In short:

- **Two shapes.** Session verbs stay at the top level. Every other object is a noun with verbs under it: `host`, `policy`, `agents`, `image`, `debug` and `config`.
- **One verb, one meaning.** `ls` lists, `show` describes one object, `add` and `rm` change a collection, and `set` changes named settings. `rm` deletes, and `stop` keeps the files.
- **One address.** A session is `<host>/<session>`. The host part may be left out when the name is unique. `-` means the last session.
- **Forgiving verbs.** No command refuses only because a session is in another state.
- **One meaning for each flag**, and prompts only when input is on.
- **Data on stdout**, errors with a fix, and exit codes by kind.
- **Help with examples**, and safe renames.

This spec does not change the rules. The sections below build them.

### 2.1 The top level

The root holds only the words in this table. `TestTopLevel` in `internal/cli` walks `Root()`, and fails on any other command at the top level.

| Group | Commands | Tasks |
| --- | --- | --- |
| Sessions | `new`, `attach`, `resume`, `ls`, `show`, `rm`, `rename`, `send`, `ports`, `forward`, `pull` | today, C5 |
| Sessions, from the isolation plan | `stop`, `shell`, `exec`, `cp`, `fork`, `rewind`, `prune`, `events` | P1, P1.13, P1.16, P5 |
| Hosts | `host` | today |
| Policy | `policy` | P1.8 |
| Images | `image` | P3 |
| Agents | `agents` | today |
| Setup and help | `setup`, `doctor`, `debug`, `config`, `demo`, `version`, `completion`, `help`, `ssh-config`, `ssh-proxy` | today, C10, P2.15 |

A hidden command that carries an old name ([section 3](#3-old-names-and-warnings-c5-c6-c7)) is not in the table, and the test skips it. So is a help topic of [section 8.3](#83-help-topics), which only prints itself.

### 2.2 Depth

No command is deeper than two levels, with one exception. `host alerts set`, `test`, `log` and `off` sit at three levels today. They stay until a decision record moves them (question C-12, [section 17](#17-open-questions)). `TestDepth` lists `host alerts` as the only exception.

### 2.3 Tests

- C1 changes only the record. Its status is accepted, or a new record replaces it, and `just docs-check` passes.
- C3 adds `TestDepth`, and the groups of [section 8.1](#81-the-root-help). C5 adds `TestTopLevel`, because `kill` and `flip` stay visible until C5 hides them.

## 3. Old names and warnings (C5, C6, C7)

An old name keeps working for a time, and warns. This section gives the mechanism and every old name. Sections 9, 10 and 11 say what the new forms do.

### 3.1 Behaviour

1. One table in `internal/cli/deprecated.go` holds every old form. Each row has the old form, the new form, the kind, the command whose reference page lists it, and the version that adds the warning. The day of that release is in the same file, and the release step sets it, because the 12 months start when users first see the warning. C4 makes the file, with the row of `host ls --json`. Each later task of section 3.2 adds its rows.
2. When a user types an old form, the command does what the old form did. It also prints one warning on stderr, once per run.
3. The warning has this form, in yellow when stderr is a terminal:

   ```text
   Warning: "kill" is now "rm". "kill" works until v0.4.0 and 10 November 2027, whichever comes later.
   ```

4. The end is two releases after the version that adds the warning, and 12 months after its date, whichever comes later. Kubernetes uses the same rule for a deprecated command ([Kubernetes](https://kubernetes.io/docs/reference/using-api/deprecation-policy/)).
5. With `--json`, the warning is one JSON line on stderr ([section 7.4](#74-errors-and-warnings-with---json)). This also holds when parsing or argument checks stop the command before it runs. `--no-color` applies to early warnings too. A flag after `--`, or a string flag's value, does not change their format.
6. A warning never prints during shell completion (`__complete`), and never on stdout.
7. Help and completion leave out old names. The reference page of the new command lists them under "Old names", with their end.
8. After the end, a release removes the row. The old name then moves to the synonym table of [section 4.3](#43-suggestions), so a user who types it gets "Did you mean". An old flag moves to the flag synonyms in the same file, so its unknown-flag error names the new flag.

### 3.2 Every old form

| Old form | New form | Kind | Task | Note |
| --- | --- | --- | --- | --- |
| `kill <address>` | `rm <address> -y` | command | C5 | `kill` never asks, as before. |
| `flip [<address>]` | `attach [<address>]` | command | C5 | `flip` alone opens the launcher's switcher, as today. |
| `rm` or `delete` with input off and no `-y` | `rm -y` | behaviour | C5 | Until the end, it deletes and warns. After the end, it fails with exit code 4. |
| `host idle <host> <time>` | `host set <host> idle=<time>` | command | C7 | |
| `host destroy <host> [-y] [--force]` | `host rm <host> --destroy [--confirm <host>] [--force]` | command | C7 | `-y` becomes `--confirm <host>`. |
| `host alerts set --token-cmd` | `--token-command` | flag | C6 | |
| `new -d <folder>` | `new -C <folder>` | short flag | C6 | After the end, `-d` means `--detach` ([section 10.4](#104-the-change-of--d)). |
| `new --setup`, `new --no-setup` | `--setup-script`, `--no-setup-script` | flag | C6 | "Setup" then names only the guided setup. |
| `agents install --force` | `--reinstall` | flag | C6 | |
| `host alerts log -n` | `--lines` | short flag | C6 | `-n` then means only `--name`. |
| `-v` on the root | `--version` | short flag | C6 | Cobra adds `-v` today, because `Root()` sets `Version` ([section 10.1](#101-short-flags)). |
| `host create --memory 8`, a bare number | `--memory 8G` | value | C6 | A bare number still means GiB. |
| `host create --disk 40`, a bare number | `--disk 40G` | value | C6 | The same. |
| The keys of `host ls --json` in Go form: `Name`, `SSH`, `SSHConfig`, `Labels`, `Provider`, `Machine`, `Secrets`, and the keys inside `Provider` and `Machine` | The same keys in `snake_case`: `name`, `ssh`, `ssh_config` and so on | JSON key | C4 | Both forms print until the end (question C-9). |
| `[keymap] "session: preview"` | `"session: peek"` | keymap name | C11 | `config check` warns, and the launcher shows one notice at its start. |

Kept aliases do not warn and do not end: `a` for `attach`, `delete` for `rm`, `ls --host <host>` for `ls <host>`, and `agents install --host <host>` for its second argument.

### 3.3 Tests

- A unit test checks each row: the old form runs, does what it did, and prints the warning once.
- A unit test checks that each row's end is two releases after its version, and 12 months after its date.
- A unit test runs `__complete` with an old name, and checks that stderr holds no warning. Cobra writes one line of its own there, "Completion ended with directive", which the shell's script drops. Nothing else may come.
- The release checklist in [releasing](releasing.md) gets one step: remove each row whose end has passed, and add its old name to the synonym table.

## 4. Errors and suggestions (C2)

### 4.1 The form of an error

Every error prints in one form, on stderr:

```text
Error: no host named "bx".

Did you mean "box"?

Try: simstim attach box/abcd2345
```

1. `Error:` comes first, bold and red when stderr has colour ([section 7.5](#75-streams-and-colour)). The message follows.
2. The message names the bad input and says what went wrong, in one or two sentences. The code keeps it without a full stop at its end, and the CLI adds one when it prints a user error. An error that is not a user error prints as it is ([section 4.5](#45-errors-that-are-not-user-errors)). The launcher and `--json` show the message without the full stop.
3. A suggestion line follows when [section 4.3](#43-suggestions) finds one, after a blank line.
4. Exactly one `Try:` line comes last, after a blank line. `Try:` is blue when stderr has colour.
5. Nothing goes to stdout.
6. With `--json`, the error is one JSON line on stderr instead ([section 7.4](#74-errors-and-warnings-with---json)).
7. What reaches the terminal has no control or hidden characters: the client cleans the message and the hint that a host sends with `api.CleanText` ([isolation spec 14.15](isolation-spec.md#1415-go-sketches)), and drops them from the user's own words that a `Try:` line repeats.

`client.UserError` keeps `Msg`, `Hint`, `Code` and `Data`, and gains `Kind`, the kind of [section 5](#5-exit-codes-c2). `Code` names the error, such as `host_not_found` or `wall_floor`. Every `UserError` that the client builds sets `Code` and `Hint`. A zero `Kind` means `KindFailed`. One code has one kind everywhere. `client.UnknownNameError` wraps the `UserError` of a name that does not exist, with the names that do, so that the CLI can suggest the nearest ([section 4.3](#43-suggestions)).

### 4.2 The "Try:" line

C2 writes these rules into [the style guide](style.md):

1. The line is a full command that runs as written, or one imperative sentence that ends with a full stop.
2. A command starts with the product name, and uses the real names from the error, such as `box`, not `<host>`. A placeholder in angle brackets appears only when the value is unknown.
3. A command may end with a short note in brackets, such as `simstim host upgrade devbox (installs the files again)`.
4. Two commands may join with ", or" when either one fixes the error.
5. The line is never a question or a fragment.
6. A command that repeats a word the user typed quotes it for the shell. A name that no host could have, such as one with a space, a `;` or a leading dash, is not repeated: the line names the command that lists the hosts instead.

### 4.3 Suggestions

"Did you mean" helps when a user types a name that does not exist. A suggestion never runs ([clig.dev](https://clig.dev/#help), [git](https://git-scm.com/docs/git-config#Documentation/git-config.txt-helpautoCorrect)).

**Where.** Suggestions follow an unknown command at any level, and an unknown flag. C2 also gives them to an unknown host (every command that names one) and an unknown agent (`agents install` and `setup --agents`). An unknown session gets them with the addresses of C5 ([section 9.2](#92-addresses)). An unknown key of `host set` or of a config file gets them with C7 and C9.

**The rule.**

1. Case does not count.
2. A typed word of one or two runes matches only by prefix: a candidate is a thing, such as a command, a flag, a host or an agent, with a name that starts with the word, never one a few edits away. When more than three things match, there is no suggestion. So `x` gets none, `at` gets `attach`, and `d` gets none, because `debug`, `demo`, `doctor` and `rm` (through its alias `delete`) all start with it.
3. For a longer word, a candidate is a thing with a name at most one edit from the typed word when the word has three or four runes, and at most two edits when it is longer. The edit distance is Levenshtein's.
4. For commands, a name that starts with the typed word also makes a candidate, as in Cobra. Kept aliases count as names, but each command counts once, by its name: `delte` suggests `rm`, through its alias `delete`. Until its end, an old name of [section 3](#3-old-names-and-warnings-c5-c6-c7) counts as a name of its new command, which help shows: `kil` suggests `rm`.
5. A synonym of the table below that matches the typed word is the only candidate.
6. The line shows at most three candidates. They are in order of the real edit distance of their nearest name, prefixes included, then in alphabetical order.

**The line.**

- One candidate: `Did you mean "attach"?`. Two or three: `Did you mean one of these: "bash", "zsh"?`.
- The `Try:` line is the user's command line with the nearest candidate in place of the typed word, at the place where the parser found it, when that line parses. For a host or an agent, the error says where the name came from, an argument or a flag, and the item when the word is a list such as `codex,cluade`. Only that word, or that item, changes. The line parses when it names a real command, with flags that it has and the words that it takes. Words with spaces or shell characters are quoted with single quotes.
- The line names a session only as the user typed it by `host/ID`, or as `-` ([section 9.2](#92-addresses)). No lookup found any other session word, and a name may mean another session once cleaned. When the line's command takes a session, and the line names it any other way, the `Try:` line lists the sessions instead: `atach box/auth-fix` gives `Try: simstim ls box`, and `atach box/abcd2345` gives `Try: simstim attach box/abcd2345`. This holds for a misspelt command, a misspelt flag and an unknown host, and for the old forms, so a suggested `rm` never names a session by its name. The `Try:` line of a question with input off, and of `--json` without `--detach`, repeats the typed line under the same rule (`typedLine` in `internal/cli/printed.go`).
- When that line does not parse, or a word of it would not reach the terminal whole, the `Try:` line of a command is the help of the nearest candidate, such as `simstim rm --help`. That of a flag is the command's help, and that of a host or an agent is the error's own hint.
- With no candidate, the `Try:` line is the parent's help, or the error's own hint for a host or an agent.

**The synonym table** lives in `internal/cli/errors.go`. A unit test checks that every target exists in `Root()`. The task that adds a target adds its rows.

| Typed | Suggests | From |
| --- | --- | --- |
| `list` | `ls` | C2 |
| `remove`, `del`, and `kill` after its end | `rm` | C2, C5 |
| `connect`, `open`, and `flip` after its end | `attach` | C2, C5 |
| `create`, `run` | `new` | C2 |
| `info`, `status`, `describe`, `inspect` | `show` | C5 |
| `install` | `agents install` | C2 |
| `login` | `agents login` | C8 |
| `update`, `upgrade` | `host upgrade` | C7 |
| `hosts` | `host ls` | C2 |
| `ssh` | `shell` | P1.13 |
| under `host`: `remove`, `delete`, `forget`, and `destroy` after its end | `host rm` | C7 |
| under `host`: `list` | `host ls` | C2 |
| under `host`: `info`, `details` | `host show` | C7 |
| under `host`: `config`, `settings`, and `idle` after its end | `host set` | C7 |

### 4.4 The framework's own errors

Cobra's own errors carry no hint. The root's `cobra.NoArgs` says "unknown command" without suggestions (`args.go` in Cobra v1.10.2). Cobra prints the help of a command without a `Run` function before it checks the arguments (`command.go` in Cobra v1.10.2). So `host ad` prints the help of `host`, and exits with 0.

C2 replaces them:

- The root and every noun get an `Args` function that returns the unknown-command error with suggestions. Cobra adds its `help` and `completion` commands only when it runs, so `Root()` adds them first, and they get the same errors. `help` checks its words too: every word that is left, `-` and the words after `--` included, must name a command. `help atach`, `help host ad` and `help -` fail with the error of an unknown command, and exit with 2.
- Every noun gets a `RunE`. Without an argument it prints its help on stdout and exits with 0. With an argument it returns the unknown-command error.
- `doctor` and `host alerts` are not nouns. Each runs, and also has subcommands, so each keeps its own `RunE`. `doctor` gets the same `Args` function as a noun, so `doctor termnal` suggests `terminal`. `host alerts` keeps its one host argument.
- `SetFlagErrorFunc` on the root turns every flag error into a `UserError`.

| Case | Message | Try | Exit |
| --- | --- | --- | --- |
| An unknown command | `unknown command "atach"`, or `host has no command "ad"` | [Section 4.3](#43-suggestions) | 2 |
| An unknown flag | `new has no flag --detatch` | 4.3, else `simstim new --help` | 2 |
| A flag without its value | `--dir needs a value` | `simstim new --help` | 2 |
| A value that does not parse | `--memory-max "4X" is not a size, such as 4G` | `simstim new --help` | 2 |
| A missing argument | `attach needs <host>/<session> or -`, then the short help | `simstim attach --help` | 2 |
| An extra argument | `rename takes <host>/<session> <name>, and got 3 words` | `simstim rename --help` | 2 |

The message of a missing or an extra argument comes from the words of the command's `Use`, so it is right for every command without a message of its own. Choices, as in `<host>/<session>|-`, join with "or". The short help of question C-4 follows the message of a missing argument. It holds the usage line and the command's first example, so every command that needs a word has an example:

```text
Error: attach needs <host>/<session> or -.

Usage:   simstim attach <host>/<session>|-
Example: simstim attach my-vps/auth-fix

Try: simstim attach --help
```

[Section 6.3](#63-pickers) says when a picker opens instead.

### 4.5 Errors that are not user errors

An error that is not a `UserError` keeps its text, and gets the hint to make a debug bundle:

```text
Error: read /home/me/.config/simstim/hosts.toml: permission denied

Try: simstim debug bundle (it writes one file to attach to a bug report)
```

Its kind is `KindFailed`, and its code is `internal`.

### 4.6 Tests

- `TestEveryUserErrorHasHint` parses every Go file under `internal/` with `go/ast`, except tests, which build bare errors on purpose. It fails, with the file and line, on a `client.UserError` literal, under any name of the import, without a `Code` or with an empty `Hint`, also one made of constants, such as `"" + ""`. It also fails on `new(client.UserError)`, on a zero `UserError`, and on an empty constant set into a field named `Code` or `Hint`. A value that is empty only at run time cannot be read from the source: `TestDescribeAlwaysHasATry` checks that every error still gets a `Try:` line.
- Golden tests of `printError` for each kind, with colour and without.
- A table test of suggestions, with the whole list and the `Try:` line: `list` gives `ls`, `run` gives `new`, `at` gives `attach`, `ssh box` gives `ssh-config`, `foo box` gives none, `kil` gives `rm`, `LS` gives `ls`, `Attach` gives `attach`, and `x` and `d` give none. A test that the `Try:` line fixes the word where the parser found it, as in `--debug-file atach atach box/abcd2345`, and only the word of the unknown name, as in `new --dir bo/s bo` and `agents install --host cluade cluade`. Round-trip tests of the suggestions for `rn box/A -y`, `rm box/A --yse`, `kil`, `flip … --restrt` and an unknown host, with two sessions whose names clean to the same text: each `Try:` line names the session by `host/ID`, or lists the sessions.
- A test that every synonym target exists in `Root()`.
- A test that `host ad` fails with exit code 2, and `host` alone prints help and exits with 0.
- A test that `doctor termnal` fails with exit code 2 and suggests `terminal`, and that `doctor` alone runs its checks.

## 5. Exit codes (C2)

### 5.1 The table

The exit code comes from the error's `Kind`, never from its text (question C-6). Today `printError` in `internal/cli/main.go` matches words such as "no host" in the message, so "no hosts yet" exits as "not found".

| Code | Kind | Meaning | Examples |
| --- | --- | --- | --- |
| 0 | | Done. | A command did its job. Help printed. An attach ended with a detach, or with the agent's end. |
| 1 | `KindFailed` | The command could not do its job. | A host did not answer. The helper failed. `config check` found an error. An error that is not a user error. |
| 2 | `KindUsage` | The command line or a config file is wrong. | An unknown command, flag or key. A missing or extra argument. A bad value. Two flags that conflict. A name that matches two sessions. |
| 3 | `KindNotFound` | A named thing does not exist. | No host, session, agent, profile or rule of that name. |
| 4 | `KindNeedsAnswer` | The command needs an answer, and input is off. | `rm` without `-y`. `host add --yes` without `--tier`. A `policy` change without a layer. |
| 5 | `KindRefused` | A policy or a tier refuses the action. | A lock, a floor, a tier, a pinned host key, a draining host, a built-in rule of a door. |
| 130 | `KindCancelled` | The user or a signal cancelled. | The answer no, `esc` in a picker, a typed name that does not match, `ctrl+c` outside an attach, SIGTERM. |

Bash reports a process that signal N ended as 128 + N, so 130 is the code of SIGINT ([Bash](https://www.gnu.org/software/bash/manual/html_node/Exit-Status.html)). This spec uses 130 for SIGTERM too, because G12 has one code for a cancel.

Rules:

1. `printError` maps `Kind` to the code. It never reads the message.
2. A host error carries a `Code`. The client maps each host code to a kind with the table in [section 5.3](#53-the-codes-of-the-isolation-spec). An unknown host code is `KindFailed`.
3. A listing or describing command (`ls`, `host ls`, `host show`) reports a host that does not answer as data, in that host's entry, and exits with 0.
4. "No hosts yet" exits with 1. A host that the user named and that does not exist exits with 3.
5. `ErrClosed`, the connection that closed, exits with 1.
6. The codes that today's helper sends get a kind in the same map as section 5.3. A code takes the kind that the table above gives its meaning, the same kind as the client's own check of the same mistake. `not_found` and `unknown_agent` are `KindNotFound`. `bad_name`, `bad_secret` and `memory_max` are `KindUsage`. `no_folder` and `setup_trust` are `KindNeedsAnswer`, because `new` asks before it makes a folder or runs a setup script. Every other code of today, such as `bad_request`, `exited` and `worktree`, is `KindFailed`.
7. SIGINT and SIGTERM cancel a command outside an attach: it exits with 130, also when it returns no error, and the client prints the cancel and closes its debug file. Every read that waits for the terminal gives up at the signal, such as the key tester of `doctor keys`, and puts the terminal back. A signal at a question is a cancel, never an answer: the command keeps no answer, and changes nothing more. A signal that comes with the answer wins. Two choke points hold this for every command: the one function that sends a request to a host (`Conn.Call`) sends nothing once the command's context has ended, also when the request waits behind another frame, and the host writers (`UpdateHosts` and `SaveHostsContext`) write nothing then. A local fix looks at the cancel before each fix and each file. Until C7 changes the host writers, only a step that must finish what a change began goes on: `host destroy` forgets a host whose machine it has deleted, and `host create` saves a machine that it has made. Once C7 lands, a cancelled command never writes `hosts.toml`. If cancellation stops the initial save after `host create`, bounded cleanup under the held host lock deletes that unsaved machine, or reports where it stays. Once that initial save commits, the host stays saved if later preparation stops; no further host write runs. After a completed deletion, a cancelled forget keeps the saved host and reports what was deleted, so the user can retry. A read that cannot be cancelled, as on some Windows consoles, does not keep the command waiting. Inside an attach, Ctrl+C is a byte for the agent ([AGENTS.md](../../AGENTS.md), rule 2).
8. A `hosts.toml` that does not decode is `KindUsage`, and its message names the file, the line and the column. A file that cannot be read is `KindFailed`.
9. A prompt with input off asks nothing. It fails with exit code 4, or takes its default, as [section 6.2](#62-prompts) says.

### 5.2 `exec`

`exec` returns the code of the command that it ran (G12). It needs its own codes for the cases where no command ran. It uses the codes of GNU `timeout` and `docker run` ([timeout](https://www.gnu.org/software/coreutils/manual/html_node/timeout-invocation.html), [Docker](https://docs.docker.com/engine/containers/run/#exit-status)):

| Case | Exit |
| --- | --- |
| The command ran and exited | Its code |
| A signal N ended the command | 128 + N |
| `--timeout` ran out | 124 |
| `exec` failed before the command started: any kind of section 5.1 | 125 |
| The command was found, but could not run | 126 |
| The command was not found | 127 |

The error and its `Try:` line still print on stderr, so a person sees the kind. A command of the user's own that exits with 124 to 127 looks the same as these cases. Docker has the same limit (question C-8).

### 5.3 The codes of the isolation spec

The isolation spec owns the messages and the hints of its errors. This table owns their exit codes. The owner confirmed these kinds on 11 November 2026.

| Code or case | Where | Kind | Exit |
| --- | --- | --- | --- |
| `wall_floor` | 9.3 | `KindRefused` | 5 |
| `wall_unavailable`, `wall_setup`, `wall_helper_old` | 9.3 | `KindFailed` | 1 |
| `policy_locked` | 9.3 | `KindRefused` | 5 |
| `layer_missing` | 9.3 | `KindNeedsAnswer` | 4 |
| `grant_wildcard`, `grant_idn`, `grant_public`, `secret_unbound`, `keep_refused` | 9.3 | `KindRefused` | 5 |
| `sandbox_stopped`, `sandbox_busy`, `busy` | 9.3, 14.14 | `KindFailed` | 1 |
| `unpulled`, `unsaved` | 9.3, 14.14 | `KindFailed` | 1 |
| `pull_flagged` | 9.3 | `KindRefused` | 5 |
| `mount_microvm`, `digest_mismatch`, `judge_unavailable` | 9.3 | `KindFailed` | 1 |
| `ssh_key_kind`, `host_unknown` | 9.3, 18.8 | `KindUsage` | 2 |
| `level_below`, `runtime_missing`, `no_room` | 14.14 | `KindFailed` | 1 |
| `draining` | 14.14 | `KindRefused` | 5 |
| A changed host key | 14.14 | `KindRefused` | 5 |
| A secret, the SSH agent or an alert token above the tier. A repository outside `repos`. A shared host as the default. `--root-step run` on a shared host. No tier after the migration, where it stops a command. | 14.14 | `KindRefused` | 5 |
| No host given and no default. A reserved name. Two words before `--`. | 14.14 | `KindUsage` | 2 |
| No KVM, no sudo, no passwordless sudo for `--root-step run --yes`, a failed root step, a failed download or checksum, a missing image | 14.14 | `KindFailed` | 1 |
| A user name that a script cannot use, facts that disagree, waking that is off | 14.14 | `KindFailed` | 1 |
| A purge while a session works, a helper without the feature | 14.14 | `KindFailed` | 1 |
| `policy_unreached`, `secret_store`, `git_scope`, `git_hooks`, `git_timeout`, `git_version` | 9.3 | `KindFailed` | 1 |
| `lockdown_confirm` | 9.3 | `KindNeedsAnswer` | 4 |
| `setup_outside` | 9.3 | `KindRefused` | 5 |
| `setup_unreadable` | 19.2 | `KindFailed` | 1 |
| `image_bad_layer`, `image_root_mismatch`, `image_in_use`, `sandbox_boot`, `agents_disk` | 9.3, 20 | `KindFailed` | 1 |
| `image_moved`, `image_signature`, `runtime_advisory` | 9.3, 20 | `KindRefused` | 5 |
| `cp_too_big`, `cp_path`, `cp_git`, `cp_flagged`, `image_secret`, `image_flagged`, `image_elsewhere` | 9.3, 21 | `KindRefused` | 5 |
| `cp_exists`, `rewind_off`, `image_exists`, `machine_token` | 9.3, 21 | `KindFailed` | 1 |
| `machine_provider` | 9.3, 21 | `KindUsage` | 2 |
| `rewind_unknown` | 9.3, 21 | `KindNotFound` | 3 |
| `launcher_refused`. Lima before 2.3.0 at `host create lima`. | 14.14, 21.9 | `KindFailed` | 1 |
| `image_wall`, `cp_unwalled` | 9.3 | `KindFailed` | 1 |
| `mount_repo_rw`, `image_too_large` | 9.3 | `KindRefused` | 5 |
| `release_age_invalid`, `package_level_invalid` | 7.10 | `KindUsage` | 2 |
| `feed_unavailable` | 7.11 | `KindFailed` | 1 |
| `env_is_secret`, `copy_sensitive` | 17.2, 17.3 | `KindRefused` | 5 |
| `sudo_wall` | 17.10 | `KindRefused` | 5 |
| `secret_not_here` | 18.2 | `KindRefused` | 5 |
| `no_host` | 18.2 | `KindFailed` | 1 |
| `invite_expired`, `invite_unknown` | 18.3 | `KindFailed` | 1 |
| `code_check` | 18.3, 18.8 | `KindUsage` | 2 |
| `code_version` | 18.3, 18.8 | `KindFailed` | 1 |
| `host_rule_layer` | 18.2, 18.8 | `KindUsage` | 2 |
| An `ssh` without the fix of CVE-2023-51385, at `ssh-config` | 17.7 | `KindFailed` | 1 |
| `setup_trust` | 19.2 | `KindNeedsAnswer` | 4 |

The pinned host key lives in `known_hosts-<host>`, and `tiers.toml` keeps a copy of its fingerprint once H4 lands (isolation spec 14.7.1). A changed key is a refusal, as a tier's is.

### 5.4 Tests

- A table test of `Kind` to exit code.
- A test that lists the error codes in `internal/api`, and each code that `internal/host` passes to `UserError` or `proto.Error`, with `go/ast`. It fails on a code without a kind in section 5.1, rule 6, or in the table of section 5.3.
- Unit tests of `exec`'s codes with a fake runner. Task P1.13 adds the end-to-end run.
- `docs/reference/exit-codes.md` is written by `just docs` from the same table that `printError` uses, so `just docs-check` fails when they differ.

## 6. Prompts, pickers and input (C4)

### 6.1 When input is on

Input is on when all of these are true:

1. stdin, stdout and stderr are terminals.
2. The user did not pass `--no-input`, a flag of the root that every command takes.
3. `SIMSTIM_NO_INPUT` is not set, or is empty, `0` or `false`. Any other value turns input off.
4. The command was not given `--json` (question C-13).

A prompt never appears when input is off ([clig.dev](https://clig.dev/#interactivity)). The interactive launcher entry points (the root command with no subcommand, `flip` with no session, and `demo`) follow the same check: they fail with exit code 4 when input is off, and with exit code 1 when any of stdin, stdout or stderr is not a terminal. Input off takes precedence even when a stream is not a terminal. Its error is `no_input` with `KindNeedsAnswer`. Their terminal error is `launcher_no_terminal` with `KindFailed`. The `no_terminal` error of attach and terminal tools has `KindUsage` and exit code 2.

The launcher saves the terminal mode before opening its reader. Only one terminal handoff can be pending. Queued user input cannot start a second handoff. It stops the reader and restores that mode before handing the terminal to another command. If that reader cannot restart afterwards, the launcher exits and restores the terminal. Attach and demo open the reader before entering raw mode. They request the first read only after raw mode succeeds. The demo requests another read only after handling the last chunk, so detach leaves no pending read. A diagnostic check opens its reader before sending terminal queries. A busy or failed reader ends the check without sending queries. A reader that stays blocked is recorded and is not closed while its read runs. When a console reports that cancellation succeeded, diagnostic key reads wait at most five seconds for the read to stop.

### 6.2 Prompts

1. A prompt writes its question to stderr, and reads one line from stdin. Every question reads through one function, `answer` in `internal/cli/answer.go`, which checks for a cancel before the read, while it waits and after the line (rule 7 of section 5.1). At a terminal it puts the terminal in raw mode before it writes the question, reads one key at a time, and reads no further than the end of its line, so what the user typed after it stays for the next question. It reads a paste in blocks, so the last block of a paste can hold keys typed within moments of it, past the end of the line: they stay in the reader for the next question, and an attach does not get them. While it reads, it turns the terminal's bracketed paste on, so a paste of many lines stays text in the line and never gives the answer. On Windows, where a console may show the sequence as text, it does not. It reads through the terminal's one reader, `keyread.For`, which every question and the picker of one run share: on Windows a new reader drops what waits in the console. `keyread.For` saves the terminal's state before it makes the reader, which on Windows changes the console's modes, and each question and the picker put back that saved state when they end. At a terminal, a question never falls back to a plain line read if `keyread.For` or raw mode fails, or if an earlier attach or launcher run left a terminal read that could not be cancelled: it fails instead so a paste cannot answer a confirmation and two readers never share stdin. They end in one order: turn bracketed paste off, restore the terminal, stop the handler of signals, then look once more for a signal. A signal before that last look is a cancel, never an answer.
2. Every prompt has a flag that answers it, and the prompt table below lists it. There are two exceptions: `--root-step run` on a `shared` host, and a secret or an SSH key for a `shared` host. Only a person at a terminal answers these (isolation spec 14.4.4 and 14.8.5).
3. A yes or no question shows its default in capitals: `[Y/n]` or `[y/N]` ([Primer](https://github.com/cli/cli/blob/trunk/docs/primer/components/README.md)). Enter takes the default. `y`, `yes`, `n` and `no` work in any case. Any other answer asks again, up to three times, then cancels.
4. A question about deleting something defaults to no.
5. A severe action asks for a typed name (G9): `Type box to delete the machine, or press Enter to keep it:`. Only the exact name confirms. Enter keeps the thing. Any other text cancels. `--confirm <name>` gives the name instead.
6. `esc` or `ctrl+c` at a prompt cancels. `ctrl+c` cancels inside a paste too, as in the picker (section 6.3, rule 6). A paste at a prompt keeps only the characters that a typed key gives. A cancel prints `Cancelled. Nothing changed.` on stderr and exits with 130. If an earlier step changed something, the line says what instead. Each step records its change as it makes it, so a cancel or a signal at any point lists every change before it. A cancel during the fixes of `doctor --fix` says so, and names what was done before it: each fix that ran, and a fix that the cancel stopped part way, by how far it got, such as "removed 1 of 2 generated SSH configs". With `--json`, its data holds each fix as it ended, with each change it made. A signal at a question, with nothing changed, says that a signal ended the command at a question.
7. When input is off, each prompt does one of two things. The prompt table says which.
   - **Fail.** The command needs the answer. It fails with exit code 4, and its `Try:` line names the flag.
   - **Keep.** The default changes nothing that the user did not ask for. The command takes the default, and says so on stderr with the flag that changes it.

The form of a failure:

```text
Error: rm needs a yes to delete auth-fix on box, and input is off.

Try: simstim rm box/abcd2345 -y
```

**The prompt table.** A test checks that each prompt in the code has a row, and that each row's flag exists on its command. A task that adds a prompt adds its row in the same pull request.

| Command | Question | Default | Flag | Input off |
| --- | --- | --- | --- | --- |
| `cp` | Copy the listed files that can run code or expose session data? | No | `--allow-flagged` | Fail with `cp_flagged` |
| `pull` | Update the local branch with these flagged changes? | No | `--allow-flagged` | Fail with `pull_flagged` |
| `image save` | Save the image with these reviewed changes? | No | `--allow-flagged` | Fail with `image_flagged` |
| `rewind` | Restore the selected disk checkpoint, after showing its time and changed branches? | No | `-y` | Fail; a missing checkpoint number also needs an answer |
| `rewind` with a secret or SSH key that `[secret_tiers]` marks `shared`, for a `shared` host | Send the selected value to this host for this exact rewind? | No | None | Refuse, with exit code 5 |
| `rm` | `Delete auth-fix on box?` with what stops and what stays | No | `-y` | Fail, after the end of section 3 |
| `attach` on an ended agent | `Start claude again in auth-fix on box? [Y/n]` | Yes | `--restart` | Fail |
| `ssh-config` after preparing its generated files | `Add this Include to the top of your SSH config? [y/N]` | No | `--yes` | Keep: leave the generated files ready without changing the user's config |
| `new`, missing folder | `Make it? [y/N]` | No | `--mkdir` | Fail |
| `new`, a repository's setup script | `Run it? [y/N]` | No | `--setup-script`, `--no-setup-script` | Keep: skip it, with today's note |
| `host add` | Install the helper | No | `--yes` | Fail |
| `host add`, when the host lacks an agent | The setup's checklist of agents, and their logins | None | `--yes`, which offers none | Keep: install nothing. Each agent's line says how to install it |
| `host add`, a host key that the user's SSH settings do not trust (H3) | ssh's own question, with the key's fingerprint (isolation spec 14.7.1) | No | `--host-key SHA256:…`, or `--yes`, which pins the key on first use | Fail |
| `host add` without `--tier` (H4) | Which tier | None | `--tier` | Fail |
| `host add --accept` (H20) | Do the host keys match the machine's screen? | No | `--host-key SHA256:…`, once for each key | Fail |
| `host join` (H20) | Make these changes? | No | `--yes` | Fail |
| `host leave` (H20) | Undo these machine changes? | No | `--yes` | Fail |
| `host destroy`, until C7 makes it an old name of `host rm --destroy` | `Delete the lima machine box, with its disk and every session on it? [y/N]` | No | `-y` | Fail |
| `host rm` on a created host | The typed name of the machine | Keep it | `--destroy --confirm <host>` | Keep: forget only |
| `host rm`, purge (H15) | The typed name of the host | Keep it | `--purge --confirm <host>` | Fail |
| `host rm`, purge (H15): the agents' logins | `Delete these logins on box? [y/N]` | No | `--delete-logins` | Keep: the logins stay |
| `host prepare` with optional root changes (H7) | Show the exact root command and run it? | No | `--root-step run` | Fail |
| `host rm`, purge with optional root changes (H15) | Show the exact root command and run it? | No | `--root-step run` | Fail |
| `host set … tier=` to a lower tier (H5) | As isolation spec 9.1 | No | `-y` | Fail |
| `host set … tier=` to a higher tier (H4) | The typed name of the host | Keep the tier | `--confirm <host>` | Fail |
| `host rekey` (H3) | The typed name of the host | Keep the pin | `--confirm <host>` | Fail |
| `policy allow`, `policy deny`, `policy rm`, without a layer (P1.12) | The layer to change | None | `--all` | Fail, with exit code 4 |
| `host prepare` or `host rm` with `--root-step run` on a `shared` host (H7, H15) | The typed name of the host | Keep it | None | Refuse, with exit code 5 (section 5.3) |
| `new` or `resume` with a secret or an SSH key that `[secret_tiers]` marks `shared`, for a `shared` host (H5) | `Send CLAUDE_LAB to lab? Its owner can read it. [y/N]` | No | None | Refuse, with exit code 5 (section 5.3) |
| `doctor --fix`, `host doctor --fix` | `Fix it? [y/N]` for each fix | No | `-y` | Fail |
| `setup` | Each question of the guided setup | | `--yes` with `--host` (and `--tier`) or `--create`, and `--agents` | Fail |
| A `policy` change without a layer (P1.12) | Which layer | None | A layer flag | Fail (`layer_missing`) |
| `policy apply` (P2.14) | `Send these changes to 4 sandboxes? [y/N]` | No | `-y` | Fail |
| `policy learn` (P2.14) | The rules to write, picked from a list | None | `-y`, which writes every rule of the list | Fail |

Today `confirm` in `internal/cli/host.go` writes its question to stdout, and reads an end of file as no. C4 moves every prompt into `internal/cli/prompt.go`.

### 6.3 Pickers

A picker lets the user choose a session when a command needs one and the user named none (question C-4).

1. Only `attach`, `show` and `rm` open a picker, and only when their address is missing and input is on.
2. When input is off, the missing address is the error of [section 4.4](#44-the-frameworks-own-errors), with exit code 2.
3. The picker draws inline below the command line, never on the alternate screen. It shows at most 10 rows and a filter line.
4. A row shows `host/session`, the agent, the state and the age.
5. It starts from the session cache, so it draws at once. With an empty cache, it draws one line that says that it looks for sessions. It then asks each host for its list in the background, without waking a stopped host, as `ls` does, and updates the rows. A host that does not answer keeps its cached rows. The answers go to the session cache, as `ls` writes them. The picker stops its questions to the hosts, and waits for them, before the command goes on. It sends the terminal no query, so no reply can reach an attach: its framework runs with no input of its own, and so sends none of its queries, and the picker reads the keys itself. It reads one key at a time, and no further than the key that picks. It reads a paste in blocks, so the last block of a paste can hold keys typed within moments of it. Keys typed before the session's screen appears can still be lost, so the docs say to type after it appears.
6. Typing filters the rows by a substring of `host/session`. A paste goes into the filter whole, with only the characters that a typed key gives: without its line breaks, its control characters and its hidden characters. A line break in a paste never picks. `↑` and `↓` move. Enter picks. `esc` cancels with exit code 130, and so does a signal or the end of the command's context, also just after a pick. `ctrl+c` cancels too, also inside a paste: a terminal drops `ctrl+c` from what it pastes, so one there comes from the keyboard, and a paste whose end never comes always has a way out.
7. When it closes, it erases its rows. The command then runs as if the user had typed the address.
8. When the hosts answer with no session, and no host that did not answer has a cached session, the picker closes. The command fails with exit code 3: `no sessions on any host`, and `Try: simstim new my-vps`, with the first saved host's name. When no host answers and nothing is cached, the command fails with the first host's error, with exit code 1.
9. `rm` still asks its question after the pick.

The picker is `internal/tui/picker.go`, with Bubble Tea v2, the launcher's framework ([0001](decisions/0001-go-and-bubble-tea.md)).

A session ID from a host uses the existing legacy rule of 1 to 64 ASCII letters, digits, underscores or dashes. Every action validates the raw ID before caching it, attaching or writing an ID hint. A control or hidden character makes it invalid. Onboarding completion rejects a missing or invalid ID before changing checks, notes or its action queue. Its CLI and TUI drivers stop on that error.

### 6.4 Tests

- Unit tests with a fake terminal: yes, no, the default, three bad answers, `esc`, and a typed name that does not match.
- For each row of the prompt table, a test runs the command with `--no-input`. The command fails with 4 and names the flag, or takes the default and says so.
- A table test of `SIMSTIM_NO_INPUT`: unset, empty, `0`, `false`, `1`, `yes`.
- Golden tests of each prompt's text, and of the picker's first frame.

## 7. Machine output (C4)

### 7.1 Which commands print JSON

Every command that prints data or makes something takes `--json` (G10). This table lists each command that has it. A task that adds such a command adds its row and its shape in the same pull request. A command without a row has no `--json` yet.

| Command | Top-level value | Task |
| --- | --- | --- |
| `ls` | An array of `{v, host, sessions, error}`, as today, with `v` | today, C4 |
| `show` | A session object | C5 |
| `new --detach`, `resume --detach` | A session object | C4 |
| `new --count <n>` | An array: a session object for each session that started, and `{v, name, error}` for each that did not | P1.16 |
| `fork --detach`, `fork --count <n>` | `{v, host, parent, children, failures}`. Each child is a session object. Each failure has a one-based `index` and an `error` with the fields of section 7.4. | P5.1 |
| `rewind --list` | `{v, host, id, checkpoints, current_refs}`. Checkpoints are newest first, each with its immutable `name`, `created_at` and branch `refs`. | P5.4 |
| `rewind <host>/<session> <n> -y` | `{v, host, id, checkpoint, session}`. `checkpoint` is the immutable name selected before confirmation. | P5.4 |
| `ports` | `{v, host, session, id, ports}`: `session` is the name and `id` the ID. Each port has `port`, `address`, `pid` and `process` (`api.Port`) | C4 |
| `doctor` | `{v, checks, fixes}`, each fix with `id` and `problem`. With `--fix -y`, each fix also has `done` or `error`, and a local fix has `changed`: each change it made, such as each file it removed. `--fix` without `-y` fails with exit code 4, because `--json` asks nothing | C4 |
| `host ls` | An array of host objects, each with `v` and `tier` | today, C4, H4 |
| `host show` | A host object, with `tier`, `host_id_changed`, `reachable`, `error`, `try`, `info` and `session_count` ([section 11.2](#112-host-show)) | C7, H4 |
| `host test` | `{v, host, connect_ms, round_trip_ms}` | C4 |
| `host upgrade` | `{v, host, helper, outcome}`. `helper` is this client's helper version, and `outcome` is `unchanged`, `installed` or `reinstalled` ([section 11.4](#114-host-upgrade)) | isolation P4.1 |
| `host doctor` | Today's object, with `v` | today, C4 |
| `host restore` | `{v, host, results}`, each result as `api.RestoreResult` | C4 |
| `host status` | `{v, host, state, provider, id}` for a machine of `host create`, or `{v, host, output}` with a provider script's output | C4 |
| `host alerts` | Today's `{alerts, log}`, with `v` | today, C4 |
| `host alerts log` | An array of events, each with `v` | today, C4 |
| `agents ls` | An array of agent rows, each with `v` | today, C4 |
| `agents login` | A session object, with `started`: `true` when the command started the login, `false` for a login that already ran. Its `address` is always `host/id`, and the host's text in it is cleaned. It never attaches ([section 12.3](#123-agents-login)) | C8 |
| `config keymap` | An array of `{v, command, keys}` | today, C4 |
| `config check` | The report of [section 13.5](#135-output-and-exit-codes) | C9 |
| `version` | `{v, version, protocol, helper}` | C4 |

**A session object** holds `v`, `host`, `address` and every field of `api.SessionInfo` that has a value. P1.8 adds the fields of isolation spec 9.1.

**Every `address` in JSON is `host/ID`**, never a name, the same rule as the JSON of `agents login` (C8). A name can change, or come to mean another session, between the moment that the command reads it and the script's next step. An ID cannot. A command that the CLI prints for a person names a session by `host/ID` too, as [section 9.2](#92-addresses) says.

`new` and `resume` take `--json` only with `--detach`. Without it they fail with exit code 2: `--json needs --detach, because an attached session owns the terminal`, and `Try: simstim new my-vps --detach --json`.

`new --count` never attaches, so it takes `--json` without `--detach`. Its `error` is the object of [section 7.4](#74-errors-and-warnings-with---json). It exits with 0 when every session started, and otherwise with the exit code of the first error. When no session started, it fails as one command does.

### 7.2 Field rules

These rules are the contract of question C-5.

1. Keys are `snake_case`, and match `^[a-z][a-z0-9_]*$`. The rule covers the keys that the code names. A map that holds data, such as a host's `labels`, keeps its keys as the user wrote them. The Go keys of `host ls` are the one exception, until their end ([section 3](#3-old-names-and-warnings-c5-c6-c7)). Today `client.Host` has TOML tags and no JSON tags, so `host ls --json` prints Go field names.
2. Every top-level object, and every element of a top-level array, has `"v": 1`. Nested objects do not. Today's arrays stay arrays, so that no script breaks.
3. Times are RFC 3339 strings. New keys of times end in `_at`, and older keys, such as `since`, `debug_until` and `time`, keep their names. The client writes them in UTC. A script must accept any offset.
4. New durations are whole numbers, and their keys end in `_s` or `_ms`.
5. New sizes are whole numbers of bytes, and their keys end in `_bytes`. Older keys, such as `memory_max`, keep their names.
6. A value that is not set is left out. The client never writes `null`.
7. A value from a fixed list is a lower-case string, such as `"working"`.
8. The order of keys is not part of the contract.
9. New keys may appear in any release, and a script must ignore keys that it does not know. A key never changes its name, its type or its meaning. A change that needs one adds a new key, and keeps the old one ([Heroku](https://devcenter.heroku.com/articles/cli-style-guide)).
10. One value prints with an indent of two spaces, and one newline at its end, as `printJSON` does today. A stream, such as `--follow`, prints one compact object on each line.

### 7.3 What `--json` changes

- stdout gets the JSON value and nothing else.
- Input is off ([section 6.1](#61-when-input-is-on)), so no prompt and no picker appear.
- No colour, no spinner and no progress lines.
- stderr gets only JSON lines: warnings and the error.

### 7.4 Errors and warnings with `--json`

When a command fails, stdout stays empty, and stderr gets one line (question C-7). Only `new --count` differs, when some of its sessions started ([section 7.1](#71-which-commands-print-json)):

```json
{"v":1,"error":{"code":"host_not_found","kind":"not_found","message":"no host named \"bx\"","try":"simstim host ls","exit":3,"suggestions":["box"]}}
```

| Key | Value |
| --- | --- |
| `code` | The error's code: a host code such as `wall_floor`, or a client code such as `unknown_command` |
| `kind` | `failed`, `usage`, `not_found`, `needs_answer`, `refused` or `cancelled` |
| `message` | The message, without `Error:` |
| `try` | The text of the `Try:` line |
| `exit` | The exit code |
| `suggestions` | The candidates of section 4.3, when there are any |
| `data` | The host's `Data`, when it sent some. A cancel during the fixes of `doctor --fix` has `fixes`: each fix as it ended, with what it changed |

A line that does not parse is JSON when it gives `--json` as a flag, before any `--`. A word that is another flag's value is no flag: in `--debug-file --json`, `--json` names the debug file. The same holds for `--no-color`.

The host error conversion makes its message and hint one clean display line, as section 4.1 requires. JSON encoding preserves those derived strings exactly. Other payload fields, including the keys and values of `data`, keep their decoded values. Every JSON path physically escapes control and hidden characters, including C1, bidirectional, zero-width and tag characters. The encoder never removes them from data or merges distinct keys. Human output cleans only display copies.

A warning is one line before the result or the error:

```json
{"v":1,"warning":{"code":"old_name","message":"\"kill\" is now \"rm\".","old":"kill","new":"rm","until_version":"0.4.0","until_date":"2027-11-10"}}
```

A check command, such as `config check`, is different. Its findings are data, so its report goes to stdout. Its exit code says whether it found an error.

### 7.5 Streams and colour

- stdout gets data only: lists, details and JSON.
- stderr gets messages, confirmations such as "Deleted auth-fix", progress, warnings, prompts and errors. C4 moves today's confirmations from stdout to stderr. Human output may change between releases ([clig.dev](https://clig.dev/#future-proofing)).
- Colour is off for a stream that is not a terminal. It is also off when `NO_COLOR` holds any value ([`no-color.org`](https://no-color.org/)), when `TERM=dumb`, and with `--no-color`. Each stream decides on its own.
- A spinner or a progress line appears only when stderr is a terminal. Without one, a long step prints one line when it starts.

### 7.6 Tests

- A golden test of each JSON shape in section 7.1, from fixed data.
- A test that runs each command with `--json` against fakes, and checks `v` and the key pattern of section 7.2.
- A test that a failing command with `--json` writes nothing to stdout and one JSON line to stderr.
- A test that output to a pipe has no escape byte and no spinner.
- An end-to-end run of `new --detach --json` in a script, which parses the output and deletes the session.

## 8. Help (C3)

### 8.1 The root help

The root help groups the commands, with Cobra's `AddGroup` and `GroupID`. `SetHelpCommandGroupId` and `SetCompletionCommandGroupId` place `help` and `completion` ([Cobra](https://github.com/spf13/cobra/blob/main/site/content/user_guide.md)). An empty group would print its title, so P1.8 adds the Policy group with its first command.

```text
Run any coding agent on any host, and use it as if it ran here

Usage:
  simstim            opens the launcher
  simstim [command]

Examples:
  simstim setup                       # connect a host, step by step
  simstim new my-vps --agent claude   # start an agent, and attach to it
  simstim attach my-vps/auth-fix      # go back to a session

Sessions:
  new         Start a session on a host, and attach to it
  attach      Attach to a session
  …

Hosts:
  host        Add, create, list, test and remove hosts

Agents:
  agents      List the agents, and install one on a host

Setup and help:
  setup       …

Flags:
  …

Additional help topics:
  concepts    The words that this tool uses
  exit-codes  What each exit code means

Use "simstim [command] --help" for more information about a command.
```

The root help starts with the root's `Short`. Its usage line says what the command does alone. The root's `Long` says the same, for the reference index and the man page, so the root help leaves it out. The commands of a group come in the order of the table in [section 2.1](#21-the-top-level). The footer is Cobra's, as in the help of every other command. C8 changes the line of `agents` when it adds `agents login`.

Every line of every help fits 80 columns. The help wraps each paragraph of a `Long` and the usage of the flags. A line that starts with a space, such as a command, keeps its width, and so does an example. An example that would be wider goes on in a second line, after a backslash.

### 8.2 The help of a command

The help keeps Cobra's order: the description, usage, aliases, examples, subcommands and flags. Each part follows these rules:

1. **`Short`** is one sentence in the imperative, with no full stop at its end. An example: "Start a session on a host, and attach to it".
2. **`Use`** writes each argument with one placeholder: `<host>`, `<host>/<session>`, `<agent>`, and `<name>` only for a new name. Optional parts are in `[ ]`, and choices are `a|b`. Today `host rm <name>` and similar become `host rm <host>`.
3. **`Example`** holds at least one full command. Examples use `my-vps` for a host and `auth-fix` for a session, as the docs do. An example that needs a word of explanation has a comment after `#`.
4. **`Aliases`** holds the kept aliases. Old names of section 3 are hidden commands or hidden flags, so help does not show them.
5. **`Long`** appears only when one sentence is not enough.
6. Help never uses the internal words of section 8.4.

### 8.3 Help topics

A topic is a command with only `Use`, `Short` and `Long`, and a mark that makes it a topic (`IsTopic` in `internal/cli`). The root help lists it under "Additional help topics", and `simstim help <topic>` prints it. `simstim <topic>` prints it too. The topic runs, hidden, so that a word after it fails with exit code 2, as it does after `help <topic>`. Cobra prints the help of a command that does not run before it checks the words.

| Topic | What it holds | Task |
| --- | --- | --- |
| `concepts` | One line for each word that a user meets | C3 |
| `exit-codes` | The tables of sections 5.1 and 5.2. Until `exec` exists, the topic holds only 5.1. P1.13 adds the table of 5.2 with `exec`. | C3, P1.13 |
| `policy` | The layers, and three examples. They are the `Long` of the `policy` command, because a topic cannot share a command's name. | P1.12 |

`concepts` holds the words of the build. C3 ships these: host, helper, session, address, agent, launcher, attach and detach, worktree, secret, alert and provider script. P4.1 adds digest with the release manifest and the helper digest check. C7 adds `host upgrade`. P4.6 adds runtime pin when `host doctor` shows pin ages. Each task that adds a word adds its line in the same pull request. The isolation plan adds sandbox, wall, level, floor, tier, layer, profile, rule, door, preset and drain.

`cmd/docgen` writes each topic to `docs/reference/<topic>.md`, and the reference index links it. C2 writes `docs/reference/exit-codes.md` first, from the table of section 5. The topic and its page share their prose: `ConceptsIntro`, `ExitCodesIntro` and `ExitCodesOutro` in `internal/cli`.

### 8.4 Internal words

A test fails when one of these words appears in the help of any command, a hidden one too: its `Use`, `Short`, `Long`, `Example` or `Deprecated`, or a flag's usage or deprecation. The test reads a word in any case and in any form, such as "lingered", and with any space, line break or dash inside, such as "flight" at the end of one line and "recorder" at the start of the next.

| Word | Write instead |
| --- | --- |
| holder | "the session's process on the host", or "the session" |
| flight recorder | "the session's recent debug record" |
| linger | "keep sessions running after you log out" |
| restore unit | "the start-up entry that brings sessions back after a reboot" |

### 8.5 The reference

`cmd/docgen` adds to each command's page an "Aliases" line with the kept aliases, and an "Old names" line with each old name and its end. The command index groups the commands as the root help does.

### 8.6 Tests

- `TestEveryCommandHasExample` walks `Root()`, and fails on a runnable command with an empty `Example`, a hidden one too. A topic needs none. `just test` runs it, so `scripts/docs-check.sh` needs no change.
- `TestEveryExampleParses` finds the command of each example, and checks its flags, its words and its required flags.
- `TestHelpHasNoInternalWords`, as in section 8.4.
- `TestHelpFitsTheWidth` runs the help of every command, and fails on a line wider than 80 columns, or one that ends with a space.
- `TestTopicsTakeNoWords`: `simstim concepts extra` exits with 2.
- Golden tests of the root help and of `help concepts`.
- A `cmd/docgen` test that a page shows its aliases and old names.

## 9. Session verbs (C5)

### 9.1 Names

| Command | Kept aliases | Old names | What changes |
| --- | --- | --- | --- |
| `attach [<address>]` | `a` | `flip` | The picker, waking, and the offer to restart ([section 9.3](#93-attach)) |
| `resume <address>` | | | A running session attaches ([section 9.4](#94-resume)) |
| `ls [<host>]` | `--host` | | The host as an argument ([section 9.7](#97-ls)) |
| `show [<address>]` | | | New ([section 9.6](#96-show)) |
| `rm [<address>]` | `delete` | `kill` | It asks, and takes `-y` ([section 9.5](#95-rm)) |
| `rename`, `send`, `ports`, `forward`, `pull`, `debug start`, `stop`, `save`, `profile` | | | The address forms of section 9.2 |

`new` keeps its form here. Its flags change in [section 10](#10-flags-c6), and isolation task H12 makes its host optional.

### 9.2 Addresses

An address has one of three forms: `<host>/<session>`, `<session>` or `-`. The `<session>` part is a session's ID or its name.

**IDs and names never collide.** The helper's `NewID` makes an ID of 8 characters of `api.SessionIDAlphabet` (lower-case base32), with at least one digit from 2 to 7. An ID drawn without one gets a random digit at a random place. A word of exactly that form, 8 characters of the alphabet with at least one digit, is always an ID and never a name. A word of 8 letters, such as `frontend`, has no digit, so it is a name, and an address reaches its session by it as before. In `<host>/<word>` and `<word>`, such a word matches the IDs of sessions alone. When no session has that ID, the command fails as not found, with exit code 3: `no session has the ID "abcd2345" on box`. It never falls back to a name. So `new --name`, `rename` and the launcher refuse a name of that form, before they send anything, and the helper refuses it too. They fail with exit code 2 and the code `bad_name`: `a name of 8 letters a to z and digits 2 to 7, with a digit, looks like a session ID`, and `Try: Add a dash, as in abcd-2345, or use a name of another length.` A name that a user gives has at most 64 characters, with the same code. A session that already has such a name, from an older helper, keeps it. Its ID or the picker reaches it, and `ls` shows it as before. A new session skips any ID that a session on the host already has as its ID or as such an older name, so a new session's ID never equals an existing session's name. A name that `new` takes from a folder whose name has the form of an ID gets `-s` at its end, as `proj2345-s`, so `new` never makes a session whose name looks like an ID. The helper also refuses a name that the user gives when it is the ID of a session on the host, with `bad_name`. An older helper's ID without a digit can be such a name. An address with that word finds that session first. A name that `new` takes from the folder gets `-s` at its end instead, as `oldoldid-s`: a name with `-` is never an ID. The helper checks the final name against the sessions' IDs under the lock of the new session's first save.

An older helper made IDs without a digit, about one in five. While such a session exists, its ID matches first, before any name, as for every word that does not have the form of an ID. A script that keeps such an old ID after its session is gone may reach a session named like it. A new helper never makes such an ID.

**Printed commands name the ID.** Every command that the CLI prints for a person names a session that it found by `host/ID`: a `Try:` line, a hint, the restart hint of `attach`, the hints of `rm`, the notes of `debug start` and `resume`, the attach line of `show`, the list of an ambiguous word, and the command that the launcher copies. The session's name appears only in the text around it, cleaned. A printed command reaches the terminal whole, or the CLI prints `simstim ls <host>`, or `simstim ls`, instead: a word that cleaning would change, or a line that the cut at 4000 runes would shorten, could read as another session. A word that names no session gets `Try: simstim ls <host>`, or `simstim ls`, and suggestions within two edits of a name or an ID, each by `host/ID` with its name beside it: `Did you mean "box/abcd2345" (auth-fix)?`. The typed word is never repeated as a command.

**`-`** is the last session in the session cache. With none, the command fails with exit code 3: `no session has been attached yet`, and `Try: simstim ls`. For `new`, `-` names the host of the last session (isolation spec 14.10.1). `-` never means stdin.

**`<host>/<session>`.** The host part names a saved host. An unknown host fails with exit code 3, with suggestions from the saved hosts. On that host, the session part matches a session's ID first, then its name, unless it has the form of an ID, as `findIn` in `internal/cli/address.go` does. Two sessions with that name fail with exit code 2:

```text
Error: 2 sessions are named "a" on box: box/abcd2345, box/efgh2345.

Try: simstim attach box/abcd2345
```

**`<session>` alone** (question C-3). The session cache only says which host to ask. What the command acts on is decided by that host's own list, when the command dials it, from the word that the user typed. A session renamed since the cache saw it is then not found, and a new session of that name is found.

1. The client reads the session cache.
2. For each saved host whose list is older than 60 seconds, missing, or from the future, as after the clock was set back, it asks the host for its sessions. It asks every such host at once, without waking a stopped host, within the time limit of `ls`. It writes the answers to the cache.
3. A host that does not answer counts with its cached list, whatever its age. That list may lack a newer session of the same name.
4. On each host, the word matches a session's ID first, then its name, unless it has the form of an ID. The hosts' matches then add up, so an ID on one host and a name on another are two matches.
5. When nothing matches, the client first asks again the hosts that step 2 did not ask, because a list younger than 60 seconds may lack a session made since. No match then fails with exit code 3: `no session named "a" on any host`, with suggestions within two edits by `host/ID`, and `Try: simstim ls`.
6. Matches on one host give that host. `rm` and `kill` are the exception when a host did not answer: they fail with exit code 2, and the `Try:` line holds the host and the ID of the session found. When the host that did not answer is another one, it may have a session of the same name: `"a" is on box, but nuc did not answer, so it may have a session of that name too`. When it is the match's own host, its list may be out of date: `"a" is on box as last seen, but box did not answer, so its list may be out of date`. Other commands go on, and name on stderr each host that did not answer.
7. Matches on two or more hosts fail with exit code 2, and list each full address, by `host/ID`. The `Try:` line holds the first.
8. The command then dials the host, as it must to act, and finds the word in the host's list, as for `<host>/<session>`. It writes that list to the cache. If the word names no session there, it fails with exit code 3, and the error repeats only the word. If it names two, it fails with exit code 2, as above.

`-` and a row of the picker name a session by its ID, which the host's list finds by its ID alone, never by a name: a session named like that ID is never taken for it. When it is gone, the error names it by its name, such as `the last session, auth-fix on box, is gone`, not by an ID that the user did not type.

In an address, a bare word is always a session, also when a host has that name. `attach box` looks for a session named `box`. `debug bundle` and `events` take a host or `<host>/<session>`, not an address. So a bare word there names a host, as `debug bundle` does today. In `debug bundle <host>/<word>`, a word of the form of an ID matches IDs alone too. The hint that runs the bundle again names the session by the ID that the bundle found, and before that lists the host's sessions.

**The session cache moves.** C5 moves the code of `internal/tui/memory.go` to the new `internal/client/seen.go`, so that the client and the launcher share it. The file stays `launcher.json` in the state folder. It gains `seen`, a map from each host's name to the time of its list. The launcher, `ls`, the picker and the steps above write it. `new` and `rename` put their session in its host's list, and `rm` takes it out, without changing the time of the list. A list keeps the time when it arrived from the host. Each host's time is taken when its own answer comes, so a slow host does not make another host's list look newer. The launcher saves a list with that time, so a host whose connection failed since keeps its older time, and a command asks it again. A list older than the one that the cache has changes nothing. Each writer reads the file first, changes only its hosts, and saves through a temporary file and a rename. Each writer's temporary file has its own name, from `os.CreateTemp` in the same folder, so no writer renames a half-written file. The session last attached to is in `last.json`, a file of its own: only an attach writes it, and the writers of lists never touch it. A writer removes a temporary file that a crash left, once it is an hour old. The cache is advisory. Two writers at once can still lose one change, and the next refresh mends a lost list.

### 9.3 `attach`

1. Without an address, and with input on, `attach` opens the picker of [section 6.3](#63-pickers). With input off, the missing address fails first, with exit code 2.
2. `attach` needs stdin and stdout to be terminals. Without them it fails with exit code 2 and the code `no_terminal`: `attach needs a terminal`, and `Try: simstim send <host>/<ID> 'text'`. Nothing looked the address up, so the `Try:` line repeats it only when it means one session as it is: `-`, or a saved host's name with an ID, as `box/abcd2345`. This check comes before any lookup of the address, and before any other step of this list: a script opens no connection to a host, and wakes none. Commands that need a terminal to attach or inspect it fail the same way, with `no_terminal` and exit code 2, the attach of `new` and `resume` too. The launcher entry points follow section 6.1 instead. `new` and `resume` without `--detach` check for the terminal first too, before any lookup: a script opens no connection, and starts or resumes no session. Their `Try:` line adds `--detach` to the typed line, under the rule of [section 4.3](#43-suggestions).
3. A host with a provider script and `wake_on_attach` wakes, as today. From isolation task P1, a stopped sandbox wakes too, with `Waking auth-fix on box…` on stderr.
4. On an ended agent, with input on, it asks `Start claude again in auth-fix on box? [Y/n]`. Yes does what `resume` does, then attaches. No prints why the agent ended, as today's error does, and exits with 130.
5. At a terminal with input off, as with `--no-input`, an ended agent fails with exit code 4, and `Try: simstim attach box/abcd2345 --no-input --restart`. The `Try:` line is the command line with `host/ID` in place of the typed address, also for an older ID without a digit. A script without a terminal gets exit code 2 at step 2 instead. It can start the agent with `resume --detach`.
6. `--restart` on a running agent does not restart it. `attach` attaches, and says on stderr that the agent runs, so it did not restart it.
7. An agent that cannot start again, because the host's helper cannot resume, gets today's ended error, with exit code 1.
8. When another client starts the agent again while the question waits, the host refuses the second resume with `agent_running`. `attach` then reads the session's row again. When the row shows the agent running, it attaches to it, with a note. When the row shows that the agent ended again, it resumes once more, and a second refusal of that kind is the error of an ended agent. `resume` does the same between its list and its request.
9. `attach` records the session as the last one, as `client.RememberLast` does.
10. `flip <address>` runs `attach`, and warns. `flip` alone opens the launcher's switcher, as today, and warns. A `Try:` line of `flip` or `kill` names the new form, `attach` or `rm … -y`, with `-y` before any `--`, and parses as a command of the new form. Their help hints name `attach --help` and `rm --help`.

### 9.4 `resume`

1. On an ended agent, `resume` works as today.
2. On a running session, it prints `auth-fix on box is running, so this attaches.` on stderr, and attaches.
3. With `--detach` on a running session, it prints `auth-fix on box is already running.` and exits with 0. With `--json`, it prints the session object.
4. `--memory-max` or `--secret` on a running session does not fail, because G6 forbids a refusal that depends only on a session's state. Nothing records them, so they change nothing. `resume` says so on stderr, says to pass them again after the agent ends, and attaches. From isolation task P1.14, `resume` and `attach` treat `--cpus` and `--memory` on a running sandbox in the same way. They say to pass them again at the next wake (isolation spec 17.1).

### 9.5 `rm`

1. `rm` resolves the address as in section 9.2. Without one, and with input on, it opens the picker.
2. With input on and without `-y`, it asks. The question uses the lines of the launcher's delete sheet (`deleteDialog` in `internal/tui/dialog_sheets.go`), from one shared function:

   ```text
   Delete auth-fix on box?
   This stops claude on box and removes the session.
   The folder /home/dev/src/app stays. Unsaved work in the agent is lost.
   Delete it? [y/N]
   ```

   The command line shows the folder as the host reports it. The launcher shows `~` for the home folder, because it knows the host's user. The helper does not say which session made a worktree, so a session in the helper's folder of worktrees gets a line that is true either way: `If the session made the worktree /home/dev/.local/share/simstim/worktrees/app/fix-1a2b, it goes with its branch, unless it has changes or new commits.` Every word from the host is cleaned of control and hidden characters, here and in `show`.

3. With input off and without `-y`, `rm` deletes and warns until the end of section 3. The warning is `Warning: without a terminal, "rm" needs -y from v0.4.0 and 10 November 2027, whichever comes later.` After the end, it fails with exit code 4 (question C-10). `gh repo delete` also needs `--yes` to delete without a prompt ([gh](https://cli.github.com/manual/gh_repo_delete)).
4. `kill` runs `rm -y`, and warns. Without a session it warns too, then fails with the usage of `rm`.
5. The delete is bound to the session that the user confirmed. After the answer, `rm` reads the host's list again. When the session is gone, or has another name, it deletes nothing: `auth-fix on box was renamed api while the question waited, so it was not deleted`, with the code `session_renamed` and exit code 1. The delete request also carries the name, and the host refuses it with `session_renamed` when the session's name is another, which closes the rest of the race. The launcher's delete sends the name that its question showed in the same way. An older helper ignores the name, and the client's own check still runs.

   On the host, a rename and a delete take the same lock of the session, the file `op.lock` in its folder, which a resume and a restore take too. A delete takes it at once, or says that the session is busy, and holds it across its check of the name and the delete. A rename waits up to 10 seconds for it.

   - A resume or a delete may stop a holder whose rename waits for the lock. The holder then goes without an answer: the connection closes, or the holder answers `exiting`. The rename takes the lock itself, and looks again: a new holder from a resume renames itself, a session that does not run is renamed on disk, and a deleted one is `not_found`. When the holder that it then finds goes away too, the rename fails with `busy`. A rename never fails with the closed connection.
   - A holder that waits for its secrets answers `starting`. The start or the resume holds the lock until the holder has started, so the rename waits for the lock in the same way, and looks again.
   - A holder that does not answer within the rename's time is not one that went away: the rename fails with `busy`, and says that the holder did not answer in time.

   - A running session's holder renames itself, and takes the lock itself, around the whole rename. It writes the manifest with the new name first. Only after a good write does it change the name in memory and count that copy as saved, and only then does it answer. A failed write changes neither, and is the answer. So a rename that a client saw succeed is in the manifest. The client does not hold the lock for it: a client that stops waiting, as on a time-out, cannot leave a rename running outside the lock.
   - A holder names the feature `rename.saved` in its hello. A holder of an older helper, which goes on running after the helper's upgrade, names none. It answers a rename before it saves the name, at its next tick, and it never takes the lock. For such a holder, the helper takes the lock first and writes `rename.unsaved` before it sends the rename. That record starts as uncertain. When the older holder rejects the rename, and is not ending, the helper puts the earlier record back. When the rename fails without a clear answer, the uncertain record stays, and another rename on that holder fails with `busy`. Once the holder answers yes, the helper marks the record settled, and waits up to 5 seconds for the manifest save. When the name is not in the manifest in time, the rename fails with `busy`. A settled record stays for the whole life of that older holder, while its socket, lock, process or unit lives. The process check reads kernel state without reaping. A recorded process must belong to this user and name the same holder and session. A socketless older holder that cannot be stopped safely keeps resume and restore closed. So a slow tick copy from before the rename cannot expose the old name again. Once no older holder runs, the helper reads the latest manifest under `first.lock` before writing a settled name. It preserves the final exit state and removes `rename.unsaved` only after the save. Resume reconciles before applying memory choices and clearing the restore count. Restore reconciles before counting its attempt, and a failed reconciliation starts nothing. An uncertain record keeps guarded deletes closed with `session_renamed`, even after the holder stops, until a rename sets a new name.
   - For a session that does not run, the rename takes `op.lock` and `first.lock`, writes the manifest, and removes `rename.unsaved`.
   - A delete with a name checks it against the manifest alone, and asks the running session nothing. The empty name of a session with no name is a name too: the client sends it, and the host refuses the delete when the session got a name since. Only a delete without a name, as the helper's own `rm`, takes the session whatever its name. A manifest that cannot be read fails closed, with `name_unknown`. A rename never comes between that check and the delete. A delete stops the holder without the lock's help, so a holder's rename that waits for the lock never blocks it.
   - The holder numbers each copy of the manifest that it saves, its first save at the start too, and the saved number moves only after a good write. A copy that is not newer than the one saved last, such as one that a tick took during a rename, would put back an older state: its save writes the session as it is now instead. A tick whose save fails keeps the session marked as changed, so the next tick saves it again.
   - A new session's start holds `op.lock` from its first save until the holder has started. A resume and a restore hold `op.lock` across their start too. Each holder also holds `holder.lock` while its process runs, and takes `first.lock` around its first save. When a slow restored holder has not opened its socket yet, a rename writes the new name under `first.lock`. The holder's first save then reads that name, or stops without writing if a delete removed the folder. The holder records its own unit.
   - A failed write of the manifest leaves no temporary file, so a tick that tries again each second leaves none either.
   - When a new session's secrets cannot reach its holder, the start stops that holder and waits for it, under the lock, before it deletes the session. A resume or a restore whose secrets cannot reach the new holder stops it in the same way, so its agent never starts without them. A holder that waits for its secrets answers no other request, so it would start its agent later, and save into the deleted folder. With systemd, the unit must be inactive or failed within 3 seconds, whatever the stop answered: a unit that systemd already removed, as after its holder crashed, counts as stopped. Without systemd, the start waits for the very process that it started, with SIGTERM and then SIGKILL, so a process ID that another process took never passes for it. When the start cannot establish that the holder stopped, it keeps the session, and its error says why, with `secrets_failed`, and that the agent may start later without its secrets. A failed start removes the folders that `--mkdir` made only when it removed the session too. A session that it keeps, as one whose agent could not start, keeps its folder, so that a resume can start the agent there.
   - A holder that started, and does not answer within the start's 5 seconds, is stopped in the same way: the start deletes the session, or keeps it when the holder cannot be stopped, with `start_failed`. A holder that recorded its own end, `errored` or `exited`, before any agent started, as one whose agent could not start, is not late: its session stays, with that record. A holder that recorded nothing yet, its agent working, or an end after its agent started, is late. A resume stops a late holder too, and keeps its session. A restored holder never waits for secrets, so a restore does not stop a late one. It waits up to 30 seconds more for it, and the session runs when it answers. When it still has not answered, the restore records its process or unit in the manifest under `first.lock`. A later delete or resume then stops that pending holder before acting, and another restore skips it. When the restore cannot save that record, it stops the holder at once.
   - `launchHolder` names the unit to the holder in the variable `SIMSTIM_UNIT`. The holder takes its unit from its cgroup, or else from that variable when it names the unit of its own session, right after it loads the manifest. So it records the unit in its first save also on a host with cgroup v1, the save of an agent that cannot start too, and every later save keeps it.
6. It prints `Deleted auth-fix (abcd2345) on box.` on stderr, and the worktree line of today.
7. From isolation task P1, it refuses while commits are not pulled, unless `--force` (isolation spec 9.1).

### 9.6 `show`

`show [<address>] [--json] [--screen]` describes one session.

1. Without an address, and with input on, it opens the picker.
2. It dials the host and reads the session's details. It does not wake a stopped host. On a host that its provider script starts and that does not answer, it asks the script's `status`. Only when that says `stopped` does it fail with `box is stopped, and show does not start it`, and `Try: simstim host start box`. Any other failure, such as a key that SSH refused, keeps its own error and hint.
3. The text form starts with a title line. Then comes one line for each field of `api.SessionInfo` that has a value, in the order of the type. Each line has a label in sentence case. The last line is `Attach`, with the command. It names the session by `host/ID`, as every printed command does ([section 9.2](#92-addresses)), so it never names another session:

   ```text
   auth-fix on box
     ID              abcd2345
     Agent           claude
     Folder          /home/dev/src/app
     State           working (from hooks)
     Since           10 Nov 2026 10:40
     Created         10 Nov 2026 10:40
     Attach          simstim attach box/abcd2345
   ```

   The name is in the title. The state line holds where the state came from, and why an agent ended, as `errored (exit code 3, from process)`. A field that it says faithfully has no line of its own. Any other has one: the reason of a start that failed, a signal that the out-of-memory mark hides, or an exit code of 0. An ended agent whose session is still open says so. The columns and the rows share one line, `Size`.

4. `--json` prints the session object of [section 7.1](#71-which-commands-print-json).
5. `--screen` (C11) prints the session's last screen as plain text. It reads the screen through the `peek` op, as the launcher's peek does, and renders it with the same terminal model. It prints one line for each row, without escape sequences and without spaces at the ends of lines. With `--json`, it adds `screen`, an array of lines.
6. Isolation task P1.8 adds the wall, the layers, the unknown action, the rules and the counts.

### 9.7 `ls`

1. `ls [<host>]` takes the host as an argument. `--host` stays, as a kept alias.
2. A host as an argument and a different `--host` fail with exit code 2: `ls got two hosts: box and nuc`.
3. The output stays as today. `--json` adds `v` to each element.
4. `ls` writes each answer to the session cache.

### 9.8 Tests

- Unit tests of the address rules, with these cases:
  - `-`, `box/a`, `a` on one host, `a` on two hosts, and no `a`.
  - A stale cache, and a host that does not answer, with `rm` and with `attach`.
  - A session whose name is also a host's name.
  - Two writers of the session cache at once leave a file that parses.
  - A session whose name is another session's ID, an ID that went with a session named like it still there, `new` and `rename` with a name of an ID's form, and a script's `host/ID` from `--json`.
  - `NewID` always makes an ID with a digit, a name of 8 letters such as `frontend` is a name, and a folder named like an ID gives a name with `-s`.
- Round-trip tests of every printed command: each one parses as its command, and its address finds the same session. Two sessions have names that clean to the same text, and a host's name is long enough to cut the line.
- Golden tests of the prompts of `rm` and `attach`.
- End-to-end runs of `rm`, `attach` and `show` against `E2E_HOST`, on sessions that the test starts and deletes.

## 10. Flags (C6)

### 10.1 Short flags

Each short flag has one meaning everywhere. A test walks `Root()`, and fails when a letter has two long names, or when a short flag is not in this table. The test skips a hidden flag that keeps an old form of section 3, such as `-d` for `--dir`, until its end.

| Short | Long | Meaning | Commands |
| --- | --- | --- | --- |
| `-a` | `--agent` | The agent to start | `new` |
| `-C` | `--dir` | The folder on the host | `new` |
| `-d` | `--detach` | Start without attaching. Only after the end of section 3 | `new`, `resume`, `fork` |
| `-f` | `--force` | Go on although work is lost, such as a file that `cp` replaces | `cp` (P5.2) |
| `-h` | `--help` | Help | every command |
| `-n` | `--name` | The name of what the command makes | `new`, `setup`, `shell`, `fork` |
| `-w` | `--worktree` | Give the session its own worktree | `new` |
| `-y` | `--yes` | Answer yes to the command's questions | every command that asks |

No command has `-v` ([clig.dev](https://clig.dev/#arguments-and-flags)). Today the root has it. Cobra adds `-v, --version` at the first run, because `Root()` sets `Version` (`InitDefaultVersionFlag` in Cobra v1.10.2). So C6 defines `--version` in `Root()` without a short form, and Cobra adds none. `-v` stays as a hidden old form of section 3. The test calls `InitDefaultVersionFlag` on the root before it walks `Root()`.

### 10.2 Flags that more than one command uses

Each of these flags has one meaning on every command that takes it. Helper functions in the new `internal/cli/flags.go` define them, such as `addYes(cmd)` and `addJSON(cmd)`. A test fails when a command defines a flag of this table without its helper.

| Flag | Meaning | Commands |
| --- | --- | --- |
| `-y`, `--yes` | Answers yes to every question that the command would ask. It never skips a typed name, and never goes on where work is lost. | `rm`, `prune`, `doctor`, `host doctor`, `host add`, `host set`, `setup`, `new` with `--in` (P1.13), `policy apply` and `policy learn` (P2.14), `rewind` (P5.4) |
| `--force` | Goes on although work is lost. `cp` also takes it as `-f`. | `rm`, `pull`, `host rm`, `cp` |
| `--confirm <name>` | Gives the typed name of a severe action. | `host rm`, `host rekey`, `host set`, `policy lockdown` |
| `--json` | Prints data for scripts, and turns input off. | [Section 7.1](#71-which-commands-print-json) |
| `--dry-run` | Shows what would change, and changes nothing. | `new auto`, `prune`, `policy allow`, `deny`, `learn`, `apply` |
| `--no-input` | Turns input off. | every command, from the root |
| `--no-color` | Turns colour off. | every command, from the root |
| `--debug`, `--debug-file` | As today. | every command, from the root |
| `-n`, `--name` | Names what the command makes: a session, a host or a shell session. | `new`, `setup`, `shell`, `fork` |
| `--host <host>` | The host to act on. Under `policy`, the host layer. | `ls`, `agents install`, `agents login`, `setup`, `policy` |
| `-d`, `--detach` | Starts without attaching. | `new`, `resume`, `fork` |
| `--memory-max <size>` | Caps the memory of one session. | `new`, `resume` |
| `--memory <size>` | The memory that a machine or a sandbox gets. | `host create`, `host join`, `new`, `resume`, `attach` (P1.14) |
| `--cpus <n>` | The CPUs that a machine or a sandbox gets. | `host create`, `host join`, `new`, `resume`, `attach` (P1.14) |
| `--secret <NAME>` | Passes a secret from `[secrets]`. | `new`, `resume` |
| `--for <time>` | How long it lasts. | `debug start`, `debug profile`, `policy allow` |
| `--all` | Widens the command to every session. | `host restore`, `policy` |
| `--follow` | Keeps printing new lines until `ctrl+c`. | `policy log`, `events` |
| `--fix` | Offers each fix. | `doctor`, `host doctor` |
| `--label <key>=<value>` | A label of the host. | `host add`, `host invite` |
| `--token-command <command>` | A command that prints a token on this computer. | `host create`, `host alerts set` |
| `--root-step run\|print\|skip` | What to do with the root step. | `host add`, `host prepare`, `host rm` |
| `--images auto\|host\|laptop` | Where images come from. | `host add`, `host prepare` |
| `--profile <p>` | A profile. | `new`, `policy` |
| `--in <host>/<session>` | Inside that session's sandbox. | `new`, `policy` |
| `--count <n>` | How many to make. | `new`, `fork` |

### 10.3 Values

**Lists.** A list of names takes a repeated flag or commas: `--agents claude,codex` is the same as `--agents claude --agents codex`. pflag's `StringSlice` reads each value as comma-separated, and `StringArray` does not split (`string_slice.go` in pflag v1.0.10). A list of values that may hold a comma takes only a repeated flag. Such values are paths, commands, and `KEY=VALUE` pairs with free values. The flags are `--env`, `--set`, `--copy`, `--mount` and `--include`. `--label` takes both forms, as today.

An explicit `host alerts set --events` list must name at least one event. An empty value or a list of only commas is an error. Omitting the flag keeps the existing defaults.

**Sizes.** A size is a number and a unit in powers of 1024, such as `512M`, `4G`, `4GiB` or `1.5G`, as `sessions.memory_max` takes. One parser in `internal/cli/flags.go` reads every size, with the unit rules of `api.ParseSize`, which `api.ParseMemoryMax` also uses, and without its minimum. A size that comes to less than one byte is an error. A bare number is an error with exit code 2, except for the two old forms of section 3. In those, a bare number is GiB, and 0 or less is the default, as before. A whole number keeps the forms that the old flag read, such as `010` for 8 and `0x10` for 16.

**Durations** are Go durations, such as `30s`, `10m` or `2h`. A flag that can turn a time off also takes `never`. An empty value of such a flag keeps its default, as before.

### 10.4 The change of `-d`

1. C6 adds `-C` as the short form of `--dir`.
2. Until the end of section 3, `-d <folder>` sets the folder and warns. pflag gives a flag one short form only (`Flag.Shorthand` in pflag's `flag.go`), so `-d` is a hidden flag that sets the same value.
3. After the end, `-d` is the short form of `--detach` on `new`, `resume` and `fork`.
4. A script that still passes `<host> -d ~/src` then fails loudly. `~/src` becomes a second word before `--`, which is the usage error "Two words before `--`" of isolation spec 14.14. C6 adds that check to `new`, so that it does not wait for H12. Without a host word, which isolation task H12 allows, `~/src` names the host, and an unknown host fails with exit code 3. Only a folder that has a saved host's name would start a detached session there instead.

### 10.5 Tests

- The short-flag test of section 10.1, and the helper test of section 10.2.
- Unit tests of the list, size and duration parsers.
- A unit test of each old flag of section 3. A unit test of `new -d <folder>` after the end, with and without a host.

## 11. Host commands (C7)

### 11.1 `host set`

`host set <host> <key>=<value>…` changes named settings of a host, and only those ([Tailscale](https://tailscale.com/kb/1080/cli)).

| Key | Values | Kept in | Task |
| --- | --- | --- | --- |
| `idle` | A duration, or `never`. Only a host that `host create` made. | `machine.idle_stop` in `hosts.toml` | C7 |
| `label.<key>` | Text. An empty value removes the label. | `labels` in `hosts.toml` | H19 |
| `tier` | `own`, `throwaway` or `shared` | `tiers.toml` | H4 |
| `floor` | `none`, `process`, `gvisor` or `microvm` | `wall_floor` in `hosts.toml` | P1, H10 |
| `drain` | A reason, or `off` | `drain.json` on the host | H14 |

1. The command checks every pair before it changes anything. An unknown key fails with exit code 2, with suggestions from the keys. A bad value fails with exit code 2.
2. A key that the build does not have yet is an unknown key.
3. `idle` on a host that `host create` did not make fails with exit code 2, as `host idle` does today.
4. `drain` takes a reason of 1 to 200 characters, such as `drain="kernel update"`. `drain=off` removes the mark (isolation spec 14.11.3). The values `on`, `true` and `yes` fail with exit code 2, and `Try: simstim host set box drain="kernel update"`.
5. Raising a tier dials the host, checks its pinned host key, and asks for the host's typed name, or takes `--confirm <host>`. Lowering a tier works at once in H4, and asks first from H5 (isolation spec 9.1), where `-y` answers.
6. The command then applies the pairs in the order given. It saves the keys that live on the laptop in one write. A key that lives on the host dials it. It holds the lock of the host from before it reads the entry until it ends (section 11.3, rule 13).
7. When one pair fails, the error says which pairs took effect.
8. Each change prints one line on stderr, such as `box: idle 30m → 2h.`
9. Without a pair, it fails with exit code 2: `host set needs a setting, such as idle=2h`, and `Try: simstim host show box`.

### 11.2 `host show`

`host show <host> [--json]` describes one host. It is the command of the launcher's host details ([the UX spec, 2.7](ux-spec.md#27-sheets)).

1. It prints the saved entry: the SSH target, the tier, labels, the machine, idle, and the names of the host's secrets.
2. It dials the host for up to 30 seconds. It neither wakes the host, nor installs or prunes a helper. A client built with `go install` loads its helpers from this computer's cache first, so it looks for the right helper. It never downloads them. It takes no lock. A missing helper is a reason why the host did not answer, with `Try: simstim host upgrade box`. It prints the route, the system, the helper's version, the agents with their versions, and the limits. It cleans each string that the host sent with `api.CleanText`, and cuts it to 200 runes.
3. A host that does not answer is data, not a failure. The command prints the saved part, then `box did not answer: <reason>`, and exits with 0. The reason's `Try:` line goes to stderr. A reason without its own, such as a timeout or a closed connection, gets `Try: simstim host doctor box`.
4. `--json` prints the host object of `host ls --json`, with `reachable`, `error` and `try` when it did not answer, and `info`, the object that `host doctor --json` prints. The `info` keys and values retain the data from the host, without the text view's cleaning or length limit. JSON escapes terminal controls, including bidirectional and zero-width characters, so printing it is safe. Scripts receive the original strings when they decode it. The host object has only its `snake_case` keys: the Go keys are an old form of `host ls` alone. When the host answered, `session_count` counts its sessions, which the launcher shows with the limits. `info` has no `agent_checks`, because the launcher does not ask for them.
5. The launcher's host details and `host show` use one function in `internal/client`, so they match.
6. Isolation tasks P1 and H5 add more lines (isolation spec 14.8.6). They add each driver's probe, the levels, the floor, the tier, the room, the health and the names sent to the host.

### 11.3 `host rm`

`host rm <host> [--destroy] [--purge] [--force] [--confirm <host>] [--delete-logins] [--root-step run|print|skip]`

1. An unknown host fails with exit code 3, with suggestions.
2. The parts:
   - **Forget**, always: the host leaves `hosts.toml`.
   - **Destroy**, with `--destroy`: the machine that `host create` made.
   - **Purge**, with `--purge` (H15): everything that the tool put on the host (isolation spec 14.12).
3. With input on, the command asks about each part that the flags do not name, and that applies to this host. Each question is a typed name (G9). It says what the part removes:

   ```text
   box was made by `host create`: the lima machine box-1a2b. It holds 2 sessions.
   Type box to delete the machine too, or press Enter to keep it:
   ```

4. Enter keeps that part. A wrong name cancels the whole command, with exit code 130, and nothing changes.
5. A part named by a flag still needs the typed name, or `--confirm <host>`. Its question is `Type box to delete the machine, or press Enter to keep it:`. Enter keeps the part that the flag named, so the command then changes nothing, and exits with 130.
6. With input off, the command does only what the flags say. `--destroy` or `--purge` without `--confirm <host>` fails with exit code 4, and `Try: simstim host rm box --destroy --confirm box`. Without those flags, it does not look at the machine. It says on stderr that the machine stays, with the flags that delete it too.
7. `--confirm` with another name fails with exit code 2. So does `--confirm` or `--force` without `--destroy`, because each acts only on the delete. An unknown host gets `Try: simstim host ls`, or with `--confirm`, the line with the nearest host in both places.
8. `--destroy` on a host without a machine fails with exit code 2: `box was not made by host create, so there is no machine to delete`, and `Try: simstim host rm box`.
9. `--destroy` refuses while a session works, as `host destroy` does today, unless `--force`. The refusal has exit code 1. A running machine whose sessions the client cannot list counts as one where a session works. Today `workingSessions` in `internal/cli/create.go` counts it as idle. A machine whose state its provider cannot tell counts the same. With `--destroy`, the refusal comes before the question. Every look happens under the lock of rule 13, `--destroy --confirm`'s too. A question may wait long, so after the typed name the command asks the provider and the host again, just before the delete. As a cheap guard, it also checks that the saved entry is the same, or it changes nothing, with exit code 1. Only a changed provider, ID or `created` stamp counts as a host made again. It refuses when a session works then, or when either check fails. The names of the sessions are cleaned as in section 11.2. The look at the machine never installs or prunes a helper.
10. The work goes in this order: purge, destroy, then forget. The client records each part, and each step of isolation spec 14.12, in `~/.local/state/simstim/hosts/<host>-<hash>/removal.json` when it ends. The folder holds the name in lower case and a hash of its exact case, so `Box` and `box` keep two records. A failure stops the command with exit code 1, and the host stays saved, so the user can run the command again. The forget removes the generated SSH config before the saved entry, so a failure there keeps the host too. It also removes a config that an earlier client generated under the name alone, when the first line names that exact host and no saved case twin may use the path. A second run skips each recorded step of a purge. So it never dials a purged host, which would install the helper again. The record never decides that a machine is gone: the command always asks the provider. Only a machine that its provider no longer has counts as destroyed. The record then says that an earlier run deleted it. The record names the machine's provider, ID and `created` stamp, the time when `host create` made it. Lima and Docker give each machine of a host the same ID, so the stamp tells them apart. The forget, the last part, deletes the record, and so does the launcher's remove. `host create` deletes it before it makes anything, and refuses when it cannot. A record that cannot be deleted is an error whose `Try:` line names the file.
11. The command prints what it did and what stays, on stderr. A kept machine gets today's line, which names the provider's own tools.
12. `host destroy <host>` runs `host rm <host> --destroy`, and warns. Its `-y` becomes `--confirm <host>`, and its `--force` stays.
13. One rule keeps the commands that change a host apart: each save or forget of a host compares. The command names the saved entry that it read before it began, or none for a new host. Under the lock of `hosts.toml`, the client saves only while the saved entry is still that one, every field and the `created` stamp too. An empty table or list counts as none. Otherwise the command changes nothing, and exits with 1. A host removed, made again or changed meanwhile is `host_changed`. An example is `box changed while setup ran, so it was not saved`, with `Try: simstim host show box (then run the command again)`. A new host whose name another command took meanwhile is `host_exists`. So a removed host never comes back, and a host made again is never overwritten. This covers `host add`, `setup --host`, and the launcher's add, its save after a create and its remove.
    - A command that works on a machine also holds the lock of the host, from its first step to its end. These are `host create` with its setup, the guided setup's create, and `host set`. `host rm` holds it from its first look through the question to the forget. So nothing is looked at or written outside it. Each save or forget takes the lock for its moment too. No lock is held across a dialog of the launcher or across the add-host wizard.
    - The lock is a file beside the record of rule 10. A command that finds it taken waits for nothing. It fails at once with `host_busy`, and names the kind of command that holds it, such as `box is busy: host rm changes it`. So a `host create` fails while the question of a `host rm` of that name is open. A context that outlives its lock no longer holds it.
    - A lock file that cannot be made is `lock_failed`. Its `Try:` line names the nearest folder that exists, to make writable, or the file that stands in the path, to move. A lock that the file system refuses, as on a network disk, asks for a local disk.
    - Each read, change and save of `hosts.toml` holds one short lock of that file, after the lock of the host. So two commands that change two hosts at once lose no change. A command waits for that lock. After a second it says `Waiting for another command that saves …` on stderr, as a warning with `--json`. After 10 seconds it fails with `hosts_busy`, and Ctrl+C stops the wait as it stops any step. A cancelled command never saves: the client checks for the cancel after it takes the lock, and again just before the save. `host rm` keeps the cancel, with exit code 130, and keeps the code and hint of an error such as `hosts_busy`.
    - When the save after `host create` fails, the client deletes the machine that it made, so no machine stays that no saved host names. When that delete fails too, the error names the machine and the provider's own tools.
    - After `host_changed` or `host_exists`, the launcher reads the host again, so its next try compares with the saved entry. A host that the launcher made and could not save stays off its list when the wizard closes. The launcher's forget deletes the record of rule 10 under the same lock of the host.

### 11.4 `host upgrade`

`host upgrade <host>` updates the helper on one host.

1. It does not start the bridge first. A dial installs a missing helper by itself (`Dial` in `internal/client/conn.go`). From isolation task P4.1, a changed helper stops a dial with `digest_mismatch`. So `host upgrade` reads the host over the transport, as `Probe` in `internal/client/bootstrap.go` does. From P4.1 it also runs the digest check there.
2. This client's helper is there, and its digest matches: it prints `box has helper 0.1.2-1a2b3c4d already.` on stderr, and exits with 0. The version is the helper's, as `version` shows it.
3. This client's helper is missing, or its digest does not match: it installs the helper, as `host add` does, without the other steps of `host add` and without a question. The verb is the user's yes. Then it dials, with the check of P4.1.
4. A newer helper on the host, from a newer client, stays. Each client runs its own version of the helper. So `host upgrade` never runs the helper's `prune`, which `host add` runs after its install.
5. Running sessions keep their own process, as today. New sessions use the new helper.
6. Isolation task H9 adds the runtime files.
7. A host that does not answer fails with exit code 1.
8. `--json` prints what it did as one object (section 7.1). Its confirmations go to stderr (section 7.5).
9. Isolation task P7.1 adds `--feed`. It also copies the newest malicious-package feed to the host (isolation spec 7.11).

### 11.5 `host add --yes` and the tier

Once isolation task H4 adds tiers, `host add --yes` without `--tier` fails with exit code 4, and `Try: simstim host add box --tier own --yes`. A script must name the tier.

A host added by an older client after the tiers migration counts as `shared`. At its next foreground terminal use, the client checks its pinned connection and asks the same tier question. It saves no default answer. Background uses and commands with input off keep `shared` until an explicit answer is saved. `--yes` cannot answer this question. `host set` can record an explicit tier instead.

### 11.6 Tests

- Unit tests of `host set`: each key and value, an unknown key, a bad value, and a failure in the middle.
- A unit test that `--destroy` refuses when the machine runs and its session list fails.
- Golden tests of the questions of `host rm`.
- A test that `host show box --json` and the launcher's host details come from one fixture with the same data.
- An end-to-end run against a test host: `host show` and `host upgrade` with the same version, which change nothing.

## 12. Agents: install and log in (C8)

The agent table already holds what each agent needs: `Setup.Install`, `Setup.Source`, `Setup.Login`, `Setup.LoginStatus`, `Setup.LoginFiles` and `Setup.LoginEnv` (`internal/agents/setup.go`). C8 adds no field. [The CLI study, section 12](../research/cli-dx.md#12-installing-agents-and-logging-in) shows how other launchers do this.

Their sessions change the host's own installs and login files, which no sandbox reaches. So from isolation task P2.10, when walls are on by default, they still run at the level `none`. The command names that level for the user, so it needs no `--downgrade`. The trusted manifest still records a downgrade, the row shows `!`, and the `wall_downgraded` alert goes out (isolation spec 8.2, rule 5).

### 12.1 Which host

`agents install` and `agents login` take the host as their second argument, or as `--host`.

1. An argument and a different `--host` fail with exit code 2.
2. Without a host, the only saved host is the host. With several, the command fails with exit code 2: `agents login needs a host: you have box, nuc`, and `Try: simstim agents login claude box`.
3. Once isolation task H12 adds the default host, the rules of isolation spec 14.10.1 pick it.

### 12.2 `agents install`

`agents install <agent> [<host>] [--reinstall]` works as today ([install agents](../how-to/install-agents.md)), with these changes:

1. `--reinstall` runs the installer again on an agent that is installed. `--force` stays as an old name.
2. An unknown agent fails with exit code 3, with suggestions from the agent kinds, and `Try: simstim agents ls`.
3. An agent without an install command fails with exit code 2.
4. The line after the install names the new command: `Next, log in: simstim agents login claude box`.

### 12.3 `agents login`

`agents login <agent> [<host>]` starts the agent's own login in a session on the host.

1. An unknown agent fails with exit code 3, with suggestions.
2. An agent without `Setup.Login` fails with exit code 2. The message names its variables from `Setup.LoginEnv`: `aider has no login of its own. It reads ANTHROPIC_API_KEY or OPENAI_API_KEY.` The `Try:` line says: `Add the key under [secrets] in config.toml, and pass it with --secret.`
3. The command dials the host and checks the agent. An agent that is not installed fails with exit code 1, and `Try: simstim agents install claude box`.
4. When a session named `login-<agent>` runs on the host, the command attaches to it, and says so. With two such sessions, it attaches to the newer. Two clients that start a login at the same moment can still make two sessions.
5. Otherwise it starts a session named `login-<agent>`. The session runs `Setup.Login`, or the agent itself when `Setup.Login` is the agent's first binary, as `onboard.LoginCommand` decides today. The session gets no secret.
6. With input on, the command attaches. The text before the attach names the detach key of `config.toml`. When the user leaves, by a detach or at the end, it checks the login as `host doctor` does. It prints the agent's line with its mark: ✓, !, ? or ✕. The line's title is the agent table's, and the host's path and reason print cleaned.
7. After a detach, it asks the host whether the session still runs, because a login can end while the client checks. If it runs, the agent's line has no `Try:`, and the command prints `The login keeps running. Attach again: simstim attach --id -- box/k3v7qa2m`. If it ended, the line keeps its `Try:`. A check that fails after a detach says so, prints the same attach line, and exits with 0, because an attach that ended with a detach is done (section 5.1).
8. With input off, it starts the session and prints `Started box/k3v7qa2m. Attach to finish the login: simstim attach --id -- box/k3v7qa2m` on stderr. It exits with 0, as `agents install` does today. For a login that runs, it prints `The login of Claude Code already runs in box/k3v7qa2m.` and the same attach step.
9. The client never reads, stores or passes the agent's credentials. The agent writes them on the host, in the files of `Setup.LoginFiles`.
10. A walled session never sees those files (isolation spec 7.3). From isolation task P1.8, when the host's floor is above `none`, the command adds one note. The note says that a walled session needs a token in `[secrets]`, such as `CLAUDE_CODE_OAUTH_TOKEN` from `claude setup-token`.
11. Every address that the command prints uses the session's ID, never its name, because two logins that start at the same moment share the name. The client checks the ID that the host sent before it prints it: 1 to 64 letters, digits, `-` or `_`. Any other ID fails with exit code 1.
12. `--json` starts the login without attaching, or finds the one that runs, and prints its session object ([section 7.1](#71-which-commands-print-json)), with `started`: `true` when the command started it. Its `address` uses the ID too. Unsafe runes are physically escaped in JSON. Its decoded keys and values stay exact, as section 7.4 requires.
13. The login runs again also when the agent is logged in, for example to change the account.

`onboard.LoginCommand` then returns `simstim agents login <agent> <host>`. The hints of `host doctor`, `agents install` and the guided setup use it.

### 12.4 Tests

- Unit tests: an unknown agent, an agent without a login, and an agent that is not installed. Also a login session that already runs, and input off.
- Golden tests of the output after a login.
- An end-to-end run of `agents login claude` against `E2E_HOST` that stops at the agent's first prompt, then deletes its session. It never types into the agent.

## 13. `config check` (C9)

### 13.1 What it reads

1. `config.toml` and `hosts.toml`, the two files whose paths `config path` prints.
2. From the isolation plan: `tiers.toml` (isolation spec 14.6), and the repository file `.simstim/sandbox.toml` of the current folder's repository (isolation spec 8.5).

A file that does not exist is not a finding. The report says `none yet`.

The check opens a file once, and asks the open file what it is. It reads a regular file only, and at most 1 MiB of it, whatever size the file reports. It takes a key of at most 64 parts, and a value of at most 64 levels of arrays and inline tables. The full names of all keys, with the tables that hold them, may add up to 8 MiB, because the TOML package keeps a string of each. A file past a limit is a finding, so that a file cannot make the check slow. These limits are a choice of this project. The time and the memory of the check grow with the size of the file, and no faster.

### 13.2 What it checks

| Check | Severity | Code | Task |
| --- | --- | --- | --- |
| A file that cannot be read, or that is not a regular file, such as a FIFO or a device | error | `unreadable` | C9 |
| A file larger than 1 MiB, a key of more than 64 parts, a value of more than 64 levels, or full names of keys that add up to more than 8 MiB | error | `too_large` | C9 |
| TOML syntax | error | `toml_syntax` | C9 |
| A key that the file's struct does not know, with suggestions from the keys of that table | error | `unknown_key` | C9 |
| A value of the wrong type | error | `wrong_type` | C9 |
| Two spellings that match one known key or table, such as `motion` and `Motion` | error | `bad_value` | C9 |
| A `[host.machine]` without a provider or a non-empty `id` | error | `bad_value` | C9 |
| A value outside its list, such as `ui.motion` or `attach.scrollback` | error | `bad_value` | C9 |
| A size that `api.ParseMemoryMax` refuses, or a duration that does not parse. An `idle_stop` takes the rule of `host idle`: a minute or more, or `never` | error | `bad_value` | C9 |
| A `[keymap]` name that is not a launcher command, or a key that does not parse or that the launcher never matches | error | `bad_value` | C9 |
| A secret with an empty command | error | `bad_value` | C9 |
| A host name that `ValidHostName` refuses, or two hosts with one name | error | `bad_host` | C9 |
| A host that the client reaches with SSH and an empty or missing `ssh`. A host with a `[machine]` needs none: its provider reaches it, Docker and E2B without SSH, and Lima through the Host of Lima's own SSH config | error | `bad_host` | C9 |
| An old name of section 3 | warning | `old_name` | C9 |
| A key that a layer may not hold (isolation spec 8.2) | error | `layer_key` | P1.12 |
| A rule that names a host that is not saved | error | `host_unknown` | H19 |
| `sandbox.host` in the layer for all sessions, or in `[lock]` | error | `host_rule_layer` | H19 |

The known keys come from the TOML tags of `client.Settings` and `client.Host`, read with `reflect`, as `cmd/docgen` reads them for the reference. A key matches its field as the TOML package matches it: exactly, or else in any case (`decode.go` in BurntSushi/toml v1.6.0). So `[UI]` with `Motion` is no finding. Two spellings that match one field are an error, even when their values agree. The report names both spellings and the line of the second key in sorted order. This rule does not fold host names or the keys of free maps. A finding names the key as the file spells it, and the rules above know it by its field. `[keymap]` and `[secrets]` are free maps: their keys are checked by the rows above.

### 13.3 Lines

A syntax error carries its line and column in `toml.ParseError.Position` (`error.go` in BurntSushi/toml v1.6.0). `toml.MetaData` has no exported position for a key (`meta.go` in v1.6.0), so `config check` finds the line itself:

1. A small lexer reads the file once. It knows basic and literal strings on one line or several, comments, arrays and inline tables. So quotes in a comment or in another string start nothing. It skips a byte order mark, and reads the escapes of a quoted key, as the TOML package does.
2. It records the line of each header, of each element of an array of tables, and of each key, with every part of a dotted key.
3. A key in an inline table gets its own line, also when the table spans lines, as TOML 1.1 allows. An inline table in an array is an element of an array of tables, with the line of its `{`.
4. When a key has no line that the lexer recorded, the finding has no line.

### 13.4 What other commands do with a broken file

- Every command reads `config.toml` and `hosts.toml` as `config check` does: once, regular files only, and at most 1 MiB. So a FIFO or a device never makes a command wait.
- A syntax error or two aliases of one field in `hosts.toml` stops a command, as `LoadHosts` does today, with exit code 2 and `Try: simstim config check`. A `hosts.toml` that is not a regular file, or is larger than 1 MiB, stops it with exit code 1 and the same `Try:` line.
- A `config.toml` that does not decode, with a syntax error or a value of the wrong type, does not stop a command. Neither does one that cannot be read. Two aliases of one known field also discard the file, with a warning that names both spellings and their table. The loaders reject these aliases before they choose any setting or host route. Unknown keys and free map keys keep their existing rules. Today `LoadSettings` ignores it without a word. After C9, the command prints one warning, `config.toml has an error on line 4, so its settings are not used`, or `config.toml cannot be read, so its settings are not used`. Then it goes on with the defaults, for every setting. The launcher shows the warning as a notice at its start. After an edit in its settings editor that breaks the file, the launcher keeps the settings that it had, and says so with the same warning. Each action that uses the settings reads the file again. An attach uses the new detach and image paste keys and scrollback. A start uses the new inline mode, memory cap and secrets. A resume uses the new commands for secrets. An install or login also reads it. A good read gives the new settings, also after an edit in another window, and the launcher keeps them. A changed `ui.motion` takes effect at that read, or after the settings editor closes. The launcher applies only its latest requested read. An older action or editor reload cannot replace newer settings or restore an obsolete warning. A file that cannot be used gives the settings that the launcher holds, and the warning, once until a good read. From isolation task P1.12, `config.toml` holds locks, policy layers and secrets. Then a syntax error in it stops each command that writes or sends them, with exit code 2 and `Try: simstim config check`. These are `new`, `resume`, `fork`, `image pull` and each `policy` command that writes or sends layers. `policy lockdown` still runs (isolation spec 8.2, rule 4). `ls` and `attach` keep the warning, send no layers and arm no secrets.
- An unknown key stops nothing. `doctor` reports it as today, and adds `Try: simstim config check`.

### 13.5 Output and exit codes

The report goes to stdout. Each finding is one line in the GNU form `file:line: message` ([GNU](https://www.gnu.org/prep/standards/html_node/Errors.html)), with `warning:` before the message of a warning. A TOML syntax error also has its column, in the GNU form `file:line:column: message`. Paths start with `~` for the home folder. These abbreviated paths use forward slashes on every platform.

```text
~/.config/simstim/config.toml:4: unknown key "motoin" in [ui]. Did you mean "motion"?
~/.config/simstim/config.toml:12: warning: "session: preview" is an old name. Use "session: peek".
~/.config/simstim/hosts.toml: no problems
1 error and 1 warning in 2 files.
```

Once isolation task P1.12 adds layers, the report ends with the layers that apply to the current folder:

```text
Layers for ~/src/app:
  all sessions  config.toml
  host          box (the default host): hosts.toml
  repository    github.com/acme/app: config.toml [repos."github.com/acme/*"], .simstim/sandbox.toml
  profile       none
```

| Exit | When |
| --- | --- |
| 0 | No error. Warnings may exist. |
| 1 | At least one error. |
| 2 | A usage error of `config check` itself. |

`--json` prints the report as one object on stdout, with the same exit codes:

```json
{"v": 1, "ok": false,
 "files": [{"path": "~/.config/simstim/config.toml", "exists": true}],
 "findings": [{"path": "~/.config/simstim/config.toml", "line": 4, "severity": "error", "code": "unknown_key",
               "message": "unknown key \"motoin\" in [ui]", "suggestions": ["motion"]},
              {"path": "~/.config/simstim/hosts.toml", "line": 2, "column": 7, "severity": "error", "code": "toml_syntax",
               "message": "expected value but found '\\n' instead"}],
 "layers": []}
```

A finding has `line` when the line is known, and `column` for a TOML syntax error only. A `message` has no full stop at its end.

### 13.6 Tests

- Unit tests with one broken file for each row of section 13.2, and a check of each line number.
- Golden tests of the text and JSON reports.
- A test that a syntax error in `config.toml` gives one warning, and the defaults. From P1.12, the same error stops `new` and `policy allow` with exit code 2.

## 14. Completion (C10)

### 14.1 Shells

Cobra's `completion` command prints a script for bash, zsh, fish and PowerShell. bash shows descriptions with its second version of the script ([Cobra](https://github.com/spf13/cobra/blob/main/site/content/completions/_index.md)). The release already ships the four scripts (`cmd/docgen -completions`, `.goreleaser.yaml`).

Cobra adds its default `completion` command only when a command runs, so `cmd/docgen` does not see it today. C10 calls `root.InitDefaultCompletionCmd()` in `Root()`, which Cobra v1.10.2 exports (`completions.go`). The reference then gets a page for `completion` and each shell. [The UX spec](ux-spec.md#6-cli) lists PowerShell beside the other three shells.

### 14.2 What completes

The shell's script runs the hidden command `__complete`, and Cobra calls a command's `ValidArgsFunction` or a flag's completion function. Each value may carry a description after a tab ([Cobra](https://github.com/spf13/cobra/blob/main/site/content/completions/_index.md)).

| Place | Values | Description | Read from |
| --- | --- | --- | --- |
| A host argument, and `--host` | Saved host names | The SSH target | `hosts.toml` |
| An address | `host/name` for each cached session, or `host/id` when two sessions on one host share a name | `claude, working`, and `seen 5m ago` when the list is older than 60 seconds | The session cache |
| `--agent`, and the agent of `agents install` and `agents login` | Agent kinds | The title | The agent table |
| A key of `host set` | `idle=`, `label.`, and later `tier=`, `floor=` and `drain=` | | Fixed, with `NoSpace` |
| A value of `host set` | `never`, the tiers, the levels, `off` | | Fixed |
| `--debug` | The categories | | As today |
| `completion` | `bash`, `zsh`, `fish`, `powershell` | | Cobra |
| A door word (P1.12) | `net`, `push`, `mount`, `secret`, `ssh-key`, `port`, `paste`, `device` | | Fixed |
| `--profile` (P1.12) | The names in `[profiles]` | | `config.toml` |
| A rule ID (P1.12) | The IDs of the rules in the config layers | The target | `config.toml`, `hosts.toml` |
| `--wall`, `--tier`, `--preset`, `--root-step`, `--images` | Their fixed values | | Fixed |

### 14.3 Rules

1. Completion never dials a host, runs a provider script or opens a network connection. A dial can take seconds, and SSH can ask for a password or a host key ([ssh_config, BatchMode](https://man.openbsd.org/ssh_config.5#BatchMode)). kubectl and docker call their API at each completion. gh completes branch names from the local git repository ([the CLI study, section 10](../research/cli-dx.md#10-completion-of-values)).
2. It reads local files only, and never writes the session cache.
3. It prints only completions on stdout, and never a warning. Debug lines go only to the file in `BASH_COMP_DEBUG_FILE`, as Cobra's `CompDebugln` does.
4. A missing or broken file gives no values for that place, with the directive `ShellCompDirectiveNoFileComp`. It never returns `ShellCompDirectiveError`.
5. Every list of values uses `ShellCompDirectiveNoFileComp`, so the shell never offers file names in their place.
6. Old names of section 3 never complete.
7. The root's persistent hooks run for `__complete` too, so each hook skips it, as `startDebug` in `internal/cli/debug.go` does today.

### 14.4 Tests

- Golden tests of `__complete` for each row of section 14.2, with `XDG_CONFIG_HOME` and `XDG_STATE_HOME` set to fixture folders.
- A test with a dialer that fails the test when it is called.
- A test that `__complete` with an old name writes nothing to stderr.
- A benchmark of session completion with 20 hosts and 1,000 cached sessions. It records the time, and does not gate CI.

### 14.5 Docs

The new `docs/how-to/shell-completion.md` gives the line to install the script in each shell. It says that values come from the last list that the launcher or `ls` saw.

## 15. The launcher and the CLI agree (C11)

### 15.1 One word for one action

The launcher's palette and the docs say "peek", never "preview". The palette command `session: preview` becomes `session: peek`. `[keymap]` takes the old name until its end ([section 3](#3-old-names-and-warnings-c5-c6-c7)).

### 15.2 A command for each action

Each palette command names its command, or says why it has none.

| Palette command | Command |
| --- | --- |
| session: new | `new <host>` |
| session: new with the form | `new <host>` with its flags |
| session: attach | `attach <host>/<session>` |
| session: peek | `show <host>/<session> --screen` |
| session: rename | `rename <host>/<session> <name>` |
| session: delete | `rm <host>/<session>`, whose kept alias is `delete` |
| session: resume | `resume <host>/<session>` |
| session: copy attach command | Launcher only: it copies a command to the clipboard. |
| session: next waiting | Launcher only: it moves the selection. |
| host: open | `ls <host>` |
| host: add | `host add <host>` |
| host: set up | `setup --host <host>` |
| host: details | `host show <host>` |
| host: test | `host test <host>` |
| host: reconnect | `host test <host> --reconnect` |
| host: remove | `host rm <host>` |
| view: all sessions | `ls` |
| view: hosts, search, toggle ended, shortcuts, commands | Launcher only: they change the view. |
| `<name>`: settings | Launcher only: it edits `config.toml`, whose path `config path` prints. |
| `<name>`: about | `version` |
| `<name>`: quit | Launcher only. |
| debug: start, stop, save, bundle | `debug start`, `stop`, `save`, `bundle` |

`host test <host> --reconnect` is new. It closes the shared SSH connection to the host, as `host destroy` does today with `ExitMaster`, then tests a new connection. It does what the launcher's reconnect does: it drops the connection and dials again.

The isolation plan adds palette commands (isolation spec 9.4), each with its command:

| Palette command | Command | Task |
| --- | --- | --- |
| session: policy | `policy ls --in <host>/<session>` | P1.8 |
| session: policy log | `policy log --in <host>/<session>` | P1.8 |
| session: shell | `shell <host>/<session>` | P1.13 |
| session: stop | `stop <host>/<session>` | P1.8 |
| policy: lockdown | `policy lockdown` | P2.14 |
| session: fork | `fork <host>/<session>` | P5 |

The session-shell action uses the same shell runner as the CLI, with its exact
selected session ID and actual terminal files. It retains the original accepted
host connection. Later dials, grants and reconnects check that connection's saved
Source, held settings and host tier before using the original route. A removed
and re-added host, changed configuration or ended parent makes that action fail;
it never resolves a new host under the old selection. This restriction grants
no new permission. The ordinary CLI path keeps its existing behaviour.

### 15.3 How the code keeps them in step

1. Each entry of the palette's registry in `internal/tui/palette.go` gets one of two fields: `cli`, the command path such as `host show`, or `launcherOnly`, the reason.
2. A test in `internal/cli` walks the registry. It fails on an entry with neither field or with both, and on a `cli` path that `Root()` does not have.
3. A test in `internal/cli` reads [the UX spec's CLI table](ux-spec.md#6-cli). It fails when a command of `Root()` that is not hidden has no row. C11 rewrites the table with one row for each command.

### 15.4 Tests

- The two tests of section 15.3.
- Golden tests of the palette with the new name "peek".
- A unit test of `host test --reconnect` with a fake SSH master.

## 16. Docs for agents (C12)

### 16.1 What the docs site publishes

[The CLI study, section 11](../research/cli-dx.md#11-docs-for-agents) gives the formats.

1. **`llms.txt`** at the root of each build of the site, such as `/llms.txt` or `/v1.2/llms.txt`, in the format of [llmstxt.org](https://llmstxt.org/):
   - An H1 with the display name.
   - A quote with the site's description.
   - One paragraph: each page has a Markdown copy, and `simstim <command> --help` prints a command's description, usage, examples and flags, as its page under Reference does.
   - One H2 for each tab of the site, in the order of `sidebar.json`. Each page is one item: `[title](copy URL): description`. The description is the one that `describe` in `cmd/docgen/site.go` gives the page. The landing page's copy is the first item.
   - An H2 "Optional" last, whatever the order of the tabs, with the contributing docs, the research notes and the project pages. These pages make up the Project tab, so the Project tab's H2 is "Optional", and each page is listed once.
2. **A Markdown copy of each page.** Its URL is the page's URL without its trailing slash, plus `.md` (question C-11). `/how-to/add-a-host/` has `/how-to/add-a-host.md`, `/how-to/` has `/how-to.md`, and `/` has `/index.md`.
   - The copy is `# <title>`, a blank line, then the page's body as `cmd/docgen` gives it to the site, without front matter.
   - Its links to other pages point to their copies.
   - The landing page draws its own content. Its copy is the repository's README, the file that links to `/` stand for.
3. **A link to the copy in each HTML page:** `<link rel="alternate" type="text/markdown" href="…">`, as llmstxt.org proposes. `cmd/docgen` adds it through Starlight's `head` front matter ([Starlight](https://starlight.astro.build/reference/frontmatter/#head)). It joins a `head` list that the page has already. The landing page is an Astro page, so `site/src/pages/index.astro` adds its own link.
4. **A snippet for a project's `AGENTS.md`**, in the new `docs/how-to/use-with-agents.md`. It has at most 15 lines of plain Markdown, and no front matter. When every task of this spec has shipped, it reads:

   ```markdown
   ## Remote sessions

   This project runs coding agents in simstim sessions on remote hosts.

   - List the sessions: `simstim ls --json`
   - List the hosts: `simstim host ls --json`
   - Describe one: `simstim show <host>/<session> --json`
   - Read its screen: `simstim show <host>/<session> --screen`
   - In scripts, pass `--no-input`.
   - Exit codes: `simstim help exit-codes`
   - Help for a command: `simstim <command> --help`
   - Docs for agents: https://<docs site>/llms.txt
   ```

   The snippet names only commands that exist. C12 ships the lines of `ls`, `host ls`, `--help` and the docs. Each other task adds its own line when it ships its command: C5 the `show --json` line, C11 the `show --screen` line, C4 the `--no-input` line, and C3 the `help exit-codes` line.

   The page says where each agent reads it. Codex and others read `AGENTS.md`. Claude Code reads it when the folder has no `CLAUDE.md`, and a `CLAUDE.md` can import it. Gemini CLI and Aider read it only after a setting, which the page gives.

Not in scope: `llms-full.txt`, which would hold every page in one file, and a docs server for agents.

### 16.2 How the build makes them

1. `cmd/docgen -site site/src -site-url <url>` writes `site/public/llms.txt` and each copy under `site/public/`. `-site` needs `-site-url`. It adds the `head` link to each page's front matter. Before it writes, it removes the copies of its last run, as it clears `site/public/images/` today. It removes only regular files, and refuses a `site/public` that is a symbolic link. It writes the images, the copies and `llms.txt` through `site/public/` alone, so no write follows a link out of it. A link where a file goes is removed first. A linked folder that leads out of `site/public/` stops the build, with a hint.
2. `scripts/docs-site.sh` passes the URL that it already builds from `SITE_URL` and `SITE_BASE`. A dev build uses `https://example.invalid/`, as today.
3. Astro copies `site/public/` into the build as it is ([Astro](https://docs.astro.build/en/basics/project-structure/#public)).
4. `.gitignore` gets `/site/public/llms.txt` and `/site/public/**/*.md`, beside `/site/public/images/`, which `cmd/docgen` already writes.

### 16.3 Checks

`just docs-check` builds the site, then fails in these cases:

- `llms.txt` misses a page of `site/src/generated/pages.json`.
- `llms.txt` lists a page twice, or lists a page without a description.
- A copy that `llms.txt` names is missing from `build/site/public/`.
- A copy's title differs from its source's title, or its text differs from its source's text. Code counts as it is. Only the destinations of the links that `cmd/docgen` rewrites may differ.
- A link that `cmd/docgen` rewrites leads elsewhere than `cmd/docgen` sends the source's link: to the copy of the page it names, to the image, or to the file on GitHub.
- A relative link in a copy leads to no Markdown copy or image in the build. lychee checks the copies' links too.

A unit test in `cmd/docgen` builds a small docs tree. It checks that each copy equals its source after the link rewrite, and compares `llms.txt` with a golden file. The golden file holds no product name: the test fills it in from `internal/brand`. `scripts/test-check-llms-txt.py`, in `just test`, breaks a small built site in each of the ways above, and expects the check to fail.

## 17. Open questions

C-1 to C-6 come from [the plan](cli-plan.md#open-questions-and-their-defaults). C-7 to C-13 are new in this spec. Each task uses the default, and names the question in its pull request.

| # | Question | Default | Sections |
| --- | --- | --- | --- |
| C-1 | After the warning period, do old names go away, or stay hidden? | They go away, and stay as suggestions. | 3 |
| C-2 | Should `-d` change from `--dir` to `--detach`? | Yes, at the end of section 3. `-C` is the new short form of `--dir`. | 10.4 |
| C-3 | How does the CLI find the host of a session named without one? | From the session cache, refreshed when older than 60 seconds. Two matches fail with both full names. | 9.2 |
| C-4 | What does a command do when its required argument is missing? | `attach`, `show` and `rm` open a picker when input is on. Every other command prints its short help and an example. | 4.4, 6.3 |
| C-5 | Are the names of `--json` fields part of the contract? | Yes. They are `snake_case`, and each top-level object carries `"v": 1`. | 7.2 |
| C-6 | Which exit codes? | G12. `exec` returns the command's own code. | 5 |
| C-7 | Where does an error go with `--json`? | stderr, as one JSON line. stdout stays empty, so a script never parses half a result. | 7.4 |
| C-8 | Which code does `exec` return when it fails before the command runs? | 125, and 124, 126 and 127 as GNU `timeout` and `docker run` use them. | 5.2 |
| C-9 | How do the Go keys of `host ls --json` become `snake_case`? | Both forms print until the end of section 3. | 3.2, 7.2 |
| C-10 | Does `rm` with input off need `-y`? | Yes, after the end of section 3. Until then it deletes and warns. | 9.5 |
| C-11 | Where is a page's Markdown copy? | At its URL without the trailing slash, plus `.md`, with a `rel="alternate"` link in the page. | 16.1 |
| C-12 | `host alerts` sits at three levels. Does it move? | No. It is the one exception to G1 until a record moves it. | 2.2 |
| C-13 | Does `--json` turn input off? | Yes. A script that asks for JSON cannot answer a prompt. | 6.1, 7.3 |

## 18. Traceability

| Task | Spec sections | Research |
| --- | --- | --- |
| C1 | [2](#2-the-grammar-c1) | [CLI study 8](../research/cli-dx.md#8-the-grammar-we-propose) |
| C2 | [4](#4-errors-and-suggestions-c2), [5](#5-exit-codes-c2) | CLI study 2.5, 5.4, 5.5, G11, G12 |
| C3 | [8](#8-help-c3) | CLI study 2.5, 5.5, G13 |
| C4 | [3](#3-old-names-and-warnings-c5-c6-c7), [6](#6-prompts-pickers-and-input-c4), [7](#7-machine-output-c4) | CLI study 5.3, 5.4, G8, G10 |
| C5 | [3](#3-old-names-and-warnings-c5-c6-c7), [9](#9-session-verbs-c5) | CLI study 2.2, 2.3, 6, 7, 9.1, G2, G3, G6, G14 |
| C6 | [3](#3-old-names-and-warnings-c5-c6-c7), [10](#10-flags-c6) | CLI study 2.4, 5.2, G7 |
| C7 | [3](#3-old-names-and-warnings-c5-c6-c7), [11](#11-host-commands-c7) | CLI study 3, 5.3, 5.6, 9.3, G9 |
| C8 | [12](#12-agents-install-and-log-in-c8) | [CLI study 12](../research/cli-dx.md#12-installing-agents-and-logging-in) |
| C9 | [13](#13-config-check-c9) | CLI study 9.3, 5.6 |
| C10 | [14](#14-completion-c10) | [CLI study 10](../research/cli-dx.md#10-completion-of-values) |
| C11 | [3](#3-old-names-and-warnings-c5-c6-c7), [15](#15-the-launcher-and-the-cli-agree-c11) | CLI study 2.2 |
| C12 | [16](#16-docs-for-agents-c12) | [CLI study 11](../research/cli-dx.md#11-docs-for-agents), [parity 3.5](../research/parity.md#35-integration-teams-and-data) |
