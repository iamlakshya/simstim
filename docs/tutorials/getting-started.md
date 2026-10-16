# Getting started

In about five minutes you will install the client, set up a host and its agents, start a session, leave it running, and come back to it.

You need:

- a Mac or a Linux machine with OpenSSH (it has it already);
- somewhere for agents to run. The guided setup offers each of these, and says what it needs:
  - a Linux host you can reach with `ssh`: a VPS, a cloud VM, or a machine at home. If `ssh my-server` logs you in without a password prompt, you are ready. This page calls the host `my-server`; use your own name for it.
  - a local VM, with [Lima](https://lima-vm.io);
  - a server you will rent: the setup walks you through it.

## 1. Install the client

<!-- test: skip (needs a published release; scripts/test-install.sh tests the installer) -->
```sh
curl -fsSL https://raw.githubusercontent.com/iamlakshya/simstim/main/install.sh | sh
```

The installer checks the release's signature and checksum, then puts the client in `~/.local/bin`. Check it:

```sh
simstim version
```

## 2. Run the guided setup

<!-- test: help (it asks questions) -->
```sh
simstim setup
```

It asks one thing at a time:

1. **Where will agents run?** Pick a host in your SSH config, a local VM with Lima, a container for testing with Docker, an E2B sandbox, or "a server I will rent". Each one says whether this computer can use it now. If not, it shows the steps to make it ready, such as `brew install lima`, and checks again when you press Enter.
2. **Connect.** It checks that the host answers, and asks before it installs a small helper there: in your home folder, with nothing that listens on the network. A host it makes for you, such as a Lima VM, counts as your yes.
3. **Agents.** A checklist of the agents it can install. Installed ones are ticked already. Type the numbers of the ones you want, such as `1 2`. Each one installs with its vendor's own installer, in a session you watch: answer its questions there.
4. **Log in.** For each agent that is not logged in, it offers the agent's own login, in a session. Follow its prompts, usually a link to open in your browser. simstim never sees the login.
5. **Done.** A summary of each agent, and the command that starts your first session.

Press **Ctrl+C** at any step to stop. The setup says what it did, and running it again picks up from there.

The launcher offers the same setup: run `simstim` with no hosts, and press Enter.

For a script, or to skip the questions, answer them with flags:

<!-- test: help (it changes the host) -->
```sh
simstim setup --host my-server --agents claude,codex --yes --no-login
```

### Or step by step

The setup runs commands you can also run yourself. `host add` checks that the host answers, installs the helper, and saves the host:

```sh
simstim host add my-server --yes
```

`--yes` agrees to the helper's install; leave it out to see what it installs first. Then check the host:

```sh
simstim host test my-server
```

[Install an agent](../how-to/install-agents.md) has the rest: `agents install`, and each agent's login.

## 3. Start a session

Start a shell session that keeps running on the host:

```sh
simstim new my-server --name first --detach
simstim ls
```

`ls` shows the session, with its state and folder.

To start an agent instead, name it. This opens the session in your terminal straight away:

<!-- test: help (opens the session) -->
```sh
simstim new my-server --agent claude --dir ~/src/app
```

If the folder does not exist on the host yet, `new` asks whether to make it. `--mkdir` makes it without asking, and the launcher asks the same question.

While you are attached, the agent owns your terminal: its keys, colours and scrollback are its own. Press **Ctrl+\\** to detach. The session keeps running.

## 4. Come back

Close the laptop, change networks, come back tomorrow. Then attach again:

<!-- test: help (opens the session) -->
```sh
simstim attach my-server/first
```

Or run `simstim` with no arguments to open the launcher: Enter on a host shows its sessions, Enter on a session joins it, and `n` starts a new one. Press `?` in the launcher to see every key.

## 5. Clean up

```sh
simstim kill my-server/first
```

## Next

- [Add a host](../how-to/add-a-host.md) behind a jump host, or with a repo's own SSH config.
- [Your first cloud host](first-cloud-host.md): what the setup's "a server I will rent" does, step by step.
- [Keep each task in its own worktree](../how-to/worktrees.md).
- Read [how it works](../explanation/architecture.md).
