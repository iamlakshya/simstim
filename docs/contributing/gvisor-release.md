# Prepare the native gVisor release files

The fixed controller needs five signed companions from one patched build:
`runsc`, `gvisor_sentry`, `gvisor-sentry-prewarmer`, `gvisor-proc-umounter`
and `runsc-fd-parking`. The parking helper is required for this route. An
upstream binary or an earlier patch suffix cannot replace one companion.

Use the exact upstream commit in `third_party/gvisor/hostguard-inputs.json`
and the patch in that folder. The native builder checks every original source
file before applying the patch and every resulting source file afterwards.
It derives its version from the actual patch digest and checks the same pin
in `internal/wall/gvisor/native_build.go`.

Prepare tools from `third_party/gvisor/build-tools.json` on each matching native Linux architecture. It pins official Bazel 8.8.1 and the upstream source-selected Go 1.27.3 archive by size and SHA-256. Preparation checks bytes before copying Bazel or making it executable. It keeps the SDK archive unextracted for Bazel's `distdir`. The project client, host and test toolchain stays at Go 1.26.11.

```sh
python3 scripts/prepare-gvisor-build.py --output <new-tools> --cache <downloads>
GVISOR_BAZEL=<new-tools>/bazel \
GVISOR_TOOL_INPUTS=<new-tools>/tools.json \
GVISOR_DISTDIR=<new-tools>/distdir \
python3 scripts/build-gvisor-runtime.py <clean-source> <new-output>
```

The builder checks the actual upstream `MODULE.bazel`, `go.mod`, `.bazelrc` and SDK patch against original Git-blob SHA-256 values. It checks the source-selected Bazel version and `rules_go.from_file` SDK selection. The SDK is upstream Go 1.27.3 with `tools/go_types_memoize.patch`; the repository module is unchanged.

The builder starts no runtime or VM. Keep its original five files and
`build.json`, `build-inputs.json`, `build.spdx.json` and the generated `MODULE.bazel.lock` together. Prepare the two results under `build/gvisor/amd64/`
and `build/gvisor/arm64/`. The release manifest can read a different prepared
root through `GVISOR_MANIFEST_INPUTS`; completed release files still enter
`build/gvisor/assets/`, which GoReleaser checksums and uploads.

The manifest writer requires both complete closures during release:

```sh
go run ./cmd/supply write -release <version> \
  -gvisor build/gvisor -require-gvisor internal/client/helpers
```

It derives the current source pin itself. It accepts only the original
complete build record, with its exact source, architecture and five roles.
It checks each actual file's size and SHA-256, native ELF architecture and
absence of an external interpreter. Both Go executables must enter through
the pinned native guard. It checks both architectures before publishing
completed assets or adding manifest items. Unknown output entries are kept
and stop the write.

The record is unsigned correlation data. The release manifest's normal
signature path binds the checked bytes. The writer grants no native suite,
controller, operation or descriptor authority. A runtime label includes the
actual gVisor items with the rest of the runtime closure.

The uploaded-file check also takes `-gvisor <original-build-root>` and
`-require-gvisor`. It compares the manifest against both original native
builds, then the ordinary asset checks compare the downloaded release bytes.
Missing source pins, another patch, missing roles and old builds stop the
release. They never select an earlier binary.

Both native build jobs and their toolchain preparation must supply these
inputs before the release hook runs. A missing build stops the hook. Source
checks and synthetic ELF fixtures do not establish an actual controller,
memory boundary, guest boot or hardware acceptance.

The signed runtime items bind the build-input and SPDX digests. The input record binds the actual SDK patch, prepared tool hashes, source files, runtime patch and source map, generated module lock, and output files. The SPDX document includes those observed inputs and the five output checksums. All three provenance assets are included in the ordinary release checksums and uploads. The downloaded-file check verifies the signed provenance digests and module-lock binding.

The release workflow prepares both native builds before the release job. It uses isolated HOME, USERPROFILE and all seven XDG roots for those builds. The source epoch supplies the provenance timestamp. This does not pin the runner image, apt packages, Python patch version or every resolved transitive tool. The generated module lock records the admitted build's resolution; it is not a claim that the full dependency closure was pre-resolved. Native build and controller acceptance still require the coordinated runner gates.

The SDK patch filesystem path is `tools/go_types_memoize.patch`. Its exact upstream Bazel label is `//tools:go_types_memoize.patch`; the declaration check preserves that distinction.
