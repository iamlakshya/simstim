---
status: proposed
date: 2026-11-21
---

# 0052 Build the original HTTP/2 hooks with an exact official compiler

## Context

HTTP interception owns selected native writes and retires streams through the
pinned public HTTP/2 scheduler hooks. The default Go 1.27 wrapping server does
not call those hooks. Unknown builds must refuse before reading requests or
forwarding credentials.

Go 1.26.11 omits dependency modules from ordinary non-main test binaries. Its
strict runtime guard therefore refuses the same binaries that the standard
race and coverage commands produce. An ordinary main driver cannot replace
native test discovery, coverage and fuzzing.

The official compiler fixes this producer in Go 1.27
([issue 76926](https://github.com/golang/go/issues/76926),
[released source](https://github.com/golang/go/blob/go1.27.4/src/cmd/go/internal/load/test.go#L403)).
The pinned module [documents `http2legacy`](https://github.com/golang/net/blob/v0.62.0/http2/README.md)
to select the original implementation on that compiler.

## Options

1. Build with official Go 1.27.4 and the documented legacy implementation.
2. Keep Go 1.26.11 and change test package identities and coverage aggregation.
3. Replace the native ownership hooks with another supported design.

## Decision

Qualify option 1. Pin exact Go 1.27.4 and unreplaced `golang.org/x/net` 0.62.0,
with its original module checksum. Require the actual compiled legacy tag
and genuine runtime build metadata before interception. A missing tag,
unknown module, replacement or unqualified compiler remains a refusal.

The shared `scripts/go-build-tags.sh` contract gives main builds, helpers,
tests, source analysers and releases the complete
`userns_cohort_v3,http2legacy` list. It does not infer tags from caller
metadata or append a second `-tags` flag that would erase the cohort.
The policy-header emitter alone uses the legacy-only bootstrap mode before
the C outputs exist. Once the carried-code producer is part of the source,
both actual static launchers precede its strict identity generator, and the
complete tag list excludes the empty Linux fallback. Missing preparation
fails rather than issuing unsupported code. Source trees without that
producer do not gain CODE authority merely from a tag. Independent
verifier and gVisor toolchain and module preparation retain their own pins.
Raw Go build and install instructions select the exact compiler and tag.

## Consequences

- Standard Go test discovery, race checking, coverage and fuzzing remain in use.
- All test counts, coverage floors and performance gates remain binding.
- A new compiler changes cache identity and native behaviour. Existing client,
  runtime, scheduler, cancellation and resource checks need fresh qualification
  before this choice is adopted.
- The original module receives critical and security fixes only. Later upgrades
  still need explicit hook and native-behaviour review.
