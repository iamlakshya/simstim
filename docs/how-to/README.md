# How-to guides

One task per page. New here? Start with [getting started](../tutorials/getting-started.md).

## Hosts

| Guide | Task |
| --- | --- |
| [Add a host](add-a-host.md) | Connect any host you can reach with SSH, with the guided setup or by hand |
| [Use a Mac as a host](a-mac-as-a-host.md) | Run sessions on this Mac in a pinned Lima VM started with `limactl autostart` |
| [Create a host](create-a-host.md) | Make a host in one step: a local Lima VM, a Docker container for testing, or an E2B sandbox |
| [Sandboxes](sandboxes.md) | Check which sandbox wall levels a host supports, install bubblewrap and set a host's floor |
| [Restore after a reboot](restore-after-reboot.md) | Bring sessions back after the host restarts |

## Agents

| Guide | Task |
| --- | --- |
| [Install agents](install-agents.md) | Pick agents from a checklist, or install one with its own installer, then log in |
| [Manage session images](images.md) | Pin, pull, list and remove OCI session images and the read-only agents disk |
| [Pass secrets](secrets.md) | Give a session an API key from your keychain or password manager, never stored |
| [Use with coding agents](use-with-agents.md) | Tell a coding agent how your project uses the tool: a snippet for `AGENTS.md`, and the docs in Markdown |

## Daily work

| Guide | Task |
| --- | --- |
| [Work with sessions](sessions.md) | Attach, describe and delete a session, and leave out its host when the name is unique |
| [Shells and commands in a sandbox](shells.md) | Open a shell, run one command, or add a second agent inside a running sandbox |
| [Worktrees](worktrees.md) | Give each task its own git worktree and branch |
| [Setup hooks](setup-hooks.md) | Prepare each new session or worktree with the repository's own script |
| [Bring work to your laptop](pull.md) | Fetch a session's branch as a verified git bundle into your own clone |
| [Copy files in and out](copy-files.md) | Copy files and folders between your laptop and a session over SFTP |
| [Open a session in an editor](editors.md) | Connect VS Code, Cursor, Zed, `scp`, `sftp`, `rsync` and `ssh` to a session |
| [Ports](ports.md) | Open a web server that runs in a session |
| [Network policy](policy.md) | Control which hosts and DNS names a walled session may reach |
| [Paste images](image-paste.md) | Paste a screenshot into a remote agent |
| [Keep your scrollback](scrollback.md) | Start agents inline, so your terminal keeps the conversation |
| [Memory](memory.md) | Give a session more memory, and see what a host gives its sessions |
| [Get alerts](alerts.md) | Hear from a session that needs you, with the laptop shut: ntfy, webhooks, commands |

## Your setup

| Guide | Task |
| --- | --- |
| [Change keys](rebind-keys.md) | Rebind the launcher's keys and the detach key |
| [Shell completion](shell-completion.md) | Turn on Tab completion for bash, zsh, fish or PowerShell |
| [Use in scripts](scripting.md) | Ask nothing, read JSON and exit codes, and keep data apart from messages |

## Install and remove

| Guide | Task |
| --- | --- |
| [Install](install.md) | Install, update or build the client |
| [Uninstall](uninstall.md) | Remove the client, and the helper from your hosts |

## When something goes wrong

| Guide | Task |
| --- | --- |
| [Troubleshoot](troubleshoot.md) | Find out what is wrong, and fix it |
| [Debug a session](debug-a-session.md) | Record why a session goes wrong: debug files, profiles and traces |
