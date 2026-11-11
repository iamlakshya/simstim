# Set up a repository when a session starts

A repository can prepare each new session itself: install dependencies, start a database, write a config file. Put an executable script at `.simstim/setup` in the repository. When a session starts in the repository, or in a new worktree of it, the script runs in the session's terminal before the agent, so you see its output when you attach.

## Write the script

<!-- test: skip (writes files in a repository) -->
```sh
mkdir -p .simstim
cat > .simstim/setup <<'EOF'
#!/bin/sh
set -e
echo "Setting up $SIMSTIM_SESSION_NAME in $SIMSTIM_SESSION_DIR"
if [ "$SIMSTIM_WORKTREE_NEW" = 1 ]; then
    npm ci
fi
EOF
chmod +x .simstim/setup
git add .simstim/setup
```

It must be executable, in the repository's top folder: a session that starts in a subfolder finds it there too. It runs in the session's folder. Commit it, so new worktrees have it.

The script must be a file in the repository. It can be a relative symlink to another file in the repository. A symlink to an executable file outside the repository, or an absolute symlink to one, stops the start with this error:

```text
Error: the setup script of /home/me/src/app, .simstim/setup, points outside the repository, so it cannot run

Try: Make .simstim/setup a file in the repository, or start with --no-setup.
```

A symlink out of the repository to nothing, or to a file that is not executable, counts as no script, because it could not run. The session then starts as in a repository with no script.

An executable script that your user on the host cannot read stops the start too, because its content cannot be checked. So does a repository folder that your user can enter but not list, such as one of mode 0111.

For an executable script outside the repository, and for a script or folder that cannot be read, the launcher offers to start the session without the repository's scripts.

The script gets the session's environment, including any [secrets](secrets.md), and these variables:

| Variable | Value |
| --- | --- |
| `SIMSTIM_HOOK` | `setup` for a new session, `resume` after a host restart or a `resume` |
| `SIMSTIM_SESSION` | The session's ID |
| `SIMSTIM_SESSION_NAME` | The session's name |
| `SIMSTIM_SESSION_DIR` | The session's folder |
| `SIMSTIM_WORKTREE_NEW` | `1` when the session got a new worktree, else `0` |
| `SIMSTIM_REPO` | For a worktree session, the repository's main folder |

If the script fails, the terminal shows its exit code, and the agent starts anyway:

```text
[simstim: running /home/me/src/app/.simstim/setup]
npm ERR! ...

[simstim: /home/me/src/app/.simstim/setup exited with 1; the agent starts anyway]
```

An executable `.simstim/resume` runs the same way before an agent comes back: after a host restart, or after a [`resume`](restore-after-reboot.md#bring-back-one-session).

## Trust a repository first

A repository is code from someone else, and its script runs as you. So it runs only after you say yes, for each repository on each host. The first time a session starts in a repository with a script, `new` asks:

```text
The repository /home/me/src/app on my-server has a setup script:

    /home/me/src/app/.simstim/setup

It runs in the session before the agent, as you, with the session's environment and
secrets. Say yes only for a repository you trust. The answer is kept for this
repository on my-server until the script changes; --setup and --no-setup override it.

Run it? [y/N]
```

The launcher asks the same in a dialog, from the quick start and from the form. The answer is kept on your laptop, in `~/.local/state/simstim/setup-trust.json`, and every worktree of the repository shares it. To change an answer, edit or delete that file.

For one session, `--setup` runs the script and `--no-setup` skips it, whatever the answer:

<!-- test: help (needs a host and a repository) -->
```sh
simstim new my-server --agent claude --dir ~/src/app --worktree --setup
simstim new my-server --agent claude --dir ~/src/app --no-setup
```

Without a terminal to ask on, for example in a script or with its output in a pipe, `new` skips a script you have not answered for, or one that changed after your yes, and says so. `--no-setup` and a no read no script, so they also start a session whose script cannot run.

### A changed script asks again

A yes holds only for the content of the scripts that you said yes to. The host reads `.simstim/setup`, `.simstim/resume` and `.simstim/stop` at each start, and works out a SHA-256 hash of the three. This version does not run `.simstim/stop` yet, but a change to it asks again too. The client keeps that hash with your yes. After a commit or an edit that changes any of these scripts, the next start asks again, and says why:

```text
The setup script of /home/me/src/app on my-server changed after you trusted it:

    /home/me/src/app/.simstim/setup
```

A yes keeps the new hash. Without a terminal to ask on, `new` skips the changed script, and says that it changed. A no is kept as before, whatever the scripts hold.

The hash covers only the bytes of these three files. A file that a script runs or reads can change with no question. For example, `npm ci` runs the install scripts of the packages that it installs, and the hash does not cover them.

Before a restore after a host restart, or a `resume`, the host reads the scripts again. If they changed after the session started, the host skips the repository's `resume` script, and runs your own resume hook instead, if you have one. The session's log, `~/.local/state/simstim/sessions/<id>/holder.log` on the host, gets a line that says so.

A session that started before this version kept no hash, so it skips the repository's `resume` script at each restore or resume. Start a new session to run the script again.

### The answers file

The file holds each no under `hosts`, and each yes under `trusted`, with its hash and the time of the yes. A yes from an older version of the file has no hash, so the next start in that repository asks once more. That question says that you trusted the script before this version checked its content, because the client cannot know whether it changed. An older client that writes the file keeps every no, and drops the yes answers. The client then asks again for those repositories.

If the file does not parse, the client renames it to `setup-trust.json.bad`, warns once, and asks again for each script. When an older `setup-trust.json.bad` is there, the client keeps it, and uses the next free name: `setup-trust.json.bad.1`, then `.bad.2`, with no limit. The warning names the file. If the file breaks while a question waits, keeping your answer moves it aside in the same way, and the warning names it too. Compare the two files, then delete the bad one. If the client cannot rename the file, it keeps no answer until you fix or delete `setup-trust.json`.

Each read and save of the file holds a lock on `setup-trust.json.lock`, so two clients that answer at once keep both answers. While another client holds the lock, a client waits up to 2 seconds. If the lock stays busy, it trusts no script for that start, and says so. An answer is then not kept, and the start stops with an error. Try again a moment later.

## Your own setup on a host

An executable `~/.config/simstim/hooks/setup` on the host runs for every session in a repository with no script of its own, or one whose script you did not trust. It is yours, so nothing asks first. `~/.config/simstim/hooks/resume` does the same after a restart or a resume.

## With .worktreeinclude

A new worktree starts with only the files git tracks. List other files the repository needs in `.worktreeinclude`, one pattern a line, for example `.env`, and they are copied from the main folder into each new worktree ([worktrees](worktrees.md)). The copy happens before the setup script runs, so the script can use them. Keep the copy in `.worktreeinclude`, and the commands in the script.
