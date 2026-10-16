# Testing

Every change keeps the tests green, and every bug fix adds the test that would have caught it.

| Command | What it runs | Where |
| --- | --- | --- |
| `just test` | Unit tests with the race detector | Anywhere |
| `just cover` | The unit tests' coverage, which must stay at 70 % or more | Anywhere |
| `go test ./internal/tui -run TestGolden -update` | Rewrites the golden screens after an intended change; review the diff | Anywhere |
| `just fuzz` | Fuzzes the input filter and the frame reader for 30 s each (`just fuzz 5m` for longer) | Anywhere |
| `just lint` | `go vet`, the strict golangci-lint set, and the name, private-detail, script and file-size checks | Anywhere |
| `just e2e` | The end-to-end suite against a real host | Needs a test host |
| `just screenshots` | The README images, drawn from `demo` | Anywhere |
| `just repro` | Two release builds from empty caches; every binary must match | Needs GoReleaser and Zig |
| `just test-install` | `install.sh` against a local, signed snapshot release | Needs GoReleaser and Zig |

## Quality bars

Each bar is checked by a tool, locally and in CI; a change that breaks one does not merge.

| Bar | Checked by |
| --- | --- |
| No Go source file over 500 lines (tests aside) | `scripts/check-size.sh` |
| Cognitive complexity of 30 or less in every function | gocognit |
| A doc comment on every exported name, and on each package | revive |
| Errors from other packages wrapped with what was being done | wrapcheck |
| A context on every blocking call | noctx, contextcheck |
| Logs through `log/slog` | sloglint, and depguard, which keeps the standard `log` package to tests |
| The strict lint set (`.golangci.yml`) reports nothing; every `nolint` names its linter and gives a reason | golangci-lint, nolintlint |
| No known vulnerability in the code we call, and no dead code | govulncheck, deadcode (`just vulncheck`) |
| Race-free tests, and 70 % or more coverage of `internal/` without the launcher | `go test -race`, `just cover` |
| The frame reader and the input filter never lose a byte | fuzz targets (`just fuzz`) |
| The attach pump is no slower than before | benchmarks (`just bench`), on every pull request |

## Unit and golden tests

- **Screens** are golden files in `internal/tui/testdata`, at 60×20, 80×24 and 120×36, with motion off. A golden test fails on any change of text or layout. Run it with `-update` only when the change is meant, and read the diff.
- **Colour** is tested separately in `internal/tui/theme_test.go`: each screen is rendered as a truecolour, 256-colour, 16-colour, `NO_COLOR` and no-terminal profile would receive it, and every fixed colour must keep a contrast of 3:1 on dark and light backgrounds.
- **Model tests** (`internal/tui/model_test.go`) drive the launcher with keys against a fake backend, and check what it asks the backend to do.
- **The attach path** is byte-exact: any change to it needs a test that proves the bytes are unchanged.

## The host in unit tests

Most of the host helper runs in the unit tests, on the machine that runs them, with no SSH and no systemd. The package `internal/host/hosttest` sets it up, and it works on macOS and Linux:

- `hosttest.New(t)` points `$HOME`, the XDG folders and `$SHELL` at a new temporary folder for the rest of the test, and leaves only the system's programs on `$PATH`, so no agent you installed runs in a test. At the end, it deletes every session and waits for each holder to exit.
- **Holders are real child processes.** They are the test binary itself, started again with the helper's own arguments (`holder <id>`, `hook <event>`), so a package whose tests start sessions calls `hosttest.Main` from its `TestMain`. They start detached, as they do in a container.
- **The bridge is real too.** `ClientHost` returns a client host whose transport runs `host.RunBridge` in the test's process, over pipes. `client.Dial`, `client.Attach` and the commands in `internal/cli` run against it. `Transport.Drop` ends every connection at once, as a dropped network would, and `SetDown` makes the host unreachable.
- **A terminal for the client.** `NewTerminal` opens a pseudo-terminal that stands in for the user's: the test types into it, and reads what the client draws, byte for byte.
- **Races in holders count.** The race detector writes a holder's report to a file, and the test fails.

Some parts need Linux: ports and idle stop read `/proc`, and memory caps need systemd. Their tests read a fake `/proc` instead (`procRoot`). The memory cap's tests use a fake `/proc/meminfo`, cgroup tree (`cgroupRoot`) and `systemctl`. Tests that need the shadow terminal skip when it is not built. Its Go side is also tested with a fake child, a shell script that echoes every frame.

## Coverage

The unit tests must cover at least 70 % of the statements in `internal/`, without the launcher (`internal/tui`, which the golden screens test). CI fails below that. To see the number:

```sh
just cover
```

It runs the same as this:

```sh
pk=$(go list ./internal/... | grep -v '/tui$' | tr '\n' ',')
go test -count=1 -coverpkg="${pk%,}" -coverprofile=cover.out ./internal/...
go tool cover -func=cover.out | tail -1
```

The coverage is counted across packages: a test in one package counts for the code it runs in another, so an attach test in `internal/client` counts for the bridge in `internal/host`. Holders that a test starts write their coverage too, where `go test` collects it. `go tool cover -html=cover.out` shows which lines ran.

The bar is a floor, not the goal. A test checks what the code does, not just that it runs. When the bar was set, in October 2026, the coverage was 83 %, and 82 % without the shadow terminal.

## End-to-end tests

`test/e2e/run.py` drives the real client in a pseudo-terminal, as a person in Ghostty would, against a real Linux host.

1. Use a disposable Linux VM with systemd. The suite starts and deletes sessions named `e2e-*`, and installs the helper. To test the cap, it fills one session's memory to its cap of 256 MiB. It also checks that the sessions' slice has a limit. That limit stays on the host after the suite, in a drop-in under `~/.config/systemd/user.control/`. `systemctl --user revert simstim.slice` removes it. Do not point it at a machine you care about.
2. Make one with `simstim host create lima e2e` ([how to](../how-to/create-a-host.md)), or add one you reach with `ssh`: `simstim host add <name> <ssh-target>`.
3. Run `E2E_HOST=<name> just e2e`. It prints `PASS` or `FAIL` for each check and a total.
4. Delete a created host afterwards with `simstim host destroy e2e`.

A container made with `host create docker` runs the suite too, over `docker exec` instead of SSH. It has no systemd, so the memory cap check is skipped. CI runs the suite this way on every change (the `docker-host` job).

It covers start, attach and detach, resize, byte-exact output, replay after a dropped link, a simulated laptop sleep, the memory cap, snapshots after the ring wraps, a crashed shadow terminal, image paste and file drops, worktrees, ports, rename and delete. Host reboots are tested by hand, with the host owner's agreement.

`test/e2e/agent_setup.py` checks setting up hosts and sessions: the agent checks, a secret that reaches the session's environment and no file, log, command line or unit on the host, setup scripts with and without trust, and `host doctor --fix` on a stale socket it makes. It uses a throwaway client config, so your own secrets and answers are never read. It installs no agent: it runs `agents install` only for an agent the host already has, which installs nothing. To run only these checks, set `E2E_ONLY=agent-setup`.

`test/e2e/guided_setup.py` checks the guided setup: `setup --host <host> --agents shell --yes --no-login` connects the host, shows the checklist, installs nothing, asks nothing, and ends with the summary and the first session's command; `setup` without a terminal or flags, or with an unknown agent, refuses before it changes anything; and Ctrl+C at the first question stops it and says nothing was changed. It uses a throwaway client config with a copy of `hosts.toml`. To run only these checks, set `E2E_ONLY=guided-setup`.

## Benchmarks

The attach pump has benchmarks at each step a byte takes: the host's output ring and screen model (`internal/ring`, `internal/host`), the frames (`internal/proto`), the client's catch-up and mode tracker (`internal/term`), the output frames through the client's attach (`internal/client`), and the input filter (`internal/keys`). `internal/term/termtest` makes the output they use: redraws shaped like a coding agent's, and a transcript that scrolls the screen.

`just bench` (or `scripts/bench.sh <base>`) builds the benchmarks at a base commit and in the working tree, runs them in turns on this machine, and compares them with benchstat. A benchmark is slow when it is more than 10 % slower with p below 0.05. A slow benchmark runs again, alone, for 12 more rounds, and the check fails only if it is still slow. One noisy round then does not fail the check, but a real slowdown does. CI runs it on every pull request against the base branch, with a 15 % limit for the noisier runners.

Numbers on an Apple silicon laptop, 15 October 2026, per 32 KiB of agent output:

| Step | Time | Throughput |
| --- | --- | --- |
| Ring write (host) | 0.4 µs | 75 GB/s |
| Screen model (host, for status rules) | 1.3 ms | 26 MB/s |
| Screen model, a transcript that scrolls (host, 16 October) | 4.3 ms | 7.7 MB/s |
| Output frame write (host) | 12 µs | 2.8 GB/s |
| Output frame through the client's attach | 63 µs | 520 MB/s |

The screen model is the slowest step, and it is slower when the output scrolls. It is a Go terminal emulator used only to read the screen for status rules. Even 7.7 MB/s is far more than an agent prints. `BenchmarkScreenModelScroll` also reports the heap that the model keeps (`MiB-kept`).

## Vulnerabilities and dead code

`just vulncheck` runs govulncheck, which reports known vulnerabilities only in code the project calls, and deadcode, which fails on any function that neither the program nor a test reaches. CI runs both on every push.

## Time to first agent

`scripts/time-to-agent.sh <ssh target> [ssh config] [agent]` measures the path in [getting started](../tutorials/getting-started.md) on a host you can already reach: it builds a snapshot release, signs it with a throwaway key, serves it on localhost, and times `install.sh`, `host add` and `new --agent` in a throwaway config folder, until the agent's state is known.

| Date | Client | Host | install | host add | agent running | Total |
| --- | --- | --- | --- | --- | --- | --- |
| 2026-10-15 | macOS, Apple silicon | Debian 13 VM, over a jump host on WiFi | 0.1 s | 1.8 s | 0.2 s (Claude Code) | 2.2 s |
| 2026-10-16 | macOS, Apple silicon (0.1.0 at 42c42d1) | Debian 13 VM, over a jump host on WiFi | 0.2 s | 2.1 s | 0.2 s (Claude Code) | 2.6 s |
| 2026-10-16 | macOS, Apple silicon, as a clean Mac: the released 0.1.0 from GitHub, an empty home and environment, and only macOS's own tools on `PATH` | Debian 13 VM, over a jump host on WiFi | 2.4 s | 2.2 s | 0.3 s (Claude Code) | 4.9 s |

The release is served locally, so the download is not in the time: a client archive is about 11 MB, a few seconds on a home connection. The rest of the five minutes is typing three commands, and the agent's own login.

## Seeing a screen

`test/ttyshot` runs a program in a pseudo-terminal, sends keys from a script, and prints the screen it ends on, or draws it as SVG:

```sh
go run ./test/ttyshot -script keys.tty -- ./bin/<name> demo
```

`just screenshots` uses it to draw the README's images.
