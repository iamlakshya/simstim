# Copy files in and out

Copy a file from a running sandbox to this computer:

```sh
simstim cp box/work:~/notes.md .
```

`~/` on the sandbox means `/home/agent/`. Sandbox paths may also start with `/work`, `/tmp` or `/mnt`.

Copy a file or folder into the sandbox:

```sh
simstim cp ./data box/work:/work/project/data
simstim cp ./notes.md box/work:~/
```

A trailing slash selects a destination folder. A folder at a new destination keeps its files and empty subfolders. On this computer, an existing destination folder receives the source's basename.

One command copies at most `sandbox.cp_max` (256 MiB by default), with at most 10,000 entries. Choose a smaller folder when its listing is too large. Laptop links, special files, dot-dot paths and Git metadata stop a copy. Links and special files listed in the sandbox are skipped and named.

An existing file needs `-f` or `--force` to replace it. Returned files use mode 0644, and folders use 0755. Incoming file bytes stay anonymous in the Linux guest until publication, so an interrupted receiver leaves no temporary filename there.

Files that can change a command, an agent's settings or CI need a review. Read the listed files in the sandbox first. A live terminal can confirm the copy, or an explicit choice can use:

```sh
simstim cp box/work:/work/project/output ./review --allow-flagged
```

The client also checks received text for hidden characters and the session's locally available secrets before putting files at their final names. Use the computer that owns the session's original local approval and keep that policy record. The local record must include the original operation's acknowledgement. A missing or older copy approval stops the command before local file bytes are read.

A stopped sandbox needs a resume before copying. For an unwalled session, use `scp` for that host's own files. Use [pull](pull.md) to bring back work with its Git history.
