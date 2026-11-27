# Open a sandbox on any of your hosts

Use `simstim new auto` to choose a host from your saved pool. Add `--dry-run`
to see the eligible hosts and the reason for each choice without starting a
session.

The client checks local tiers, selected credentials and declared features before
it contacts a host. It then compares host facts with the sandbox's size and the
values saved by `host prepare`. Automatic placement requires `gvisor` or
`microvm`. It does not choose `process`.
By default the request needs `microvm`. Name `--wall gvisor`, or configure
`sandbox.wall = "gvisor"`, to permit `gvisor`. The client uses the host's
pinned floor to select the actual level. It skips a host whose selected level
is too weak. Prepare a host before asking it to choose a default level.

Use `--need arch=arm64,gpu` to require an architecture and a local feature.
Set the feature on each matching host in `[host.place]`:

```toml
[host.place]
features = ["gpu"]
```

An own-tier credential still requires an own host. `--need tier=shared` and
`--need tier=throwaway` add the named tier to the pool when its other checks
pass. They do not change any credential's minimum tier or approve a grant.

The client prefers awake hosts, then free wakes, then paid wakes. Within each
wake class, pressure, battery use and recent placements affect the order.
`[placement] prefer` ranks named hosts next. Cached images, nearby hosts, room
and the current sandbox count break the remaining ties.

```toml
[placement]
strategy = "spread"
prefer = ["box", "nuc"]
wake = "ask"
```

`spread` prefers the most room. `pack` prefers the least room that fits.
`wake = "never"` prevents automatic wakes. A paid wake asks before it starts.
`--allow-wake` or `wake = "always"` skips that wake question. Secret and other
grant questions still apply.

A stopped host needs previously recorded facts. The client reads fresh facts
after waking it and repeats the filter before preparing the session. Facts and
the local placement record do not reserve capacity on the host. The host still
checks admission under its own lock when it receives the start.

The client tries at most three hosts. It moves on only after the original
attempt has proved that it did not start a session and has finished its local
cleanup. If a start may have reached a host, it keeps the original record and
reports the uncertainty.

`ls --json` includes `placed_by` and `place_reason` for a session placed this
way.

A current helper can prove a capacity refusal before dispatching the primary
start. The client waits for that attempt to close before trying another host.
An older helper cannot supply that proof. In that case the client keeps the
original attempt and stops. Update the helper before starting a fresh request.

The room estimate counts a pending start once when the native sample already
includes it. A completed start that has stopped does not keep a spare memory
reservation for a minute. Placement history remains available for balancing.
