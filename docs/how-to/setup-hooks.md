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

A repository is code from someone else, and its script runs as you. So it runs only after you say yes, once for each repository on each host. The first time a session starts in a repository with a script, `new` asks:

```text
The repository /home/me/src/app on my-server has a setup script:

    /home/me/src/app/.simstim/setup

It runs in the session before the agent, as you, with the session's environment and
secrets. Say yes only for a repository you trust. The answer is kept for this
repository on my-server; --setup and --no-setup override it.

Run it? [y/N]
```

The launcher asks the same in a dialog, from the quick start and from the form. The answer is kept on your laptop, in `~/.local/state/simstim/setup-trust.json`, and every worktree of the repository shares it. To change an answer, edit or delete that file.

For one session, `--setup` runs the script and `--no-setup` skips it, whatever the answer:

<!-- test: help (needs a host and a repository) -->
```sh
simstim new my-server --agent claude --dir ~/src/app --worktree --setup
simstim new my-server --agent claude --dir ~/src/app --no-setup
```

Without a terminal to ask on, for example in a script or with its output in a pipe, `new` skips a script you have not answered for, and says so.

## Your own setup on a host

An executable `~/.config/simstim/hooks/setup` on the host runs for every session in a repository with no script of its own, or one whose script you did not trust. It is yours, so nothing asks first. `~/.config/simstim/hooks/resume` does the same after a restart or a resume.

## With .worktreeinclude

A new worktree starts with only the files git tracks. List other files the repository needs in `.worktreeinclude`, one pattern a line, for example `.env`, and they are copied from the main folder into each new worktree ([worktrees](worktrees.md)). The copy happens before the setup script runs, so the script can use them. Keep the copy in `.worktreeinclude`, and the commands in the script.
