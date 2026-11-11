# Versions and releases

## Versioning

Versions follow [Semantic Versioning](https://semver.org): `MAJOR.MINOR.PATCH`.

- **Before 1.0**, a minor version may break the CLI, the config or the wire protocol between the client and the host helper. The CHANGELOG says how to move over.
- **From 1.0**, a breaking change to the CLI, the config file, the key map or the wire protocol needs a new major version.
- **Client and helper.** A client installs the helper that matches it, on each host, side by side with older ones, so running sessions keep their own helper. The hello message carries a protocol version, and an older client says what to update.
- **Security fixes** go to the latest minor release only ([SECURITY.md](../../SECURITY.md)).

## What a release contains

Every release is built by `.github/workflows/release.yml` from a tag that a maintainer signed ([the signed-tag gate](#the-signed-tag-gate)), with nothing cached:

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
2. Make sure that `.github/tag-signers` on `main` lists your tag key ([set up your tag key](#set-up-your-tag-key)).
3. In `CHANGELOG.md`, rename `## [Unreleased]` to `## [X.Y.Z] - YYYY-MM-DD`, and add a new empty `## [Unreleased]` above it. The release notes are taken from this section (`scripts/release-notes.sh`), so write them for users.
4. Commit ("Release X.Y.Z") on a branch, open a pull request, and merge it when CI passes: `main` takes changes only through a pull request ([decision 0023](decisions/0023-protect-main.md)). Then sign a tag on the merged commit with your tag key, check it, and push it. Touch the key when it asks:

   ```sh
   git switch main && git pull --ff-only
   git tag -s vX.Y.Z -m "X.Y.Z"
   git -c gpg.ssh.allowedSignersFile=.github/tag-signers verify-tag vX.Y.Z
   git push origin vX.Y.Z
   ```

5. The release workflow checks the tag, then builds, signs and uploads a **draft** release. Check its files and notes, then publish it.
6. Check one archive by hand:

   ```sh
   ssh-keygen -Y verify -f .github/release-signers -I release -n file -s checksums.txt.sig < checksums.txt
   sha256sum -c --ignore-missing checksums.txt
   ```

## The signed-tag gate

The release job builds only from a tag that a maintainer signed with a key from the list on `main` ([decision 0041](decisions/0041-a-signed-tag-release-gate.md)). Right after the checkout, the job fetches `main` and runs `main`'s copy of `scripts/verify-tag.sh`, before any build step. It never runs the tagged commit's copy, so a tag cannot bring its own gate. Any failure stops the job there. Both fetches use no token, on purpose. This repository is public, and a token in the gate would be one more secret that a tag's run can reach. A private fork must give the gate's fetch a read-only token, and remove it before the build. The script does these checks, in this order:

1. The tag's name does not start with `-`, and the runner has git 2.34 or later and OpenSSH 8.8 or later.
2. It fetches `main` and the tag again.
3. The tag is an annotated tag, not a lightweight one.
4. It reads `.github/tag-signers` from `main`, never from the tag, so a tag cannot bring its own key. Each line that is not a comment must be a hardware key with `namespaces="git"`.
5. The tag's last signature block is an SSH signature, and `git verify-tag` finds a good signature by a key in that file.
6. The signed tag was made for this name, not for an older release.
7. The tag names a commit, the same commit that the job checked out, and that commit is on `main`.

The checkout is pinned at v6.0.2 or later, as [spec 20.7](isolation-spec.md#207-a-signed-tag-release-gate-p44) asks. The gate fetches the tag again, so it does not rely on the checkout's copy.

GoReleaser then builds the tag that the gate checked. The job sets `GORELEASER_CURRENT_TAG` to the pushed tag. Without it, GoReleaser picks the highest version among the tags on the commit, which may be another, unsigned tag.

`scripts/test-verify-tag.sh` tests each case in a temporary repository, with throwaway keys. It runs the gate's step as `release.yml` has it. `just test` and CI's `test` job run it. Never push a tag to test the gate.

### Set up your tag key

Each maintainer signs release tags with a hardware key, which needs a touch for each signature. The release job needs only the public key. You need OpenSSH 8.8 or later and git 2.34 or later. The steps use a key file of its own, `~/.ssh/id_ed25519_sk_tags`, so that they do not overwrite a key you have.

1. On macOS, install OpenSSH from Homebrew. Apple's OpenSSH has no built-in support for hardware keys, so it cannot make or use this key:

   ```sh
   brew install openssh
   ```

2. Plug in the key, and make an SSH key on it. On macOS, use Homebrew's `ssh-keygen`:

   ```sh
   "$(brew --prefix)/bin/ssh-keygen" -t ed25519-sk -C "release tags" -f ~/.ssh/id_ed25519_sk_tags
   ```

   On Linux, run `ssh-keygen -t ed25519-sk -C "release tags" -f ~/.ssh/id_ed25519_sk_tags`.

3. Tell git, in your checkout, to sign with it. `user.signingkey` names the key file, not the `.pub` file, so that signing needs no `ssh-agent`. The key file of a hardware key is only a handle: the secret stays on the key.

   ```sh
   git config gpg.format ssh
   git config user.signingkey ~/.ssh/id_ed25519_sk_tags
   ```

4. On macOS, tell git to sign and check with Homebrew's `ssh-keygen`:

   ```sh
   git config gpg.ssh.program "$(brew --prefix)/bin/ssh-keygen"
   ```

5. Add the key's line to `.github/tag-signers`:

   ```sh
   echo "you@example.com namespaces=\"git\" $(cut -d' ' -f1,2 ~/.ssh/id_ed25519_sk_tags.pub)" >> .github/tag-signers
   ```

6. For the first key, delete the comment "No key is listed yet" and the line after it from the file.
7. Open a pull request to `main` with the file.

A key counts only after its pull request merges. To retire a key, remove its line the same way.

### The settings the gate needs

On a tag push, the release job checks out the tagged commit. The workflow file probably comes from that commit too (UNVERIFIED: GitHub's docs do not say so in these words). This leaves two ways around the gate:

- **A tag on a commit that changes `release.yml`.** The gate's script comes from `main`, so a change to the script alone changes nothing. A change to `release.yml` can skip the gate.
- **A tag on a commit from before the gate,** such as the commit of 0.1.2. That commit's `release.yml` has no gate, and it reads the release key. The tag can be lightweight or unsigned.

The gate needs three repository settings, which the owner sets once. The gate does not check them:

- **A tag ruleset** on `refs/tags/v*` that lets only the owner create, move or delete those tags.
- **Branch protection on `main`**, so that commits reach it only through reviewed pull requests ([decision 0023](decisions/0023-protect-main.md)).
- **The `release` environment**, which holds the release secrets. Only the gated release job names it, so a workflow from before the gate reads an empty release key.

With all three, only the owner's account can push a release tag, and only reviewed commits reach `main`. The check that the tagged commit is on `main` catches a tag on an unreviewed branch by mistake. It does not stop a person who can change the workflow in the tagged commit.

A tag on a commit from before the gate still runs. It makes a draft release with the job's own token, and cosign signs its checksums with the workflow's identity. Then the step that signs with the release key fails, so the draft has no `checksums.txt.sig`. `install.sh` and the client's download of helpers refuse it. The cosign check below accepts it. Delete any draft release that you did not start.

### Set up the release environment

Until you do this, the gated job reads the release key from the repository's secrets, and so can a workflow from before the gate. A stolen token could push a branch made from a commit before the gate, change only Go code, and tag it. That commit's `release.yml` would then sign the build with the release key. Only a key that old workflows cannot read closes this.

The release key exists only as the secret `RELEASE_SSH_KEY` ([decision 0017](decisions/0017-release-signatures.md)), and GitHub never shows a secret again. So the move is a rotation to a new key. It has two steps, so that `install.sh` and the client can always check the latest release.

**Step A: list both keys, and release with the new one.**

1. In the repository's **Settings**, open **Environments**, and make an environment named `release`.
2. Under **Deployment branches and tags**, choose **Selected branches and tags**, and add a tag rule for `v*`.
3. Make the new release key in the git-ignored `private/` folder. The private key never enters the checkout's tracked tree:

   ```sh
   mkdir -p private
   ssh-keygen -t ed25519 -N '' -C release -f private/release-key
   ```

4. Store it as the `release` environment's secret `RELEASE_SSH_KEY`, and delete the private key at once, before any pull request. The `&&` deletes it only when the upload worked. If the upload fails, the key stays: run the command again.

   ```sh
   gh secret set RELEASE_SSH_KEY --env release < private/release-key && rm private/release-key
   ```

5. Delete `RELEASE_SSH_KEY` from the repository's secrets, under **Settings**, then **Secrets and variables**, then **Actions**. A workflow from before the gate then reads an empty key.
6. In one pull request, add the new public key from `private/release-key.pub` as a second `release` line, beside the old one. It goes in `.github/release-signers`, in `RELEASE_SIGNERS` in `install.sh`, and in `ReleaseSigners` in `internal/client/helpers/fetch.go`. A test keeps the three lists equal. Merge it. A release before this merges fails safely, because the job checks its signature against `.github/release-signers`.
7. Publish the next release as usual. The environment's key signs it. Both keys are listed, so `install.sh` and the client accept the older releases and the new one. Say in its notes that a new key signs it, as 0017 asks.
8. When the tap repository exists, add `HOMEBREW_TAP_TOKEN` to the `release` environment, never to the repository.

**Step B: drop the old key.** After that release is published, a later pull request removes the old key's line from the same three places. Then delete `private/release-key.pub`. From then on, `install.sh` accepts only releases signed by the new key. A release from before the rotation needs the old key, as 0017 says.

### When the gate fails

Each failure prints a line that starts with `release:`, and a `Try:` line. A hint that tags again names the commit that the tag names now. It quotes the tag for the shell, because a tag name may hold `;`, `$(…)`, quotes or a leading `-`.

| The failure | What to do |
| --- | --- |
| `vX.Y.Z is a lightweight tag, and a release needs a signed annotated tag.` | Sign the tag again with `git tag -s -f -m vX.Y.Z -- vX.Y.Z <commit>`, then push it again with `git push --force origin refs/tags/vX.Y.Z` |
| `vX.Y.Z is not signed by a key in .github/tag-signers on main.` | Sign it with your key from that file, as for a lightweight tag. If your key is new, add it to the file through a pull request first |
| `line N of .github/tag-signers on main is not a hardware key with namespaces="git".` | Fix the line through a pull request |
| `vX.Y.Z is a signed tag made for vA.B.C, under another name.` | If you did not push it, delete it and find out who did. Otherwise sign it again, as for a lightweight tag |
| `vX.Y.Z does not name a commit.` | Tag a commit that is on `main` |
| `the commit of vX.Y.Z is not on main.` | Tag a commit that is on `main` |
| `vX.Y.Z now names another commit than the one this job checked out.` | The tag moved while the job ran. The push that moved it starts its own release run, which checks the tag again. Use that run |
| `the runner has OpenSSH 8.7, and the check needs 8.8 or later.` (or git older than 2.34) | Pin a newer runner image in `release.yml` |
| `main has no .github/tag-signers, so no key may sign a release.` | Add the file to `main` through a pull request, with the line of your tag key |
| `cannot read scripts/verify-tag.sh from main. The gate fetches with no token, so it needs a public repository.` | Run the release job again. If it fails again, check that `main` has the script. A private fork must give the gate's fetch a read-only token, and remove it before the build |
| `cannot fetch main and vX.Y.Z from origin. The gate fetches with no token, so it needs a public repository.` | Run the release job again. If it fails again, check that the tag still exists. In a private fork, give the fetch a read-only token, as above |
| `-v1 starts with "-", and git tag cannot make a tag with that name.` | Delete it by its full ref, with `git push --delete origin refs/tags/-v1` and `git update-ref -d refs/tags/-v1`, then choose a release tag name that starts with `v` |
| `cannot make a temporary folder in <folder>.` | Run the release job again |
| `verify-tag.sh needs one argument, the tag to check.` | Pass the tag. `release.yml` passes it from the push |

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
