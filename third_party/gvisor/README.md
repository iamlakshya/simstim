# Pinned gVisor host patch

The patch applies to upstream commit `7a675ac66a1e2357abdaa6cccedabcd3cce9aa5c`
from `release-20261102.0`. It uses gVisor's Apache-2.0 licence. Upstream source
is at <https://github.com/google/gvisor>.

`hostguard.patch` adds a raw ELF entry before Go package initialisation for
runsc and the Sentry. It clears and verifies dumpability before either reads
configuration. Each freestanding C companion clears and verifies the same flag
before reading its arguments or donated input. Credential and capability
changes reassert this protection. Each exec enters the guard again.

The strict native map reader checks real, effective and saved identities. In
the existing single mapped identity, the Gofer translates native owner 0 to
virtual guest owner 1000 and maps that guest owner back to 0 for creation and
ownership changes. It adds no host identity. Other requested owners fail.

The fixed controller entry reads only its sealed public native object layout.
It creates the Gofer and Sentry through original atomic creation handles and
joins their final barriers. The Sentry positively joins its tiny proc unmount
helper, then re-executes the same process through the same held code. Its raw
final entry applies the prepared filesystem boundary before Go and systrap.
Controller and Gofer use the existing all-thread filesystem-only branch.

The controller publishes its own final filesystem result and aggregate before
accepting the original late guest stream. The outside host must authenticate
that exact creation, current code, namespace and socket chain and recheck its
original operation. A setup frame or transport EOF grants no private input.

Build all companions together on a supported Linux machine:

```sh
python3 scripts/build-gvisor-runtime.py /path/to/pinned-upstream /path/to/new-output
```

The script checks the exact clean commit before applying the patch. The build
also checks every path's exact before hash, type and mode against
`hostguard-inputs.json`, rejects links and requires new source paths to be
absent. The zero-context patch has no whitespace-only context lines. It is
applied with `--unidiff-zero`, then every exact postimage is checked before
Bazel starts. No fuzz, offset guess or whitespace rewrite is accepted.

The build
version suffix is the first eight digits of the patch's actual SHA-256, pinned
in `internal/wall/gvisor/native_build.go`. It builds all five targets in one
Bazel invocation and checks that each Go ELF entry points to the pre-init
guard. It records the real output sizes and SHA-256 values in `build.json`.

The release builder must give those exact files their own release URLs and
include their records in the existing signed release manifest. `build.json`
does not grant execution. The runtime selects the complete signed closure from
the original authenticated operation and checks every opened file's bytes.

Building, source checks and type checks do not establish native memory privacy,
read-only alias protection, file fidelity or recursive PID namespace closure.
Those checks remain required on supported host kernels before availability is
accepted. The Landlock availability gates remain in place.
