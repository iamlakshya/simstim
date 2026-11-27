# Review a host's root steps

Some sandbox features need a change that only the host's owner can make. Review the exact script before running it:

```sh
simstim host prepare box --root-step print
```

The command first probes the saved host and delivers the runtime files pinned by this client. It then lists the needed steps. Each step has a reason, the change and its undo. A step that affects other accounts is marked as machine wide. The output includes the script and its SHA-256.

`print` writes a new review file with mode 0600. It does not overwrite an existing file or run the script. A failed write may leave a partial review file. Inspect and move that file before trying again.

After the root phase, preparation boots a fixed test sandbox and waits for its cleanup. Only a successful boot and cleanup can save the prepared cache and wall floor. A stronger existing floor is kept. A failed boot, or a host with no runnable wall, leaves both unchanged.

Use `--images auto`, `--images host` or `--images laptop` to choose where runtime and image bytes come from. The default follows `[images] fetch`. Each phase uses its own connection to the same saved host. If that host is removed or replaced, start preparation again.

## Choose what happens

| Option | Behaviour |
| --- | --- |
| `--root-step print` | Write the script for review |
| `--root-step skip` | Check the host and record that you skipped its root steps |
| `--root-step run` | Ask for the required permission, then run only the reviewed steps |
| `--delegate-io` | Include optional disk I/O delegation when the host needs it |

Without `--root-step`, `--yes` selects `print`. The default also selects `print` for a shared host or a host with a recorded sandbox start. Otherwise an own or throwaway host can offer to run the script at a terminal.

To select a run explicitly:

```sh
simstim host prepare box --root-step run
```

The shared-host permission check requires its typed name. `--yes` does not answer that check. An unattended run also needs freshly checked passwordless sudo. A failed permission or identity check sends no root script.

## Inspect the result

The client records an attempt before starting a root command. It keeps the same checked host and waits for that command to end. An interrupted or uncertain attempt remains recorded so that a later command cannot treat it as a first run.

The client checks the host again after a run. A successful command alone does not prove that a step worked. The fresh probe decides whether the required change is present. Disk I/O delegation can also require a user-manager reload, which the reviewed operation performs before that final check.

Root steps are separate from ordinary helper updates. Review a new script when a helper needs a different namespace launcher or the host's support changes. See [sandbox walls](sandboxes.md) for the current checks.
