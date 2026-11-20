---
status: proposed
date: 2026-11-11
---

# 0041 A release builds only from a tag that a maintainer signed with a hardware key

This record extends [0017](0017-release-signatures.md). The release key of 0017 still signs each release. This record adds a second key, which signs the tag that starts a release. It also moves the release key into an environment that only the gated release job can read.

## Context

Today, any tag that starts with `v` starts the release job. The job builds, signs the checksums with the release key, signs them again with cosign, and adds provenance. Each of these proves that the job ran. None of them proves that a maintainer chose to release that commit. So a person with a token that can push a tag can make a release that every check accepts. The attack on tj-actions showed how a stolen token turns into a release ([GHSA-mw4p](https://github.com/tj-actions/changed-files/security/advisories/GHSA-mw4p-6x4p-x5m5)).

Sandboxes raise the stakes. The client will embed a release manifest with the digest of each artefact that hosts run ([spec 14.9.1](../isolation-spec.md#1491-the-release-manifest)). A host then runs only what a release says. So a release must come only from a maintainer's choice ([isolation research 8.5](../../research/isolation.md#85-the-winner-laptop-held-digests-node-34)).

Git 2.34 and later check SSH signatures on tags, against a file of allowed keys. Git's notes warn against the `ssh-keygen` of OpenSSH 8.7, so the check needs 8.8 or later. A hardware key, such as `ed25519-sk`, needs a touch for each signature, so a copied key file cannot sign ([build facts 6.2](../../research/build-facts.md#62-signed-tags)).

On a tag push, the job checks out the tagged commit, and its workflow probably comes from that commit too (UNVERIFIED). So a tag on an old commit runs that commit's old workflow, which has no gate and reads the release key.

## Options

1. **The tag ruleset alone.** Only the owner may create, move or delete tags that start with `v`. Nothing in the job checks who made the tag.
2. **A signed tag, checked by the tagged commit's own gate and list.** On a tag push, the job checks out the tagged commit. So a tag whose commit adds its own key to the list, or replaces the gate, would pass.
3. **A signed tag, checked by `main`'s gate against `main`'s list.** Before any build step, the release job fetches `main` and runs `main`'s copy of `scripts/verify-tag.sh`. The keys come from `.github/tag-signers` on the protected `main` branch. The tagged commit must be on `main`, and must be the commit that the job checked out. The tag key is a hardware `sk-ssh-ed25519` key. The release key lives in an environment that only the gated job names.
4. **Option 3, with each signature also in a transparency log,** such as Sigstore's gitsign.

## Decision

Option 3, under [question 30](../isolation-spec.md#15-open-questions). The release key of 0017 keeps signing each release, and the client embeds the release manifest. A separate hardware key, which the owner adds, signs each release tag. A transparency log comes later.

- **The gate** is `main`'s copy of `scripts/verify-tag.sh`. The job never runs the tagged commit's copy. Both fetches use no token, on purpose. This repository is public, and a token in the gate would be one more secret that a tag's run can reach. A private fork must give the gate's fetch a read-only token, and remove it before the build. It runs before any build step, and any failure stops the job. It refuses a lightweight tag, a tag that no key on `main`'s list signed, and the signed tag of another release under a new name. It also refuses a tag on a commit that is not on `main`, and a tag that moved after the checkout. [Spec 20.7](../isolation-spec.md#207-a-signed-tag-release-gate-p44) gives each step and each error.
- **The list** is `.github/tag-signers`, one line for each maintainer's key, in the allowed signers format of OpenSSH. The gate takes only hardware keys with `namespaces="git"`, and refuses any other line. A new key reaches the list only through a pull request to `main`, under [0023](0023-protect-main.md). The job needs no private key to check a signature.
- **The tools** are git 2.34 or later and OpenSSH 8.8 or later on the runner. `actions/checkout` is pinned at v6.0.2 or later, as spec 20.7 asks. The gate fetches the tag again, so it does not rely on the checkout's copy.
- **GoReleaser builds the tag that the gate checked.** The job sets `GORELEASER_CURRENT_TAG` to the pushed tag. Otherwise GoReleaser picks the highest version among the tags on the commit, which may be another, unsigned tag.
- **The release environment.** The release job names the environment `release`, with a deployment rule for tags `v*`. GitHub never shows a secret again, so the owner rotates the release key into that environment's secrets, and deletes the repository's copy. The tap token goes there too. A workflow from before the gate does not name the environment, so it reads an empty key.
- **A rotation in two steps.** First the new public key is listed beside the old one, in `.github/release-signers`, `install.sh` and the client, and a release signed by the new key ships. Then a later pull request removes the old key. So `install.sh` and the client can always check the latest release. The new private key is made in the git-ignored `private/` folder, and deleted as soon as it is stored as the secret.
- **Three settings** narrow the gaps that the gate leaves, and [releasing](../releasing.md#the-settings-the-gate-needs) names them: the tag ruleset, branch protection on `main`, and the release environment.

Option 1 trusts one setting, which a stolen admin token can change. Option 2 lets a tag bring its own key and its own gate. Option 4 adds a service to each release, for little gain while the project has one maintainer.

## Consequences

- Good, because the release key signs a release only after `main`'s gate passed: a tag that a key on `main`'s list signed, on a reviewed commit. One way around it remains, a tag on a commit that changes `release.yml` itself.
- Good, because a tag cannot bring its own key or its own gate. The tagged commit's list and script are never read.
- Good, because the check needs only git and OpenSSH, which the runner has. `scripts/test-verify-tag.sh` tests each case in a temporary repository, and pushes no tag.
- Bad, because the workflow file of a tag push probably comes from the tagged commit (UNVERIFIED). A tag on a commit that changes `release.yml` could skip the gate and read the release environment. The gate alone does not close this gap. The tag ruleset narrows it to the owner's account, and branch protection keeps unreviewed commits off `main`.
- Bad, because a tag on a commit from before the gate still runs that commit's workflow, with no gate. Once the release key lives only in the environment, that run cannot sign with it. It still makes an unsigned draft release, which cosign signs with the workflow's identity. `install.sh` and the client's download of helpers refuse that draft, but the cosign check in [releasing](../releasing.md#cosign-bundle) accepts it. The tag ruleset narrows this to the owner's account.
- Bad, because the owner must create the environment and rotate the release key by hand. The rotation takes two pull requests and a release between them. The gate cannot check repository settings.
- Bad, because each release needs the maintainer and their key. A lost key blocks releases until a pull request adds a new key to the list.
- Bad, because the list ships with no key. Until the owner adds the line of their hardware key, the release job refuses every tag.
