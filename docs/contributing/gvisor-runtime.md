# gVisor runtime ownership

The supervisor uses one process filesystem preparer for process and gvisor
walls. It preserves the same typed grants, legacy program path, cache and kept
state plan, capture generation and complete private-session disk accounting.
The gvisor factory first requires the accepted outside native operation and
matches its original runtime, primary identity, cap and state projection.
Copying a manifest or an expected-spec context supplies no such receipt.

The host's actual gvisor constructor fixes the runtime issuer, storage domain
and command owner. New runtime storage uses Prepare; an existing private layout
uses Open with the same current original context. Layout bytes never choose
execution pins. Closing factory resources calls the actual driver's prepared
release, which refuses a live or uncertain init. Process Close keeps its
existing metadata-only meaning.

The gvisor driver uses the process wall's mount and kept-state plan. It writes
an OCI bundle with a fresh home and system folders mounted read-only. The bundle
has no hooks, caller environment, image root or OCI user namespace.

The original authenticated client operation supplies the runtime expectation.
A saved bundle, installed-runtime list or VM need cannot supply it. The existing
release manifest names each executable with kind `gvisor`:

| Name | Purpose |
| --- | --- |
| `runsc` | Runtime command |
| `gvisor_sentry` | Userspace kernel |
| `gvisor-sentry-prewarmer` | Sentry bootstrap |
| `gvisor-proc-umounter` | Bootstrap proc cleanup |

The four entries have the same build version, release date and architecture.
The normal runtime store checks each open file against its signed size and
SHA-256. The known-bad list covers the selected closure, including a downstream
version's upstream date. A signed `runsc-fd-parking` entry is optional. Its
absence makes upstream teardown slower.

The namespace owner projects these checked file descriptors into a fixed,
read-only runtime folder. It also consumes the original typed mount identities.
No environment variable chooses a host companion folder. The command sets
`--sidecar-release-enforcement-policy=ALWAYS`, `--network=none`,
`--directfs=false`, `--ignore-cgroups` and `--host-uds=none`. Only the link is
passed into the guest, with `--pass-fd 3:3`.

The existing namespace owner must prove its init has physically ended. A logical
wait error keeps the runtime descriptors, storage roots and exclusive runtime
lock. Kill uses that same owner. Destroy and prepared-resource release refuse
an init whose exit remains uncertain. Runtime cleanup preserves the session's
work, kept state and manifest, and refuses a substituted runtime directory.

The runtime pin and namespace bootstrap must agree. In particular, an upstream
runtime that maps its inner nobody identity to an unmapped parent identity
cannot be used with a single mapped login identity. Its signed build, bootstrap
profile and actual probe must establish the complete path before it is offered
as an available wall. A failed smoke run supplies no readiness evidence.
