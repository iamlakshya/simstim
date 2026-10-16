# Going public

What had to be true before the repository went public, what `just public-check` proves, and the owner's steps.

## Already done

- Private material lives only in git-ignored folders: `private/` and `docs/research/private/`. `just check-private` fails on personal paths, private addresses, tailnet names and emails, and on the owner's own words in `private/deny.txt`.
- The history was audited with gitleaks (no secrets), and rewritten once, with the owner's yes, to remove private details from old commits. CI scans the whole history on every push.
- Every action is pinned by SHA, tokens are least-privilege, and zizmor lints the workflows.
- Releases are signed (SSH key, cosign) with SBOMs and SLSA provenance, and the builds are reproducible.
- Community files are at the root and in `.github/`.

## Before you flip the switch

Run `just public-check`. It must pass. Then read the README and the docs home once more as a stranger would.

## The owner's steps

The repository went public on 15 October 2026.

| Step | State |
| --- | --- |
| Make the repository public | Done, 15 October 2026 |
| Delete the workflow runs that pointed at commits from before the history rewrite (63 runs) | Done, 15 October 2026 |
| Turn on private vulnerability reporting (SECURITY.md links to it) | Done |
| Turn on Discussions (the forms in `.github/DISCUSSION_TEMPLATE/` appear by themselves) | Done |
| Protect `main`: rulesets block force pushes and deletion, and let changes in only through a pull request that passes CI ([decision 0023](decisions/0023-protect-main.md)) | Done |
| Turn on GitHub Pages, from GitHub Actions; the docs workflow deploys the site | Done |
| Ask GitHub Support to purge the old commits by SHA (the request with the SHAs is in `private/`) | The owner |
| Publish the Homebrew tap: create `homebrew-tap`, and add a token that can write to it as the Actions secret `HOMEBREW_TAP_TOKEN` | The owner |
| Cut the first release, with the steps in [releasing](releasing.md) | Done: 0.1.0, 16 October 2026 |
| Add the Scorecard badge to the README once the score is 8 or more | Next |
| Apply for the OpenSSF Best Practices badge at <https://www.bestpractices.dev> (it needs an account) | The owner |
| Announce it | The owner |
