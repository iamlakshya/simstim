# How well-run Go repos are laid out, documented and opened up

Researched on 15 October 2026.

This note studies thirteen Go repositories on a stack like ours (Go, Bubble Tea, Lip Gloss, GoReleaser) and turns what they do into a target layout for this project's code, docs and repository, with a migration plan. It backs three draft decision records: [0014 Code layout](../contributing/decisions/0014-code-layout.md), [0015 Docs toolchain](../contributing/decisions/0015-docs-toolchain.md) and [0016 Public repo layout](../contributing/decisions/0016-public-repo-layout.md).

## Summary

1. **Layout.** Two shapes recur. Small tools put `main.go` at the root (Crush, Gum, Glow, lazygit, k9s, GoReleaser). Projects with several binaries put each `main` in `cmd/<name>/` (GitHub CLI, Soft Serve, restic, Tailscale). Every application keeps its code in `internal/`. Only projects with a public Go API have `pkg/`. We ship two binaries, so `cmd/cli` and `cmd/host` stay.
2. **The command tree must be importable.** GitHub CLI keeps `cmd/gh` thin and builds its whole cobra tree from `pkg/cmd`, so `cmd/gen-docs` can turn it into the manual and man pages. Crush does the same with `internal/cmd`. Our commands live in `package main`, where no generator can reach them. Moving them to `internal/cli` is the one structural change with a strong reason.
3. **Tests.** Golden files in each package's `testdata/` are the norm (Crush 363, GoReleaser 243). None of the thirteen repos imports `teatest`. Black-box suites live outside the packages: testscript files (Soft Serve, GitHub CLI), Ruby and tmux (fzf), or an in-repo framework (lazygit). Our Python harness belongs in `test/e2e/`.
4. **CI and security.** The best-scoring repos pin every action to a full commit SHA, set least-privilege permissions, and run CodeQL, govulncheck and Scorecard. GoReleaser scores 8.7 on OpenSSF Scorecard, the highest of the set. Most others score 4 to 7.
5. **Releases.** The common modern set is a cosign signature bundle on `checksums.txt`, SBOMs per archive, and GitHub artifact attestations. GoReleaser has deprecated Homebrew formulas in favour of casks. Scorecard gives full marks for signed releases only when a `*.intoto.jsonl` provenance file is a release asset, and none of the studied repos ships one.
6. **Docs generators in October 2026.** Material for MkDocs has been in maintenance mode since 5 November 2025, with support promised for at least 12 months. MkDocs itself has had no stable release since 1.6.1 (August 2024), and its 2.0 pre-releases are reported to drop plugins. Its successor, Zensical, is at 0.0.70 and has no native versioning yet. GoReleaser moved its own site from Material for MkDocs to Hugo with the Hextra theme on 22 March 2026.
7. **Docs toolchain for us: Hugo with Hextra.** Hugo is one binary, written in Go. The theme is pinned in a small `go.mod`. Search, and light and dark themes, are built in. Versions can be built per release tag. A spike built our kind of tree (Markdown in `docs/`, site config in `site/`) and confirmed search, the theme toggle, relative `.md` links, and a title taken from the environment, so the config never spells the product name.
8. **Generated reference.** GitHub CLI generates its manual from cobra. lazygit generates keybinding cheat sheets and a config schema, and its CI fails when they are stale. We should do both: `cmd/docgen` writes `docs/reference/`, and CI runs `just docs` and fails on a diff.
9. **Public files.** Root community files vary a lot. Charm keeps CONTRIBUTING, SECURITY and SUPPORT in one org-wide `.github` repository. GitHub CLI keeps them in `.github/`. restic has GOVERNANCE.md. None of the thirteen has SUPPORT, MAINTAINERS or NOTICE in the repository itself, and none keeps labels as code. We should put the full set at the root, because we have no organisation to inherit from.
10. **Migration.** Fifteen small commits, each with behaviour unchanged and a check that proves it. Code moves come first, then docs generation, the docs moves, the site, the checks, the community files, CI hardening, releases, and versioned Pages last.

## Method

- I cloned each repository with `git clone --depth 1` on 15 October 2026 and read its tree, config and workflows. Every link below pins the commit I read, so the claims stay checkable.
- I used the GitHub API for Discussions, label counts and release assets, the OpenSSF Scorecard API for scores, and PyPI and npm for tool versions. All of these were read on 15 October 2026.
- I built a two-page spike site with Hugo 0.168.0 (extended) and Hextra 0.13.0, outside the repo, to test the recommended docs toolchain.
- Anything I could not confirm is marked **UNVERIFIED**.

| Repository | Commit read | Commit date |
| --- | --- | --- |
| [charmbracelet/crush](https://github.com/charmbracelet/crush/tree/c89ef9d3d1a7) | `c89ef9d3d1a7` | 2026-10-15 |
| [charmbracelet/gum](https://github.com/charmbracelet/gum/tree/879f048103ad) | `879f048103ad` | 2026-09-24 |
| [charmbracelet/glow](https://github.com/charmbracelet/glow/tree/8b0600fa4455) | `8b0600fa4455` | 2026-10-02 |
| [charmbracelet/soft-serve](https://github.com/charmbracelet/soft-serve/tree/6b169bc3af4d) | `6b169bc3af4d` | 2026-10-05 |
| [charmbracelet/wish](https://github.com/charmbracelet/wish/tree/2af054847951) | `2af054847951` | 2026-10-01 |
| [charmbracelet/vhs](https://github.com/charmbracelet/vhs/tree/24fa2254a980) | `24fa2254a980` | 2026-09-24 |
| [cli/cli](https://github.com/cli/cli/tree/819f972eff77) | `819f972eff77` | 2026-10-15 |
| [jesseduffield/lazygit](https://github.com/jesseduffield/lazygit/tree/a0fa1becdb18) | `a0fa1becdb18` | 2026-10-15 |
| [derailed/k9s](https://github.com/derailed/k9s/tree/6450b4055e92) | `6450b4055e92` | 2026-10-15 |
| [goreleaser/goreleaser](https://github.com/goreleaser/goreleaser/tree/1015c53f53a9) | `1015c53f53a9` | 2026-10-13 |
| [tailscale/tailscale](https://github.com/tailscale/tailscale/tree/b83f28ebfbd9) | `b83f28ebfbd9` | 2026-10-14 |
| [restic/restic](https://github.com/restic/restic/tree/9da3b87b0770) | `9da3b87b0770` | 2026-10-11 |
| [junegunn/fzf](https://github.com/junegunn/fzf/tree/c8c2d0828a94) | `c8c2d0828a94` | 2026-10-15 |

Supporting repos: [charmbracelet/meta](https://github.com/charmbracelet/meta/tree/4f7f704b30a4) (Charm's shared CI and release config), [charmbracelet/.github](https://github.com/charmbracelet/.github/tree/16e6bbf0747e) (Charm's org-wide community files), [charmbracelet/fang](https://github.com/charmbracelet/fang/tree/e4b230db8d15) and [imfing/hextra](https://github.com/imfing/hextra/tree/f7568498c5ef).

## 1. The repos, one by one

### 1.1 Crush

- **Layout.** `main.go` at the root. Everything else is in [`internal/`](https://github.com/charmbracelet/crush/tree/c89ef9d3d1a7/internal) (49 packages). The cobra commands live in [`internal/cmd`](https://github.com/charmbracelet/crush/tree/c89ef9d3d1a7/internal/cmd). There is no `cmd/` and no `pkg/`.
- **CLI.** cobra with Fang v2 ([go.mod](https://github.com/charmbracelet/crush/blob/c89ef9d3d1a7/go.mod)). Fang adds styled help and errors, a hidden `man` command built on mango, and a `completion` command ([Fang README](https://github.com/charmbracelet/fang/blob/e4b230db8d15/README.md)).
- **Tests.** Golden files through `charmbracelet/x/exp/golden`: 363 files under [`internal/ui/diffview/testdata`](https://github.com/charmbracelet/crush/tree/c89ef9d3d1a7/internal/ui/diffview/testdata). Recorded HTTP for model calls through `charm.land/x/vcr` ([agent_test.go](https://github.com/charmbracelet/crush/blob/c89ef9d3d1a7/internal/agent/agent_test.go)). This is the pattern for testing cloud providers with no live calls.
- **Lint and CI.** [`.golangci.yml`](https://github.com/charmbracelet/crush/blob/c89ef9d3d1a7/.golangci.yml) adds 10 linters and gofumpt. [`build.yml`](https://github.com/charmbracelet/crush/blob/c89ef9d3d1a7/.github/workflows/build.yml) runs on Linux, macOS and Windows, and fails if `go mod tidy` changes anything. [`security.yml`](https://github.com/charmbracelet/crush/blob/c89ef9d3d1a7/.github/workflows/security.yml) runs CodeQL for Go and for Actions, Grype, govulncheck (as SARIF) and dependency review with a licence allow-list. All 44 action references are pinned to a full SHA.
- **Releases.** [`.goreleaser.yml`](https://github.com/charmbracelet/crush/blob/c89ef9d3d1a7/.goreleaser.yml) (GoReleaser Pro) generates completions and a man page in `before.hooks`. It signs `checksums.txt` with cosign into a `.sigstore.json` bundle, and makes SBOMs for each archive and for the source archive. It publishes a Homebrew formula, Scoop, npm, NUR, winget, AUR, and apk, deb, rpm and Arch packages signed with GPG. macOS binaries are signed and notarised. Changelog groups follow Conventional Commit prefixes, and a release footer explains how to verify the files.
- **Docs.** The README, plus [`docs/config`](https://github.com/charmbracelet/crush/tree/c89ef9d3d1a7/docs/config) and [`docs/hooks`](https://github.com/charmbracelet/crush/tree/c89ef9d3d1a7/docs/hooks). `crush schema` generates a JSON Schema of the config, and a bot commits it ([schema-update.yml](https://github.com/charmbracelet/crush/blob/c89ef9d3d1a7/.github/workflows/schema-update.yml)).
- **Public repo.** LICENSE.md (FSL-1.1-MIT), CLA.md with a CLA workflow, AGENTS.md (234 lines), and a labeler that matches PR titles ([labeler.yml](https://github.com/charmbracelet/crush/blob/c89ef9d3d1a7/.github/labeler.yml)). The other community files come from the org.

### 1.2 Gum

- **Layout.** One top-level package per subcommand (`choose/`, `filter/`, `spin/` and 12 more), helpers in `internal/`, and `main.go` at the root.
- **CLI.** kong, with a man page through `mango-kong` in [man/command.go](https://github.com/charmbracelet/gum/blob/879f048103ad/man/command.go).
- **CI.** The workflows call Charm's reusable workflows at `@main` ([build.yml](https://github.com/charmbracelet/gum/blob/879f048103ad/.github/workflows/build.yml), [lint.yml](https://github.com/charmbracelet/gum/blob/879f048103ad/.github/workflows/lint.yml)). A weekly job copies the shared lint config ([lint-sync.yml](https://github.com/charmbracelet/gum/blob/879f048103ad/.github/workflows/lint-sync.yml), source [meta/golangci.yml](https://github.com/charmbracelet/meta/blob/4f7f704b30a4/golangci.yml), 22 linters). The shared [build.yml](https://github.com/charmbracelet/meta/blob/4f7f704b30a4/.github/workflows/build.yml) runs govulncheck, then tests on Linux, macOS and Windows.
- **Releases.** [`.goreleaser.yml`](https://github.com/charmbracelet/gum/blob/879f048103ad/.goreleaser.yml) is a stub that includes [meta/goreleaser-full.yaml](https://github.com/charmbracelet/meta/blob/4f7f704b30a4/goreleaser-full.yaml) by URL. The shared file sets `mod_timestamp`, generates completions and a man page, and adds nfpms, Homebrew, Scoop, AUR, NUR, winget, a cosign signature on the checksums, SBOMs, and changelog groups.
- **Docs and demos.** The README, and 10 VHS tapes in [`examples/`](https://github.com/charmbracelet/gum/tree/879f048103ad/examples) that make the README's GIFs.
- **Public repo.** Only README and LICENSE. CONTRIBUTING, SECURITY, SUPPORT and the issue forms come from [charmbracelet/.github](https://github.com/charmbracelet/.github/tree/16e6bbf0747e).

### 1.3 Glow, Soft Serve, Wish and VHS

- **Glow.** A flat `main` package plus `ui/` and `utils/`. cobra, with a hidden `man` command through mango-cobra ([man_cmd.go](https://github.com/charmbracelet/glow/blob/8b0600fa4455/man_cmd.go)). Coverage goes to Coveralls ([coverage.yml](https://github.com/charmbracelet/glow/blob/8b0600fa4455/.github/workflows/coverage.yml)).
- **Soft Serve.** [`cmd/soft`](https://github.com/charmbracelet/soft-serve/tree/6b169bc3af4d/cmd/soft) and public packages in [`pkg/`](https://github.com/charmbracelet/soft-serve/tree/6b169bc3af4d/pkg). Integration tests are 38 testscript files in [`testscript/testdata`](https://github.com/charmbracelet/soft-serve/tree/6b169bc3af4d/testscript/testdata). Package extras (systemd unit, sysusers, post-install script) are in [`.nfpm/`](https://github.com/charmbracelet/soft-serve/tree/6b169bc3af4d/.nfpm). `demo.tape` sits at the root.
- **Wish.** A library: one top-level package per middleware. [`examples/`](https://github.com/charmbracelet/wish/tree/2af054847951/examples) holds 16 runnable programs, and [examples.yml](https://github.com/charmbracelet/wish/blob/2af054847951/.github/workflows/examples.yml) builds them.
- **VHS.** A flat `main` package plus `lexer/`, `parser/` and `token/`. [`examples/`](https://github.com/charmbracelet/vhs/tree/24fa2254a980/examples) holds 109 tapes and 68 GIFs. The [README](https://github.com/charmbracelet/vhs/blob/24fa2254a980/README.md) says VHS needs `ttyd` and `ffmpeg`, and that its `.txt` or `.ascii` output can serve as golden files.

### 1.4 GitHub CLI

- **Layout.** [`cmd/gh`](https://github.com/cli/cli/tree/819f972eff77/cmd/gh) is a thin `main`. [`cmd/gen-docs`](https://github.com/cli/cli/blob/819f972eff77/cmd/gen-docs/main.go) is a second `main` for docs. Each command is a package at `pkg/cmd/<command>/<subcommand>/` (39 command groups) and gets its dependencies from a `cmdutil.Factory`. gh-specific helpers live in `internal/`. [docs/project-layout.md](https://github.com/cli/cli/blob/819f972eff77/docs/project-layout.md) marks the older top-level `api/`, `context/`, `utils/` and `test/` as historical or deprecated.
- **Tests.** Unit tests stub HTTP and git. Acceptance tests use testscript behind a build tag, against a real GitHub ([acceptance/README.md](https://github.com/cli/cli/blob/819f972eff77/acceptance/README.md)). The README notes that the real `main` is "an extremely thin adapter", which is what lets testscript call it in-process.
- **Lint and CI.** [`.golangci.yml`](https://github.com/cli/cli/blob/819f972eff77/.golangci.yml) starts from `default: none` and enables 20 linters. Tool versions are pinned in files such as [`.github/golangci-lint-version`](https://github.com/cli/cli/blob/819f972eff77/.github/golangci-lint-version). CodeQL runs with custom queries ([.github/codeql](https://github.com/cli/cli/tree/819f972eff77/.github/codeql)), plus govulncheck and zizmor. All 228 action references are pinned by SHA.
- **Releases.** [deployment.yml](https://github.com/cli/cli/blob/819f972eff77/.github/workflows/deployment.yml) runs GoReleaser per OS, signs the macOS and Windows binaries, signs RPMs, and attests the artifacts with `actions/attest`. Man pages come from `make manpages` before the build ([.goreleaser.yml](https://github.com/cli/cli/blob/819f972eff77/.goreleaser.yml)).
- **Docs.** The user manual is generated from cobra by `cmd/gen-docs --website` and pushed to a separate site repository ([Makefile](https://github.com/cli/cli/blob/819f972eff77/Makefile), target `site-docs`). `gh help reference` prints the whole reference in the terminal ([help_topic.go](https://github.com/cli/cli/blob/819f972eff77/pkg/cmd/root/help_topic.go)). [`docs/`](https://github.com/cli/cli/tree/819f972eff77/docs) holds only contributor and maintainer docs: layout, testing, releasing, triage, working with the team.
- **Public repo.** CONTRIBUTING, CODE-OF-CONDUCT, SECURITY and CODEOWNERS are in [`.github/`](https://github.com/cli/cli/tree/819f972eff77/.github). Issue templates are Markdown, and [config.yml](https://github.com/cli/cli/blob/819f972eff77/.github/ISSUE_TEMPLATE/config.yml) links to security reporting and Discussions. External pull requests are accepted only for issues labelled `help wanted` with acceptance criteria ([CONTRIBUTING.md](https://github.com/cli/cli/blob/819f972eff77/.github/CONTRIBUTING.md)). There is an AGENTS.md (103 lines) and a dev container.

### 1.5 lazygit

- **Layout.** `main.go`, the code in `pkg/` (19 packages), and dev tools as mains in `cmd/` (the integration test runner, i18n). `vendor/` is committed.
- **Tests.** An integration framework drives the real TUI without a screen ([pkg/integration](https://github.com/jesseduffield/lazygit/tree/a0fa1becdb18/pkg/integration)). Demo recordings reuse it with VHS ([Demo_Recordings.md](https://github.com/jesseduffield/lazygit/blob/a0fa1becdb18/docs/dev/Demo_Recordings.md)). That page pins VHS 0.11.0, because 0.12.0 wrote no video ([vhs#787](https://github.com/charmbracelet/vhs/issues/787), now closed).
- **Generated docs.** Keybinding cheat sheets in nine languages come from the code ([pkg/cheatsheet/generate.go](https://github.com/jesseduffield/lazygit/blob/a0fa1becdb18/pkg/cheatsheet/generate.go)), and so does a JSON Schema of the config. CI runs `go generate ./...` and fails on any diff ([ci.yml](https://github.com/jesseduffield/lazygit/blob/a0fa1becdb18/.github/workflows/ci.yml)).
- **Versioned docs without a site.** `docs/` matches the latest release and `docs-master/` matches the main branch. The release script copies one over the other ([update_docs_for_release.sh](https://github.com/jesseduffield/lazygit/blob/a0fa1becdb18/scripts/update_docs_for_release.sh)).
- **Release notes.** GitHub's own generator, grouped by label ([.github/release.yml](https://github.com/jesseduffield/lazygit/blob/a0fa1becdb18/.github/release.yml)). A workflow requires exactly one release label on each pull request ([check-required-label.yml](https://github.com/jesseduffield/lazygit/blob/a0fa1becdb18/.github/workflows/check-required-label.yml)).
- **Lint.** 14 linters plus tuned staticcheck and revive ([.golangci.yml](https://github.com/jesseduffield/lazygit/blob/a0fa1becdb18/.golangci.yml)). 5 of 27 action references are pinned.
- **Public repo.** CONTRIBUTING, CODE-OF-CONDUCT, VISION.md, AGENTS.md (531 lines) and a one-line CLAUDE.md that points to AGENTS.md. A justfile and a dev container.

### 1.6 k9s

- **Layout.** `main.go`, cobra commands in `cmd/`, 17 packages in `internal/`, and data folders `skins/` and `plugins/`. Release notes are one file per version in [`change_logs/`](https://github.com/derailed/k9s/tree/6450b4055e92/change_logs).
- **Lint and CI.** About 30 linters, including funlen, gocyclo, lll, depguard and sloglint ([.golangci.yml](https://github.com/derailed/k9s/blob/6450b4055e92/.golangci.yml)). One Linux test job ([test.yml](https://github.com/derailed/k9s/blob/6450b4055e92/.github/workflows/test.yml)). No action is pinned by SHA.
- **Releases.** A Homebrew formula, nfpms and SBOMs ([.goreleaser.yml](https://github.com/derailed/k9s/blob/6450b4055e92/.goreleaser.yml)).
- **Docs.** A 1,538-line README. The site is k9scli.io ([CNAME](https://github.com/derailed/k9s/blob/6450b4055e92/CNAME)); where its source lives is **UNVERIFIED**.
- **Public repo.** README and LICENSE only. Discussions are off.

### 1.7 GoReleaser

- **Layout.** `main.go`, the cobra commands in `cmd/`, one package per release step in `internal/pipe/` (65 of them), and a public API in `pkg/` (config, context). The site is in [`www/`](https://github.com/goreleaser/goreleaser/tree/1015c53f53a9/www).
- **Tests.** A golden helper ([internal/golden](https://github.com/goreleaser/goreleaser/tree/1015c53f53a9/internal/golden)) and 243 golden files, mostly the generated Homebrew, winget and Nix files.
- **Lint and CI.** 20 linters, including depguard, forbidigo, godoclint and modernize ([.golangci.yaml](https://github.com/goreleaser/goreleaser/blob/1015c53f53a9/.golangci.yaml)). CodeQL, govulncheck, Grype, gitleaks, dependency review, an SBOM scan and Scorecard ([scorecard.yml](https://github.com/goreleaser/goreleaser/blob/1015c53f53a9/.github/workflows/scorecard.yml)). All 96 action references are pinned.
- **Releases.** [`.goreleaser.yaml`](https://github.com/goreleaser/goreleaser/blob/1015c53f53a9/.goreleaser.yaml) uses `homebrew_casks`. Formulas (`brews`) are deprecated since v2.10 ([deprecations.md](https://github.com/goreleaser/goreleaser/blob/1015c53f53a9/www/content/resources/deprecations.md)). It signs the checksums with cosign, makes SBOMs, and uses `verify` (a Pro feature since v2.17) to download the published files again and check their signatures ([verify.md](https://github.com/goreleaser/goreleaser/blob/1015c53f53a9/www/content/customization/verify.md)). The docs recommend `actions/attest` with `subject-checksums: ./dist/checksums.txt` ([attestations.md](https://github.com/goreleaser/goreleaser/blob/1015c53f53a9/www/content/customization/publish/attestations.md)). Custom notes can replace the changelog with `--release-notes=FILE` ([scm/_index.md](https://github.com/goreleaser/goreleaser/blob/1015c53f53a9/www/content/customization/publish/scm/_index.md)).
- **Docs.** Hugo with the Hextra theme. The theme is pinned as a Hugo module in its own [www/go.mod](https://github.com/goreleaser/goreleaser/blob/1015c53f53a9/www/go.mod), so it stays out of the main module. Search is FlexSearch, and the theme has a light, dark and system toggle ([hugo.yaml](https://github.com/goreleaser/goreleaser/blob/1015c53f53a9/www/hugo.yaml)). CI builds the site and checks internal links with htmltest ([docs.yml](https://github.com/goreleaser/goreleaser/blob/1015c53f53a9/.github/workflows/docs.yml)). The site moved from Material for MkDocs to Hugo on 22 March 2026 ([new-site.md](https://github.com/goreleaser/goreleaser/blob/1015c53f53a9/www/content/blog/new-site.md)). The root CONTRIBUTING and SECURITY files are copied into the site at build time, so each has one source ([Taskfile.yml](https://github.com/goreleaser/goreleaser/blob/1015c53f53a9/Taskfile.yml), task `docs:generate`).
- **Public repo.** CONTRIBUTING (Conventional Commits), SECURITY, THREAT_MODEL.md, INCIDENT_RESPONSE.md, USERS.md and CODEOWNERS. Scorecard 8.7.

### 1.8 Tailscale

- **Layout.** Many top-level packages and 55 mains in `cmd/`. Too big to copy, but it has two useful guards.
- **Guards as tests.** [pkgdoc_test.go](https://github.com/tailscale/tailscale/blob/b83f28ebfbd9/pkgdoc_test.go) fails if a package has no doc comment. [license_test.go](https://github.com/tailscale/tailscale/blob/b83f28ebfbd9/license_test.go) checks each file's header against [header.txt](https://github.com/tailscale/tailscale/blob/b83f28ebfbd9/header.txt).
- **Lint and CI.** A small, strict set: five linters, with govet running 30 analysers ([.golangci.yml](https://github.com/tailscale/tailscale/blob/b83f28ebfbd9/.golangci.yml)). Workflows lint the workflows themselves with zizmor ([zizmor.yml](https://github.com/tailscale/tailscale/blob/b83f28ebfbd9/.github/workflows/zizmor.yml)) and check that actions are pinned ([pin-github-actions.yml](https://github.com/tailscale/tailscale/blob/b83f28ebfbd9/.github/workflows/pin-github-actions.yml)). All 123 references are pinned.
- **Public repo.** CODE_OF_CONDUCT, SECURITY and CODEOWNERS at the root, and CONTRIBUTING in `.github/`. Issue forms in YAML ([bug_report.yml](https://github.com/tailscale/tailscale/blob/b83f28ebfbd9/.github/ISSUE_TEMPLATE/bug_report.yml)). Discussions are off.

### 1.9 restic

- **Layout.** `cmd/restic` and `internal/` (25 packages). depguard rules keep the layers apart: for example, backends may not import the repository package ([.golangci.yml](https://github.com/restic/restic/blob/9da3b87b0770/.golangci.yml)).
- **Changelog as files.** Each change adds a file to [changelog/unreleased](https://github.com/restic/restic/tree/9da3b87b0770/changelog/unreleased) from a [TEMPLATE](https://github.com/restic/restic/blob/9da3b87b0770/changelog/TEMPLATE). The tool `calens` builds CHANGELOG.md, and CI checks the files ([tests.yml](https://github.com/restic/restic/blob/9da3b87b0770/.github/workflows/tests.yml)).
- **Docs.** Sphinx on Read the Docs ([.readthedocs.yaml](https://github.com/restic/restic/blob/9da3b87b0770/.readthedocs.yaml)), in reStructuredText under [doc/](https://github.com/restic/restic/tree/9da3b87b0770/doc). Man pages come from cobra's `doc` package ([cmd_generate.go](https://github.com/restic/restic/blob/9da3b87b0770/cmd/restic/cmd_generate.go)) and are committed in [doc/man](https://github.com/restic/restic/tree/9da3b87b0770/doc/man) (37 pages) by the release script ([prepare-release](https://github.com/restic/restic/blob/9da3b87b0770/helpers/prepare-release/main.go)).
- **Releases.** Reproducible since 0.6.1 ([README](https://github.com/restic/restic/blob/9da3b87b0770/README.md)), with a script that checks it ([verify-release-binaries.sh](https://github.com/restic/restic/blob/9da3b87b0770/helpers/verify-release-binaries.sh)). Releases carry PGP signatures (`SHA256SUMS.asc`, from the GitHub API).
- **CI.** Linux, macOS and Windows, and two Go versions. codespell checks spelling.
- **Public repo.** CONTRIBUTING (a changelog file is required) and GOVERNANCE.md, which describes a benevolent-dictator model ([GOVERNANCE.md](https://github.com/restic/restic/blob/9da3b87b0770/GOVERNANCE.md)).

### 1.10 fzf

- **Layout.** `main.go` and `src/` (with `src/tui` and `src/algo`).
- **Tests.** Go unit tests and fuzzing, plus end-to-end tests in Ruby that drive fzf inside tmux ([Makefile](https://github.com/junegunn/fzf/blob/c8c2d0828a94/Makefile), target `itest`; [test/](https://github.com/junegunn/fzf/tree/c8c2d0828a94/test)). The closest match to our Python harness.
- **Lint.** `gofmt -s` and rubocop; no golangci-lint. typos checks spelling ([typos.yml](https://github.com/junegunn/fzf/blob/c8c2d0828a94/.github/workflows/typos.yml)).
- **Docs.** A hand-written roff man page ([man/man1/fzf.1](https://github.com/junegunn/fzf/blob/c8c2d0828a94/man/man1/fzf.1)), README, ADVANCED.md, BUILD.md, RELEASE.md (the release steps) and a hand-written CHANGELOG.md of 3,477 lines.
- **Public repo.** One issue form with required checkboxes ([issue_template.yml](https://github.com/junegunn/fzf/blob/c8c2d0828a94/.github/ISSUE_TEMPLATE/issue_template.yml)), a path-based labeler ([labeler.yml](https://github.com/junegunn/fzf/blob/c8c2d0828a94/.github/labeler.yml)), SECURITY.md, and CODEOWNERS in `.github/`.

## 2. Patterns

### 2.1 Code layout

| Repo | Where `main` is | `cmd/` holds | `internal/` | `pkg/` | Other top-level code |
| --- | --- | --- | --- | --- | --- |
| Crush | root | none | yes (49) | no | none |
| Gum | root | none | helpers | no | one package per subcommand |
| Glow | root | none | no | no | `ui/`, `utils/` |
| Soft Serve | `cmd/soft` | mains | no | yes | `testscript/` |
| Wish | library | none | no | no | one package per middleware |
| VHS | root | none | no | no | `lexer/`, `parser/`, `token/` |
| GitHub CLI | `cmd/gh` | mains (incl. `gen-docs`) | yes | yes | `api/`, `git/` (historical) |
| lazygit | root | dev tools | no | yes | `vendor/` |
| k9s | root | cobra commands | yes (17) | no | data folders |
| GoReleaser | root | cobra commands | yes | public API | `www/` |
| Tailscale | `cmd/*` (55) | mains | yes | no | many packages |
| restic | `cmd/restic` | mains | yes (25) | no | `helpers/` (release tools) |
| fzf | root | none | no | no | `src/` |

What this shows:

1. **`cmd/<name>/` is for projects with more than one binary.** We have two, so `cmd/cli` and `cmd/host` fit. There is no reason to rename them.
2. **Keep the command tree in an importable package.** GitHub CLI (`pkg/cmd`), Crush (`internal/cmd`), k9s and GoReleaser (`cmd/` as a package) all do. Doc generators and in-process tests need it.
3. **`internal/` is the default for an application.** `pkg/` appears only where there is a real public Go API (GoReleaser's config, Soft Serve). We have none.
4. **Dev tools are mains too.** GitHub CLI's `cmd/gen-docs`, lazygit's `cmd/integration_test` and Tailscale's many tools sit beside the shipped binaries and are simply not released.
5. **`examples/` is for things a user runs or copies:** Wish's programs, Gum's and VHS's tapes, our provider scripts.
6. **Black-box suites get their own top-level folder:** fzf's `test/`, GitHub CLI's `acceptance/`, Soft Serve's `testscript/`.
7. **Package fixtures stay with the package.** Every repo keeps golden files in the package's own `testdata/`. Only GoReleaser and k9s add a root `testdata/`, for fixtures that several packages share.

### 2.2 Package boundaries

- **Enforce layers in the linter.** restic's depguard rules forbid a lower layer from importing a higher one. GoReleaser and k9s use depguard to ban whole packages (`github.com/pkg/errors`, `math/rand`).
- **A thin `main`.** GitHub CLI's acceptance tests rely on it. Our `cmd/cli` holds about 1,250 lines of command code in `package main`.
- **Guards as tests.** Tailscale tests that every package has a doc comment and every file a licence header. Our `just check-name` and `just check-private` are the same idea.
- **What our code says.** I mapped which files use which symbols inside our packages. In `internal/host`, `ports.go`, `scan.go`, `screen.go` and `env.go` use nothing from the rest of the package, and `vt.go` uses only the `RingSize` constant. So the shadow-terminal supervisor could become its own package cheaply. `internal/client` and `internal/tui` are tightly knit: their files call each other in both directions, so splitting them into packages would be churn without a clear gain. Files over 500 lines can still be split within their package.

### 2.3 Testing

| Style | Who uses it | Notes |
| --- | --- | --- |
| Golden files in `testdata/` | Crush (363, `x/exp/golden`), GoReleaser (243, own helper), Fang (40), us (46, own helper with `-update`) | The norm for rendered output |
| `teatest` | None of the thirteen | Bubble Tea apps test the model directly and snapshot the view, as we do |
| testscript (`.txtar`) | Soft Serve (38), GitHub CLI (147, behind a build tag) | Good for CLI behaviour; could test our commands against `demo` or a fake host |
| End-to-end in a real terminal | fzf (Ruby and tmux), lazygit (own framework), us (Python and a PTY) | Kept out of `go test ./...` |
| Recorded HTTP | Crush (`charm.land/x/vcr`) | The model for provider API tests with no live calls |
| Fuzzing | fzf (Makefile target), us (`FuzzFilter`) | Scorecard's Fuzzing check counts Go native fuzzing ([checks.md](https://github.com/ossf/scorecard/blob/main/docs/checks.md#fuzzing), read 15 Oct 2026) |

### 2.4 Lint and CI

| Repo | Linters | OS in CI | Security jobs | Actions pinned by SHA | Scorecard |
| --- | --- | --- | --- | --- | --- |
| Crush | 10 added, gofumpt | Linux, macOS, Windows | CodeQL, Grype, govulncheck, dependency review | 44 of 44 | no result |
| Gum (shared config) | 22 | Linux, macOS, Windows | govulncheck | 0 of 6 (calls shared workflows at `@main`) | 5.6 |
| GitHub CLI | 20, from none | Linux, macOS, Windows | CodeQL (custom queries), govulncheck, zizmor | 228 of 228 | 6.6 |
| lazygit | 14 | Linux, Windows | none | 5 of 27 | 3.9 |
| k9s | about 30 | Linux | none | 0 of 7 | 4.4 |
| GoReleaser | 20 | Linux, Windows | CodeQL, govulncheck, Grype, gitleaks, dependency review, Scorecard | 96 of 96 | 8.7 |
| Tailscale | 5 (govet with 30 analysers) | Linux, macOS | CodeQL, govulncheck, zizmor | 123 of 123 | 7.8 |
| restic | 12, from none | Linux, macOS, Windows, two Go versions | codespell | 8 of 23 | 7.3 |
| fzf | gofmt only | Linux, macOS | CodeQL, dependency review | 4 of 19 | 6.5 |
| This project today | standard set + 5 | Linux, Windows | gitleaks | 0 of 15 | not public |

Scores are from the [Scorecard API](https://api.securityscorecards.dev) (read 15 October 2026). Pin counts are `uses:` lines in each repo's workflows.

What this shows:

- **Score follows practice.** The repos that pin every action, set job-level permissions and run Scorecard and CodeQL score highest.
- **Check generated files.** Crush and the shared Charm build fail if `go mod tidy` changes anything. lazygit fails if `go generate` changes anything.
- **Pin tool versions in files**, as GitHub CLI does for golangci-lint and govulncheck, so a bump is a reviewed change.
- **Test Windows** if the client claims Windows. We already do.
- **For our quality bars** (no file over 500 lines, cognitive complexity of 30 or less, a doc comment on every exported name), revive has the rules `file-length-limit`, `cognitive-complexity` and `exported` ([RULES_DESCRIPTIONS.md](https://github.com/mgechev/revive/blob/master/RULES_DESCRIPTIONS.md), read 15 Oct 2026), and golangci-lint v2.14.0 is current.

### 2.5 Releases

| Repo | Signing | SBOM | Provenance | Homebrew | Linux packages |
| --- | --- | --- | --- | --- | --- |
| Crush, Gum (Charm) | cosign bundle on `checksums.txt` | per archive and source | none | formula (`brews`) | apk, deb, rpm, Arch (GPG-signed) |
| GitHub CLI | macOS and Windows code signing, signed RPMs | none in assets | `actions/attest` | **UNVERIFIED** | deb and rpm repositories |
| GoReleaser | cosign bundle, then `verify` | per archive | `actions/attest` | cask | many |
| k9s | none | per archive | none | formula | nfpms |
| restic | PGP (`SHA256SUMS.asc`) | none | none | not in repo | none |
| fzf | macOS notarisation | none | none | not in repo | nfpms |

Release assets were read with the GitHub API on 15 October 2026.

What this shows:

- **cosign + SBOM + attestation** is the modern set. GoReleaser does all three. Charm does the first two.
- **Use casks.** GoReleaser deprecated formulas in v2.10. Our `.goreleaser.yaml` already has a commented `homebrew_casks` block, which is the right direction.
- **Scorecard's Signed-Releases check reads file names.** A `.sigstore.json` asset on each release gives 8 of 10. Only a `*.intoto.jsonl` provenance asset gives 10 ([checks.md](https://github.com/ossf/scorecard/blob/main/docs/checks.md#signed-releases), read 15 Oct 2026). GitHub attestations live in GitHub's attestation store, not in the assets, so they do not count on their own. None of the studied repos uploads a `.intoto.jsonl` file. The SLSA generator for GitHub exists, but its last release, v2.1.0, was on 24 February 2025.
- **Reproducible builds.** restic proves them with a script. GoReleaser offers "verifiable builds" through the Go module proxy ([verifiable_builds.md](https://github.com/goreleaser/goreleaser/blob/1015c53f53a9/www/content/customization/builds/verifiable_builds.md)). That will not work for us: our client embeds host helpers that are build output and are not in git, so a build from the module proxy would lack them. This is my inference from the page, not something I tested. For us, reproducible means: `-trimpath`, `mod_timestamp` (already set), pinned Go and Zig versions, and a CI job that builds twice and compares the checksums.
- **Release notes.** Four approaches: GoReleaser's commit groups (Charm, GoReleaser), GitHub's native generator with labels (lazygit), files per change (restic), or a hand-written CHANGELOG (fzf). Our goal says the CHANGELOG is written with each change, so the notes for a tag should be that version's section, passed with `--release-notes`.

### 2.6 Contributor flow

- **Issue first.** GitHub CLI accepts outside pull requests only for issues labelled `help wanted`. Tailscale and Charm ask for a bug report or a Discussion before a large change. Our CONTRIBUTING already asks for an issue before a large change.
- **One command to set up.** lazygit and GoReleaser give one command for the common loop (`just`, `task`). lazygit and GitHub CLI ship dev containers.
- **Commit style.** GoReleaser and Charm use Conventional Commits, because their changelog groups depend on them. Tailscale has its own guide ([commit-messages.md](https://github.com/tailscale/tailscale/blob/b83f28ebfbd9/docs/commit-messages.md)). We write plain subjects and bodies, and our notes come from the CHANGELOG, so we do not need Conventional Commits.
- **Agent instructions.** Crush, GitHub CLI and lazygit all have AGENTS.md. Sizes range from 103 to 531 lines. GitHub CLI's is short and points to the contributor docs. That is the model for ours.

### 2.7 Docs

#### Site generators in October 2026

| Generator | Runtime | Search | Light and dark | Versioned docs | State on 15 October 2026 |
| --- | --- | --- | --- | --- | --- |
| Hugo + Hextra | Hugo (extended edition), Go for modules | FlexSearch, built in | toggle with light, dark and system | per-release builds into `/versions/<v>/`, as Hextra's own [build.sh](https://github.com/imfing/hextra/blob/f7568498c5ef/build.sh) does | Hugo v0.168.0 (13 Oct 2026), Hextra v0.13.0 (29 Sep 2026); used by GoReleaser |
| Zensical | Python 3.11 or later | built in | yes (**UNVERIFIED** for this version) | only through a transitional fork of mike ([mike page](https://zensical.org/docs/compatibility/mkdocs/mike/)) | v0.0.70 (12 Oct 2026); versioning on the [roadmap](https://zensical.org/roadmap/) with no date |
| MkDocs + Material + mike | Python | built in | yes | mike | Material in maintenance mode since 5 Nov 2025 ([announcement](https://squidfunk.github.io/mkdocs-material/blog/2025/11/05/zensical/)); MkDocs 1.6.1 is from Aug 2024 |
| Docusaurus | Node | Algolia, or a community local plugin ([search](https://docusaurus.io/docs/search)) | yes (**UNVERIFIED** for this version) | built in, by copying `docs/` per version ([versioning](https://docusaurus.io/docs/versioning)) | v3.10.2 |
| Starlight (Astro) | Node | Pagefind, built in ([site search](https://starlight.astro.build/guides/site-search/)) | yes (**UNVERIFIED** for this version) | community plugin `starlight-versions` 0.10.1, "still in early development" | 0.42.6 |
| VitePress | Node | MiniSearch, built in ([search](https://vitepress.dev/reference/default-theme-search)) | yes (**UNVERIFIED** for this version) | none built in (**UNVERIFIED**) | v2 still in alpha (v2.0.0-alpha.20) |
| mdBook | Rust binary | built in ([reading](https://rust-lang.github.io/mdBook/guide/reading.html)) | six themes, including Light | not mentioned | v0.5.4 |
| Sphinx | Python | built in | depends on theme | Read the Docs | used by restic |

Versions are from each project's latest GitHub release, PyPI or npm, read on 15 October 2026.

Notes on the MkDocs family:

- Material for MkDocs 9.7.0 was its last feature release. The team promised critical fixes and security updates for at least 12 months from 5 November 2025, so the promised minimum ends next month ([announcement](https://squidfunk.github.io/mkdocs-material/blog/2025/11/05/zensical/)). Its latest release, 9.7.7, requires `mkdocs<2` (PyPI metadata).
- MkDocs has had no stable release since 1.6.1 on 30 August 2024. PyPI lists `2.0.dev1` to `2.0.dev6`, uploaded between 29 August and 15 September 2026. The Zensical newsletter says MkDocs 2.0 removes the plugin system and cannot build 1.x projects ([Zensical Monthly, February 2026](https://mail.zensical.org/monthly/2026/02/)). That source is a competitor, and I found no statement from the MkDocs maintainers, so the details are **UNVERIFIED**.
- Zensical reads `mkdocs.yml` and is MIT-licensed. At 0.0.x, with versioning still to come, it is too early to bet a docs site on.

#### The Diátaxis split

[Diátaxis](https://diataxis.fr/) names four kinds of docs: tutorials, how-to guides, reference and explanation. None of the thirteen repos follows it by name. GoReleaser comes close: "Getting started" (tutorials), "Customization" (reference) and "Cookbooks" (how-to). GitHub CLI and lazygit split user docs from contributor docs cleanly: GitHub CLI's `docs/` is for contributors only, and lazygit has `docs/dev/`.

#### Generated CLI reference and man pages

| Method | Who | Output |
| --- | --- | --- |
| cobra `doc` package, Markdown tree | GitHub CLI ([cmd/gen-docs](https://github.com/cli/cli/blob/819f972eff77/cmd/gen-docs/main.go)) | one page per command, with front matter |
| cobra `doc` package, man tree | GitHub CLI, restic | one man page per command (git-style) |
| mango (roff, one page) | Glow and GoReleaser (`mango-cobra`), Gum (`mango-kong`), Crush (through Fang) | one man page for the whole tool |
| A help topic in the binary | GitHub CLI (`gh help reference`) | the whole reference in the terminal |
| Hand-written roff | fzf | one page |
| Generated cheat sheets and schema | lazygit, Crush | keybindings per language; JSON Schema of the config |

Man pages and completions are usually made at release time in GoReleaser's `before.hooks` and packed into the archives (Charm, GitHub CLI). Committed generated Markdown is checked for staleness in CI (lazygit). GoReleaser's cask stanza can install man pages and completions ([homebrew_casks.md](https://github.com/goreleaser/goreleaser/blob/1015c53f53a9/www/content/customization/publish/homebrew_casks.md)).

#### Versioned docs

- Built-in: Docusaurus (copies `docs/` per version, and its docs advise keeping fewer than 10).
- Plugin: mike for MkDocs; the Zensical fork of mike; `starlight-versions`.
- Do it yourself: Hextra builds each tag into `/versions/<name>/` with a shell loop and links them from a menu. lazygit keeps two folders, `docs/` and `docs-master/`.

#### Search

FlexSearch (Hextra), Pagefind (Starlight, or any static site after the build: it ships as an `npx` package, a Python package or a standalone binary, per its [docs](https://pagefind.app/docs/)), MiniSearch (VitePress), or Algolia (Docusaurus). All of these except Algolia build the index into the static site and need no server.

#### Recorded demos

VHS tapes in the repo are the Charm habit: Gum (10), VHS (109), Soft Serve (2). lazygit records demos from its integration tests. VHS needs `ttyd` and `ffmpeg`, and lazygit pins an older version after a bug. VHS can also write `.txt` or `.ascii` output for golden tests. We already draw SVG stills from `demo` with our own `ttyshot`. VHS would add GIFs and videos of motion.

#### Link and style checks

| Check | Who uses it | Tool |
| --- | --- | --- |
| Internal links on the built site | GoReleaser | htmltest |
| Spelling | fzf (typos), restic (codespell) | typos supports `locale = "en-gb"` ([reference](https://github.com/crate-ci/typos/blob/master/docs/reference.md#defaultlocale), read 15 Oct 2026) |
| Markdown lint | none of the thirteen | markdownlint-cli2 0.23.3 (Node) |
| Links in sources and on the site | none of the thirteen | lychee 0.24.2 (one binary, with a GitHub Action) |
| Prose style | none of the thirteen | Vale 3.24.0 (one binary). Its spelling check uses American English unless you supply an `en_GB` dictionary ([spelling](https://docs.vale.sh/checks/spelling), read 15 Oct 2026) |

### 2.8 Public repo setup

#### Root and community files

| Repo | Where the community files are |
| --- | --- |
| Charm (all six) | README and LICENSE in each repo. CONTRIBUTING, SECURITY, SUPPORT, issue forms and a PR template in the org-wide [charmbracelet/.github](https://github.com/charmbracelet/.github/tree/16e6bbf0747e) |
| GitHub CLI | CONTRIBUTING, CODE-OF-CONDUCT, SECURITY, CODEOWNERS in `.github/`; AGENTS.md at the root |
| lazygit | CONTRIBUTING, CODE-OF-CONDUCT, VISION, AGENTS.md at the root |
| k9s | README and LICENSE only |
| GoReleaser | CONTRIBUTING, SECURITY, THREAT_MODEL, INCIDENT_RESPONSE, USERS at the root; CODEOWNERS in `.github/` |
| Tailscale | CODE_OF_CONDUCT, SECURITY, CODEOWNERS at the root; CONTRIBUTING in `.github/` |
| restic | CHANGELOG, CONTRIBUTING, GOVERNANCE at the root |
| fzf | CHANGELOG, SECURITY at the root; CODEOWNERS in `.github/` |

None of the thirteen has SUPPORT, MAINTAINERS or NOTICE in the repository itself. GitHub reads CODEOWNERS from `.github/`, the root or `docs/`, in that order ([about code owners](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/about-code-owners), read 15 Oct 2026).

#### `.github/` layout, issue forms and labels

- **Issue forms in YAML** (Tailscale, fzf, Charm's org repo) let you require fields. Markdown templates (GitHub CLI, lazygit, GoReleaser, restic, k9s) cannot.
- **Contact links** in `ISSUE_TEMPLATE/config.yml` send security reports and questions elsewhere (GitHub CLI).
- **Labels.** Counts run from 11 (Soft Serve, VHS) to 157 (Tailscale). Eleven of the thirteen have a `good first issue` label. None keeps its labels as code. The tool for it is [crazy-max/ghaction-github-labeler](https://github.com/crazy-max/ghaction-github-labeler) (v6.0.0): it syncs `.github/labels.yml`, and it has `dry-run`, `skip-delete` and `exclude` options. Crush and fzf instead use labelers that add labels by title or path.
- **Discussions** are on for ten of the thirteen (GitHub API). Category forms live in `.github/DISCUSSION_TEMPLATE/<category-slug>.yml`, but the categories themselves are made in the settings, not from files ([category forms](https://docs.github.com/en/discussions/managing-discussions-for-your-community/creating-discussion-category-forms), read 15 Oct 2026).
- **Release notes config.** lazygit uses GitHub's `.github/release.yml`. No repo here uses release-drafter (v7.10.0 is current).
- **Scorecard.** Only GoReleaser runs the Scorecard action in its own CI, and it has the highest score.

## 3. Docs toolchain for this project

### 3.1 What it must do

From the goal: search; dark and light themes; versioned docs per release; a static build that CI attaches as an artifact; GitHub Pages later. It should also fit a Go repo, and it should not add runtimes that most contributors must install.

Contributors need Go, `just` and golangci-lint today. Zig is optional, and Python 3 runs the end-to-end suite.

### 3.2 Recommendation: Hugo with the Hextra theme

1. **Fits a Go repo.** Hugo is a single Go binary. The theme is a Hugo module pinned in `site/go.mod`, a nested module, so it stays out of our main `go.mod`, and Dependabot can update it. This is exactly GoReleaser's setup.
2. **One added runtime, and only for previewing.** A contributor who wants to preview the site installs Hugo (extended edition, which Hextra requires per its [getting started](https://github.com/imfing/hextra/blob/f7568498c5ef/docs/content/docs/getting-started.md)). CI builds the site for everyone else. There is no Node or Python in the docs path.
3. **Search and themes built in.** FlexSearch builds an offline index at build time, and the theme has a light, dark and system toggle.
4. **Versioned docs, done simply.** At release time, CI builds that version's site and attaches it to the release as `docs.tar.gz`. The Pages job puts the newest release at `/`, each release at `/vX.Y/`, and main at `/dev/`. Old versions are never rebuilt with newer tools, so they cannot break.
5. **Docs stay plain Markdown in `docs/`**, readable on GitHub. The spike confirmed that relative `.md` links work on the site.
6. **Proven on our stack.** GoReleaser, the release tool we already use, moved to it in March 2026.

What the spike showed (Hugo 0.168.0 extended, Hextra 0.13.0, content mounted from `../docs`):

- The site built in about 0.2 seconds.
- A link such as `../reference/cli/x.md` became `/reference/cli/x/`.
- The search index held every page, and the theme toggle was present.
- `HUGO_TITLE` set the site's title, so `site/hugo.yaml` need not spell the product name. The `just` recipe passes the name from `internal/brand`, as the justfile already does.
- **Two gaps to handle.** A page needs a `title` in its front matter; without one, Hugo uses the file name. And with Hugo's default "pretty" URLs, a relative image link such as `../images/x.svg` breaks, because each page becomes a folder. `uglyURLs: true` fixed it in the spike; a small image render hook is the alternative.

Risks:

- Hextra is at 0.x, and one maintainer writes most of it: the top contributor has 343 commits and the next has 32 (GitHub API). It is MIT-licensed.
- Versioning is our own script, not a feature.
- Mitigation: the content is plain Markdown with a `title` line. Moving to another Hugo theme, or to another generator, means changing `site/` only.

### 3.3 Why not the others

- **Zensical:** the best lineage, but it is at 0.0.x, and versioning works only through a transitional fork of mike that deploys to a `gh-pages` branch. It also adds Python 3.11 or later. Look again when it reaches 1.0 with native versioning.
- **Material for MkDocs:** in maintenance mode, and its promised support window ends in November 2026. MkDocs itself is stalled at 1.6.1.
- **Docusaurus:** has the best built-in versioning, but it brings Node and React. It copies the whole `docs/` folder for each version, and local search needs a community plugin.
- **Starlight:** has good built-in search with Pagefind, but versioning is an early-stage plugin, and it needs Node.
- **VitePress:** no built-in versioning found, and v2 is still in alpha.
- **mdBook:** simple, but it has no versioning, and its book layout suits a manual more than a Diátaxis site.

### 3.4 Docs checks to pair with it

- **Generated reference is never stale:** CI runs `just docs && git diff --exit-code` (lazygit's pattern).
- **Links:** lychee on the Markdown sources on every push (offline, internal links), on the built site (internal), and on external links once a week, so a dead outside link does not block a pull request.
- **Markdown lint:** markdownlint-cli2 through its GitHub Action. Node is needed in CI only.
- **Spelling:** typos with `locale = "en-gb"`, which corrects towards British spelling.
- **Plain English:** Vale with a small house style (sentence length, weasel words). Start it as warnings only.
- **Tutorials that run:** a script runs the fenced commands in each tutorial against `demo` or a local host. That one is our own design.

## 4. Target code layout

```text
cmd/
  cli/              main of the client. Thin: it calls cli.Main. The binary's name comes from internal/brand.
  host/             main of the host helper. Unchanged.
  docgen/           dev tool, never shipped. Writes docs/reference/ from the code, and man pages and
                    completions into a build folder.
internal/
  brand/            the product name. The only place that spells it.
  version/          version and protocol strings, set at build time.
  cli/              the cobra command tree, moved from cmd/cli, so docgen and tests can build it.
  api/              the types the client and the host helper exchange.
  proto/            wire framing and messages.
  ring/             the output ring with byte offsets.
  term/             mode tracking, reset without RIS, replay sanitising.
  keys/             the input filter: the detach and image-paste keys.
  agents/           the agent table and screen rules, as data (rules/*.toml).
  paths/            every file and folder the product uses on a host.
  client/           the laptop side: config, SSH, bootstrap, the attach pump, the clipboard.
    helpers/        embed folder for the host helper and shadow-terminal binaries (build output, not in git).
  host/             the host side: holder, bridge, sessions, worktrees, hooks, ports, restore, doctor.
  tui/              the launcher. One package: its files depend on each other too much to split well.
  provider/         when cloud hosts arrive: one sub-package per provider, tested against recorded responses.
vt/                 the shadow terminal (Zig 0.17 and libghostty-vt). A separate build, so it stays at the top.
examples/
  providers/        provider scripts a user copies. Unchanged.
scripts/            shell helpers that just calls: check-name, check-private, rename, screenshots, release notes.
test/
  e2e/              the Python end-to-end suite (moved from e2e/).
  ttyshot/          the Go tool that runs a TUI in a pseudo-terminal and draws it (moved from e2e/ttyshot).
testdata/           fixtures shared by more than one package, such as recorded provider API responses.
                    Created with its first shared fixture. Package fixtures and golden files stay in each
                    package's own testdata/.
site/               the docs site: hugo.yaml, and go.mod to pin the theme. Content is mounted from docs/.
install.sh          stays at the root, because its URL is what users run.
```

Rules that keep it this way:

- `internal/brand` stays the only place that spells the name. `cmd/docgen`, `site/` and the scripts read it from there.
- Later, depguard rules can encode the direction of imports, as restic does: `internal/host` must never import `internal/client` or `internal/tui`, and the low-level packages (`proto`, `ring`, `term`, `keys`, `api`) import nothing above them.
- Splitting large files to meet the 500-line bar happens inside each package and is a separate job. One cheap package split is possible later: the shadow-terminal supervisor (`internal/host/vt.go`), which depends only on `RingSize`.

## 5. Target docs layout

```text
docs/
  README.md             the docs home: where to start. Also the site's home page.
  tutorials/            learning by doing
    getting-started.md  five minutes from install to an agent on a host
    first-cloud-host.md
  how-to/               one task per page
    install.md, add-a-host.md, use-a-provider.md, worktrees.md, ports.md,
    image-paste.md, restore-after-reboot.md, rebind-keys.md, troubleshoot.md, uninstall.md
  reference/            generated by `just docs`; never edited by hand
    cli/                one page per command, from cobra
    config.md           from the settings struct
    keymap.md           from the launcher's command registry
    agents.md           from internal/agents
  explanation/          why it works the way it does
    architecture.md     (from docs/architecture.md)
    fidelity.md         the byte-for-byte contract
    security.md         the threat model
    comparison.md       other tools
    launcher.md         the launcher's ideas, in user terms (from docs/ux.md)
  contributing/         for contributors and maintainers
    README.md           start here
    development.md      set-up, just recipes, the dev container
    testing.md          unit, golden, fuzz and end-to-end tests, ttyshot
    docs.md             how the docs work: Diátaxis, generated reference, the site, the checks
    style.md            code rules and writing style
    releasing.md        the release process, versioning policy, signing, notes from the CHANGELOG
    going-public.md     the owner's steps to make the repo public
    renaming.md         (from docs/renaming.md)
    ux-spec.md          the launcher's design spec (from docs/ux.md)
    decisions/          the decision records (from docs/decisions/)
  research/             the evidence. Same path; research/private/ stays git-ignored.
  roadmap.md            features scored and marked now, next or later
  images/               screenshots and casts, made by `just screenshots`
private/                git-ignored: the goal, status, test-host log, owner checklist, history audit, private notes
```

The site shows all of `docs/`. The user sections come first in the navigation. Contributing, research and the roadmap sit under "Project".

## 6. Target repo layout

### 6.1 Root

| File | Job | Today |
| --- | --- | --- |
| README.md | what it is, a picture, install, quick start, a link to the docs | yes; trim the docs table to a link |
| LICENSE | Apache-2.0 | yes |
| NOTICE | credits for third-party code and data | yes |
| CHANGELOG.md | Keep a Changelog; `Unreleased` written with each change | yes |
| CONTRIBUTING.md | how to contribute; short, links to `docs/contributing/` | yes |
| CODE_OF_CONDUCT.md | Contributor Covenant | yes |
| SECURITY.md | private reporting, supported versions, how to verify a release | yes; add verification |
| SUPPORT.md | where to ask: Discussions, issues, what is out of scope | add |
| GOVERNANCE.md | how decisions are made: maintainer-led, through decision records | add |
| MAINTAINERS.md | who maintains it, and how to become a maintainer | add |
| AGENTS.md | short rules for AI contributors that point to CONTRIBUTING and `docs/contributing/` | yes; shorten |
| `.editorconfig`, `.gitignore`, `.gitattributes` | editor and git settings; mark generated files with `linguist-generated` | add `.gitattributes` |
| `.golangci.yml`, `.goreleaser.yaml` | lint and release | yes |
| `.typos.toml`, `.vale.ini`, `.markdownlint-cli2.yaml`, `.lychee.toml` | docs checks | add |
| `go.mod`, `go.sum`, `justfile`, `install.sh` | build and install | yes |

CODEOWNERS stays in `.github/`. GitHub reads it there first, and it keeps the root short. The goal lists it under the root; the owner can choose at Gate 1.

### 6.2 `.github/`

```text
.github/
  CODEOWNERS
  dependabot.yml            gomod for / and /site, github-actions; grouped
  labels.yml                labels as code
  ISSUE_TEMPLATE/
    config.yml              blank issues off; links to private security reports, Discussions and the docs
    bug.yml
    feature.yml
    provider.yml            ask for a new host provider
    docs.yml                report a docs problem
  DISCUSSION_TEMPLATE/
    ideas.yml
    q-a.yml
  PULL_REQUEST_TEMPLATE.md
  workflows/
    ci.yml                  tests (race), Windows, lint, name and private checks, govulncheck,
                            generated files, docs-impact check, zizmor
    docs.yml                build the site, check links and prose, upload the site as an artifact;
                            later, deploy to Pages
    release.yml             GoReleaser, cosign, SBOMs, attestation, notes from the CHANGELOG, docs tarball
    codeql.yml
    scorecard.yml
    labels.yml              sync labels.yml
```

- **Labels:** kinds (`bug`, `enhancement`, `docs`), areas (`area: client`, `area: host`, `area: launcher`, `area: attach`, `area: providers`, `area: docs`, `area: ci`), triage (`needs triage`, `needs design`, `help wanted`, `good first issue`), and roadmap (`now`, `next`, `later`).
- **Every action pinned by SHA** with a version comment, and permissions set per job. Dependabot keeps the SHAs current.
- **Release notes come from the CHANGELOG.** No release-drafter, because it builds notes from pull request titles and would repeat the CHANGELOG.

## 7. Migration plan

Each step is one commit. Behaviour does not change. Each commit also updates the docs that name the moved paths (AGENTS.md, CONTRIBUTING.md, the docs index), and the CHANGELOG where a user could notice.

**Step 0 (no commit): record the baseline.** Run `just test lint`, `just build` and `just e2e` on the test host. Save the `--help` output of every command and subcommand to a file in `private/`. Note the number of passing end-to-end checks.

1. **Move the command tree to `internal/cli`.** `cmd/cli/main.go` keeps only `main`, and calls `cli.Main`. Check: `just test lint`; `just build`; the `--help` output of every command matches the baseline exactly; the justfile and `.goreleaser.yaml` still build `./cmd/cli`.
2. **Move `e2e/ttyshot` to `test/ttyshot`.** Update `scripts/screenshots.sh` and AGENTS.md. Check: `go build ./...`; `just screenshots` makes the same images as a run on the commit before; `just lint`.
3. **Move the Python suite to `test/e2e/`.** Fix its path to the repo root, the `just e2e` recipe, and the file list in `scripts/check-name.sh`, so the name check still covers the Python files. Check: `just check-name` lists the moved files in its scan; running the suite without `E2E_HOST` prints its usual message; `just e2e` passes on the test host with the same count as the baseline.
4. **Add `cmd/docgen` and `just docs` for the CLI reference.** It uses cobra's `doc` package to write `docs/reference/cli/*.md` with front matter, and commit the output. Check: running `just docs` twice gives no diff; `just test lint`.
5. **Generate the config, keymap and agent references.** Add small read-only accessors to `internal/client`, `internal/tui` and `internal/agents`. Check: `just docs` is stable; `just test` (with `TestGolden`) passes; the accessors change no existing behaviour.
6. **Fail CI when generated docs are stale.** Add `just docs && git diff --exit-code` to `ci.yml`. Generate man pages and completions into a git-ignored build folder, and in GoReleaser's `before.hooks`. Check: CI is green; on a scratch branch, changing a flag's help without `just docs` fails CI.
7. **Create `docs/contributing/`.** Move `renaming.md`, `decisions/` and `ux.md` (as `ux-spec.md`) with `git mv`, and fix relative links only. Check: `git diff -M --stat` shows renames at about 100 % similarity; an offline lychee run finds no broken internal link; `just lint`.
8. **Create `docs/explanation/`.** Move `architecture.md`, and split out the fidelity and security sections as pages, with the text moved rather than rewritten. Check: lychee offline; `just check-private`; the moved text matches the old text, apart from headings and links.
9. **Turn `docs/plan.md` into `docs/roadmap.md`**, plus explanation pages. Move anything private to `private/`. Check: `just check-private`; lychee offline; the docs index links to every page.
10. **Add `site/` (Hugo and Hextra) and `just docs-site`, and a `docs.yml` workflow** that builds the site and uploads it as an artifact. Add `title` front matter to every page. Check: `just docs-site` builds locally; the CI artifact holds `index.html` and the search index; every page has a title.
11. **Add the docs checks:** markdownlint-cli2, lychee, typos (en-gb) and Vale (warnings only). Fix what they find in separate commits. Check: `docs.yml` is green.
12. **Complete the root files:** SUPPORT, GOVERNANCE, MAINTAINERS and `.gitattributes`. Shorten AGENTS.md, and point CONTRIBUTING to `docs/contributing/`. Check: `just check-private`; lychee; markdownlint.
13. **Complete `.github/`:** the provider and docs issue forms, `config.yml`, Discussion forms, `labels.yml` and its sync workflow (dry run on pull requests), and the PR template. Check: zizmor and the docs checks are green; the "New issue" page in the private repo shows every form.
14. **Harden CI:** pin every action by SHA, set permissions per job, and add `codeql.yml`, `scorecard.yml` and govulncheck. Check: zizmor finds no high-severity issue; CI is green. Scorecard can only publish once the repo is public, so until then run it without publishing (**UNVERIFIED** whether it runs fully on a private repo).
15. **Release pipeline:** notes from the CHANGELOG (`scripts/release-notes.sh` with `--release-notes`), a cosign signature on `checksums.txt`, `actions/attest` on the checksums, the docs tarball as a release asset, and the `homebrew_casks` block ready but disabled until the tap exists. Check: `goreleaser check`; `just snapshot` succeeds; the release-notes script prints the `Unreleased` section; two snapshot builds give the same checksums.

**After the repo is public:** a Pages job assembles `/`, `/vX.Y/` and `/dev/` from the release tarballs and main. Check: the deployed site has those three kinds of folder, and lychee passes on it.

**Separate from the layout (quality bars):** split files over 500 lines within their packages. Today these are `internal/host/holder.go`, `internal/host/vt.go`, `internal/host/ops.go`, `internal/client/attach.go`, `internal/tui/model.go`, `launcher.go`, `dialog.go` and `wizard.go`. Use one commit per file. Check: `just test`, `TestGolden` and `just e2e`.

## 8. Questions for the owner at Gate 1

1. Hugo with Hextra for the docs site, or wait for Zensical 1.0?
2. Move the decision records to `docs/contributing/decisions/`, with relative links fixed and no change to their text? Or keep them at `docs/decisions/`?
3. CODEOWNERS in `.github/` (as now) or at the root?
4. Release notes from the CHANGELOG through `--release-notes`, rather than release-drafter?
