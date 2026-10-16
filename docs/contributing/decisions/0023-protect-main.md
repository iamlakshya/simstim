---
status: accepted
date: 2026-10-16
---

# 0023 Changes reach main only through a pull request that passes CI

## Context

Until now, `main` had one ruleset: no force push and no deletion. Changes went straight to `main`, and CI checked them after the push. A broken change was on `main` before anyone knew.

OpenSSF Scorecard rates this too. On 16 October 2026 the repository scored 7.6 of 10. Two of its checks look at how changes reach `main`: Branch-Protection gave 3 of 10, and CI-Tests had no pull requests to score. The goal for going public is 8 or more ([going public](../going-public.md)).

The project has one maintainer, the owner. A rule that needs a second person to approve every change would stop all work.

## Options

1. **Keep pushing to `main`.** No change; CI only reports after the fact. Scorecard stays under 8 until the repository is 90 days old, when its Maintained check starts to count.
2. **Pull requests that must pass CI, with review that the owner can skip.** Everyone, the owner included, goes through a pull request whose checks pass. A review is required, but repository admins may merge their own pull request without one.
3. **Pull requests with a required review, and no exceptions.** Right for a team; with one maintainer, nothing could merge.

## Decision

Option 2, chosen by the owner on 16 October 2026. Two rulesets cover the default branch:

- **Protect main**, with no exceptions: no deletion, no force push, a linear history, changes only through a pull request, and these checks must pass on a branch that is up to date with `main`: `test`, `lint`, `docs-impact`, `secrets` and `workflows` (CI), and `check` (docs).
- **Review on main**: one approving review, from someone other than the last pusher. Repository admins may skip it when they merge a pull request, and only then.

## Consequences

- Good, because a change that fails CI cannot reach `main`, for anyone.
- Good, because contributors get a review before their change merges.
- Good, because Scorecard's Branch-Protection check rises to 8 of 10, and CI-Tests can score the pull requests.
- Bad, because the owner's own changes are not reviewed by a second person. The rulesets say so honestly: the review ruleset names admins as able to skip it, and Scorecard counts that.
- Bad, because every change waits for CI before it lands, and a pull request must be brought up to date with `main` before it merges.
- Revisit when a second maintainer joins: then drop the admins' exception.
