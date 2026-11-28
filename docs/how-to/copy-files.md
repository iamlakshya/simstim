# Copy files in and out of a session

Copy a file or a folder between your laptop and a session's workspace over SFTP (`github.com/pkg/sftp`), without setting up a separate server:

<!-- test: help (needs a running session) -->
```sh
simstim cp my-server/auth-fix:notes.md .
simstim cp ./data.csv my-server/auth-fix:data.csv
```

A relative path on the remote side starts in the session's workspace. Use `-f` to replace existing files at the destination.

## Safety checks

- **Laptop writes stay inside `os.Root`.** Symbolic links in the destination path or inside the session are never followed, and files in `.git` folders are refused.
- **No tar extraction.** Every file streams directly over SFTP with a default cap of 256 MiB and 10,000 entries.
- **Flagged files stop for review.** Files that can run code on your laptop or in CI (such as `.envrc`, `.githooks/**`, `.github/workflows/**`, or files with hidden Unicode characters) stop before landing unless you confirm at a terminal or pass `--allow-flagged`. A `.gitmodules` file with a carriage return is always refused.
- **Private laptop files stay put.** Uploading files from private folders (`~/.ssh`, `~/.aws`, `~/.gnupg`, `~/.kube`, `~/.docker`, `~/.config`) or private names (`.env`, `*.pem`, `*.key`, `id_*`, `credentials*`) is refused.
- **Modes do not cross.** Files copied out arrive with mode `0644` and folders with `0755`.

For work in git, use [`pull`](pull.md) to bring commits back as a verified git bundle. For editors, `scp`, `sftp` or `rsync`, see [editors over SSH](editors.md).
