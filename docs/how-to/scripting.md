# Use simstim in scripts

Every command works without a terminal. Three things make it safe in a script: `--no-input`, `--json` and the exit codes.

## Ask nothing

A command asks a question only when input is on: stdin, stdout and stderr are all terminals. Any of these turns input off:

- `--no-input`, which every command takes.
- `--json`.
- The variable `SIMSTIM_NO_INPUT`, set to anything but an empty value, `0` or `false`.

With input off, a question that the command needs fails with exit code 4. Its `Try:` line is your command with the flag that answers the question, before any `--`. A question whose default changes nothing takes the default, and says so on stderr. SSH asks nothing either: it runs with `BatchMode=yes`, so a host that needs a password or a new host key fails with its SSH error.

<!-- test: help (a host with no helper yet) -->
```sh
simstim host add my-server --no-input
```

```text
Error: host add needs a yes to install the helper on my-server, and input is off.

Try: simstim host add my-server --no-input --yes
```

These are the questions, and the flag that answers each:

| Command | Question | Flag | With input off |
| --- | --- | --- | --- |
| `host add` | Install the helper? | `--yes` | Fails with exit code 4 |
| `host add` | Which agents to install, when the host lacks one | `--yes` offers none | Installs nothing |
| `new` | Make the folder that does not exist? | `--mkdir` | Fails with exit code 4 |
| `new` | Run the repository's setup script? | `--setup-script` or `--no-setup-script` | Skips the script, and says so |
| `doctor --fix`, `host doctor --fix` | Fix it? | `-y` | Fails with exit code 4. With `--json` and `-y`, the JSON holds how each fix ended |
| `host destroy` | Delete the machine? | `-y` | Fails with exit code 4 |
| `setup` | Each question of the guided setup | `--yes`, with `--host` or `--create`, and `--agents` | Fails with exit code 4 |

At a terminal, `attach` with no session opens a short list of your sessions to pick from. It shows the sessions that it saw last at once, then asks the hosts. Type to filter it, then press Enter, or Esc to cancel. Type to the session after its screen appears: keys typed before that can be lost. With input off, `attach` needs the session, and fails with exit code 2 without one. When the hosts answer with no session, it fails with exit code 3, and when no host answers, with the host's error and exit code 1.

Opening the launcher, running `flip` with no session, or running `demo` also needs input on. With input off, each fails with exit code 4 and names a command for scripts in `Try:`. Without a terminal on stdin, stdout and stderr, each fails with exit code 1.

If the terminal's reader cannot restart after an editor, a shell or key creation, the launcher exits and restores the terminal. Its error says how to continue in a new terminal. Diagnostic key reads also end when cancellation claims success but the read stays blocked. A terminal check refuses a busy or failed reader before sending queries. The session picker gives a terminal recovery hint. Key creation and editor processes use the command context, so cancellation stops them. Failed key creation gives its command in the recovery hint. A check after an install uses SSH without prompts if an earlier terminal read remains blocked.

`agents install` with input off starts the installer in a session, and does not attach to it. The command says how to attach and answer the installer's questions.

Esc, Ctrl+C, the end of input, or a signal at a question cancels. The command prints `Cancelled. Nothing changed.`, or what it did before the cancel, and exits with 130.

## Read JSON

These commands take `--json`: `ls`, `new --detach`, `resume --detach`, `ports`, `doctor`, `host ls`, `host test`, `host doctor`, `host restore`, `host status`, `host alerts`, `host alerts log`, `agents ls`, `config keymap` and `version`. `new` and `resume` need `--detach` with `--json`, because an attached session owns the terminal.

<!-- test: help (needs a host) -->
```sh
simstim ls --json
simstim new my-server --agent claude --detach --json
simstim ports my-server/app --json
```

The JSON follows these rules, so a script keeps working from one release to the next:

- stdout gets one JSON value and nothing else, with one newline at its end.
- Each top-level object, and each object of a top-level array, has `"v": 1`.
- Keys are in `snake_case`. A map of your own data, such as a host's `labels`, keeps your keys.
- A value that is not set is left out, and is never `null`. Two outputs keep their older form until the end of their old form: the sessions in `ls --json` give a zero time, `0001-01-01T00:00:00Z`, for a time that is not set, and the old keys of `host ls --json` give `null` for an empty value.
- A new release may add keys. It never renames a key or changes its type or its meaning, so ignore the keys you do not know.

A session that `new --detach --json` starts prints as one object, with its `host`, its `address` and every field that has a value:

```json
{
  "v": 1,
  "address": "my-server/app",
  "agent": "claude",
  "alive": true,
  "cols": 120,
  "created_at": "2026-11-11T10:40:00Z",
  "cwd": "/home/dev/src/app",
  "host": "my-server",
  "host_version": "0.2.0",
  "id": "1a2b3c4d",
  "name": "app",
  "rows": 36,
  "since": "2026-11-11T10:40:00Z",
  "source": "hooks",
  "state": "starting"
}
```

The `address` uses the session's ID instead of its name when another session on the host has that name, or has that name as its ID. If the host does not list its sessions after the start, the address uses the ID, and a warning on stderr says why. The command still exits with 0, because the session started.

The other commands print these shapes. Each object at the top also has `"v": 1`:

| Command | JSON |
| --- | --- |
| `ls` | An array with one object for each host: `host`, `sessions`, and `error` when the host did not answer |
| `ports` | `host`, `session` (the name), `id`, and `ports`: each with `port`, `address`, `pid` and `process` |
| `doctor` | `checks`, each with `name`, `ok`, `detail` and `fix`, and `fixes`, each with `id`, `problem`, `action` and `command` |
| `host ls` | An array of hosts, with the keys of `hosts.toml`: `name`, `ssh`, `ssh_config`, `labels`, `provider`, `machine` and `secrets` |
| `host test` | `host`, `connect_ms` and `round_trip_ms` |
| `host restore` | `host`, and `results`: each with `id`, `name`, `action` and `why` |
| `host status` | `host`, `state`, `provider` and `id` for a host that `host create` made, or `host` and `output` with a provider script's output |
| `version` | `version`, `protocol` and `helper` |

The `address` is what other commands take, so a script can start a session and type into it:

<!-- test: skip (a script that starts an agent) -->
```sh
address=$(simstim new my-server --agent claude --dir ~/src/app --detach --json | jq -r .address)
simstim send "$address" "run the tests and fix what fails"
```

## Read errors and exit codes

Every command exits with a code that says what kind of thing went wrong: 0 done, 1 failed, 2 wrong usage, 3 not found, 4 needs an answer, 5 refused, and 130 cancelled. The [exit codes](../reference/exit-codes.md) page has the full table.

The error goes to stderr. With `--json`, stdout stays empty, and stderr gets the error as one JSON line, with the same exit code:

```json
{"v":1,"error":{"code":"host_not_found","kind":"not_found","message":"no host named \"my-servr\"","try":"simstim host add my-servr","exit":3}}
```

`try` is the next step, as in the `Try:` line. A mistyped command or flag also gets `suggestions`: the names that you may have meant.

<!-- test: skip (a script) -->
```sh
if ! simstim host test my-server --json >test.json 2>error.json; then
  jq -r .error.try error.json
fi
```

## Streams and colour

stdout gets data only: lists, details and JSON. Everything else goes to stderr: confirmations such as `Deleted app (1a2b3c4d) on my-server.`, progress, warnings, questions and errors. A line that says that there is no data, such as `No hosts yet.` or `Nothing to restore on my-server.`, goes to stderr too. So `simstim ls > sessions.txt` writes only the list. To read the ID of a session that you start, use `new --detach --json`: the line `Started my-server/1a2b3c4d` is a message, on stderr.

Colour and progress lines appear only on a terminal. Colour is also off with `--no-color`, with `NO_COLOR` set to any value, and with `TERM=dumb`. With `--json`, every line on stderr is JSON: warnings and notes, such as `debug on` or a skipped setup script, come as `{"v":1,"warning":{"code":…,"message":…}}`.

## Old names

A name or a form that changes keeps working for two releases or 12 months, whichever is longer, and prints a warning on stderr. With `--json`, the warning is one JSON line, before the result or the error:

```json
{"v":1,"warning":{"code":"old_keys","message":"\"host ls --json\" now prints its keys in snake_case too, such as \"ssh_config\" for \"SSHConfig\".","new":"snake_case keys","old":"the Go keys of \"host ls --json\"","until_date":"2027-11-11","until_version":"0.4.0"}}
```

Today this applies to `host ls --json`. It printed each host's keys as `Name`, `SSH` and `SSHConfig`. It now prints them in `snake_case` too, as `name`, `ssh` and `ssh_config`. Both forms print until v0.4.0 and 11 November 2027, whichever comes later. The 12 months start on the day of the release that adds the warning. Move your scripts to the `snake_case` keys. `host ls --json` prints the warning on every run, also when you have no hosts, so that a script learns of the change before it needs it.
