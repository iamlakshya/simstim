---
status: proposed
date: 2026-11-28
---

# 0039 Deliver sandbox runtime files by host download or laptop copy against client-held digests

This record extends [0017](0017-release-signatures.md) and [0055](0055-lean-release-signing.md), and follows [0031](0031-the-egress-process-is-not-a-relay.md).

## Context

Sandboxes need runtime artefacts on each host that are too large or platform-specific to embed inside every client binary: the microVM VMM, the guest kernel, the tools image and `runsc`. Users run hosts in three kinds of network environment:

1. Cloud VMs and servers with fast direct HTTPS access to the project's release assets, where copying tens of megabytes through a laptop's uplink wastes time and bandwidth.
2. Firewalled, private or air-gapped hosts that can be reached over SSH from the laptop but cannot reach public release URLs directly.
3. Fleets of several hosts managed by one laptop, where a compromised host must never supply trusted runtime bytes or credentials to another host.

Because the client already carries the signed release's expected SHA-256 digests and exact byte sizes, integrity does not depend on which network path carries the bytes.

## Options

1. **Always copy from the laptop.** Works on firewalled hosts, but makes every `host add` and `host upgrade` on a cloud VM upload the full runtime over the laptop's uplink.
2. **Always download on the host.** Fast on cloud VMs, but fails on private hosts without outbound HTTPS.
3. **Copy from one host to another.** Avoids re-downloading on a fast cluster, but violates the trust boundary: a compromised host could serve tampered bytes or receive SSH credentials for peers.
4. **Offer `auto`, `host` and `laptop` delivery against client-held digests, with a content-addressed host cache.** In `auto` (the default), the host downloads over HTTPS when `sandbox.probe` reports the release is reachable and falls back once to a laptop copy on failure. `host` uses only direct host downloads; `laptop` always downloads into the laptop's cache and copies over the host transport. No host ever copies files from another host.

## Decision

Option 4.

- **Client-held digests are the sole authority.** Every `runtime.fetch` and `runtime.install` request carries the expected SHA-256 hex digest and exact byte size from the client's release manifest. The host enforces the byte-size ceiling while reading, checks the SHA-256 before moving the file, and deletes any part file that fails either check.
- **Three fetch modes via `[images] fetch` and `--images`.** `auto` (default), `host` and `laptop`. In `auto`, `host` is tried when the host's probe reports `release_reachable`; if the host download fails, the client falls back to `laptop` once and prints a notice. `host` mode polls `runtime.status` once a second for live byte progress.
- **Content-addressed host store with atomic placement.** Verified files land in `~/.local/share/<name>/artefacts/sha256/<hex>` with mode `0444` (`0555` for `vmm` and `gvisor` binaries) via a per-transfer incoming part file (`artefacts/incoming/<hex>.<random>.part`) and atomic link or rename. When two transfers race for the same digest, an existing verified file wins and the duplicate part file is removed.
- **Bounded cache retention.** Runtime labels live in `artefacts/runtime/<label>.json`. `prune` and `host doctor --fix` (`old-runtime`) keep files referenced by the two newest runtime labels, files or labels named by any non-deleted session manifest, and files less than one hour old.
- **Never copy host to host.** Runtime files come only from the release over HTTPS or from the laptop's `~/.cache/<name>/runtime/sha256/` cache over the existing transport.

## Consequences

- Good, because cloud hosts download runtime files directly at full speed while private or offline hosts still work automatically through the laptop fallback or `--images laptop`.
- Good, because a compromised mirror or interrupted transfer cannot plant a wrong or oversized file into the host store, and concurrent installs of the same file do not clobber one another.
- Good, because no host ever trusts another host or receives another host's SSH keys.
- Bad, because `auto` mode makes one outbound HTTPS check and download attempt from the host unless the user sets `[images] fetch = "laptop"` or passes `--images laptop`.
