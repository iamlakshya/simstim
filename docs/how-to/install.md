# Install

## With the installer (macOS and Linux)

<!-- test: skip (needs a published release; scripts/test-install.sh tests the installer) -->
```sh
curl -fsSL https://raw.githubusercontent.com/iamlakshya/simstim/main/install.sh | sh
```

It downloads the release for your system, checks its signature with `ssh-keygen` and its SHA-256, and installs into `~/.local/bin`. It changes nothing else. Options, as environment variables:

| Variable | Does |
| --- | --- |
| `INSTALL_DIR` | Install somewhere else |
| `VERSION` | A release tag, such as `v0.1.0`, instead of the latest |

## With Go

<!-- test: skip (installs into your GOBIN) -->
```sh
go install github.com/iamlakshya/simstim@latest
```

A client built this way has no host helpers inside it. The first time it connects a host, it downloads the helper bundle of its own release, checks the release's signature with `ssh-keygen` and the bundle's SHA-256, and keeps it in `~/.cache/simstim/helpers/`. Set `SIMSTIM_RELEASES_URL` to fetch from a mirror instead.

## With Homebrew

Each release builds a Homebrew cask, but the tap is not published yet. Until it is, use the installer or Go.

## From source

```sh
git clone https://github.com/iamlakshya/simstim
cd simstim
scripts/setup.sh
just build
```

The client is `bin/simstim`.

## Check a release by hand

[Releasing](../contributing/releasing.md#checking-a-release) shows the three ways: the SSH signature, the cosign bundle and the GitHub attestation.

## Update

Run the installer again. The next time the client connects to a host, it installs the matching helper beside the old one; sessions that are running keep the helper they started with.
