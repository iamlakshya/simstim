# The command line: implementation plan

**Status:** proposed, 10 November 2026. **Spec:** [cli-spec.md](cli-spec.md). **Research:** [the CLI study](../research/cli-dx.md). **Decision:** [0033](decisions/0033-one-command-grammar.md).

This plan makes the command line easy to guess, hard to misuse and consistent. [The CLI study](../research/cli-dx.md) found the problems and proposes the grammar G1 to G14 and a command map. Record [0033](decisions/0033-one-command-grammar.md) proposes the grammar for the owner's yes. [The CLI spec](cli-spec.md) states each behaviour, input, error and test. This plan says which files each task changes, what proves it done, and in what order to build the tasks. Where the two differ, the spec wins, and the pull request corrects this plan.

[The isolation plan](isolation-plan.md) adds the commands of isolated sessions. Those commands follow 0033 from the start, so this plan does not rename them. This plan changes today's commands, and builds the shared parts that both plans need: errors, exit codes, prompts, help and completion.

## How to work

Follow the rules of [the isolation plan](isolation-plan.md#how-to-work), with these changes:

1. Name each branch `cli/<id>-<short-slug>`, such as `cli/c2-errors`.
2. Build C1 first. Every other task depends on the owner's yes to [0033](decisions/0033-one-command-grammar.md).
3. Keep each old name or meaning working as an alias that prints one warning line to stderr. The warning names the new form and the release that removes the old one. Remove it after two releases or 12 months, whichever is later ([spec 3](cli-spec.md#3-old-names-and-warnings-c5-c6-c7)).
4. Never change the stdout of a command that has `--json` in a way that breaks a script. Add fields, and never rename one.
5. Run `just docs` after each change to a command, a flag or a key, and update [the UX spec](ux-spec.md) in the same commit.

## Open questions and their defaults

| # | Question | Default | Tasks |
| --- | --- | --- | --- |
| C-1 | After the warning period, do old names such as `kill` go away, or stay hidden? | They go away. A hidden alias that never warns teaches nothing, and keeps two names for one thing. | C5, C6, C7 |
| C-2 | Should `-d` change from `--dir` to `--detach`? | Yes, after two releases in which `-d` keeps its old meaning and warns. `-C` is the new short form of `--dir`. | C6 |
| C-3 | How does the CLI find the host of a session named without one? | From the session cache, the launcher's memory file. It refreshes a host's list when it is older than 60 seconds. Two matches fail with both full names. | C5 |
| C-4 | What does a command do when its required argument is missing at a terminal? | `attach`, `show` and `rm` open a picker when input is on. Every other command prints its short help and an example. | C4, C5 |
| C-5 | Are the names of `--json` fields part of the contract? | Yes. They are `snake_case`, and each top-level object, or each element of a top-level array, carries `"v": 1`. | C4 |
| C-6 | Which exit codes? | G12: 0 done, 1 failed, 2 wrong usage, 3 not found, 4 needs an answer, 5 refused by policy or tier, 130 cancelled. `exec` returns the command's own code. | C2 |
| C-7 | Where does an error go with `--json`? | stderr, as one JSON line. stdout stays empty. | C4 |
| C-8 | Which code does `exec` return when it fails before the command runs? | 125, and 124, 126 and 127 as GNU `timeout` and `docker run` use them. | C2, P1.13 |
| C-9 | How do the Go keys of `host ls --json` become `snake_case`? | Both forms print until the end of the warning period. | C4 |
| C-10 | Does `rm` without a terminal need `-y`? | Yes, after the warning period. Until then it deletes and warns. | C5 |
| C-11 | Where is a page's Markdown copy? | At its URL without the trailing slash, plus `.md`. | C12 |
| C-12 | `host alerts` sits at three levels. Does it move? | No. It is the one exception to G1 until a record moves it. | C3 |
| C-13 | Does `--json` turn input off? | Yes. | C4 |

## Order

| # | ID | Title | Depends on | Questions | Effort |
| --- | --- | --- | --- | --- | --- |
| 1 | C1 | The grammar record | none | none | S |
| 2 | C2 | Errors, suggestions and exit codes | C1, 0033 | C-6, C-8 | M |
| 3 | C4 | Prompts, pickers and machine output | C2 | C-4, C-5, C-7, C-9, C-13 | M |
| 4 | C3 | Help that teaches | C2 | C-12 | M |
| 5 | C5 | Session verbs | C4 | C-1, C-3, C-4, C-10 | M |
| 6 | C6 | One meaning for each flag | C2 | C-1, C-2 | S |
| 7 | C7 | Host commands | C4 | C-1 | M |
| 8 | C8 | Agents: install and log in | C4 | none | S |
| 9 | C9 | `config check` | C2 | none | S |
| 10 | C10 | Completion | C5, C7 | none | S |
| 11 | C11 | The launcher and the CLI agree | C5, C7 | none | S |
| 12 | C12 | Docs for agents | none | C-11 | S |

Some isolation tasks depend on tasks of this plan, so their commands use the shared errors, prompts and machine output. H3 depends on C2 and C4, H4 on C7, P1.8 on C2, C4 and C5, and P1.12 and P2.8 on C9. P1.8 also depends on 0033.

[The spec's open questions](cli-spec.md#17-open-questions) hold C-1 to C-13 with the sections that use them.

## C1 The grammar record

- **Goal:** the owner accepts or changes [0033](decisions/0033-one-command-grammar.md). This branch already holds it, as a proposed record.
- **Spec:** [cli-spec.md, section 2](cli-spec.md#2-the-grammar-c1).
- **Files:** `docs/contributing/decisions/0033-one-command-grammar.md`, `docs/contributing/decisions/README.md`.
- **Acceptance:** the record's status is accepted, or a new record replaces it.
- **Tests:** `just docs-check`.
- **Docs:** the record.
- **CHANGELOG:** none. The record changes no behaviour.
- **Depends on:** nothing.
- **Questions:** none.
- **Effort:** S.

## C2 Errors, suggestions and exit codes

- **Goal:** G11 and G12 of the study.
  - The framework's own errors get the user-error form, with a "Try:" line: a missing or extra argument, an unknown flag and an unknown command.
  - An unknown command gets "did you mean" for names within two edits. Synonyms map to real commands: `remove` to `rm`, `list` to `ls`, and `ssh` to `shell` once P1.13 adds `shell`. `delete` stays a kept alias of `rm`. A guess never runs.
  - Every error has one "Try:" line, with a full command or one imperative sentence. An error that is not a user error gets the hint to run `debug bundle`.
  - Exit codes come from the error's code, never from its text (question C-6).
- **Spec:** [cli-spec.md, section 4](cli-spec.md#4-errors-and-suggestions-c2) and [section 5](cli-spec.md#5-exit-codes-c2).
- **Files:** `main.go`, new `internal/cli/errors.go`, `internal/cli/main.go` (the root's argument check, which today turns suggestions off), `internal/client/config.go` (`UserError` gains `Kind`, and every error sets its existing `Code`), every command file in `internal/cli` that returns a plain error.
- **Acceptance:**
  - `atach x` prints `Did you mean "attach"?` and a "Try:" line, and exits with 2.
  - `attach` with no argument prints the short help and an example, and exits with 2. C4 adds the picker at a terminal.
  - "No hosts yet" exits with 3 only when a host is not found, and with 1 otherwise.
  - A test parses every `client.UserError` literal under `internal/`, and fails on one without a `Code` or a hint.
- **Tests:** unit tests for the mapping, and golden tests of the error output.
- **Docs:** new `docs/reference/exit-codes.md`, written by `just docs` from the code. `docs/contributing/style.md` gets the one style for hints.
- **CHANGELOG:** Changed. "Every error now ends with a "Try:" line, a mistyped command gets a suggestion, and exit codes follow a documented table."
- **Depends on:** C1, and the owner's yes to 0033.
- **Questions:** C-6, C-8.
- **Effort:** M.

## C3 Help that teaches

- **Goal:** G13 of the study.
  - The root help groups the commands: Sessions, Hosts, Policy, Agents, Setup and help.
  - Every command has at least one example. A unit test fails when a runnable command has none.
  - The reference shows each command's aliases.
  - Help topics: `help concepts` explains each word of the build that a user meets, in one line each. `help exit-codes` prints the table. The isolation plan adds its words to `help concepts`, and P1.12 adds `help policy`.
  - No internal words in help: "holder", "flight recorder", "linger" and "restore unit" become words that a user knows, or go.
- **Spec:** [cli-spec.md, section 8](cli-spec.md#8-help-c3).
- **Files:** `internal/cli/main.go`, every command file in `internal/cli`, `cmd/docgen` (aliases, old names and topics).
- **Acceptance:** every reference page has an example and its aliases. `help concepts` exists. A test fails on the internal words above in any help text. `TestDepth` passes. C5 adds `TestTopLevel`, once `kill` and `flip` are hidden.
- **Tests:** a unit test that walks the command tree, and golden tests of the root help.
- **Docs:** the reference through `just docs`, and [the UX spec](ux-spec.md)'s CLI table, which now lists every command.
- **CHANGELOG:** Changed. "The help groups commands, every command has an example, and `help concepts` explains the words that the tool uses."
- **Depends on:** C2.
- **Questions:** C-12.
- **Effort:** M.

## C4 Prompts, pickers and machine output

- **Goal:** G8 and G10 of the study.
  - Prompts appear only when input is on: stdin, stdout and stderr are terminals. `--no-input`, `--json`, or the environment variable `brand.Env("NO_INPUT")`, turns them off. A question with no answer then fails with exit code 4 and the flag that answers it, unless its default keeps things as they are.
  - Every prompt has a flag, and shows its default answer in capitals.
  - A shared picker: a short inline list of hosts or sessions, with search, for a command whose required argument is missing at a terminal (question C-4).
  - `--json` on every command that prints data or makes something: `new --detach`, `resume --detach`, `ports`, `doctor`, `host test`, `host restore`, `host status` and `version`. Field names follow question C-5. With `--json`, an error is one JSON line on stderr (question C-7).
  - Data goes to stdout, and messages, confirmations, progress and warnings to stderr. Spinners only at a terminal. Colour follows `NO_COLOR`, `TERM=dumb` and `--no-color`.
- **Spec:** [cli-spec.md, section 6](cli-spec.md#6-prompts-pickers-and-input-c4), [section 7](cli-spec.md#7-machine-output-c4) and [section 3](cli-spec.md#3-old-names-and-warnings-c5-c6-c7).
- **Files:** new `internal/cli/prompt.go`, new `internal/tui/picker.go`, `internal/cli/session.go`, `internal/cli/resume.go`, `internal/cli/host.go`, `internal/cli/create.go`, `internal/cli/doctor.go`, `internal/cli/main.go` (`version`), new `internal/cli/deprecated.go` (the table of spec 3, with its first row).
- **Acceptance:**
  - `new box --detach --json` prints one JSON object with `id`, `name`, `host` and `"v": 1`, and nothing else on stdout.
  - With `--no-input`, `host add box` fails with exit code 4 and the flag `--yes`.
  - Piped output has no colour and no spinner.
  - `attach` with no argument at a terminal opens the picker. With `--no-input`, it fails with exit code 2.
- **Tests:** unit tests with a fake terminal, golden tests of each `--json` shape, and an end-to-end run of `new --detach --json` in a script.
- **Docs:** `docs/how-to/scripting.md` (new): `--json`, `--no-input`, exit codes and examples.
- **CHANGELOG:** Added. "`--no-input`, `--json` on every command that prints data, and a picker when a command needs a session or a host that you did not name."
- **Depends on:** C2.
- **Questions:** C-4, C-5, C-7, C-9, C-13.
- **Effort:** M.

## C5 Session verbs

- **Goal:** G2, G3 and G6 of the study, and section 9.1 of its command map.
  - `rm` is the command that deletes a session. `delete` stays an alias. `kill` becomes a warned alias that never asks. `rm` asks at a terminal, and takes `-y`. Without a terminal it deletes and warns until the end of the warning period, then needs `-y` (question C-10).
  - `attach` with no argument opens the picker. It wakes a stopped session, as today's provider hosts do. On an ended agent it asks to start the agent again, or takes `--restart`. `flip` becomes a warned alias of `attach`.
  - `resume` on a running session attaches, with a note, instead of an error.
  - `show <host>/<session>`: everything about one session, with `--json`.
  - `ls [<host>]`: the host as an argument. `--host` stays.
  - The host part of an address may be left out when the session's name is unique (question C-3). `-` means the last session, and nothing else.
- **Spec:** [cli-spec.md, section 9](cli-spec.md#9-session-verbs-c5) and [section 3](cli-spec.md#3-old-names-and-warnings-c5-c6-c7).
- **Files:** `internal/cli/session.go`, `internal/cli/resume.go`, `internal/cli/ended.go`, `internal/cli/deprecated.go`, new `internal/cli/show.go`, new `internal/cli/address.go`, and the session cache, which moves from `internal/tui/memory.go` to the new `internal/client/seen.go`.
- **Acceptance:**
  - `kill box/a` works, warns once on stderr, and names `rm`.
  - `attach box/a` on an ended agent asks "Start claude again? [Y/n]" at a terminal. With `--no-input` it fails with exit code 4 and `--restart`.
  - `attach a` finds `box/a` when no other host has a session named `a`. Two matches fail with both full names.
  - `TestTopLevel` passes, with `kill` and `flip` hidden ([CLI spec, 2.3](cli-spec.md#23-tests)).
- **Tests:** unit tests for the address rules, golden tests of the prompts, and end-to-end runs of `rm`, `attach` and `show`.
- **Docs:** the README's first-session path, new `docs/how-to/sessions.md`, [the UX spec](ux-spec.md), the reference through `just docs`.
- **CHANGELOG:** Changed. "`rm` deletes a session, and asks first. `kill` still works for now, and warns. `attach` wakes a stopped session and offers to restart an ended agent. `show` describes one session. You can leave out the host when a session's name is unique."
- **Depends on:** C4.
- **Questions:** C-1, C-3, C-4, C-10.
- **Effort:** M.

## C6 One meaning for each flag

- **Goal:** G7 of the study.
  - `-C` is the short form of `--dir`. `-d` keeps its old meaning for two releases, with a warning, then means `--detach` (question C-2).
  - `--token-command` everywhere. `--token-cmd` becomes a warned alias.
  - Every list of names accepts a repeated flag and commas. A list of values that may hold a comma, such as `--env`, takes only a repeated flag.
  - Every size takes units. `host create --memory 8` and `--disk 40` still mean GiB, and warn.
  - `--force` only where work is lost. `agents install --force` becomes `--reinstall`, with a warned alias.
  - `-y/--yes` only skips a question.
  - `new --setup` and `--no-setup` become `--setup-script` and `--no-setup-script`, so that "setup" names only the guided setup.
  - `-n` is `--name` everywhere. `host alerts log -n` becomes `--lines` alone, with a warned alias.
- **Spec:** [cli-spec.md, section 10](cli-spec.md#10-flags-c6) and [section 3](cli-spec.md#3-old-names-and-warnings-c5-c6-c7).
- **Files:** `internal/cli/session.go`, `internal/cli/alerts.go`, `internal/cli/create.go`, `internal/cli/agents.go`, `internal/cli/guided.go`, new `internal/cli/flags.go` (shared list and size parsers), `internal/cli/deprecated.go` (the warned aliases).
- **Acceptance:** each old form works and warns once. A test fails when two commands give one short flag two meanings.
- **Tests:** unit tests for the parsers and the aliases.
- **Docs:** the reference through `just docs`, and every how-to guide that shows an old form.
- **CHANGELOG:** Changed. "Flags mean one thing everywhere. Old forms still work for two releases, and warn."
- **Depends on:** C2.
- **Questions:** C-1, C-2.
- **Effort:** S.

## C7 Host commands

- **Goal:** section 9.3 of the study's command map.
  - `host set <host> key=value …` for every host setting: `idle` today, and `tier`, `floor` and `drain` when the isolation plan adds them. `host idle` becomes a warned alias.
  - `host show <host>`: what the launcher's host details show, with `--json`.
  - `host rm <host>` forgets the host. At a terminal it first asks about each part that it could also remove, and says what each removes. `--destroy` deletes a machine that `host create` made. `--purge` comes with the isolation plan's task H15. Both need the typed name or `--confirm=<name>`. `host destroy` becomes a warned alias of `host rm --destroy`, and its `-y` becomes `--confirm`.
  - `host upgrade <host>` updates the helper. Today the user runs `host add` again.
  - With `--yes`, `host add` needs `--tier` once the isolation plan adds tiers (H4). Without it, the command fails with exit code 4 and the flag to pass.
- **Spec:** [cli-spec.md, section 11](cli-spec.md#11-host-commands-c7) and [section 3](cli-spec.md#3-old-names-and-warnings-c5-c6-c7).
- **Files:** `internal/cli/host.go`, `internal/cli/create.go`, new `internal/cli/host_set.go`, `internal/cli/deprecated.go`, `internal/client/config.go`.
- **Acceptance:** `host set box idle=2h` does what `host idle box 2h` did. `host rm` on a created machine says that it holds 2 sessions. It asks "Type box to delete the machine too, or press Enter to keep it:". `host show box --json` matches the launcher's details.
- **Tests:** unit tests, golden tests of the prompts, and an end-to-end run against a test host.
- **Docs:** `docs/how-to/add-a-host.md`, `docs/how-to/create-a-host.md`, `docs/how-to/uninstall.md`, the reference through `just docs`.
- **CHANGELOG:** Changed. "`host set` changes any host setting, `host show` describes a host, and `host rm` asks what else to remove. `host idle` and `host destroy` still work for now, and warn."
- **Depends on:** C4.
- **Questions:** C-1.
- **Effort:** M.

## C8 Agents: install and log in

- **Goal:** `agents install <agent> [<host>]`, with `--host` kept, and a new `agents login <agent> [<host>]`. `login` starts the agent's own device login in a session and attaches, as `new <host> -- <agent> login` does today. The login command for each agent is already data in `internal/agents`, as `Setup.Login`.
- **Spec:** [cli-spec.md, section 12](cli-spec.md#12-agents-install-and-log-in-c8).
- **Files:** `internal/cli/agents.go`, `internal/onboard/flow.go` (`LoginCommand` names `agents login`). The agent table gains no field.
- **Acceptance:** `agents login claude box` starts the documented login of Claude Code on `box`.
- **Tests:** unit tests and an end-to-end run that stops at the agent's first prompt.
- **Docs:** `docs/how-to/install-agents.md`.
- **CHANGELOG:** Added. "`agents login` starts an agent's own login on a host."
- **Depends on:** C4.
- **Questions:** none.
- **Effort:** S.

## C9 `config check`

- **Goal:** `config check` reads every config file and reports each error with its file and line, and each unknown key with the nearest known key. Once the isolation plan adds layers and profiles, it also prints which layers apply to the current folder.
- **Spec:** [cli-spec.md, section 13](cli-spec.md#13-config-check-c9).
- **Files:** new `internal/cli/config_check.go`, `internal/client/config.go`.
- **Acceptance:** a typo in a key name gets `~/.config/simstim/config.toml:4: unknown key "motoin" in [ui]. Did you mean "motion"?`, and `config check` exits with 1.
- **Tests:** unit tests with broken files.
- **Docs:** `docs/reference/config.md` through `just docs`.
- **CHANGELOG:** Added. "`config check` finds mistakes in your config files."
- **Depends on:** C2.
- **Questions:** none.
- **Effort:** S.

## C10 Completion

- **Goal:** shell completion for values, not only for names: hosts, sessions, agents, door words, and later profiles and rule IDs, from the launcher's cached state. The reference documents `completion`.
- **Spec:** [cli-spec.md, section 14](cli-spec.md#14-completion-c10).
- **Files:** `internal/cli/completion.go` (new), `internal/cli/main.go` (`InitDefaultCompletionCmd`, so that the reference shows `completion`), every command that takes a host or a session.
- **Acceptance:** `attach <tab>` lists sessions as `host/session`, without a network call.
- **Tests:** unit tests of each completion function.
- **Docs:** `docs/how-to/shell-completion.md` (new).
- **CHANGELOG:** Added. "Shell completion fills in hosts, sessions and agents."
- **Depends on:** C5, C7.
- **Questions:** none.
- **Effort:** S.

## C11 The launcher and the CLI agree

- **Goal:** one word for each action in both, and a command for each action of the launcher.
  - The palette says "peek", never "preview". The keymap name `session: preview` becomes `session: peek`, with a warned alias, and `show --screen` gives peek a command.
  - "host: details" runs `host show`, and "host: reconnect" gets `host test --reconnect`.
  - The palette's "delete" matches `rm`.
  - [The UX spec](ux-spec.md)'s CLI table lists every command.
- **Spec:** [cli-spec.md, section 15](cli-spec.md#15-the-launcher-and-the-cli-agree-c11) and [section 3](cli-spec.md#3-old-names-and-warnings-c5-c6-c7).
- **Files:** `internal/tui/palette.go`, `internal/cli/deprecated.go` (the old keymap name), `internal/cli/show.go` (`--screen`), `internal/cli/host.go` (`--reconnect`), `docs/contributing/ux-spec.md`.
- **Acceptance:** a test compares the palette's actions with the command tree. It fails on an action with neither a command nor a launcher-only reason, or with both, and on a command that the tree lacks.
- **Tests:** a unit test and golden tests.
- **Docs:** [the UX spec](ux-spec.md).
- **CHANGELOG:** Changed. "The launcher and the command line use the same words."
- **Depends on:** C5, C7.
- **Questions:** none.
- **Effort:** S.

## C12 Docs for agents

- **Goal:** agents read this project's docs as a hosted VM service's docs are read ([parity](../research/parity.md#35-integration-teams-and-data)). The docs site publishes `llms.txt`, a Markdown copy of each page, and a short `AGENTS.md` snippet for projects that use this tool.
- **Spec:** [cli-spec.md, section 16](cli-spec.md#16-docs-for-agents-c12).
- **Files:** `cmd/docgen` (the `-site` build), `scripts/docs-site.sh` (the site's URL for `cmd/docgen`), `.gitignore`, new `docs/how-to/use-with-agents.md`.
- **Acceptance:** `llms.txt` lists every page with its one-line summary, and each page's Markdown copy matches its source. `just docs-check` checks both.
- **Tests:** a unit test of the generator.
- **Docs:** the new how-to page.
- **CHANGELOG:** Added. "The docs site publishes `llms.txt` and a Markdown copy of each page, for coding agents."
- **Depends on:** nothing.
- **Questions:** C-11.
- **Effort:** S.

## Risks to the delivery

| Risk | Mitigation |
| --- | --- |
| Scripts break when a name changes. | Warned aliases for two releases or 12 months, and `--json` fields that only grow. |
| The isolation plan ships a command before this plan's shared parts exist. | P1.8 depends on C2 and C4. |
| A picker appears where a script expected an error. | Pickers only when stdin, stdout and stderr are terminals, and never with `--no-input` or `--json`. |
| Users keep typing old names after the aliases go. | The suggestion map in C2 keeps old names as suggestions, without running them. |
