# Manage session images

Microvm sandboxes boot from read-only ext4 disk images with a dm-verity Merkle tree, built from OCI images that your laptop pins by digest. The host never chooses which digest to trust, and no registry login ever leaves your laptop.

## The default image

The default session image is the official Debian `buildpack-deps:trixie-scm` image, which includes `git`, `curl` and CA certificates with no proprietary agents. simstim pins its OCI index digest and its per-architecture (`amd64` and `arm64`) dm-verity check values.

Pull it onto a host:

<!-- test: help (needs a host and registry access) -->
```sh
simstim image pull default my-server
```

## Pull and pin another OCI image

Pass any public OCI image reference to resolve its tag on your laptop, pin the digest in `~/.config/simstim/images.lock`, and convert it into the host's content-addressed store:

<!-- test: help (needs a host and registry access) -->
```sh
simstim image pull ghcr.io/acme/dev:1 my-server
```

On the first conversion for each architecture, `images.lock` records the dm-verity root hash as a check value. Pulling the same pinned digest onto a second host checks that it produces the same root hash.

If a tag later moves to a new digest, `image pull` stops with `image_moved` so you can check the new image first. Pass `--update` to move the pin in `images.lock`:

<!-- test: help (needs a host and registry access) -->
```sh
simstim image pull ghcr.io/acme/dev:1 my-server --update
```

## List and remove stored images

List the converted images on a host:

<!-- test: help (needs a host) -->
```sh
simstim image ls my-server
simstim image ls my-server --json
```

Remove a stored image by its full digest or an unambiguous prefix:

<!-- test: help (needs a host) -->
```sh
simstim image rm sha256:ab12cd34 my-server
```

`image rm` refuses while any session still uses that image. `simstim-host prune` removes unreferenced images older than 7 days while keeping the default image and any image referenced by a session.

## The agents disk

When `erofs-utils` (`mkfs.erofs`) is installed on the host, `agents install` rebuilds a read-only EROFS disk containing the host's installed agents, owned by uid and gid 1000, under `~/.local/share/simstim/images/agents/`. Microvm sandboxes mount this disk read-only at `/opt/agents`, so agents cannot modify their own binaries.
