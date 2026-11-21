# Open a shell beside an agent

Run `simstim shell box/auth-fix` to open the newest running shell in that sandbox. If none runs, it starts one in the same working directory. `--new` always starts another shell. `--name inspect` starts a named shell. `--detach` leaves it running; add `--json` for its session object.

The shell shares the sandbox's files and network policy. It uses the image's login shell, or the configured `sandbox.shell`. A shell session has its own terminal and ring. Removing it leaves the other sessions running. Stopping any member stops the sandbox and all its sessions.

At `none`, the command starts a host shell in the session's folder through the ordinary new-session request and its current local grants. It creates no wall and inherits no SSH signer or private source permission from a row.

Work written in a sandbox still reaches the laptop through `pull` and its review. Opening a shell does not make those files trusted on the laptop.
