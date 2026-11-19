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

Anyone can check a release with the SSH signature above, the cosign bundle, or the GitHub attestation. Use files from the same selected release throughout.

### Cosign bundle

Install [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) and the GitHub CLI (`gh`). In a fresh directory, select a tag and archive from this repository's release assets. This example selects the Linux amd64 client for v0.1.0:

```sh
tag='v0.1.0'
archive='simstim_0.1.0_linux_amd64.tar.gz'
gh release download "$tag" --repo iamlakshya/simstim \
  --pattern "$archive" \
  --pattern checksums.txt \
  --pattern checksums.txt.sigstore.json

cosign verify-blob checksums.txt \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity "https://github.com/iamlakshya/simstim/.github/workflows/release.yml@refs/tags/$tag" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Change `tag` to the exact release tag, including `v`, and `archive` to that release's exact asset name for your OS and architecture. Keep `iamlakshya/simstim` and `.github/workflows/release.yml` unchanged in both the download and expected identity. Do not copy the expected identity from an untrusted bundle or replace it with a wildcard: that could accept another repository's signature.

The exact identity binds the signer to this repository, this workflow and the selected tag. The [Sigstore CI quickstart](https://docs.sigstore.dev/quickstart/quickstart-ci/#verifying-a-blob) documents the GitHub Actions workflow identity and issuer; [cosign verification](https://docs.sigstore.dev/cosign/verifying/verify/) documents verifying a blob with a bundle and expected identity. Here the workflow runs on a tag, so its identity ends in `@refs/tags/` followed by that tag, not a branch reference.

Only after cosign succeeds, check the selected archive against the signed checksums. On Linux:

```sh
awk -v file="$archive" '$2 == file' checksums.txt | sha256sum -c -
```

On macOS:

```sh
awk -v file="$archive" '$2 == file' checksums.txt | shasum -a 256 -c -
```

Require an `OK` result for the selected archive before using it. A valid signature on the checksums alone does not check the archive's bytes.

### GitHub attestation

Use an up-to-date [GitHub CLI](https://cli.github.com/manual/gh_attestation_verify) and an authenticated session (`gh auth login`). If `gh attestation --help` is not available, update the CLI first. With the selected archive downloaded and `archive` set as above:

```sh
gh attestation verify "$archive" --repo iamlakshya/simstim
```

## Advisories for pins outside package ecosystems

Dependabot and govulncheck watch the Go modules, the npm packages and the actions. Some pins have no package ecosystem, so no scanner matches an advisory to them. The Ghostty commit of the shadow terminal is one: its advisories have free-text commit ranges, and they are in neither OSV nor GitHub's advisory database ([build facts](../research/build-facts.md#61-ghostty-and-its-advisories)).

The pins watch (`.github/workflows/pins-watch.yml`) checks these pins every day. It also runs on demand, and on pull requests that change a pin, the watch's script, its tests, its verdicts or its workflow. [Spec 19.5](isolation-spec.md#195-the-watch-job-for-pins-p09) has its rules.

| Pin | Where it is |
| --- | --- |
| The Ghostty commit | The URL of the `ghostty` dependency in `vt/build.zig.zon`, in the form `…/archive/<commit>.tar.gz` |
| Each guest pin | A `[[pin]]` entry of `guest/supply.lock`, with its name, repository, tag and commit |

For each pin on GitHub, `scripts/pins-watch.py`, with its code in `scripts/pinswatch/`, first checks that the pin's commit is in its repository. Then it reads the repository's own advisories. When a range names commits, the script asks GitHub's compare API whether the pin holds each fix. When a range names release versions, the script finds the release tags, and asks whether the pin holds a patched release. It also asks OSV about every pin by its commit. A pin with `watch = "osv"` gets the OSV check only. The daily run also runs govulncheck for the Go toolchain of `go.mod` and the modules.

The job fails in each of these cases:

- **A pin is in an advisory's range.** The pin lacks the advisory's fix, or a patched release. Move the pin past the fix: `vt/README.md` says how for Ghostty. If the advisory does not apply to the way the project uses the pin, record a verdict instead.
- **An advisory's range cannot be read.** The range is outside the grammar of spec 19.5, it holds a character that is not printable ASCII, or it names a commit or a tag that the repository does not have. Or the pin is older than the first affected release, and no patched release reaches it. Check the advisory by hand against the pin's commit, then record a verdict.
- **A pin's commit or ref is wrong.** The commit is not in the pin's repository, or the `ref` of a `guest/supply.lock` pin names another commit. Correct the pin.
- **A file is bad.** `vt/build.zig.zon` has no Ghostty commit in the expected form, or `guest/supply.lock` or `scripts/pins-watch-reviewed.toml` breaks its rules. Or one of these files is not UTF-8 text. The message names the problem.
- **An API call fails.** The job never passes on a partial read. Run it again later.
- **govulncheck finds a vulnerability** in the Go toolchain or a module, on the daily run. Update the toolchain or the module, as for the same finding in CI.

On the daily run, a separate job opens an issue for each finding and for each advisory that cannot be read. It skips a problem when one of its own open issues already has the same title. It counts only issues that `github-actions[bot]` opened with the label `dependencies`, so an issue that someone else opens cannot silence a problem. A finding's title is `pins-watch: <pin> is in <advisory>`. An unread advisory's title is `pins-watch: <pin> cannot be checked against <advisory>`. Both get the labels `dependencies` and `needs triage`. Only this job may write to the repository's issues.

A run on your machine needs no token. It makes about 20 calls to GitHub, and GitHub allows 60 calls an hour without a token. If you hit that limit, wait for the hour to end, or set `GITHUB_TOKEN` to a fine-grained token with no permissions. The script sends it to `api.github.com` only, and it follows no redirect to another host.

```sh
python3 scripts/pins-watch.py
```

### Verdicts

A verdict records what a person found when they checked one advisory against one pin commit. It goes in `scripts/pins-watch-reviewed.toml`:

```toml
[[verdict]]
advisory = "GHSA-3r5j-c6rj-xq2v"
pin = "ghostty"
commit = "<the pin's 40-character commit>"
verdict = "fixed"     # "fixed" or "not-affected"
why = "The pin holds 44c4c8f5."
```

A verdict silences the advisory for that commit only. When the pin moves, the watch checks the new commit again, and each advisory needs a new verdict if it still fails. A verdict may name the advisory by its GHSA ID, by its CVE ID, or by an alias that OSV gives. Say in `why` what you checked, so that a reviewer can check it too.

### The time to answer an advisory

A release ships within 7 days of an advisory's publication. It carries the advisory's entry in the release manifest's list of known-bad versions, and a fixed pin where one exists ([spec 20.9](isolation-spec.md#209-known-bad-versions-p46)). The rule covers an advisory of severity high or critical (CVSS 7.0 or more), and any advisory whose range holds a pin of the current release. The 7 days are the project's own choice ([question 42](isolation-spec.md#15-open-questions)). The nearest outside norm is the linux-distros list, whose members release a fix within 10 days of a public issue.

## Why one action is pinned by tag

Every action in the workflows is pinned to a full commit SHA, except the SLSA generator (`slsa-framework/slsa-github-generator`), which the release job calls by tag. The generator checks how it was called, and a provenance file made from a SHA reference does not verify. OpenSSF Scorecard counts this one reference against Pinned-Dependencies (9 of 10), which `just public-check` accepts.
