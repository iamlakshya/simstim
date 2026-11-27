# Save a sandbox image

Save tools you installed in a running microvm, then start another session from that image on the same host.

```sh
simstim image save devbox/setup tools
simstim new devbox --image tools
```

The image name starts with a lower-case letter or digit. It can contain lower-case letters, digits, dots, underscores and dashes, up to 64 characters.

Before saving, the client reads the session's current configured secrets on your computer. Their values travel only to the original host for the scan. They are kept in memory. A process or gVisor sandbox cannot save an image. Resume a stopped microvm before saving it.

The review lists paths left out and changes that can run code at login or start. These include shell start files, Git settings, services, scheduled commands, files on the original runtime's `PATH`, and files with the set-user-ID or set-group-ID bit. Read flagged files through `simstim shell devbox/setup`. Confirm at the terminal, or pass `--allow-flagged` when you intend to keep them.

```sh
simstim image save devbox/setup tools --allow-flagged
```

The root's files are scanned for the session's known secret values, sentinels and decoys, including standard base64 forms. A match stops the save with `image_secret` and keeps no image. A filename that contains a known value is withheld from the diagnostic too. Remove the value from the sandbox before trying again.

The image leaves out workspaces, mounts, temporary files, runtime files, agent state, kept caches, every agent's known login files, generated package settings and decoys. Private credential paths beneath the agent's home and `/root` stay out too. The review shows the exclusions. New sessions apply their own cold-start settings.

The guest supplies the list of changes. It can omit a path, including a change made while the root is being read. The scan and exclusions check the whole root stream independently. A saved image from a hostile sandbox remains hostile.

A login made inside the sandbox stays out at its known login path. A copy at another path can enter the image if its value is unknown to the scan. Treat saved images as private to their host.

The image stays on the host that made it. Its pin in `images.lock` is tied to that host's identity. The launcher marks it as made there. The client refuses to use it on another host. To use the same tools elsewhere, build a template on your computer.

Use `--replace` to give an existing name on this host to a new image. Existing sessions keep their pinned root. A prepared image that you do not keep expires after one hour. After a helper or host restart, the next image operation completes cleanup of expired preparations. Changed or unknown files stay for inspection.

```sh
simstim image save devbox/setup tools --replace
```
