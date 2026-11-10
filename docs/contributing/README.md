# Contributing

How the project is built, tested, documented and released. New here? Read [CONTRIBUTING.md](../../CONTRIBUTING.md) first, then run `scripts/setup.sh`.

| Page | What it covers |
| --- | --- |
| [Testing](testing.md) | Unit, golden, fuzz and end-to-end tests, coverage, and how to run them |
| [Tracing typing lag](debug-trace.md) | The debug trace: turn it on, copy the host's file, and read where a key waits |
| [How the docs work](docs.md) | How the docs are organised, generated, checked and published |
| [Style](style.md) | Rules for code and for writing |
| [Releasing](releasing.md) | Versions, what a release contains, and how to cut and check one |
| [Going public](going-public.md) | What is done, and the owner's steps to make the repo public |
| [UX spec](ux-spec.md) | The launcher's screens, keys, colours, glyphs and motion |
| [Renaming](renaming.md) | How to rename the product in one change |
| [Isolated sessions: spec](isolation-spec.md) | What to build so that each session runs in its own sandbox, on hosts that the user adds: terms, interfaces, policy layers, the classifier, shells, config, commands, protocol, platforms, risks and open questions (proposed) |
| [Isolated sessions: plan](isolation-plan.md) | The tasks that build the spec, in order, one pull request each, with acceptance criteria and tests, the goal that says when the design is done, and a table that traces each task to its spec and research (proposed) |
| [The command line: spec](cli-spec.md) | What each task of the CLI plan builds: the grammar, errors and exit codes, prompts, machine output, help, verbs, flags, host commands, agent logins, config check, completion and docs for agents (proposed) |
| [The command line: plan](cli-plan.md) | The tasks that make the command line easy to guess and hard to misuse: errors, exit codes, prompts, help, session verbs, flags, host commands and completion, under record 0033 (proposed) |
| [Decisions](decisions/README.md) | Every design decision, with its reasons |
| [Research](../research/README.md) | The evidence behind the design |
| [History](history.md) | The first plan, as built |
