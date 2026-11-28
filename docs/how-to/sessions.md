# Work with sessions

A session is one agent, or one shell, that runs on a host and keeps running when you leave. These commands find a session, attach to it, describe it and delete it. The launcher does the same with keys: run `simstim` with no arguments.

## Name a session

A command that acts on one session takes its address. An address has one of three forms:

| Address | Means |
| --- | --- |
| `my-server/app` | The session `app` on the host `my-server`. The part after `/` is the session's name or its ID. |
| `app` | The session `app` on whichever host has it. This works when no other host has a session of that name. |
| `-` | The session that you attached to last, by its ID. A session that has its ID as its name is never taken for it. |

A word without a `/` is always a session, also when a host has that name. So `simstim attach my-server` looks for a session named `my-server`.

A new session's ID has 8 characters, each a lower-case letter or a digit from 2 to 7, and at least one digit, such as `abcd2345`. A word of that form is always an ID, never a name. It reaches only the session with that ID. So `new --name` and `rename` refuse such a name, with exit code 2. Add a dash, as in `abcd-2345`, or use another length. A name of 8 letters, such as `frontend`, has no digit, so it is a name as any other. When a session takes its name from a folder named like an ID, such as `proj2345`, the name gets `-s` at its end: `proj2345-s`. The same happens when the folder's name is the ID of a session on the host. A name can have at most 64 characters. A session that already has a name like an ID keeps it. Reach it by its ID, or from the list of sessions.

To find the host of `app`, the client reads the sessions that it saw last. It asks a host again when its list is more than 60 seconds old. It asks every such host at once, and it never starts a stopped machine to ask. When no list has `app`, it asks the other hosts too before it gives up, so a session made a moment ago is found. A host that does not answer counts with the sessions that it had when last seen, and the command names that host on stderr.

The sessions that the client saw last only say which host to ask. The host's own list then decides which session `app` is, when the command acts. So after a rename, `app` never reaches the session that used to have that name.

When two sessions share the name, the command lists both and stops with exit code 2. Its `Try:` line names the first by its host and its ID:

```text
Error: 2 sessions are named "app": my-server/abcd2345, my-nuc/5e6f7g2h.

Try: simstim attach my-server/abcd2345
```

Every command that the client prints for you names a session by its host and its ID, as in that `Try:` line. A name can mean another session, and an ID cannot. The name is in the text around it. When a word names no session, the error suggests the sessions that you may have meant, each by its ID, and its `Try:` line lists the sessions.

`rm` is stricter, because it deletes. When a host does not answer, `rm app` stops with exit code 2. That host may have an `app` too, or its list may be out of date. The `Try:` line names the session that the client found, by its host and its ID.

## Attach

<!-- test: help (opens a session) -->
```sh
simstim attach my-server/app
simstim attach app
simstim attach -
```

Press `Ctrl+\` to detach. The session keeps running.

At a terminal, `attach` with no session opens a short list of your sessions. Type to filter it, then press Enter, or Esc to cancel. Type to the session after its screen appears: keys typed before that can be lost.

A host with a provider script and `--wake` starts before the attach, as it does for the launcher.

When the session's agent has ended, `attach` asks to start it again:

```text
Start claude again in app on my-server? [Y/n]
```

Enter or `y` starts the agent again in the same session, as `resume` does, then attaches. `n` says why the agent ended, and exits with 130. `--restart` answers yes without the question. At a terminal with `--no-input`, `attach` fails with exit code 4 and names `--restart`. On an agent that still runs, `--restart` does nothing, and `attach` says so. When another client starts the agent first, `attach` attaches to it.

`attach` needs a terminal, and checks for one first. In a script it fails with exit code 2, before it looks for the session or wakes a host. To start an ended agent from a script, use `resume --detach`. To type into a session, use `send`:

<!-- test: help (needs a session) -->
```sh
simstim send my-server/app "run the tests and fix what fails"
```

## Start an agent again

`resume` starts an ended agent again, in the same session, and attaches. On an agent that still runs, it attaches, with a note. With `--detach`, it says that the agent runs, and exits with 0. `--memory-max` and `--secret` change nothing while the agent runs, and `resume` says so: pass them again after the agent ends.

<!-- test: help (needs an ended session) -->
```sh
simstim resume my-server/app
simstim resume my-server/app --memory-max 6G --detach
```

## See one session

<!-- test: help (needs a session) -->
```sh
simstim show my-server/app
simstim show app --json
```

`show` prints one line for each detail that the session has, and ends with the command that attaches to it:

```text
app on my-server
  ID              abcd2345
  Agent           claude
  Folder          /home/dev/src/app
  State           working (from hooks)
  Since           11 Nov 2026 10:40
  Created         11 Nov 2026 10:40
  Host helper     0.2.0
  Size            120×36
  Attach          simstim attach my-server/abcd2345
```

The state says where the client learnt it, and why an agent ended, such as `errored (exit code 3, from process)`. A detail that the state does not say has its own line, such as the reason of a start that failed. With `--json`, `show` prints the session as one object, with `host`, `address` and every field that has a value. [Use in scripts](scripting.md) has its keys. Without a session, at a terminal, `show` opens the list of sessions. It never starts a stopped host. When a provider script says that the host is stopped, `show` says so, and names `simstim host start`.

## List sessions

```sh
simstim ls
simstim ls my-server
```

`ls` lists the sessions of every host, or of one. `--host my-server` still works. Each list goes to the cache that the address of a session without a host reads.

## Delete a session

<!-- test: help (needs a session) -->
```sh
simstim rm my-server/app
```

`rm` stops the session's agent and removes the session. At a terminal, it asks first:

```text
Delete app on my-server?
This stops claude on my-server and removes the session.
The folder /home/dev/src/app stays. Unsaved work in the agent is lost.
Delete it? [y/N]
```

Enter or `n` keeps the session, and exits with 130. The folder stays. A worktree that the session made with `--worktree` goes, with its branch, unless it has changes or new commits. For a folder in the helper's worktrees, the question says so. If the session is renamed while the question waits, `rm` deletes nothing, and says so. Without a session, at a terminal, `rm` opens the list of sessions, then asks. `delete` is another name for `rm`.

`-y` deletes without the question. Use it in scripts:

<!-- test: help (needs a session) -->
```sh
simstim rm my-server/app -y
```

Until v0.4.0 and 11 November 2027, whichever comes later, `rm` without a terminal and without `-y` still deletes, and prints a warning. After that, it fails with exit code 4, and names `-y`.

## Old names

These still work, and print a warning on stderr, until v0.4.0 and 11 November 2027, whichever comes later. Their `Try:` lines name the new form:

| Old | New |
| --- | --- |
| `simstim kill my-server/app` | `simstim rm my-server/app -y`. Like `kill`, it does not ask. |
| `simstim flip my-server/app` | `simstim attach my-server/app` |
| `simstim flip` | The launcher's list of every host's sessions, as before. `simstim attach` with no session opens a short list instead. |
