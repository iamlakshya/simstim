# Going public

The repository went public on 15 October 2026. This page records the preparation, the checks to run again, and the owner's remaining tasks.

## Already done

- Private material lives only in git-ignored folders: `private/` and `docs/research/private/`. `just check-private` fails on personal paths, private addresses, tailnet names and emails, and on the owner's own words in `private/deny.txt`.
- The history was audited with gitleaks (no secrets), and rewritten once, with the owner's yes, to remove private details from old commits. CI scans the whole history on every push.
- Actions are pinned by full commit SHA except the SLSA generator reusable workflow, which must be called by a version tag ([why](releasing.md#why-one-action-is-pinned-by-tag)). Tokens are least-privilege, and zizmor lints the workflows.
- Releases are signed (SSH key, cosign) with SBOMs and SLSA provenance, and the builds are reproducible.
- Community files are at the root and in `.github/`.

## Checks to run again

Run `just public-check` before a public-readiness review. Then read the README and the docs home once more as a stranger would.

The name, private-detail, history, secret, licence, workflow and docs checks inspect the local checkout. The Scorecard step instead queries the hosted repository using a GitHub token (`GITHUB_TOKEN`, `GITHUB_AUTH_TOKEN` or `gh auth login`); it does not score unpushed local edits. It requires selected checks to meet their thresholds, including 9 of 10 for Pinned-Dependencies because of the SLSA exception, not a perfect overall score.

An empty, incomplete or malformed Scorecard report fails. Every required check must appear once and meet its threshold; a failed Scorecard command also fails the run even if it prints a complete report. `python3 scripts/test-scorecard-check.py` tests these cases offline, and runs in `just test`. CI currently runs the Go tests directly, so this Python suite must also be run locally before pushing.

The normal docs check (`just docs-check`, also called by `public-check`) builds the site and checks internal links, generated pages, Markdown, spelling, prose and documented commands. External links need `just docs-check --external`. Neither check completes the owner-only tasks below.

## The owner's steps

Completed steps and pending owner-only tasks are listed separately by state below. Going public did not complete every task.

| Step | State |
| --- | --- |
| Make the repository public | Done, 15 October 2026 |
| Delete the workflow runs that pointed at commits from before the history rewrite (63 runs) | Done, 15 October 2026 |
| Turn on private vulnerability reporting (SECURITY.md links to it) | Done |
| Turn on Discussions (the forms in `.github/DISCUSSION_TEMPLATE/` appear by themselves) | Done |
| Protect `main`: rulesets block force pushes and deletion, and let changes in only through a pull request that passes CI ([decision 0023](decisions/0023-protect-main.md)) | Done |
| Turn on GitHub Pages, from GitHub Actions; the docs workflow deploys the site | Done |
| Review and enable GitHub secret scanning and push protection in the repository's security settings; a local history scan does not enable them | The owner |
| Ask GitHub Support to purge the old commits by SHA (the request with the SHAs is in `private/`) | The owner |
| Publish the Homebrew tap: create `homebrew-tap`, and add a token that can write to it as the Actions secret `HOMEBREW_TAP_TOKEN` | The owner |
| Cut the first release, with the steps in [releasing](releasing.md) | Done: 0.1.0, 16 October 2026 |
| Add the Scorecard badge to the README once the score is 8 or more | Done: 8.1 on 16 October 2026, after the first release and decision 0023 |
| Apply for the OpenSSF Best Practices badge at <https://www.bestpractices.dev> (it needs an account) | The owner |
| Announce it | The owner |
