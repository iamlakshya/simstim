# Routine commands. Run `just` to list them. New here? Run scripts/setup.sh.

# The pinned tools from scripts/setup.sh come first.
export PATH := justfile_directory() / ".tools/bin" + ":" + env_var("PATH")

# The product name comes from internal/brand, so renaming needs no change here.
name := `sed -n 's/^const Name = "\(.*\)"/\1/p' internal/brand/brand.go`
module := `sed -n 's/^module //p' go.mod`
version := `git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev`
ldflags := "-s -w -X " + module + "/internal/version.Version=" + version
helpers := "internal/client/helpers"
# A shell line that sets zig_note when this machine's Zig cannot build the
# shadow terminal. vt/build.zig.zon pins Zig 0.17.0.
zig_check := 'have=$(zig version 2>/dev/null || echo "no Zig"); case "$have" in 0.17.*) zig_note="" ;; *) zig_note="The shadow terminal needs Zig 0.17.0, and this machine has $have." ;; esac'

default:
    @just --list

# Install the pinned developer tools into .tools/, then build and test once.
setup:
    scripts/setup.sh

# Build the host helpers for every host platform, into the client's embed folder.
helpers: userns launchers image-verifiers
    #!/usr/bin/env sh
    set -eu
    for arch in amd64 arm64; do
        CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags '{{ldflags}}' \
            -o {{helpers}}/host-linux-$arch ./cmd/host
    done
    {{just_executable()}} _manifest
    ls -l {{helpers}}

# Build the fixed static namespace launcher for both Linux host architectures.
userns:
    cmd/userns/build.sh

# Build both static separate-account launchers, without installing root steps.
launchers:
    #!/usr/bin/env sh
    set -eu
    mkdir -p bin {{helpers}}
    for arch in amd64 arm64; do
        CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags '{{ldflags}}' -o bin/launch-$arch ./cmd/launch
        cp bin/launch-$arch {{helpers}}/launch-$arch
    done

# Build the isolated verifier with its separately verified compiler.
image-verifiers:
    scripts/build-image-verifier.sh '{{version}}'

# Write the release manifest from the helpers and any prepared VMM inputs.
_manifest:
    go run ./cmd/supply write -release '{{version}}' -guest build/guest -vmm build/vmm {{helpers}}

# Download and verify the pinned VMMs, without starting them.
mirror-vmm:
    scripts/mirror-vmm.sh '{{version}}'
    {{just_executable()}} _manifest

# Build the shadow terminal (vt/, Zig 0.17) for every host platform, into the client's embed folder.
vt:
    #!/usr/bin/env sh
    set -eu
    {{zig_check}}
    if [ -n "$zig_note" ]; then
        echo "error: $zig_note" >&2
        echo "Try: just setup, which puts the pinned Zig into .tools/." >&2
        exit 1
    fi
    # Zig fetches Ghostty's dependencies on the first build; a download can
    # fail now and then, so try three times before giving up.
    for try in 1 2 3; do
        (cd vt && zig build linux) && break
        [ "$try" -lt 3 ] || exit 1
        echo "zig build failed; trying again in $((try * 10)) s" >&2
        sleep $((try * 10))
    done
    for arch in amd64 arm64; do
        cp vt/zig-out/linux/shadow-vt-$arch {{helpers}}/vt-linux-$arch
    done
    {{just_executable()}} _manifest
    ls -l {{helpers}}/vt-linux-*

# Build the shadow terminal if Zig 0.17 is installed; otherwise say so and go on
# without it. An old build of it is removed, so the client never embeds one.
_vt-if-zig:
    #!/usr/bin/env sh
    set -eu
    {{zig_check}}
    if [ -n "$zig_note" ]; then
        old=$(ls {{helpers}}/vt-linux-* 2>/dev/null || true)
        rm -f {{helpers}}/vt-linux-*
        if [ -n "$old" ]; then
            echo "note: $zig_note The build goes on without the shadow terminal, and removed the old build of it (see vt/README.md)."
        else
            echo "note: $zig_note The build goes on without the shadow terminal (see vt/README.md)."
        fi
        echo "Try: just setup, which puts the pinned Zig into .tools/."
    else
        {{just_executable()}} vt
    fi

# Build the kernel and tools image with the pinned builder (ARCH=amd64, arm64 or all).
guest *args: helpers vt
    guest/build.sh {{args}}
    {{just_executable()}} _manifest

# Boot the built guest under the pinned Cloud Hypervisor (Linux with KVM).
guest-boot *args:
    guest/boot-test.sh --print-console {{args}}

# Build the helpers, then the client for this machine, into ./bin.
build: launchers helpers _vt-if-zig
    {{just_executable()}} _manifest
    mkdir -p bin
    CGO_ENABLED=0 go build -trimpath -ldflags '{{ldflags}}' -o bin/{{name}} .
    cp {{helpers}}/host-linux-amd64 bin/{{name}}-host-linux-amd64
    @echo "built bin/{{name}} {{version}}"

# Install the client into ~/.local/bin.
install: build
    install -m 0755 bin/{{name}} ~/.local/bin/{{name}}

# Unit tests with the race detector.
test:
    go test -race -count=1 ./...
    LINK_TEST_PIPE=1 go test -race -count=1 ./internal/link
    python3 scripts/test-scorecard-check.py
    python3 test/e2e/test_sandbox_hosts.py
    python3 test/e2e/test_conformance_evidence.py
    python3 scripts/test-check-conformance-go.py
    python3 test/e2e/test_session_sockets.py
    python3 scripts/test-pins-watch.py
    python3 scripts/test-bench-pattern.py
    scripts/test-verify-tag.sh
    python3 test/e2e/test_host_git.py
    python3 scripts/test-check-llms-txt.py

# The unit tests' coverage of internal/ without the launcher, counted across packages (the bar is 70 %).
cover:
    scripts/cover.sh

# Fuzz the input filter, the frame readers, the hook handler, the text cleaner and the paste guard's reader for a while.
fuzz time="30s":
    go test ./internal/keys -run '^$' -fuzz FuzzFilter -fuzztime {{time}}
    go test ./internal/proto -run '^$' -fuzz FuzzRead -fuzztime {{time}}
    go test ./internal/host -run '^$' -fuzz FuzzApplyHook -fuzztime {{time}}
    go test ./internal/api -run '^$' -fuzz FuzzCleanText -fuzztime {{time}}
    go test ./internal/client -run '^$' -fuzz FuzzPasteGuard -fuzztime {{time}}
    go test ./internal/link -run '^$' -fuzz FuzzReadFrames -fuzztime {{time}}

# Static checks.
lint: check-name check-private check-scripts check-size
    go mod tidy -diff
    go vet ./...
    golangci-lint run

# Compare the attach pump's benchmarks with a base commit (default origin/main), on this machine.
bench base="origin/main":
    scripts/bench.sh {{ base }}

# Known vulnerabilities in the code we call, and code nothing reaches, tests included.
vulncheck:
    go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...
    out=$(GOTOOLCHAIN=auto go run golang.org/x/tools/cmd/deadcode@v0.38.0 -test ./...); [ -z "$out" ] || { echo "$out"; echo "Dead code: remove it, or use it."; exit 1; }

# Lint the GitHub workflows and Dependabot config with zizmor (needs uv).
lint-workflows:
    uvx zizmor --offline .github

# Fail if the product name is spelled outside the allowed places.
check-name:
    scripts/check-name.sh

# Fail if a shell script lets a variable name run into a non-ASCII character.
check-scripts:
    scripts/check-scripts.sh

# Fail if a Go source file is over 500 lines.
check-size:
    scripts/check-size.sh

# Fail if a tracked file holds a personal path, a private address or an email.
check-private:
    scripts/check-private.sh

# Format the code.
fmt:
    gofmt -w cmd internal

# End-to-end tests against a real host: E2E_HOST=<a host added with `host add`>.
e2e: build
    python3 test/e2e/run.py bin/{{name}}

# Make the README's screenshots (docs/images) from the demo.
screenshots: build
    scripts/screenshots.sh bin/{{name}}

# A local release build, with no publishing.
snapshot:
    APP_NAME={{name}} goreleaser release --snapshot --clean

# Rename the product (see docs/contributing/renaming.md).
rename new:
    scripts/rename.sh {{new}}

# Build every release binary twice from empty caches and fail if any differs.
repro:
    scripts/repro.sh

# Test install.sh against a local, signed snapshot release.
test-install:
    scripts/test-install.sh

# Everything that must hold before the repository goes public (docs/contributing/going-public.md).
public-check:
    scripts/public-check.sh

# Score the pushed repository with OpenSSF Scorecard (needs the scorecard CLI and `gh auth login`).
scorecard:
    GITHUB_AUTH_TOKEN=$(gh auth token) scorecard --repo={{module}} --format=default

# Write the reference docs from the code (docs/reference). CI fails if they are stale.
docs:
    go run ./cmd/docgen

# Build the docs site into build/site/public (Astro with Starlight; needs Node and pnpm from scripts/setup.sh).
docs-site version="dev":
    scripts/docs-site.sh {{version}}

# Run the commands in the tutorials and how-to guides. Set DOCS_HOST (and DOCS_HOST_TARGET, DOCS_HOST_SSH_CONFIG) to run them on a test host.
test-docs: build
    python3 scripts/test-docs-commands.py bin/{{name}}

# Check the docs: generated pages, Markdown lint, spelling, prose and links (--external for other sites).
docs-check *args:
    scripts/docs-check.sh {{args}}

# Write man pages and shell completions into build/ (the release build does this too).
man:
    go run ./cmd/docgen -out docs/reference -man build/man -completions build/completions
