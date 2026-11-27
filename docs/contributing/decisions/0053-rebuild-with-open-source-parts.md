---
status: accepted
date: 2026-11-27
decision-makers: the owner
---

# 0053 Rebuild on the published base with maintained open-source parts

## Context

The integration stack grew this branch to about 450,000 lines of mostly custom Go. Its tests had never passed together, and much of the code defended a threat that spec 3.4 rules out (N5, a compromised login account). The [reuse research](../../research/reuse.md) checked every area against maintained open-source projects. The daily-use path needs a small part of the stack, and well-known projects cover much of the rest.

## Options

1. Slim the merged stack, area by area.
2. Go back to the published base (main plus the 17 finished tasks), keep the research, specs and plans, and build each remaining task with maintained open-source parts.

## Decision

Option 2. If a well-known, maintained open-source library or tool meets a requirement, we use it. We build from scratch only when no available tool can meet the requirement, and one thing at a time. The parts chosen per area are in [reuse](../../research/reuse.md):
- the process wall and the jails: bubblewrap
- sessions: systemd user units
- egress: goproxy, and a model gateway route with no TLS interception on the daily path
- files: SFTP and git bundles
- host trust: the system ssh with pinned host keys
- config checks: go-toml/v2
- a Mac as a host: Lima
- images: go-containerregistry and an official base image
- gVisor: upstream runsc
- microVMs: a thin Cloud Hypervisor driver
- release: GoReleaser, SSH signatures and GitHub attestations

Every task of the plans stays in scope. Tasks that the first working product does not need are done after it, and none is dropped.

The proposed records 0050, 0051 and 0052 are rejected. They served custom machinery that this record replaces.

## Consequences

- Good, because a working product comes much sooner, and most of the code that must be maintained is code that others already maintain and test.
- Good, because the research, the specs and the plans are kept, so the full scope stays visible.
- Bad, because some spec sections describe the custom designs. Each task updates the sections that it changes.
- Bad, because our behaviour now depends on outside projects' releases. Pins, Renovate and the existing supply checks cover that.
