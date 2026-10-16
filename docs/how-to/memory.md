# Give a session more memory

Each session has a memory cap. It covers everything the session runs: the agent, its tools, its MCP servers and language servers, and your builds. When a session reaches its cap, the kernel stops its biggest process, normally the agent, and the session shows "out of memory". A [resume](#when-the-cap-stops-an-agent) starts the agent again.

## What a host gives sessions

A host keeps a reserve of memory for its system, and its sessions share the rest. The reserve follows the one GKE keeps on its nodes: a quarter of a small host, less of a big one ([decision 0024](../contributing/decisions/0024-memory-cap.md)).

| Host memory | For sessions |
| --- | --- |
| 2 GiB | 1.4 GiB |
| 4 GiB | 2.9 GiB |
| 8 GiB | 6.1 GiB |
| 16 GiB | 13.3 GiB |
| 32 GiB | 28.3 GiB |

By default, one session may use all of it, and so may all sessions together. A VM sold as 4 GB has a little less than 4 GiB, so its sessions get about 2.8 GiB. `host add` says what a host gives sessions, and so does `host doctor`:

```sh
simstim host doctor my-server
```

The launcher shows what a session uses under the list, such as `942 MiB of 2.9 GiB`.

## For one session

<!-- test: help (starts a session) -->
```sh
simstim new my-server --agent claude --dir ~/src/app --memory-max 2G
```

## For every session

In `config.toml` (`simstim config path` says where it is):

```toml
[sessions]
memory_max = "2G"
```

A size is a number and a unit, in powers of 1024: `512M`, `4G`, `4GiB` or `1.5G`. The smallest is `256M`. `auto` gives the host's default, and `--memory-max auto` gives it to one session whatever the config says. A size above what the host gives all its sessions is lowered to that. `max` and `infinity` are not sizes here: `auto` already lets a session use all that the host gives its sessions.

A running session keeps the cap it started with. A new cap applies to the sessions that start after the change, and to the sessions that `host restore` starts again.

## When the cap stops an agent

The launcher shows the session as `out of memory`, and `ls` shows `errored (out of memory)`. To start the agent again in the same session, with a bigger cap:

<!-- test: help (resumes a session) -->
```sh
simstim resume my-server/app --memory-max 4G
```

In the launcher, select the session and press `u`. The launcher then attaches to it, as after a start. `u` gives the agent the same cap again, and its notice says which: only the command line gives another.

A resume first stops what is left of the session: its old holder, and any process that the agent started, with the memory they hold. Then it starts the agent in the same folder, with its own resume command. Claude Code and Codex pick up the same conversation, but not a turn that was running. The session keeps its ID, name and worktree.

Without `--memory-max`, the session gets the cap it asked for before, and `--memory-max auto` gives it the host's default. The size you give stays with the session for later resumes and restores. A size above what the host gives all its sessions is lowered to that. So when a session already had the default, `--memory-max` cannot give it more: give the host more memory ([Swap](#swap)), or stop other sessions.

## After an update of the helper

The first start after an update also puts the host's limit on all sessions together. If the running sessions already use more than that limit, the helper waits, so that it stops no session. A later start sets the limit once they use less, and until then `host doctor` says that it waits. Once the limit is in place, the running sessions share it too.

## When a session comes close to its cap

Before the cap stops a process, the host warns. The session then needs you, with a reason such as "close to its memory cap (2.7 of 2.9 GiB)". The launcher shows the reason under the list, and the session sends one `memory` [alert](alerts.md#what-is-sent).

The warning comes when two things hold:

- The memory that the kernel cannot free reaches 90 % of the session's cap. Without swap, that is the memory of the session's processes and its files in a tmpfs, such as `/tmp` on Debian 13. The kernel's own memory for the session counts too, such as page tables and stacks.
- The kernel has started to work to keep the session under its cap.

The page cache does not count. The kernel fills it up to the cap in normal work. It frees the cache when a process needs the room.

The cap can then stop a process at any time, often the agent. So:

1. Save your work, for example commit what the agent changed.
2. Stop what the session does not need, such as a dev server or a watcher, and delete large files that it wrote in a tmpfs. Or end the session.
3. If the session's cap is below what the host gives sessions, start the next session with a larger one ([for one session](#for-one-session)). Otherwise give the host more memory ([swap](#swap) says how).

The mark stays until that memory falls below 80 % of the cap. Only then can the host warn again. To clear the mark before that, attach to the session and type. While you are attached, the session's screen is the agent's own, so it never shows the warning. So keys that you type while you stay attached keep the mark, also across a reconnect after a laptop sleep or a dropped link. [The architecture](../explanation/architecture.md#37-a-session-ends) explains the levels and their sources.

The warning watches each session's own cap. When sessions share the host's limit, they can reach it before any one of them comes close to its own cap. Then no warning comes.

## When sessions share the memory

When all sessions together reach the host's limit, the kernel stops the biggest process of any agent, normally the one that grew. Before that, every session slows down, as the kernel takes back the memory they cache. To keep each session in its own share, give each one a `memory_max`, so that together they stay below the limit.

## Swap

A session's memory never goes to swap, so swap does not give sessions more room. Swap on the host still helps the system and your programs outside the sessions. To give sessions more memory, give the host more: resize the VM, then start the sessions again.

Hosts without a systemd user manager, such as a [Docker container](create-a-host.md), run sessions without a cap. So do hosts where systemd cannot cap memory, such as hosts with cgroup v1. The "memory caps" line of `host doctor` says so.
