# The command line

Researched on 10 November 2026.

This note asks how to make the command line easy to guess, hard to misuse and consistent. It audits today's commands and the commands that [the isolation spec](../contributing/isolation-spec.md) plans. It then reads published CLI guidelines, the commands of other session and sandbox tools, and user research. [The CLI plan](../contributing/cli-plan.md) turns the findings into tasks.

The rules for the note:

- Facts about this repository come from its code and docs on 10 November 2026. Links to code point to files, not lines, because lines drift.
- Facts about other tools come from their public docs, repositories and papers. Each fact links its source.
- **UNVERIFIED** marks a claim that no primary source confirmed.
- No account was made and nothing was installed. One researcher built the CLI from this repository to read its real error messages.

## Summary

1. **Today's command line has 48 reference pages, and the same few problems repeat.** Removal has three names (`kill`, `rm`, `delete`), and `host rm` only forgets a host. `attach` and `resume` refuse each other's states, so the user must know a session's state first. One flag has two names (`--token-cmd`, `--token-command`), `-d` means `--dir` where other tools mean detach, and lists take four forms ([section 2](#2-todays-command-line)).
2. **Errors and help are uneven.** The framework's own errors carry no "Try:" line and no "did you mean". Exit codes come from the text of a message. Only 14 of the 48 reference pages have an example ([section 2.5](#25-errors-help-and-exit-codes)).
3. **The isolation spec would make it worse before it makes it better.** It adds 18 commands, takes `new` from 12 flags to 28, and asks a user to learn about 17 new words. Some planned names clash: `grant` beside `grants`, `--root` with two meanings, `save` beside `debug save`, and `pull --yes` that skips a security review. `host add --yes` would quietly give a host the `shared` tier ([section 3](#3-what-the-isolation-spec-adds)).
4. **The guidelines agree on most rules.** Use two levels at most. Give one verb one meaning for every noun. Give every prompt a flag, and prompt only when a person is at a terminal. Put data on stdout and messages on stderr. Make every error say how to fix it. Keep an old name as a warned alias for two releases or 12 months ([section 5](#5-published-guidelines)).
5. **Sandbox tools share one rule model.** Docker Sandboxes puts every network rule under `policy`: `allow`, `deny`, `ls`, `rm`, `log` and `check`. No tool in the sample uses `grant` and `revoke` for network rules ([section 6](#6-other-session-and-sandbox-tools)).
6. **People guess different words.** In a classic study, two people chose the same word for a command less than one time in five. Aliases improved success several times over ([section 7](#7-user-research)).
7. **The proposal.** A grammar of 14 rules ([section 8](#8-the-grammar-we-propose)) and a command map from today's and the planned names to the new ones ([section 9](#9-the-command-map)). Sessions keep short verbs at the top level. Everything else is a noun with verbs under it: `host`, `policy`, `agents`, `image`, `debug` and `config`. Rules for every door live under `policy`, at any layer from all sessions down to one session.

## 1. Method

- One researcher read `main.go`, every file in `internal/cli`, the generated reference in `docs/reference`, [the UX spec](../contributing/ux-spec.md), the README and the how-to guides. They listed every command and flag, and every place where two names meet one thing. They built the CLI and ran wrong commands to read the real errors.
- The same researcher read sections 9 and 14 of the isolation spec for the planned commands.
- A second researcher read the published guidelines, the commands of ten session and sandbox tools, and four studies.
- This note lists each finding once, with its evidence. Section 8 ties each rule to its sources.

## 2. Today's command line

### 2.1 What exists

| Group | Commands |
| --- | --- |
| Sessions | `new`, `attach` (alias `a`), `flip`, `resume`, `ls`, `send`, `rename`, `kill` (aliases `rm`, `delete`), `ports`, `forward`, `pull` |
| Hosts | `host add`, `host create`, `host ls`, `host test`, `host doctor`, `host restore`, `host rm`, `host destroy`, `host idle`, `host start`, `host stop`, `host status`, `host alerts` with `set`, `test`, `log` and `off` |
| Agents | `agents ls`, `agents install` |
| Help and repair | `setup`, `doctor` with `terminal` and `keys`, `debug` with `start`, `stop`, `save`, `profile` and `bundle`, `config keymap`, `config path`, `demo`, `version`, `completion` |

Running the CLI with no command opens the launcher ([main.go](../../main.go)).

### 2.2 Names

- **Removal has three names.** The command is `kill`, with the aliases `rm` and `delete`, and it prints "Deleted". The launcher's palette says "delete", and the docs say "remove" ([session.go](../../internal/cli/session.go), [palette.go](../../internal/tui/palette.go)).
- **`rm` means two things.** Through the alias, `rm` deletes a session and its worktree. `host rm` only forgets a host, and the host's sessions keep running ([host.go](../../internal/cli/host.go)).
- **"Setup" means three things:** the guided `setup` command, the "prepare it" step inside `host add`, and the repository's setup script that `new --setup` runs ([guided.go](../../internal/cli/guided.go), [session.go](../../internal/cli/session.go)).
- **Four commands overlap.** `attach` refuses a session whose agent ended and suggests `resume`. `resume` refuses a running session and suggests `attach`. `flip <host>/<session>` is the same as `attach`, and `host restore` restarts sessions after a reboot ([ended.go](../../internal/cli/ended.go), [resume.go](../../internal/cli/resume.go)).
- **One flag has two names.** `host alerts set --token-cmd` and `host create --token-command` do the same job ([alerts.go](../../internal/cli/alerts.go), [create.go](../../internal/cli/create.go)).
- **`--name` names two objects.** It is the session's name on `new` and the host's name on `setup`.
- **Short flags clash.** `-n` is `--name` on `new` and `--lines` on `host alerts log`. `-d` is `--dir`, and `--detach` has no short form, where docker and tmux users expect `-d` to detach.
- **The launcher uses two words for one action:** "preview" and "peek" ([palette.go](../../internal/tui/palette.go)).

### 2.3 Addressing

- The host is an argument on `new <host>`, and a flag on `ls --host`, `agents install --host` and `setup --host`.
- The help calls one argument `<name>` in some places and `<host>` in others.
- Hints name a session by its ID in some places and by its name in others. A hint with a name fails when two sessions share it.
- `new box htop` drops `htop` without an error. The command needs `--` before it.

### 2.4 Flags

- **Lists take four forms:** a repeated flag (`--secret`), a comma list (`--agents`), a slice (`--events`) and pairs in one string (`--label k=v,k=v`).
- **Sizes take two forms:** `--memory-max 4G` and `host create --memory 8` in GiB.
- **`--force` means three things:** reinstall an agent, fetch over a branch that moved, and destroy a machine while a session works. **`--yes` also means three things:** install the helper, apply every fix, and skip a question.
- **Machine output is missing.** `new --detach` prints a sentence with the session's ID, so a script must parse prose. `ports`, `doctor`, `host test`, `host restore` and `version` have no `--json`.
- **Some destructive commands do not ask.** `kill` deletes at once, though the launcher's `d` asks first. `host alerts off` deletes the alerts file. `host rm` on a machine that `host create` made leaves the VM running, and says so only afterwards.

### 2.5 Errors, help and exit codes

- The framework's own errors have no "Try:" line. `attach` with no argument prints "accepts 1 arg(s), received 0". A typo such as `atach` gets no suggestion, because the root command accepts no arguments ([main.go](../../main.go)).
- Some errors have no hint at all, and any error that is not a user error goes out without one.
- Hints use four styles: a bare command, a sentence, a fragment and a question.
- Exit codes come from matching text in the message, so "no hosts yet" exits with the "not found" code ([main.go](../../main.go)).
- 14 of the 48 reference pages have an example. `attach`, `kill`, `ls`, `host add`, `host rm` and `doctor` have none. The reference does not show aliases, and the root help is a flat list of 21 commands.
- The help uses internal words: "holder", "flight recorder", "linger" and "restore unit".
- No command logs in to an agent. To update the helper, the user runs `host add` again.

## 3. What the isolation spec adds

- **Size.** 18 new commands. `new` grows from 12 flags to 28, and `host add` from 5 to 10.
- **Words.** A user meets about 17 new words: tier, level, floor, downgrade, preset, grant, scope, armed, bound, exposed, root step, prepare, runtime, image, drain, purge and pool.
- **The tier trap.** `host add --yes` without `--tier` gives the `shared` tier. The docs show `host add my-server --yes` as the scripted form, so that command would change its meaning without a word.
- **A first command that breaks.** `new` defaults to the home folder, and a walled session refuses a folder outside git. The README's first command, `new my-vps --name first`, would fail.
- **One flag name, two meanings.** `--root reset|keep` on `new` and `--root run|print|skip` on `host add`. `new auto --wake` and `host add --wake`. `pull --yes` skips a security review, where `--yes` only skips a question everywhere else.
- **Names that clash.** `grant` and `grants` are one letter apart. `host grants` lists what was sent to a host, not the session's grants. `save` sits beside `debug save`. `stop` keeps the work, and `host stop` stops a machine. `kill` now refuses unless `--force`, the opposite of what the word suggests.
- **Host settings in six forms.** `host tier <host> own` takes a value, `host wall --floor` a flag, `host default --clear` a flag to clear, `host idle <host> never` a value to clear, and `host drain` and `host undrain` are two commands.
- **Doors.** Section 7 says that each door has one kind of grant, but the network door opens four ways. The grant words (`net`, `mount`) do not match the door names (Network, Files).

### 3.1 Ten common tasks

The counts are commands, then the words that the user must know.

| Task | Today | Planned | Proposed |
| --- | --- | --- | --- |
| First setup | 1 / 4 | 1 / 8 | 1 / 6. The guided setup asks plain questions and writes the answers. |
| Add an SSH host in a script | 1 / 3 | 1 / 7 | 1 / 4. `host add my-vps --tier own --yes` |
| Start an agent in a repository | 1 / 5 | 1 to 2 / 10 | 1 / 5. The policy layers decide, and a blocked site names one command. |
| Find a session and go back to it | 2 / 3 | 2 / 5 | 1 / 2. `attach` with no argument opens the picker, and it wakes or restarts whatever it finds. |
| A worktree task, then the branch | 2 / 5 | 2 to 3 / 8 | 2 / 5 |
| Reach a dev port | 2 / 2 | 2 / 4 | 2 / 3 |
| Alerts | 2 / 3 | 2 / 4 | 2 / 3 |
| Recover an ended agent | 2 / 5 | 2 / 8 | 1 / 3. `attach` asks, or `resume` does it. |
| Install an agent and log in | 2 / 4 | 2 / 4 | 2 / 2. `agents install claude my-vps`, then `agents login claude my-vps` |
| Clean up | 2 / 3 | 3 / 7 | 2 / 4. `rm` asks, and `host rm` says what each choice removes. |

## 4. What the isolation spec helps

- `host default` lets `new` run without a host.
- The spec's error for two words before `--` fixes the silent drop in `new box htop`.
- `stop` beside a removal command matches every sandbox tool in section 6.

## 5. Published guidelines

### 5.1 Grammar and names

- With many kinds of object, use two levels: a noun and a verb. "Either noun verb or verb noun ordering works, but noun verb seems to be more common." Use the same verbs for every kind of object ([clig.dev](https://clig.dev/#subcommands)).
- GitHub's CLI puts the object first and the action second ([Primer](https://github.com/cli/cli/blob/trunk/docs/primer/foundations/README.md)). Heroku names topics with plural nouns and commands with verbs ([Heroku](https://devcenter.heroku.com/articles/cli-style-guide)).
- Azure avoids a group that holds only other groups. It does not use `get` or `new` beside `show` and `create`, because the pairs sound alike. All network rules sit in one group with `add`, `remove` and `list` ([Azure](https://github.com/Azure/azure-cli/blob/dev/doc/command_guidelines.md)).
- Avoid names that are close, such as "update" and "upgrade" ([clig.dev](https://clig.dev/#subcommands)).
- kubectl puts the verb first for verbs that work on many kinds, and has no built-in aliases ([kubectl conventions](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-cli/kubectl-conventions.md)).

### 5.2 Arguments and flags

- Prefer flags. "1 type of argument is fine, 2 types are very suspect, and 3 are never good" ([12 Factor CLI Apps](https://medium.com/@jdxcode/12-factor-cli-apps-dd3c227a0e46)).
- Give every flag a long form. Keep the common ones: `-h`, `-f/--force`, `-q`, `--json` and `--no-input`. Avoid `-v`, which can mean verbose or version ([clig.dev](https://clig.dev/#arguments-and-flags)).
- Options come before operands, and `--` ends the options ([POSIX](https://pubs.opengroup.org/onlinepubs/9799919799/basedefs/V1_chap12.html)). Always support `--help` and `--version` ([GNU](https://www.gnu.org/prep/standards/html_node/Command_002dLine-Interfaces.html)).
- Never take a secret as a flag value or from the environment. Read it from a file, a pipe or a prompt ([clig.dev](https://clig.dev/#environment-variables)).

### 5.3 Prompts and confirmation

- Prompt only when stdin is a terminal. `--no-input` turns every prompt off. "Never require a prompt" ([clig.dev](https://clig.dev/#interactivity)).
- Give a flag for every prompt, and show the default answer in capitals ([Primer](https://github.com/cli/cli/blob/trunk/docs/primer/components/README.md)).
- With `--no-input`, Docker Sandboxes makes a command that needs an answer fail with the command to run ([sbx](https://docs.docker.com/reference/cli/sbx/)).
- Match the confirmation to the risk. A mild change needs no question. A moderate one asks, and offers a dry run. A severe one makes the user type the name, or pass `--confirm=<name>` ([clig.dev](https://clig.dev/#arguments-and-flags)).
- kubectl gives every change `--dry-run` ([kubectl conventions](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-cli/kubectl-conventions.md)). Terraform shows a plan, then applies exactly that plan ([Terraform](https://developer.hashicorp.com/terraform/cli/commands/plan)).

### 5.4 Output and exit codes

- Data goes to stdout, and messages and errors to stderr ([clig.dev](https://clig.dev/#the-basics)).
- Offer `--json`. Human output may change, and scripts should use `--json` ([clig.dev](https://clig.dev/#future-proofing)). After a release, do not change stdout in ways that break scripts ([Heroku](https://devcenter.heroku.com/articles/cli-style-guide)).
- When output goes to a pipe, GitHub's CLI drops colour and truncation, and writes states in words ([Primer](https://github.com/cli/cli/blob/trunk/docs/primer/foundations/README.md)).
- Turn colour off when the stream is not a terminal, when `NO_COLOR` is set, when `TERM=dumb`, or with `--no-color` ([`no-color.org`](https://no-color.org/), [clig.dev](https://clig.dev/#output)).
- Map exit codes to the main kinds of failure. GitHub's CLI uses 0 for success, 1 for failure, 2 for a cancel and 4 for a missing login ([gh](https://cli.github.com/manual/gh_help_exit-codes)). `kubectl exec` passes on the remote exit code.

### 5.5 Help, errors and suggestions

- Lead the help with examples, and show short help when an argument is missing ([clig.dev](https://clig.dev/#help)).
- Suggest a correction, and never run a guess that changes state ([clig.dev](https://clig.dev/#help)). Git only shows its guess by default ([git config](https://git-scm.com/docs/git-config#Documentation/git-config.txt-helpautoCorrect)).
- The CLI framework that this repository uses suggests names within two edits, and can map synonyms such as "remove" to `delete` ([cobra](https://github.com/spf13/cobra/blob/main/site/content/user_guide.md)).
- An error answers two questions: what went wrong, and how to fix it ([Google](https://developers.google.com/tech-writing/error-messages)). Put the most important part last ([clig.dev](https://clig.dev/#errors)). After each command, suggest the next step ([Atlassian](https://www.atlassian.com/blog/it-teams/10-design-principles-for-delightful-clis)).

### 5.6 Configuration and change

- Flags beat the environment, which beats a project file, which beats the user's file ([clig.dev](https://clig.dev/#configuration)).
- When an explicit flag conflicts with a file, kubectl stops with an error instead of a silent override ([kubectl conventions](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-cli/kubectl-conventions.md)).
- Tailscale's `set` changes only the settings that the user names ([Tailscale](https://tailscale.com/kb/1080/cli)).
- Keep changes additive, and warn before a break. Do not accept prefixes of commands, and keep aliases stable ([clig.dev](https://clig.dev/#future-proofing)).
- Keep a deprecated command working for 12 months or two releases, and print a warning when it runs ([Kubernetes](https://kubernetes.io/docs/reference/using-api/deprecation-policy/)). Docker grouped 40 top-level commands by object and kept the old forms ([Docker 1.13](https://www.docker.com/blog/whats-new-in-docker-1-13/)). Git split `checkout` into `switch` and `restore` ([git 2.23](https://github.blog/open-source/git/highlights-from-git-2-23/)).

## 6. Other session and sandbox tools

| Tool | Create | Attach | List | Run one command | Copy | Stop | Start again | Delete | Rules and logs |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| tmux | `new` | `attach` | `ls` | | | | | `kill-session` | |
| zellij | `attach -c` | `attach` | `ls` | | | `kill-session`, which keeps it for later | `attach` | `delete-session` | |
| Docker Sandboxes | `create`, `run` | `run` | `ls` | `exec` | `cp` | `stop` | `run` | `rm`, `prune`, `reset` | `policy allow`, `deny`, `ls`, `rm`, `log`, `check` |
| Hosted VM service | `new`, `fork` | `ssh` | `list` | `exec` | `scp` | `stop` | `resume` | `delete` | firewall rules inside the VM |
| GitHub Codespaces | `create` | `ssh` | `list` | | `cp` | `stop` | | `delete` | `ports`, `logs` |
| Coder | `create` | `ssh` | `list` | `ssh <cmd>` | | `stop` | `start` | `delete` | `logs` |
| Daytona | `create` | `ssh` | `list` | `exec` | | `stop`, `archive` | `start` | `delete` | |
| E2B | `create` | `connect` | `list` | `exec` | | `pause` | `resume` | `kill` | `logs` |
| devcontainer | `up` | `exec` | | `exec` | | | `up` | | |

Sources: [tmux](https://github.com/tmux/tmux/blob/master/tmux.1), [zellij](https://zellij.dev/documentation/commands.html), [sbx](https://docs.docker.com/reference/cli/sbx/), [gh codespace](https://cli.github.com/manual/gh_codespace), [Coder](https://coder.com/docs/reference/cli), [Daytona](https://www.daytona.io/docs/en/tools/cli/), [E2B](https://e2b.dev/docs/sdk-reference/cli/v2.8.0/sandbox), [devcontainer](https://github.com/devcontainers/cli).

What the table shows:

- **"Kill" has three meanings.** tmux destroys the session, zellij keeps it to bring back, and E2B shuts the sandbox down. `rm` and `delete` mean one thing everywhere.
- **Every sandbox tool splits stop from delete.** The devcontainer CLI has no stop, and its users ask how to end a container ([discussion](https://github.com/devcontainers/spec/discussions/139), [issue](https://github.com/devcontainers/cli/issues/386)).
- **One rule model.** Docker Sandboxes keeps every network rule under `policy`. No tool in the sample uses `grant` and `revoke` for network rules.
- **A shell next to the agent is common.** `sbx exec -it <name> bash`, the hosted VM service's `ssh`, `gh codespace ssh` and `modal shell` all give one ([Modal](https://modal.com/docs/reference/cli/shell)).

## 7. User research

- **Words.** In a 1987 study, two people chose the same word for the same command with a probability under 0.20. A design that used one designer's word failed 80 to 90 percent of the time, and many aliases improved success several times over ([doi:10.1145/32206.32212](https://doi.org/10.1145/32206.32212)).
- **Errors.** Developers read error messages, and reading them is as hard as reading source code. It took 13 to 25 percent of task time ([ACM](https://dl.acm.org/doi/10.1109/ICSE.2017.59)). A review of more than 50 years of research says that error messages "present substantial difficulty" ([doi:10.1145/3344429.3372508](https://doi.org/10.1145/3344429.3372508)).
- **Screen readers.** Twelve blind developers found tables, man pages and long output hard to use. The study asks for status on every command, no ASCII art, errors that make sense read aloud, and a way to save long output ([doi:10.1145/3411764.3445544](https://doi.org/10.1145/3411764.3445544)).
- **Concepts.** A study of git puts its difficulty in its concepts, such as the staging area, more than in its command names ([paper](https://spderosso.github.io/onward13.pdf)).
- **The gap.** No study of command recall for session tools was found.

## 8. The grammar we propose

Each rule names its sources. [The CLI plan](../contributing/cli-plan.md) builds them.

| # | Rule | Sources |
| --- | --- | --- |
| G1 | **Two shapes only.** Sessions are the main object, so their verbs stay at the top level: `new`, `attach`, `ls`, `show`, `stop`, `rm`, `shell`, `exec`, `cp`, `fork`, `pull`. Every other object is a noun with verbs under it: `host`, `policy`, `agents`, `image`, `debug`, `config`. Never deeper than two levels. | clig.dev, Azure, sbx |
| G2 | **One verb, one meaning.** `ls` lists, `show` gives one object's details, `add` and `rm` change a collection, `set` changes named settings. `rm` always deletes the thing named, and `stop` always keeps its files. | clig.dev, Azure, section 6 |
| G3 | **One address.** A session is `<host>/<session>`. The host part may be left out when the name is unique across hosts. `-` means the last session, and nothing else. | audit 2.3 |
| G4 | **The object is the argument, and the place is a flag.** `policy allow api.stripe.com --in box/auth-fix`. Only one kind of argument for each command. | 12 Factor, clig.dev |
| G5 | **One rule model.** Every door's rules live under `policy`, at any layer: `--in <host>/<session>`, `--profile`, `--repo`, `--host` or `--all`. No `grant` or `revoke` commands. | sbx, Azure |
| G6 | **Forgiving verbs.** `attach` wakes a stopped sandbox, and offers to restart an ended agent. `resume` on a running session attaches with a note. No command refuses only because a session is in another state. | audit 2.2 |
| G7 | **One meaning for each common flag.** `-y/--yes` skips a question and nothing more. `--force` goes on although work is lost. `-n` is `--name`. `-d` is `--detach`, after a warned change. `--json` prints data for scripts. `--dry-run` shows what would change. Lists accept a repeated flag or commas. Sizes take units. | clig.dev, audit 2.4 |
| G8 | **Prompts only at a terminal.** Each prompt has a flag. `--no-input`, and the environment variable from `brand.Env("NO_INPUT")`, turn prompts off, and a missing answer fails with the flag to pass. With no argument at a terminal, `attach`, `rm` and `show` open a picker. | clig.dev, Primer, sbx |
| G9 | **Confirmation by risk.** Mild changes do not ask. `rm` of one session asks, and takes `-y`. `host rm --purge`, `host rm --destroy` and `policy lockdown --off` make the user type the name, or pass `--confirm=<name>`. Policy changes take `--dry-run`. | clig.dev, kubectl, Terraform |
| G10 | **Data on stdout, messages on stderr.** Every command that prints data or makes something has `--json`, and its fields are a contract. Colour follows `NO_COLOR` and the terminal. | clig.dev, Primer, gh |
| G11 | **Errors with a fix.** Each error names the bad input and ends with one "Try:" line that holds a full command or one imperative sentence. The framework's own errors get the same treatment, with "did you mean" for names within two edits. A guess never runs. | Google, clig.dev, cobra, git |
| G12 | **Exit codes by kind.** 0 done, 1 failed, 2 wrong usage, 3 not found, 4 needs an answer, 5 refused by policy or tier, 130 cancelled. `exec` returns the command's own code. | clig.dev, gh, kubectl |
| G13 | **Help that teaches.** Each command has at least one example. The root help groups commands. The reference shows aliases. Help topics explain the words a user meets: `help concepts`, `help policy`, `help exit-codes`. No internal words in help. | clig.dev, audit 2.5 |
| G14 | **Safe renames.** An old name stays as an alias that prints a warning, for two releases or 12 months. Synonyms that users guess, such as `ssh`, `delete` and `remove`, map to the real command. | Kubernetes, 1987 study, cobra |

## 9. The command map

"Planned" means a name in the isolation spec before this note. None of them has shipped, so they change without aliases.

### 9.1 Sessions

| Today or planned | Proposed | What changes |
| --- | --- | --- |
| `new <host>` | `new [<host>]` | The host is optional: the default host, then the last one. `-C` becomes the short form of `--dir`. `-d` means `--detach` after two releases with a warning. `--setup` and `--no-setup` become `--setup-script` and `--no-setup-script`. In a walled session, a folder outside git gives an empty workspace, not an error. |
| `attach`, `flip` | `attach [<host>/<session>]` | With no argument, the picker. It wakes a stopped sandbox. On an ended agent it asks to start it again, or takes `--restart`. `flip` becomes a warned alias. |
| `resume` | `resume` | On a running session it attaches with a note. |
| `kill`, `rm`, `delete` | `rm` | `delete` stays as an alias, and `kill` becomes a warned alias. It asks at a terminal and takes `-y`. Commits not pulled need `--force`. |
| planned `stop` | `stop` | Keeps the work and frees the memory. |
| `ls --host <h>` | `ls [<host>]` | `--host` stays as an alias. |
| none | `show <host>/<session>` | Everything about one session, with `--json`. |
| planned `shell`, `exec`, `cp`, `fork` | `shell`, `exec`, `cp`, `fork` | `shell` and `exec` move to phase 1 of the isolation plan. `exec` returns the command's exit code. |
| planned `save` | `image save` | No clash with `debug save`. |
| planned `pull --yes` | `pull --allow-flagged` | `--yes` only skips a question. |

### 9.2 Policy

| Planned | Proposed |
| --- | --- |
| `grant <s> net\|push\|port\|mount\|secret\|paste <target> --scope …` | `policy allow [<door>] <target> --in <host>/<session> \| --profile <p> \| --repo <r> \| --host <h> \| --all [--once \| --for <time>]` |
| a deny in a config file | `policy deny [<door>] <target>` with the same places |
| `revoke <s> <id>` | `policy rm <id or target>` with the same places |
| `grants <s>` | `policy ls` with the same places |
| `audit <s>` | `policy log` with the same places, and `--follow` |
| none | `policy check <target>`: which rule decides, and why |
| none | `policy learn <host>/<session>`, `policy apply`, `policy lockdown`, `policy edit` |

The door words are the same everywhere: `net`, `push`, `mount`, `secret`, `ssh-key`, `port`, `paste` and `device`. `net` is the default, so `policy allow api.stripe.com --in box/auth-fix` needs no door word.

### 9.3 Hosts and the rest

| Today or planned | Proposed | What changes |
| --- | --- | --- |
| `host idle`, planned `host tier`, `host wall --floor`, `host drain`, `host undrain` | `host set <host> idle=2h tier=own floor=microvm drain=on` | One form for every host setting. `host idle` becomes a warned alias. |
| planned `host wall <h>`, `host grants <h>` | `host show <host>` | The levels, the floor, the tier, room, health, and the names sent to the host. It also gives the launcher's "host details" a command. |
| `host rm`, `host destroy`, planned `host rm --purge` | `host rm <host> [--purge] [--destroy]` | At a terminal it asks about each part and says what it removes. `--purge` and `--destroy` need the typed name or `--confirm`. `host destroy` becomes a warned alias for `host rm --destroy`. |
| planned `--root run\|print\|skip` | `--root-step run\|print\|skip` | No clash with the guest's system files. |
| planned `new --root reset\|keep` | `new --keep-system` | The same. |
| planned `new auto --wake` | `new auto --allow-wake` | No clash with `host add --wake`. |
| planned `host images` | `image ls`, `image pull`, `image rm`, `image save` | One noun for images. |
| `host add` to update a helper | `host upgrade <host>` | One verb for one job. |
| `host alerts set --token-cmd` | `--token-command` | The old name becomes a warned alias. |
| `host add --yes` (planned meaning) | `host add --yes` with `--tier` | A script must name the tier. Without it, the command fails with the flag to pass. |
| `agents install <a> --host <h>` | `agents install <agent> [<host>]` | `--host` stays as an alias. |
| none | `agents login <agent> [<host>]` | Runs the agent's own device login in a session. |
| none | `config check` | Reads every config file and reports errors with their line. |

## 10. What could not be verified

- The exact list of Docker Sandboxes' Balanced preset.
- Whether users of session tools recall `kill` or `rm` more often. No study was found.
- Whether a stale kubectl context leads users to act on the wrong cluster.

## Sources and method

Two researchers worked on 10 November 2026. One audited this repository's CLI, the generated reference and the docs, and the planned commands in the isolation spec. They built the CLI to read its real errors. The other read the guidelines, the reference pages of the tools in section 6, and the four studies in section 7. Every external claim links its source. No account was made, and nothing was installed outside the repository's own build.
