# Contributing

Thank you for your interest. The project is young, so open an issue before a large change, and we will agree on the approach first.

## Set up

On macOS or Linux (amd64 or arm64), install Go 1.21 or newer, git and OpenSSH first. Go 1.21 is the bootstrap minimum, not the build toolchain: with automatic toolchain downloads enabled (`GOTOOLCHAIN=auto`), Go downloads the version pinned in [go.mod](go.mod), currently Go 1.26.9.

You also need bash, curl, tar (with gzip and xz support), and either `sha256sum` or `shasum`. These download, unpack and check the tools. Install Python 3 for the documentation checks and end-to-end tests. Then, from your checkout, run:

```sh
scripts/setup.sh
export PATH="$PWD/.tools/bin:$PATH"
```

The script cannot change your shell's PATH; the `export` makes the installed `just` available in that shell. Or call `.tools/bin/just` directly.

It puts pinned versions of just, golangci-lint, GoReleaser, gitleaks, Zig 0.16, and the docs tools (Node and pnpm for the site, typos, Vale, lychee) into `.tools/` (git-ignored), checks each download's SHA-256 against [scripts/tools.lock](scripts/tools.lock), then builds and tests once. Go tools built from source use Go's module checksums. The justfile puts `.tools/bin` first on its PATH. Or open the repo in its [devcontainer](.devcontainer/devcontainer.json), which runs the same script.

```sh
just build   # host helpers, the shadow terminal, then the client, into ./bin
just test    # unit tests with the race detector
just lint    # go vet, golangci-lint, the name check and the private-detail check
just e2e     # end-to-end tests against a real host (E2E_HOST=<a host you added>)
```

See [testing](docs/contributing/testing.md) for the full checks and test-host precautions.

## Send a change

Create a branch (in a fork if you cannot push here), make one logical change, and open a pull request targeting `main`. Keep it up to date with `main` and wait for the required checks before merging; do not push changes straight to `main`. [Decision 0023](docs/contributing/decisions/0023-protect-main.md) lists the required checks and review rules.

Ship the docs and a [CHANGELOG](CHANGELOG.md) entry with the change. After changing a command, flag, config key or key binding, run `just docs` and include the generated updates. Run `just docs-check` for documentation changes. See [how the docs work](docs/contributing/docs.md) and the [style guide](docs/contributing/style.md).

## Rules for the code

1. **The product name lives in one place.** Use `internal/brand`. Never spell the name in code, paths, environment variables or unit names. `just check-name` fails if you do. The name may change ([docs/contributing/renaming.md](docs/contributing/renaming.md)).
2. **Byte-exact while attached.** Nothing may change the bytes between an agent's PTY and the user's terminal, except the two keys the input filter owns. A change here needs a test.
3. **Agents are data.** Add agent knowledge to `internal/agents`, not as branches in other code.
4. **No new listening ports, relays or telemetry.**
5. **Clean-room.** Do not copy code, prompts or assets from closed-source tools. You may borrow from compatible open-source projects (MIT, Apache-2.0) with attribution in `NOTICE`.
6. **Errors say what to try next.** Every user-facing error has a message and a hint.
7. **Docs in plain English.** Short sentences, British spelling.

## Commits

- One logical change per commit, with a message that says why.
- No sign-off or attribution lines: no `Signed-off-by:`, `Co-Authored-By:` or session links ([0010](docs/contributing/decisions/0010-no-commit-trailers.md)). By contributing you agree that your work is under the Apache-2.0 licence, as its section 5 says.
- Run `just test lint` before you push.

## Releases

[docs/contributing/releasing.md](docs/contributing/releasing.md) has the versioning policy and the steps.

## Decisions

Design choices live in [docs/contributing/decisions/](docs/contributing/decisions/). To change an accepted decision, write a new record that replaces it.
