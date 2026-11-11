---
status: accepted
date: 2026-11-10
---

# 0033 One grammar for the command line: session verbs at the top, a noun and a verb for everything else

## Context

The command line grew one command at a time, and the same problems now repeat ([the CLI study](../../research/cli-dx.md)):

- Removal has three names, `kill`, `rm` and `delete`, and `host rm` only forgets a host.
- `attach` and `resume` refuse each other's states, so the user must know a session's state before choosing a verb.
- Flags differ for one job: `--token-cmd` and `--token-command`, four forms of a list, and three meanings each for `--force` and `--yes`.
- The framework's own errors carry no "Try:" line, and exit codes come from the text of a message.

[The isolation spec](../isolation-spec.md) planned 18 more commands, with new clashes: `grant` beside `grants`, `--root` with two meanings, and `pull --yes` that skips a security review. A grammar is needed before those commands ship, because a name is hard to change after scripts use it.

## Options

1. Keep the current style, and fix each problem where it appears.
2. Verbs at the top level for everything, as kubectl does: `get host`, `allow net`.
3. A noun and a verb for everything, as gh does: `session new`, `session attach`.
4. Two shapes: sessions keep short verbs at the top level, because they are the main object. Every other object is a noun with verbs under it. Docker Sandboxes, tmux and a hosted VM service use this shape for their main object.

## Decision

Option 4, with the 14 rules G1 to G14 of [the CLI study](../../research/cli-dx.md#8-the-grammar-we-propose). In short:

- **Shapes.** Session verbs stay at the top: `new`, `attach`, `ls`, `show`, `stop`, `rm`, `shell`, `exec`, `cp`, `fork`, `pull`. Every other object is a noun with verbs under it: `host`, `policy`, `agents`, `image`, `debug`, `config`. Never deeper than two levels.
- **Verbs.** `ls` lists, `show` gives details, `add` and `rm` change a collection, `set` changes named settings. `rm` deletes, and `stop` keeps the files.
- **Rules.** Every door's rules live under `policy`: `allow`, `deny`, `rm`, `ls`, `check` and `log`. A flag names the layer: `--in <host>/<session>`, `--profile`, `--repo`, `--host` or `--all`. No `grant` or `revoke` commands.
- **Forgiving verbs.** `attach` wakes a stopped sandbox and offers to restart an ended agent. No command refuses only because a session is in another state.
- **Flags.** `-y/--yes` only skips a question. `--force` goes on although work is lost. `--json` and `--dry-run` mean the same everywhere. Every prompt has a flag, and prompts appear only at a terminal.
- **Errors and exit codes.** Every error ends with one "Try:" line. Exit codes come from the kind of error: 0 done, 1 failed, 2 wrong usage, 3 not found, 4 needs an answer, 5 refused by policy or tier, 130 cancelled.
- **Renames.** An old name stays as an alias that prints a warning, for two releases or 12 months.

Option 4 keeps today's most used commands, `new`, `attach` and `ls`, as they are. Option 3 would rename all of them. Option 2 would turn short commands into long ones.

## Consequences

- Good, because a user who knows one noun can guess the verbs of the next.
- Good, because the commands of isolated sessions arrive in one shape, before any script depends on them.
- Good, because errors and exit codes become something a script and a person can both rely on.
- Good, because users of Docker Sandboxes find `policy allow` where they expect it.
- Bad, because `kill`, `flip`, `host idle`, `host destroy`, `--token-cmd` and the meaning of `-d` change. Warned aliases soften this for two releases or 12 months.
- Bad, because two shapes are one more thing to learn than one shape.
- Bad, because the docs, the generated reference, the UX spec and the launcher's palette all change with the commands.
- This record replaces no accepted record. [The CLI plan](../cli-plan.md) builds it, and [the isolation plan](../isolation-plan.md) follows it for every command that it adds.
