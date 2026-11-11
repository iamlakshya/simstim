# Paste an image into a remote agent

While attached, copy a screenshot, then press **Ctrl+V** (Alt+V on Windows). The client uploads the image to the host, into a private folder of the session's own, and pastes its path. Claude Code, Codex, opencode and the other agents attach an image when you paste its path.

- With no image on the clipboard, Ctrl+V goes to the agent as usual.
- Dropping a file on the terminal window uploads it the same way.
- A notice names each file that the client uploads, and its size, such as "uploaded ~/Desktop/shot.png (182.4 KiB)". A drop of several files gets one notice that names them all.
- Uploaded files are private to your user on the host, and deleted after a day.
- The key is a setting: `attach.image_paste_key` in [config.toml](../reference/config.md).

## Files that stay on your laptop

A drop reaches the client as a paste of file paths. So the paste guard checks each file before the upload. If it refuses one file, the client uploads nothing, sends the paste to the agent as the plain text of the paths, and shows a notice that says why.

The guard refuses these files:

- A file in one of these folders of your home folder: `~/.ssh`, `~/.aws`, `~/.config`, `~/.gnupg`, `~/.kube`, `~/.docker` and `~/Library/Keychains`.
- A file with one of these names: `.env`, `*.pem`, `*.key`, `id_*` and `credentials*`. Case does not matter.
- A file named `.env.` and more, such as `.env.local`, except `.env.example`, `.env.sample` and `.env.template`.
- A file whose path a session put on your clipboard in the last day.

The client checks a file by what it is, not by how its path is spelt. A link to a file in one of these folders counts, and so does a file in a folder that links to one of them. So does a private folder that is itself a link, as in a dotfiles set-up. After the check, the client reads the file that it checked, not the path again.

The last rule stops a trick. A session can write text to your laptop's clipboard with a terminal escape sequence, such as OSC 52. If it writes the path of a laptop file, and you then paste, the paste looks like a drop of that file.

So the client first tells a paste from a drag. A drag puts nothing on the clipboard. When the pasted text is the text on your clipboard, or on the primary selection on Linux, it is a paste, and the client uploads nothing, wherever the text came from. A notice says that a drag uploads the file. If the client can read neither, it relies on the check below alone.

The client reads every byte that goes to your terminal for clipboard writes, and changes none of them. It reads OSC 52, iTerm2's `Copy` and `CopyToClipboard`, and kitty's clipboard protocol. It keeps the SHA-256 of the last 256 writes for a day, never their text. Writes to the primary selection have a list of their own, so they never fill the clipboard's. It keeps them in one file with mode 0600, `paste-guard.json` in the client's state folder (`~/.local/state/simstim/` unless `XDG_STATE_HOME` is set). It notes each write in a journal beside that file before the session's output reaches your terminal. Every client process on the laptop shares the file. So a path that a session wrote in one terminal is refused in another terminal, and after a restart.

When the file holds 256 writes from the last day, the client refuses every drop until the oldest of them is a day old. If the client cannot read the file, it refuses the drop. If it could not note a write, it refuses every drop for a day.

The guard errs on the side of refusing. Terminals read some sequences in different ways, so it records every way that one of them might read a write. Now and then, it refuses a file that no session wrote. Then copy the file to the host yourself.

If the guard refused a file that you want the agent to have, copy the file to the host yourself.

The guard stops uploads. It does not stop a session from writing to your clipboard. To be asked before each write, change your terminal's setting, such as `clipboard-write = ask` in Ghostty.
