---
status: proposed
date: 2026-11-28
---

# 0055 Keep SSH checksum signatures, SBOMs and GitHub attestations; drop cosign keyless signing and the SLSA generator

This record replaces [0017](0017-release-signatures.md). It follows [0053](0053-rebuild-with-open-source-parts.md) and keeps [0041](0041-a-signed-tag-release-gate.md)'s signed-tag gate unchanged.

## Context

Each release currently produces four verification paths:

1. An SSH signature (`checksums.txt.sig`) made with `RELEASE_SSH_KEY`, checked by `install.sh` and `internal/client/helpers` with `ssh-keygen -Y verify`.
2. A keyless cosign bundle (`checksums.txt.sigstore.json`) made by GoReleaser's `signs` block using `sigstore/cosign-installer`.
3. GitHub build-provenance attestations (`actions/attest-build-provenance`) over `dist/checksums.txt`, which are themselves Sigstore bundles recorded in Rekor.
4. A separate `provenance` job calling `slsa-framework/slsa-github-generator`, plus a follow-up `notes` job needed only because the generator overwrote the draft release body.

Two of those four paths fail the project's rules:

- `slsa-framework/slsa-github-generator` is no longer actively maintained (last release v2.1.0, February 2025; its own notice points to GitHub artifact attestations). It also required calling the reusable workflow by version tag rather than commit SHA, which left one unpinned action exception in `.github/workflows/release.yml`.
- Keyless cosign signing on `checksums.txt` duplicates GitHub build-provenance attestations: both bind the release archives to the GitHub Actions workflow identity through Sigstore and Rekor.

Meanwhile, the client embeds the host helpers directly (and caches verified helper bundles when built with `go install`), so it already knows the exact SHA-256 digest of each helper binary it will run.

## Options

1. **Keep all four paths.** Retains an unmaintained workflow dependency, an unpinned tag reference, an extra `notes` repair job, and duplicate Sigstore bundles.
2. **Drop the SSH signature too, keeping only GitHub attestations.** Breaks `install.sh` and `go install` helper fetches on clean macOS and Linux machines that have OpenSSH but not `gh`.
3. **Keep the lean set:** the signed-tag gate (0041), GoReleaser, the SSH signature (`checksums.txt.sig`), Syft SBOMs, and GitHub artifact attestations (`actions/attest-build-provenance`), and turn on GitHub immutable releases. Check the installed host helper's SHA-256 on the host before starting the bridge.

## Decision

Option 3.

- **Signed-tag gate (0041) stays first.** No build step runs until `main`'s copy of `scripts/verify-tag.sh` accepts a hardware-key signed annotated tag on `main`.
- **SSH signature on `checksums.txt` stays.** `install.sh` and `internal/client/helpers/fetch.go` verify `checksums.txt.sig` with `ssh-keygen -Y verify` and fail closed with nothing extra to install. The key lives only in the `release` environment.
- **SBOMs and GitHub attestations stay.** GoReleaser generates CycloneDX/SPDX SBOMs via Syft, and `actions/attest-build-provenance` attests `dist/checksums.txt` in a single release job. Anyone who wants Sigstore and Rekor verification checks the release with `gh attestation verify` or `gh release verify-asset`.
- **Drop `slsa-framework/slsa-github-generator` and cosign keyless signing.** Remove the `provenance` and `notes` jobs and `sigstore/cosign-installer` from `.github/workflows/release.yml`, and remove the `signs:` block from `.goreleaser.yaml`. Pass `LINK_BASE` directly when generating release notes before GoReleaser runs. Every workflow action is now pinned by full commit SHA.
- **Turn on GitHub immutable releases.** Once enabled by the repository owner, published release tags and assets cannot be edited after publication.
- **Check the host helper's SHA-256 before the bridge starts.** When the client embeds host helpers (or has loaded its verified release cache), `dial` checks the helper on the host with `sha256sum -c` (falling back to `shasum -a 256 -c`) before `exec`ing the bridge, and stops with `digest_mismatch` if it fails. `Probe` runs the same check so `host add` detects a changed helper and reinstalls it. A separate `release-manifest.json` waits until non-embedded runtime items (VMM, kernel, tools image, `runsc`) ship.

## Consequences

- Good, because the release pipeline drops one unmaintained dependency, removes the only tag-pinned workflow exception, eliminates the `provenance` and `notes` jobs, and stops shipping duplicate Sigstore signatures.
- Good, because clean machines still verify releases with OpenSSH alone (`checksums.txt.sig`), while users who want Sigstore provenance use maintained GitHub attestations.
- Good, because the client refuses to execute a corrupted or casually altered host helper on every dial, with one round-trip and no extra tooling on the host beyond `sha256sum` or `shasum`.
- Bad, because OpenSSF Scorecard's `Signed-Releases` check looks for a `*.intoto.jsonl` release asset rather than GitHub's attestations API, so that single Scorecard sub-score may drop.
- Bad, because the pre-bridge SHA-256 check runs inside the remote shell under the user's login account: it catches corruption and casual edits, but a compromised login account on the host can still race the check or replace the shell.
