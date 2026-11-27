# Build the default image

The default `agent-base` is a minimal Debian root with Bash, git, curl, CA certificates and the fixed guest account. It holds no proprietary agent. The package list and account data live in `internal/image/default_image.toml`. It does not promise that npm, pnpm, Yarn, Bun, pip, uv or Go is installed. Record their actual versions only if they are present in the image under test.

The producer uses the container digest and signed Debian snapshot in `guest/supply.lock`. apt checks Debian's signatures and package hashes. The build also checks the exact `InRelease` SHA-256. Package versions and their architecture come from `dpkg-query` in the prepared root. They are checked against the recipe before conversion.

Prepare one architecture through the existing local Docker build path:

```sh
guest/base-image/build.sh amd64
guest/base-image/build.sh arm64
```

Use a matching native runner for each architecture. The release workflow does this with separate jobs. It adds no QEMU or host binfmt setup. Preparation may fetch the locked base and Debian packages. The conversion workload has no network, a read-only container root, dropped capabilities and a 1 GiB memory limit. Its output and all home and XDG paths are task-owned.

The producer normalises exported file timestamps to the snapshot epoch. It replaces container-specific host and resolver files with the recipe's fixed bytes. It sorts the complete root through the existing OCI flattener, then writes one OCI layer, config and platform manifest. Their digests come from their actual bytes. A Docker image ID or tag is never an image pin.

The same `image.Convert` code used for normal image imports creates ext4 with the verity tree. `image.Inspect` reads the actual output's root hash and data block count. The prepared metadata records these checks, its input pins, package observations and the exact recipe digest. Package licence files remain in the root. The signed Debian snapshot records the package and source references.

Check an output independently:

```sh
go run ./cmd/supply default-image check --dir build/default-image/amd64
```

The checker reads and hashes the actual OCI documents, layer and converted root. It checks the architecture, fixed guest config, recipe, Debian pins and verity metadata. A changed blob, missing package observation or invented image ID fails. Supply can project a release image item only from this checked artifact.

`cmd/supply write -images build/default-image -require-images` requires both architectures before the client embeds its release manifest. `cmd/supply check` takes the same flags when checking the release. The runtime's `default` lookup remains a per-architecture `KindImage` item named `agent-base`.

Publication belongs to the existing release environment. Each native image job runs the main branch's signed-tag gate before building or accessing a registry token. The copy uses `skopeo --preserve-digests`; a changed destination digest fails. The workflow reads the published manifest back by its digest and checks anonymous access before passing artifacts to the main release job. The main job repeats the artifact and publication checks before embedding.

The registry package must be publicly readable by digest for anonymous hosts. An inaccessible package stops the release rather than emitting an unusable default pin. Do not publish a prepared artifact, create a tag or run a release as part of an ordinary test.

Unit fixtures exercise deterministic OCI and verity output and reject changed input, config, layer and root metadata. They do not count as a Debian build, a real package-manager recording, a guest boot or a registry publication. Those receipts must identify the actual prepared image and pinned tool versions.

The offline signature verifier lives in its own `tools/imageverify` module and builds with the same pinned Go 1.27.4 toolchain as the root module (`go.mod` / `GOTOOLCHAIN`). `scripts/build-image-verifier.sh` checks the active Go version against the pin and builds all four client platforms with `-mod=readonly`, `-tags=http2legacy` and `-trimpath`.

On an approved native builder, `guest/base-image/repro.sh <arch>` makes two
fresh preparation containers and separate empty Go build caches. Supply checks
both actual output trees, their observed package versions and locked inputs,
then compares the OCI manifest, config, layer and converted verity values.
The script retains both builds and a `result.json` receipt. It requires the
main module's pinned compiler and an already verified module cache; it does
not publish an image. The receipt reports each whole root SHA separately and
does not claim byte-identical dm-verity superblock identifiers. Running the
small Go fixture tests alone does not satisfy this actual build comparison.

The recipe explicitly includes OpenSSH's client for Git's SSH transport.
`--no-install-recommends` does not make Git's recommended tools implicit build
dependencies. OpenSSH's actual package version and architecture are recorded
with the other signed-snapshot package observations. Its presence does not
provide a proxy route for process or gVisor sessions; those transports retain
their own egress requirements.

Before building helpers or clients, the release also runs the explicit
`cmd/feedgen` producer. It derives the package-malware snapshot from the four
fixed OSV inputs and retains each archive's SHA-256 provenance. A missing or
invalid snapshot fails the release; an empty substitute is not generated.
Ordinary runtime package checks use the embedded data without a network fetch.

Normal CI vets and race-tests the nested `tools/imageverify` module with the
pinned Go 1.27.4 toolchain and `-mod=readonly`, since the root module's `./...`
pattern excludes nested modules.

The global `just repro` command takes both original native build pairs through
`DEFAULT_IMAGE_REPRO_AMD64` and `DEFAULT_IMAGE_REPRO_ARM64`. Each path names the
retained output of that architecture's real `guest/base-image/repro.sh` run.
It rechecks both artifact trees and their source pins, then uses build one for
the first complete release pass and build two for the second. Provide the
verified module cache (covering both the root module and `tools/imageverify`)
through `GOMODCACHE`. The genuine held malware snapshot must also be prepared
by the explicit feed producer.

Each complete pass gets fresh main and verifier build caches, guest and Zig
caches, home and XDG paths. GoReleaser rebuilds both launchers and all four
verifier helpers in each pass. Their actual bytes, the release binaries and
the guest assets must match. The script retains its source-pin checks, logs
and hash evidence. It never substitutes a report JSON, image tag or emulated
preparation for either native artifact pair.

CI uses the existing reproducibility job as the two-architecture native image
matrix. The existing test job receives both checked pairs and runs the two
complete release passes after its normal race, coverage and source checks.
The matrix preserves both image pairs and their native/source evidence in
artifacts; it performs no registry publication.

The Linux test job then packages the actual cross-platform snapshot for the
existing Linux and macOS install matrix. `INSTALL_SNAPSHOT_DIR` lends those
original snapshot archives to `scripts/test-install.sh`; its throwaway-key,
signature, checksum, tamper and native install assertions still run on each
platform. Without that input the script performs its original local snapshot
build, which needs the same prepared image, compiler, feed and kernel inputs.
The hosted macOS runner is not asked to build Linux kernels with Docker.
