# Your first cloud host

In about twenty minutes you will rent a small Linux server, connect it, install an agent there, and give it a task that keeps running after you close the laptop. When it needs you, your phone tells you.

You need:

- the client, installed as in [getting started](getting-started.md);
- an account with a cloud provider that rents Linux servers by the hour, such as Hetzner Cloud, DigitalOcean, Vultr, Linode or AWS Lightsail. The server costs money while it exists; step 7 removes it.
- a login for your agent: a Claude or ChatGPT plan, or an API key.

This page calls the server `my-server`. Use your own name for it.

`simstim setup` walks you through steps 1 to 5: pick "A server I will rent". It checks each step it can, shows the lines for `~/.ssh/config` and adds them only if you say yes, then connects the server and offers to install and log in to your agents. This page explains each step, so you can also do them by hand.

## 1. Make an SSH key

If you already have a key in `~/.ssh` (a file such as `id_ed25519.pub`), skip this step. Otherwise make one on your laptop:

<!-- test: skip (makes a key) -->
```sh
ssh-keygen -t ed25519 -C "my laptop"
```

Press Enter to keep the default file, and give the key a passphrase. Your Mac keeps the passphrase in its keychain, so you type it rarely.

## 2. Rent the server

In your provider's console, create a server:

- **Image:** Ubuntu 24.04 LTS or Debian 13. Any recent Linux with systemd works.
- **Size:** at least 2 vCPUs and 4 GB of memory, which gives your sessions about 2.8 GiB. A host with 4 vCPUs and 8 GB is comfortable, with room for about five busy agents at once ([memory](../how-to/memory.md)). Swap does not give sessions more room.
- **SSH key:** paste the contents of `~/.ssh/id_ed25519.pub`. Turn password login off, if the console asks.

Note the server's IP address.

## 3. Make a user for your agents

Agents should not run as `root`. Log in once as root, and make a user with your key. Replace `203.0.113.10` with your server's address:

<!-- test: skip (runs on a new server) -->
```sh
ssh root@203.0.113.10
adduser --disabled-password --gecos "" dev
usermod -aG sudo dev
echo "dev ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/dev
install -d -m 700 -o dev -g dev /home/dev/.ssh
install -m 600 -o dev -g dev ~/.ssh/authorized_keys /home/dev/.ssh/
exit
```

Passwordless sudo lets agents install packages. Leave out the `echo` line if you prefer to type a password for sudo yourself.

## 4. Give the server a name

Add the server to `~/.ssh/config` on your laptop, so that `ssh my-server` reaches it:

```text
Host my-server
    HostName 203.0.113.10
    User dev
```

Check it. This should print `ok` with no password prompt:

<!-- test: skip (needs the new server) -->
```sh
ssh my-server echo ok
```

## 5. Connect it, and install an agent

Connect the host. This installs a small helper in your home folder there; nothing listens on the network.

```sh
simstim host add my-server --yes
simstim host doctor my-server
```

`host doctor` checks the host and lists the agents it finds: none yet. Install one with its vendor's own installer, in a session you watch:

<!-- test: help (runs the vendor's installer) -->
```sh
simstim agents install claude --host my-server
```

Answer the installer's questions in the session that opens. When it is done, the client says how to log in. For Claude Code, run its login in a session, and follow the link it prints:

<!-- test: skip (it starts a login) -->
```sh
simstim new my-server -- claude auth login
```

simstim never sees your login. [Install an agent](../how-to/install-agents.md) has the login for each agent.

## 6. Give the agent a task, and walk away

Clone a repository on the server and start the agent in its own worktree, so its changes stay on a branch of their own:

<!-- test: help (opens the session) -->
```sh
simstim new my-server --agent claude --dir ~/src/app --worktree --name first-task
```

Give the agent a task. Then press **Ctrl+\\** to detach. The agent keeps working.

To hear when it needs you, send alerts to your phone. Install the [ntfy](https://ntfy.sh) app, subscribe to a topic name that only you know, and tell the host about it:

<!-- test: help (changes the host's alerts) -->
```sh
simstim host alerts set my-server --ntfy https://ntfy.sh/your-secret-topic
simstim host alerts test my-server
```

Now close the laptop. When the agent waits for an answer, or finishes, your phone shows it. Open the laptop, run `simstim`, and press `w` to jump to the session that needs you. When the work is done, fetch the agent's branch into your own clone:

<!-- test: help (needs a session in a repository) -->
```sh
simstim pull my-server/first-task
```

## 7. Clean up

Remove the host from the client, then delete the server in your provider's console, so it costs nothing more:

<!-- test: skip (removes the host) -->
```sh
simstim host rm my-server
```

## Next

- [Keep your scrollback](../how-to/scrollback.md) in a long conversation.
- [Open the agent's dev server](../how-to/ports.md) in your browser.
- [Create a host in one step](../how-to/create-a-host.md): a local VM with Lima, or an E2B sandbox.
- Read [how simstim compares](../explanation/comparison.md) with other ways to run agents.
