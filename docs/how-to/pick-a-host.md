# Pick a host

Name a host when you want one particular machine. A name in `new`, or a host
that you choose in the launcher, wins over every host rule. The grant gate
still checks whether that host may receive the requested inputs and secrets.

```sh
simstim new box
```

Use `-` for the host of the last session you attached to. Use `auto` to choose
from the automatic placement pool.

## When you name no host

The client uses this order:

1. The session's `sandbox.host`, set with `--set`.
2. The selected profile's `sandbox.host`.
3. The most specific matching repository section's `sandbox.host`.
4. `SIMSTIM_DEFAULT_HOST`, then the saved default from `host default`.
5. The only saved host that is not shared.

If none selects a host, `new` stops and lists the saved hosts. The
launcher fills its host row from the same choice. You can change that row.

```sh
simstim host default box
simstim new --profile review
```

## Rules for repositories and profiles

A rule can name one host, an ordered list of hosts, or `auto`.

```toml
[repos."forge.example/team/*"]
sandbox.host = ["box", "other"]
sandbox.secrets = ["DEPLOY_TOKEN"]

[repos."forge.example/team/infra"]
sandbox.host = "other"

[profiles.review]
sandbox.host = "review-box"
```

The client tries a list in order. It starts on the first host that answers
and admits the session. It says which hosts it passed over and why, such as
`box has no room, so other`. A rule that names a shared host counts as a
named choice. The host's tier and every grant restriction still apply.

Repository rules use the origin of the first `--seed` folder, or the current
folder when you give no seed. A folder that `--dir` names on a host cannot
pick the host. The client reads the local origin through its checked metadata
reader before it contacts any host.

It removes the origin's scheme, user and final `.git`. It changes the host
part to lower case and keeps the path's case. For example,
`git@forge.example:team/api.git` becomes `forge.example/team/api`.
The host part of `https://FORGE.EXAMPLE/team/api.git` is normalised in the same way.

A final `/*` matches the named path and its descendants on `/` boundaries.
`forge.example/team/*` matches `forge.example/team/group/api`. It does not
match `forge.example/team-labs/api`. Other wildcard forms are refused.

Every matching repository section applies. Broad `/*` sections come first,
then narrower sections, then the exact name. A narrower host value wins.
Lists still add up. In the example, `infra` uses `other` and still selects
`DEPLOY_TOKEN` from the broader section. A selected profile overrides the
repository's host choice. The session's own `--set` overrides the profile.

Put host rules in repository sections, profiles or the session's own settings.
The all-sessions layer, locks and a repository's own settings file cannot
select the host. `config check` also reports a rule that names an unsaved host.

## Keep a secret on particular hosts

A secret's `on_hosts` names the saved hosts that may receive it. This adds a
restriction to its tier requirement.

```toml
[secrets.DEPLOY_TOKEN]
run = "keychain-read deploy-token"
on_hosts = ["box", "other"]
```

Replace the command with your own secret reader. An absent `on_hosts` adds no
host restriction. An empty list refuses every host. If the chosen host is
outside the list, the start stops with `secret_not_here` before the client
runs the command or records a grant for that host. The error helps you choose
a host that would pass. A matching name never raises the host's tier.

## Labels are notes

Labels appear in `host ls`, `host show`, JSON output and the launcher's host
details. Provider scripts receive them too. A label never selects a host or
allows a grant.

```sh
simstim host set box label.location=home
simstim host set box label.location=
```

An empty value removes that label. Automatic placement's pool and feature
settings are separate from these notes.
