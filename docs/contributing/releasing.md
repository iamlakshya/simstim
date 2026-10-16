# Versions and releases

## Versioning

Versions follow [Semantic Versioning](https://semver.org): `MAJOR.MINOR.PATCH`.

- **Before 1.0**, a minor version may break the CLI, the config or the wire protocol between the client and the host helper. The CHANGELOG says how to move over.
- **From 1.0**, a breaking change to the CLI, the config file, the key map or the wire protocol needs a new major version.
- **Client and helper.** A client installs the helper that matches it, on each host, side by side with older ones, so running sessions keep their own helper. The hello message carries a protocol version, and an older client says what to update.
- **Security fixes** go to the latest minor release only ([SECURITY.md](../../SECURITY.md)).

## What a release contains

Every release is built by `.github/workflows/release.yml` from a tag, with nothing cached:

| File | What it is |
| --- | --- |
| `<name>_<version>_<os>_<arch>.tar.gz` | The client, for macOS and Linux on amd64 and arm64 |
| `<name>-host_<version>_linux_<arch>.tar.gz` | The host helper |
| `checksums.txt` | SHA-256 of every archive |
| `checksums.txt.sig` | An SSH signature of the checksums, by the key in [.github/release-signers](../../.github/release-signers); `install.sh` checks it |
| `checksums.txt.sigstore.json` | A keyless cosign signature of the checksums (Sigstore bundle) |
| `*.sbom.json` | An SBOM for each archive |
| `multiple.intoto.jsonl` | SLSA level 3 provenance for every archive |
| GitHub attestations | Build provenance, checked with `gh attestation verify` |
| The Homebrew cask | Pushed to the tap when the `HOMEBREW_TAP_TOKEN` secret is set |

The builds are reproducible: `just repro` builds every binary twice from empty caches and checks they match, and CI runs it on every push.

## Steps to release

1. Make sure `main` is green in CI and the end-to-end tests pass (`just e2e`).
2. In `CHANGELOG.md`, rename `## [Unreleased]` to `## [X.Y.Z] - YYYY-MM-DD`, and add a new empty `## [Unreleased]` above it. The release notes are taken from this section (`scripts/release-notes.sh`), so write them for users.
3. Commit ("Release X.Y.Z") on a branch, open a pull request, and merge it when CI passes: `main` takes changes only through a pull request ([decision 0023](decisions/0023-protect-main.md)). Then tag the merged commit and push the tag:

   ```sh
   git switch main && git pull --ff-only
   git tag -s vX.Y.Z -m "X.Y.Z"   # or -a if you do not sign tags
   git push origin vX.Y.Z
   ```

4. The release workflow builds, signs and uploads a **draft** release. Check its files and notes, then publish it.
5. Check one archive by hand:

   ```sh
   ssh-keygen -Y verify -f .github/release-signers -I release -n file -s checksums.txt.sig < checksums.txt
   sha256sum -c --ignore-missing checksums.txt
   ```

## Checking a release

Anyone can check a release three ways: the SSH signature above, the cosign bundle (`cosign verify-blob --bundle checksums.txt.sigstore.json --certificate-identity-regexp 'https://github.com/.*/.github/workflows/release.yml@refs/tags/v.*' --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt`), or the GitHub attestation (`gh attestation verify <archive> --repo <owner>/<repo>`).

## Why one action is pinned by tag

Every action in the workflows is pinned to a full commit SHA, except the SLSA generator (`slsa-framework/slsa-github-generator`), which the release job calls by tag. The generator checks how it was called, and a provenance file made from a SHA reference does not verify. OpenSSF Scorecard counts this one reference against Pinned-Dependencies (9 of 10), which `just public-check` accepts.
