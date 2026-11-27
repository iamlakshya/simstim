---
title: Restart a host
---

# Restart a host

Drain a host before planned maintenance. The mark stops new sandboxes. Running sessions keep their current processes and runtime files.

```sh
simstim host set my-vps drain='kernel update'
simstim host ls
simstim ls my-vps
```

Wait for the sessions to finish, or stop the sessions you own. Then restart the host using your usual administration tools. Ask its owner before restarting a machine that belongs to someone else.

After it returns, check its health and clear the mark:

```sh
simstim host doctor my-vps
simstim host set my-vps drain=off
```

The drain mark survives a restart. Automatic restore ignores this user setting when it brings eligible sessions back. A host removal has its own drain and still blocks restore. Clearing the user setting cannot cancel a removal.

`host ls`, `host doctor` and the launcher show health observations. These describe what the host reports. A failed reading stays unavailable. Starting a sandbox still checks the current host under its placement lock.

New sandboxes use the runtime pinned by the client. An `own` or `throwaway` host can receive missing runtime files on use. A `shared` host asks first. Existing sandboxes keep their runtime. An `outdated` or `exposed` mark means you should end the session when it is idle and start a new one with the current client.
