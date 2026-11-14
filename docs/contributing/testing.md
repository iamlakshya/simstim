# Testing

Every change keeps the tests green, and every bug fix adds the test that would have caught it.

| Command | What it runs | Where |
| --- | --- | --- |
| `just test` | Go unit tests with the race detector, then the tests of `internal/link` again over a pipe with no buffer (`LINK_TEST_PIPE=1`), plus offline tests of the public-readiness checker, of the end-to-end cleanup, of the end-to-end probe of the session sockets and of the pins watch, of the release's signed-tag gate in a temporary repository (`scripts/test-verify-tag.sh`), of the cleanup of `test/e2e/host_git.py`, and of the `llms.txt` check | Needs Python 3.11 or later. The gate's test skips without git 2.34 and OpenSSH 8.8, except in CI |
| `just cover` | The unit tests' coverage, which must stay at 70 % or more | Anywhere |
| `go test ./internal/tui -run TestGolden -update` | Rewrites the golden screens after an intended change; review the diff | Anywhere |
| `just fuzz` | Fuzzes the input filter, the frame reader, the holder's hook handler, the text cleaner, the paste guard's reader and the link's frame reader for 30 s each (`just fuzz 5m` for longer) | Anywhere |
| `just lint` | `go vet`, the strict golangci-lint set, and the name, private-detail, script and file-size checks | Anywhere |
| `just e2e` | The end-to-end suite against a real host | Needs a test host |
| `just screenshots` | The README images, drawn from `demo` | Anywhere |
| `just repro` | Two release builds from empty caches; every binary must match | Needs GoReleaser and Zig |
| `just test-install` | `install.sh` against a local, signed snapshot release | Needs GoReleaser and Zig |

## Quality bars

These are the project's quality expectations, with tools to check them locally and in CI. Not every CI job is a required branch-protection check. [Decision 0023](decisions/0023-protect-main.md) lists the checks required to merge into `main`.

| Bar | Checked by |
| --- | --- |
| No Go source file over 500 lines (tests aside), and no Python file of the e2e harness, its tests too | `scripts/check-size.sh` |
| Cognitive complexity of 30 or less in every function | gocognit |
| A doc comment on every exported name, and on each package | revive |
| Errors from other packages wrapped with what was being done | wrapcheck |
| A context on every blocking call | noctx, contextcheck |
| Logs through `log/slog` | sloglint, and depguard, which keeps the standard `log` package to tests |
| The strict lint set (`.golangci.yml`) reports nothing; every `nolint` names its linter and gives a reason | golangci-lint, nolintlint |
| No known vulnerability in the code we call, and no dead code | govulncheck, deadcode (`just vulncheck`) |
| Race-free tests, and 70 % or more coverage of `internal/` without the launcher | `go test -race`, `just cover` |
| The frame reader and the input filter never lose a byte | fuzz targets (`just fuzz`) |
| No hook can put an agent session ID without the agent's form into a resume command | a fuzz target (`just fuzz`) |
| The paste guard finds the same clipboard writes in output that frames cut anywhere as in the whole output | a fuzz target (`just fuzz`) |
| The link's frame reader holds no more than its limits, whatever the other end sends | a fuzz target (`just fuzz`), and a test of the link's heap with 1-byte frames |
| The attach pump is no slower than before | benchmarks (`just bench`), on every pull request |

## Unit and golden tests

- **Screens** are golden files in `internal/tui/testdata`, at 60×20, 80×24 and 120×36, with motion off. A golden test fails on any change of text or layout. Run it with `-update` only when the change is meant, and read the diff.
- **Colour** is tested separately in `internal/tui/theme_test.go`: each screen is rendered as a truecolour, 256-colour, 16-colour, `NO_COLOR` and no-terminal profile would receive it, and every fixed colour must keep a contrast of 3:1 on dark and light backgrounds.
- **Model tests** (`internal/tui/model_test.go`) drive the launcher with keys against a fake backend, and check what it asks the backend to do.
- **Context ownership** is checked across terminal handoff and host saves. A resumed launcher keeps its own questions and notices while following the current operation's cancellation. Final cleanup still joins owned work after cancellation.
- **The attach path** is byte-exact: any change to it needs a test that proves the bytes are unchanged.
- **Clocked concurrency tests.** A `synctest.Test` owns the files and cleanup for the workers it starts. Register cleanup on the `t` passed into the bubble. Release and join each held worker before clearing its reader registry or closing its file, also when an assertion calls `t.Fatal`. A parent cleanup must not wait on a channel made inside the bubble.
- **No test touches your home folder.** The test binaries of the packages that use it run in a home of their own: `internal/testhome` points `$HOME`, `%USERPROFILE%`, the XDG folders and `$XDG_RUNTIME_DIR` at a new temporary folder before the tests, and removes it after them. `$XDG_CONFIG_DIRS` and `$XDG_DATA_DIRS` each point to a private directory there too, so a search reads no inherited user or system directory. A child test binary resets these search paths even when it keeps its parent's test home. So no test uses the runtime folder in `/tmp` that your client and other test runs share either. The folder has a short path, so that a socket in it fits in the 104 bytes that macOS allows. Go's own folders (`GOCACHE`, `GOMODCACHE`, `GOPATH` and `GOENV`) keep their places, so a build in a test stays warm. A test binary that a test starts again keeps its parent's home, but only a folder that `testhome` made: any other `TESTHOME_ROOT` gets a new home. `hosttest.Main` does this too, and a package that uses it on Unix calls `testhome.Main` on Windows, in a file of its own. A new package whose tests reach the home, or the state, config, data, cache or runtime folder, calls `testhome.Main` from its `TestMain`, and its home test checks the folders it uses.

SSH control sockets use a separate short directory owned by the test run. The harness checks its owner, mode and inode. It keeps the full connection epoch, route hash and temporary bind suffix within the socket path limit. It never uses the client's shared fallback directory. The home and every XDG directory stay isolated too.

## The host in unit tests

Most of the host helper runs in the unit tests, on the machine that runs them, with no SSH and no systemd. The package `internal/host/hosttest` sets it up, and it works on macOS and Linux:

- `hosttest.New(t)` points `$HOME`, the XDG folders and `$SHELL` at a new temporary folder for the rest of the test, and leaves only the system's programs on `$PATH`, so no agent you installed runs in a test. At the end, it deletes every session and waits for each holder to exit.
- **Holders are real child processes.** They are the test binary itself, started again with the helper's own arguments (`holder <id>`, `hook <event>`), so a package whose tests start sessions calls `hosttest.Main` from its `TestMain`. They start detached, as they do in a container. The children that run git (`git-op <step>`) are the test binary too.
- **Git in a scope.** The tests of `internal/host/gitop_test.go` put a fake `systemctl` and `systemd-run` on `PATH` to check that each git step runs once in a scope under the sessions' slice, and that a probe of systemd that fails runs no git. With real git, they check that a repository's hook scripts, the hooks that its config defines and its fsmonitor never run, and that a hanging filter is stopped at the deadline. On Linux, `gitop_peer_linux_test.go` also places each process in a fake `/proc`, where its parent is: a filter that runs in the git scope then tries to reach another session's holder, and the peer check must refuse it. The tests that make a repository (`setupRepo`) cut systemd off as `hosttest.New` does. Run them in Linux with a git of 2.54 and of 2.55 or later too, as on Alpine 3.24 and Alpine edge, because the hooks of a config differ there.
- **The bridge is real too.** `ClientHost` returns a client host whose transport runs `host.RunBridge` in the test's process, over pipes. `client.Dial`, `client.Attach` and the commands in `internal/cli` run against it. `Transport.Drop` ends every connection at once, as a dropped network would, and `SetDown` makes the host unreachable.
- **A terminal for the client.** `NewTerminal` opens a pseudo-terminal that stands in for the user's: the test types into it, and reads what the client draws, byte for byte.
- **Process observers inspect owned children only.** Holder lifetime tests record the actual child at launch. Native birth information distinguishes that child from a reused PID. They do not scan the process table or read another process's arguments. Fixture service tools stay available until the test host has stopped its sessions.
- **Races in holders count.** The race detector writes a holder's report to a file, and the test fails.

Some parts need Linux: ports and idle stop read `/proc`, and memory caps need systemd. Their tests read a fake `/proc` instead (`procRoot`). The memory cap's tests use a fake `/proc/meminfo`, cgroup tree (`cgroupRoot`) and `systemctl`. Tests that need the shadow terminal skip when it is not built. Its Go side is also tested with a fake child, a shell script that echoes every frame.

## Host keys against a real sshd

The tests of the pinned host key run a fake `ssh` in most places. `internal/client/pin_sshd_test.go` and the files beside it run the real `ssh` against a real `sshd` on `127.0.0.1` instead, and check that the client trusts the same keys as `ssh` does: a port wildcard, a revocation in a second relative file, a certificate authority, a hashed entry, a known hosts file over 16 MiB and a `KnownHostsCommand`. They also check that the changed-key error keeps its exit code and its SHA256 fingerprint with `LogLevel QUIET` and `FingerprintHash md5` in the user's config, that `CheckHostIP yes` writes nothing to the pin (on the container's own address, since `ssh` skips that check for the loopback), and that a host's banner cannot imitate the message. More checks: a certificate whose key ID imitates `ssh`'s log, also while the user's `KnownHostsCommand` fails or hangs, `ProxyJump` hops (a hop at `DEBUG1`, a hop whose banner imitates `ssh`, a hop whose key changed, and the user's own `LogLevel`), a revoked key of another type, and ECDSA keys on three curves. The tests skip unless `PIN_SSHD=1`. Run them in a Linux container with `openssh-server`, where every file they write is in a temporary folder:

```sh
docker run --rm -v "$PWD":/src -w /src -e PIN_SSHD=1 golang:1.26.11 sh -c \
  'apt-get -qq update && apt-get -qq install -y openssh-server openssh-client >/dev/null && go test -run TestRealSSHD ./internal/client/'
```

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

1. Use a disposable Linux VM with systemd. The suite starts and deletes sessions named `e2e-*`, and installs the helper. On the host, it touches only its own `e2e-*` sessions: it kills no process of another session. To test the cap, it fills one session's memory to its cap of 256 MiB. It also checks that the sessions' slice has a limit. That limit stays on the host after the suite, in a drop-in under `~/.config/systemd/user.control/`. `systemctl --user revert simstim.slice` removes it. It leaves no shell history on the host: the shells it types into save none. Do not point it at a machine you care about.
2. Make one with `simstim host create lima e2e` ([how to](../how-to/create-a-host.md)), or add one you reach with `ssh`: `simstim host add <name> <ssh-target>`.
3. Run `E2E_HOST=<name> just e2e`. It prints `PASS` or `FAIL` for each check and a total.
4. Delete a created host afterwards with `simstim host rm e2e --destroy --confirm e2e`.

On your laptop, `test/e2e/boundary.py` gives every client process of the run a fresh `HOME`, `USERPROFILE`, `XDG_CONFIG_HOME`, `XDG_STATE_HOME`, `XDG_DATA_HOME`, `XDG_CACHE_HOME` and `XDG_RUNTIME_DIR` inside a short temporary folder of the run's own (`boundary.Root`). That folder holds a one-host `hosts.toml` without secrets, the default detach key, the run's own SSH control sockets and its own client debug folder. The run never reads or changes your own `config.toml`, `hosts.toml` or control sockets. OpenSSH still finds your keys and `~/.ssh/known_hosts` through the password database (`getpwuid`), not `$HOME`, on macOS and Linux. When `~/.ssh/config` exists, `boundary.Root` writes `Include "<pw_dir>/.ssh/config"` into its `$HOME/.ssh/config`, so the client's SSH wrapper resolves host aliases too (`TestWrapperWithIsolatedHomeKeepsPasswordDBSSH` in `internal/client`). Before sending any key, `boundary.Term.send` checks `config.toml` to be sure `detach_key` is still the default `ctrl+\`. To test reconnects, `drop_link` runs `ssh -O exit` only on the control socket inside the run's root, and signals only bridge children of live client processes that the run started. Your own SSH connections and bridges stay up.

A test host is someone else's machine. On it, the e2e run does only five kinds of thing:

1. It starts sessions of its own. Each has a new ID, and the run's token in its name.
2. It runs commands that only read: session lists, `host doctor`, `host doctor --fix` with a no to every fix, `host alerts --json`, the helper version list and `current` link, and `simstim.slice`'s `MemoryMax`.
3. It attaches with `host/ID`, and sends nothing but the detach key.
4. It deletes its own sessions by ID, through the versioned helper path `bin/<version>/<name>-host rm` that the preflight checked, never `bin/current`.
5. It makes and removes files only in its own folder, `~/.e2e-run-<token>`, created with `mkdir` and stamped from standard input with a random laptop-only `.e2e-nonce` next to the token. An existing folder is never reused, overwritten or removed. After a lost SSH reply, the folder counts as the run's only when both the token and the nonce match.

`test/e2e/agent_setup.py` checks setting up hosts and sessions: the agent checks, a secret that reaches the session's environment and no file, log, command line or unit on the host, setup scripts with and without trust, a script that changed after the yes, and `host doctor --fix` on a stale socket it makes. It uses a throwaway client config, so your own secrets and answers are never read. It installs no agent: it runs `agents install` only for an agent the host already has, which installs nothing. To run only these checks, set `E2E_ONLY=agent-setup`.

`test/e2e/session_verbs.py` checks the session verbs: `show` with a session's ID without its host, `attach` by host and ID, `attach -`, the question of `attach` on an agent that has ended, with `--no-input`, no and Enter, the question of `rm`, `rm -y`, and `kill`'s warning. It uses a throwaway client config that holds only the test host. It starts its own sessions, which run shells, with names that hold the run's process ID. A session is the run's only by a new ID: the ID that `new --detach --json` prints, which the host's list did not have before the start. Every attach and every delete names it by host and ID, after the host's list shows that ID with the run's name. Its clean-up deletes by ID alone, with the host helper's own `rm`. `python3 test/e2e/session_verbs.py` runs the checks against a fake client, without a host: an owner's session named like the run's is never attached to or deleted. To run only these checks, set `E2E_ONLY=session-verbs`.

Every client call on an owner's host gets `SIMSTIM_NO_INSTALL=1`, applied after environment overrides. Both process boundaries check it before starting a client. If this build's helper disappears after preflight, the connection fails before probing, uploading or pruning. Only a disposable host permits automatic installation.

Three exceptions are accepted on an owner's host, as each happens when you use the host yourself:

- E. The host's own `setup` and `resume` hooks in the helper's config folder run in each session that the run starts, as in every session there. At its start, the run prints a notice naming any hook that is present.
- F. `host doctor` runs each agent's `--version` and status command, such as `claude auth status`, which may update the agent's own files.
- H. The host helper runs the host user's interactive login shell (`$SHELL -l -i -c`) once at every session start and every `host doctor` to read the login environment.

Passing an explicit command after `--` keeps a session from running your login shell as the session's agent, though exception H still reads the login environment first. `owned.program` wraps every session command in `env -i` with only `HOME`, `PATH`, `TERM`, `LANG` and `INPUTRC=/dev/null`, so `BASH_ENV`, `ENV` and exported shell functions do not reach the session's program. `test/e2e/boundary.py` is the sole module that runs host commands and client subprocesses, each with an explicit input and timeout. Every host command runs through `/bin/sh -c` and `env -i`, so a `fish` login shell cannot break clean-up, and every git command (including host-side git during `pull`) disables system and global git config, hooks, templates, signing and `fsmonitor`. `test/e2e/allowed.py` and `test/e2e/allowed_scan.py` enforce the allow list both at run time in `boundary.py` and statically across every module in `test/e2e/test_rules.py`.

Each check runs on every host, or only on a disposable host created for the run and destroyed after it. CI's `docker-host` job (or a disposable VM harness) writes a random 32-hex marker to `/etc/e2e-disposable` on the host and sets `E2E_DISPOSABLE=1` and `E2E_DISPOSABLE_MARKER`. `run_parts.disposable` requires `E2E_DISPOSABLE=1` and a matching 32-hex marker on the host, so setting `E2E_DISPOSABLE=1` on a laptop or against an owner's host or container never enables disposable checks. On any other host, the run prints a `SKIP` line for each disposable check, naming the Go tests that cover it in a temporary home:

| Check | Where it runs | The Go tests that cover it on other hosts |
| --- | --- | --- |
| `new --detach --json`, `new --json` refused, attach, detach, replay | Every host | None needed |
| Reconnect after a dropped link, laptop sleep, byte-exact output, snapshot after the ring wraps, resize, a screen of the session's bytes alone | Every host | None needed |
| Killed client, full screen, `pull`, `host alerts --json` | Every host | None needed |
| Agent checks of `host doctor`, secrets, setup scripts, `host doctor --fix` with a no to each fix | Every host | None needed |
| `typing`: typed commands and text in an isolated shell | Disposable host only | `TestAttachInputAndResize` in `internal/host` |
| `kitty keys`: kitty keyboard sequences and quit keys | Disposable host only | `TestPassesOtherKeys` in `internal/keys`, `TestAttachInputAndResize` in `internal/host` |
| `paste`: a dropped file and an image uploaded to the host | Disposable host only | `TestDroppedFilesAreUploaded` and `TestImagePaste` in `internal/client` |
| `desktop notification`: an OSC 777 desktop notification | Disposable host only | `TestScannerSignals` in `internal/host`, `TestNotifyOthers` in `internal/client` |
| `debug`: bridge debug writing to and pruning the shared debug folder | Disposable host only | `TestDebugStartAndStopInARunningSession` and `TestTheBridgeRecordsItsRequests` in `internal/host` |
| `resume`: starting an ended session's agent again, plus OOM cap recovery under systemd | Disposable host only (OOM cap recovery needs systemd memory caps) | `TestResumeAnEndedAgent` and `TestResumeWorksOutTheCap` in `internal/host` |
| `shadow terminal`: killing a shadow terminal process on the host | Disposable host only | `TestVTRestart` and `TestSnapshotFromTheShadowTerminal` in `internal/host` |
| `worktree`: making a worktree outside the run's folder | Disposable host only | `TestWorktreeSessions` in `internal/host` |
| `agents install`: checking install of an installed agent | Disposable host only | `TestAgentsInstallNoInput` and `TestAgentsOfInstallAndLogin` in `internal/cli` |
| `agents login`: starting a login session without the run's token in its name | Disposable host only | `TestAgentsLoginJSON`, `TestRunningLogin` and `TestAgentsLoginClaude` in `internal/cli` |
| `guided setup`: linger, the restore unit and terminfo | Disposable host only | `TestSetupWithFlags`, `TestSetupNeedsATerminalOrFlags` and `TestSetupStopsAndSaysWhatWasDone` in `internal/cli`, `TestBridgeDoctorAndSetup` in `internal/host` |
| `stale socket fix`: a socket in the helper's runtime folder, and the fix applied | Disposable host only | `TestFixesFindAndApply` and `TestFixesApplyOnlyWhatIsAsked` in `internal/host` |
| `alerts`: set, test, log and off | Disposable host only | `TestAlertsOps` and `TestHolderRaisesEvents` in `internal/host`, `TestAlertsCommands` in `internal/cli` |
| `kill`: deleting a session by address with the client's `kill` | Disposable host only | `TestSessionCommands` in `internal/cli` |
| `double detach`: sending the detach key twice so the second reaches the program | Disposable host only | `TestDetachKeyTwiceGoesToTheAgent` in `internal/client` |
| `memory cap`: child OOM kill, agent OOM stop and slice limit | Disposable host only, when `host doctor` reports systemd memory caps | `TestOOMChildKillThenAgentOOM`, `TestOOMAgentKill`, `TestMemoryWarnsOnce` and `TestLimitSessions` in `internal/host` |

`test/e2e/allowed.py` holds a second table, `DISPOSABLE_ONLY`, with each command, flag and key of these checks, and its reason. `test_rules.py` fails when one of them can run outside an `if DISPOSABLE` block, and when an entry of the owner-host table runs on no owner host. The checks of a disposable host live in `disposable.py`, `disposable_checks.py`, `disposable_setup.py`, `agent_login.py` and `guided_setup.py`, which `run.py` and `agent_setup.py` import and call only under `if DISPOSABLE`. The typing checks type into a shell started through `env -i` with `INPUTRC=/dev/null` and `bash --norc --noprofile`, so that no rc file, `~/.inputrc`, `PROMPT_COMMAND` or exported function of the host user's reaches it.

On a disposable host, the clean-up also removes what its checks made outside the run's folder, by their token: the worktree, the stale socket, the debug check's files and the alerts file. Only there may the client install the helper.

One e2e run at a time on a host. At its start, inside its `try`, the suite makes `~/.e2e-lock` on the host, with the run's token in it. `mkdir` makes a folder at once or refuses, so two runs cannot both hold the lock. When the answer of `mkdir` is lost, the token in the lock tells the run that the lock is its own. A run that finds the lock held stops at once. It names the token of the run that holds it, and what a run with that token can leave on the host, with the command that removes each, to run only when no run with that token goes on. A lock whose file holds no token of 6 hex characters goes into no command: the message names the file, and the command that removes the lock alone.

At its end, in its `finally`, the suite calls `finish` up to four times until clean-up completes. Before clean-up starts, `finish` blocks `SIGINT`, `SIGTERM` and `SIGHUP` with `pthread_sigmask`, and writes its messages through `owned.say`, which catches `OSError` when `SIGHUP` has closed the terminal, so the deletes still run. It lists the host up to three times, and deletes by ID the sessions named `e2e-*-<token>` that came after the list of their start. `ls --json` prints `[]` when a host has no sessions, never `null`, so anything other than a JSON list of sessions counts as a failed list. A list that fails three times is a failure of the clean-up, and the run prints the command to list the sessions on the laptop, `<name> ls --host <host>`. A session that is already gone counts as deleted when its folder in the helper's state folder is absent. Next, the suite removes its own folder only while both its token and its `.e2e-nonce` match. Last, it reads the lock's token back. It removes a lock with its own token, also after a failed check, an early exit, `SIGTERM` or `SIGHUP`, and also when the signal came while taking the lock. It never removes a lock with another token. When clean-up fails, the run fails and keeps its lock, so that no other run starts before you look. A run ended by `SIGKILL` leaves the lock. When no run goes on, remove what that run left with the commands that the next run prints, then remove the lock with `rm -f ~/.e2e-lock/run && rmdir ~/.e2e-lock`.

Every check follows one rule for the sessions it starts. The shared helpers in `test/e2e/owned.py` hold the rule, and `run.py` and `agent_setup.py` use them:

1. A check gives each session a name unique to the run, such as `e2e-pull-3f9a1c`.
2. A check owns a session only by a new ID. The ID comes from the JSON of `new --detach --json`, or, at a terminal, from the client's own last `Started` line. It is new when no session had it in a list taken before the start.
3. A check never takes an ID from the first match in a text, and never picks a session by its name from a list.
4. A check attaches (as `host/ID`) or runs `pull` on a session only after `exact()`. A fresh list must show the ID with the name the check gave, and no session named like the ID. If the session is gone, the attach is skipped and the check fails. Each attach and pull also passes `--id --` so the actual lookup stays exact when the session disappears after that check.
5. A check deletes a session only by its ID, through the versioned host helper's own `rm`. On a host that the run did not make, no check runs the client's `kill` or `rm` on a session, because their address could reach another session named like the ID.
6. A start that ends after its time, or that prints bytes that are not UTF-8, is a failed check, and the output names it. This holds at a terminal too. A list taken right after finds what it may have made, by its unique name and newness. The clean-up deletes that by ID.
7. A file that a check expects and that is not there is a failed check, never the end of the suite.

The offline tests run in `just test` and CI, with `python3 -W error::ResourceWarning -m unittest discover -s test/e2e`. `test/e2e/fakes.py` holds their fake client and host, and a local folder that stands for the host's home, where the harness's own shell commands run. `test_sessions.py` tests how the run owns its sessions, `test_cleanup.py` its lock, folder and clean-up, `test_signals.py` and `test_orchestration.py` signal interruption and suite orchestration, `test_host_checks.py` the preflight gates, end checks, hook notice and debug files, `test_boundary.py` the laptop root, detach-key guard, link drop and runtime refusals, `test_rules.py` and `test_mutants.py` the allow list, static scanner, skips and review mutants, `test_agent_setup.py` the setup checks and git and environment isolation, `test_agent_login.py` and `test_agent_login_client.py` the login check, and `test_disposable.py` and `test_disposable_files.py` the checks of a disposable host.

A container made with `host create docker` runs the suite too, over `docker exec` instead of SSH. Without systemd, it runs the exited-session part of `resume` and skips the systemd memory cap checks. CI runs the suite this way on every change (the `docker-host` job), as a disposable host.

It covers start, attach and detach, resize, byte-exact output, replay after a dropped link, a simulated laptop sleep, snapshots after the ring wraps, full-screen programs, pull, secrets, setup scripts and delete. On a disposable host, it also covers typing, kitty keys, image paste and file drops, desktop notifications, debug, resume, the memory cap, a crashed shadow terminal, worktrees, agent install and login, guided setup, the socket fix, alerts and kill. Host reboots are tested by hand, with the host owner's agreement.

`test/e2e/agent_setup.py` checks setting up hosts and sessions: the agent checks of `host doctor`, a secret that reaches the session's environment and no file, log, command line or unit on the host, setup scripts with and without trust, and `host doctor --fix` with a no to every fix. It uses a sub-folder of the run's own laptop root, so your own secrets and answers are never read. The one key that it sends besides the detach key is `y` to the client's own question about the setup script of the run's repository, at the local terminal. The start at that terminal prints no JSON, so the check takes the ID from the client's own last `Started` line. The client prints a host's text, such as a repository's path, before its own lines. When the check cannot own that start, the checks that need the session fail. To run only these checks, set `E2E_ONLY=agent-setup`.

On a disposable host, `test/e2e/agent_login.py` runs `agents login claude`, only when Claude Code is installed and not logged in, and a list of the sessions shows that no `login-claude` session runs. A list that fails skips the check. It stops at the login's first prompt, and never types into it. That prompt has two forms: `Paste code here if prompted >` when the login's local callback listener starts, and `Paste code here >` when it cannot. The check waits for `Paste code here`, which starts both. A link alone is no prompt, since a login that fails may print a help link. After the detach, a list must show that the login still runs, because an attach ends with 0 also when the agent ends. The check reads the bytes that the client wrote, with no newline translation, and a warning's decoded text must hold no control or hidden character either. It never takes ownership from text. It starts the login with `agents login claude <host> --json`, and owns the session only when that command ends with 0 and prints one JSON object with `"started": true` and an ID that a host makes. Its stderr may hold only the two warnings of the client's debug mode, `debug_on` and `debug_incomplete`, in their exact form, one on each line. It attaches to that ID. Task C5 makes that exact: a word of 8 characters of the ID alphabet is always an ID, never a name, and `new` and `rename` refuse such a name. Before C5, an address tries the ID first and then a name. So just before the attach, the check also lists the host, and goes on only when the ID is there and no session is named like it. That list is a guard: the host can change after it. A manifest that does not read never stops the suite. When the owned session goes away or becomes ambiguous before attach, the check fails without attaching. It deletes only that session, by its ID alone, through the host helper's own `rm`, which never takes a name, as the clean-up of every session that the checks start does. Only a concurrent external `login-claude` session that started ahead of the check skips after preconditions pass. Any failed or malformed login answer, or an answer naming a session that ran before, fails the check and deletes nothing. On every path it detaches, and ends its client, also when the detach fails. What it prints of the host's text has no character that a terminal acts on or hides. `test/e2e/test_agent_login.py` and `test/e2e/test_agent_login_client.py` test these rules offline, in `just test`.

`test/e2e/guided_setup.py` checks the guided setup, only on a disposable host. `setup --host <host> --agents shell --yes --no-login` connects the host, shows the checklist, installs nothing, asks nothing, and ends with the summary and the first session's command. `setup` without a terminal or flags, or with an unknown agent, refuses before it changes anything. Ctrl+C at the first question stops it and says nothing was changed. It uses a sub-folder of the run's laptop root with a copy of `hosts.toml`. To run only these checks, set `E2E_ONLY=guided-setup`. On any other host, that prints the skip.

`test/e2e/sandbox_hosts.py` checks hosts you add ([isolation spec, 14.16](isolation-spec.md#1416-tests)). It has two checks. The first is test 2: with `ForwardAgent` naming a throwaway agent in a throwaway client SSH config, a session on the host has no `SSH_AUTH_SOCK`. The agent holds no keys, and your own agent is never forwarded. To show that the check can fail, plain `ssh` with the same config, and without the client's options, forwards that empty agent once. The check needs OpenSSH 8.2 or later on your computer. It skips a host that is not reached over SSH, and a host whose sshd forwards no agent even to plain `ssh`. A read of the session's environment that fails is a failure. Each run starts a session with a name of its own. It passes its cleanup only when the host confirms that the session is gone. A timeout, a failed command or an error for the host fails the cleanup, keeps the throwaway folder and prints the command to delete the session by hand. The second check is test 3. It writes a pin of throwaway keys, one of each usual type, into a throwaway client config, and checks that `host test` refuses to connect, with the changed-key message and exit code 5. It makes the keys with `ssh-keygen`, with `HOME` in a throwaway folder. It starts no session, and skips a host that is not reached over SSH. `test/e2e/test_sandbox_hosts.py` tests the cleanup and the verdict of test 3 offline, and `just test` runs it. To run only these checks, set `E2E_ONLY=sandbox-hosts`.

`test/e2e/session_sockets.py` checks the session sockets. It starts two sessions of its own. From a shell in the first, the helper's bridge asks to type into the second session, to show its screen, to delete it and to attach to it, and each request must get `forbidden`. Where the host has `python3`, a script in the first session also dials the second session's socket itself, and must get `forbidden`. A hook in the first session reaches its own session, and not the second. Nothing reaches the second session's terminal. On a host without a systemd user manager, such as a Docker host, it checks only that `host doctor` reports the sockets as weak. It reads that from the doctor's own line about the user manager, so sessions that an older helper started do not skip the checks. `test/e2e/test_session_sockets.py` tests the probe offline, against a fake terminal that echoes each typed line, and `just test` runs it. To run only these checks, set `E2E_ONLY=session-sockets`.

`test/e2e/host_git.py` checks git on the host: `new --worktree` and `kill` do not run a repository's hook scripts, the hooks that its config defines or its `core.fsmonitor`. On a host with a systemd user manager, the clean filter that `git status` runs must run, under the sessions' slice. On a host with git 2.54, it checks instead that the helper refuses the worktree and runs no hook. Each run works in a folder of its own from `mktemp -d`, with a session name of its own. The product's `kill` removes the session's worktree. The run removes one path only, the folder that its own `mktemp` returned, and only once `ls --json` shows the host without an error and none of the run's sessions. After a `new` whose answer was lost, it waits up to 90 seconds for the session to appear, and deletes it. Otherwise it keeps everything, and says where. `test/e2e/test_host_git.py` checks this cleanup offline, with a fake host, and `just test` runs it. To run only the end-to-end checks, set `E2E_ONLY=host-git`.

`test/e2e/host_commands.py` checks the host commands. `host show` prints the host's details, and its JSON. `host upgrade` finds the helper that is there already, and changes nothing. `host set` refuses an unknown key, and saves `idle` on a container. `host rm --destroy` with input off fails with exit code 4. At a terminal, `host rm` says what the machine holds, and Enter keeps the machine. `host set` and `host rm` use a throwaway client config with a copy of `hosts.toml`. So they forget the host only in the copy, and never delete a machine. `host upgrade` runs only when this build's helper is on the host already, so the checks change nothing there. To run only these checks, set `E2E_ONLY=host-commands`.

## Benchmarks

The attach pump has benchmarks at each step a byte takes: the host's output ring and screen model (`internal/ring`, `internal/host`), the frames (`internal/proto`), the client's catch-up and mode tracker (`internal/term`), the output frames through the client's attach (`internal/client`), and the input filter (`internal/keys`). `internal/term/termtest` makes the output they use: redraws shaped like a coding agent's, and a transcript that scrolls the screen.

`just bench` (or `scripts/bench.sh <base>`) builds the benchmarks at a base commit and in the working tree, runs them in turns on this machine, and compares them with benchstat. A benchmark is slow when it is more than 10 % slower with p below 0.05. A slow benchmark runs again, alone, for 12 more rounds, and the check fails only if it is still slow. The check prepares its exact pinned statistics tool before it builds or samples a benchmark. Preparation runs in its own temporary directory outside every Go module, with workspace lookup off. If TMPDIR lies inside a module, the check stops before downloading anything. This keeps the repository's go.mod and go.sum unchanged. Go's checksum database must stay enabled. The installed tool's module version and checksum must match the prepared module. Offline runs need that pinned tool's dependencies and module metadata prepared first, with the usual Go checksum verification.

Set `BENCH_OUTPUT_DIR` to a new directory below an existing writable parent to choose where the evidence stays. An existing path, a file or a symbolic link is refused. Without it, the check makes a new directory under `build/bench`. It keeps the tool and test binaries, their build information and hashes, raw samples, tables, compiler output and exit code on success and failure. Only its temporary base checkout is removed. When the command runs with a disposable home, choose an evidence directory outside that home and its temporary root.

Nested benchmark cases are compared by their full names. Samples with the same case name must have the same package, file configuration and runtime CPU count on both sides. Conflicting identities stop the comparison, so unrelated cases cannot hide a slowdown. All six benchmark packages must build on both sides before sampling starts. A build failure prints the compiler output and stops the check. A missing binary, or one that cannot be executed, stops it too. The confirmation matches each part of a nested benchmark name. It keeps every slow case, including names with punctuation. One noisy round then does not fail the check, but a real slowdown does. CI runs it on every pull request against the base branch, with a 15 % limit for the noisier runners.

Numbers on an Apple silicon laptop, 15 October 2026, per 32 KiB of agent output:

| Step | Time | Throughput |
| --- | --- | --- |
| Ring write (host) | 0.4 µs | 75 GB/s |
| Screen model (host, for status rules) | 1.3 ms | 26 MB/s |
| Screen model, a transcript that scrolls (host, 16 October) | 4.3 ms | 7.7 MB/s |
| Output frame write (host) | 12 µs | 2.8 GB/s |
| Output frame through the client's attach | 63 µs | 520 MB/s |

The screen model is the slowest step, and it is slower when the output scrolls. It is a Go terminal emulator used only to read the screen for status rules. Even 7.7 MB/s is far more than an agent prints. `BenchmarkScreenModelScroll`, and `BenchmarkScreenModelAltScroll` on the alternate screen, also report the heap that the model keeps (`MiB-kept`). `BenchmarkScreenModelSwitch` feeds a program that switches to the alternate screen and back for each character.

The tests of the link of a sandbox (`internal/link`) run over a socket pair. With `LINK_TEST_PIPE=1` they run over `net.Pipe` instead, as on Windows. That pipe has no buffer, so a test that needs one fails there. `just test` runs them both ways.

The link has a benchmark of its own. `just bench` does not run it yet, because no session uses the link. It passes 1 GiB through one stream over a socket pair, for each window from 64 KiB to 1 MiB, on one core:

```sh
go test ./internal/link -run '^$' -bench Throughput -benchtime 1024x -cpu 1
```

The target is 100 MiB/s or more ([isolation spec, 12](isolation-spec.md#12-performance-targets-and-how-to-measure-them)). Every window passed it by far, the default of 64 KiB too. The default comes from the supervisor's memory budget, not from the benchmark ([isolation spec, 5.2](isolation-spec.md#52-internallink)).

## Vulnerabilities and dead code

`just vulncheck` runs govulncheck, which reports known vulnerabilities only in code the project calls, and deadcode, which fails on any function that neither the program nor a test reaches. CI runs both on every push.

The pins watch also runs govulncheck each day, so a new advisory shows up without a push. It checks the pins that no package ecosystem covers, such as the shadow terminal's Ghostty commit ([releasing](releasing.md#advisories-for-pins-outside-package-ecosystems)). `scripts/test-pins-watch.py` runs its tests offline, from `scripts/pinswatch/tests/`, with API answers recorded in `scripts/testdata/pins-watch/`.

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
