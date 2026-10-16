# Give a session more memory

Each session has a memory cap. It covers everything the session runs: the agent, its tools, its MCP servers and language servers, and your builds. When a session reaches its cap, the kernel stops its biggest process, normally the agent, and the session shows "out of memory".

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

## After an update of the helper

The first start after an update also puts the host's limit on all sessions together. If the running sessions already use more than that limit, the helper waits, so that it stops no session. A later start sets the limit once they use less, and until then `host doctor` says that it waits. Once the limit is in place, the running sessions share it too.

## When sessions share the memory

When all sessions together reach the host's limit, the kernel stops the biggest process of any agent, normally the one that grew. Before that, every session slows down, as the kernel takes back the memory they cache. To keep each session in its own share, give each one a `memory_max`, so that together they stay below the limit.

## Swap

A session's memory never goes to swap, so swap does not give sessions more room. Swap on the host still helps the system and your programs outside the sessions. To give sessions more memory, give the host more: resize the VM, then start the sessions again.

Hosts without a systemd user manager, such as a [Docker container](create-a-host.md), run sessions without a cap. So do hosts where systemd cannot cap memory, such as hosts with cgroup v1. The "memory caps" line of `host doctor` says so.
