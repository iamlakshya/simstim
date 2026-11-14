# Choose a policy layer

Every rule and setting has a layer. The order is built-in, all sessions, host,
repository, profile, then session. For a destination, the narrowest matching
layer decides. A deny wins over an allow in that layer. A lock wins over every
layer, and an attempt to change a locked value fails.

```sh
simstim policy allow api.example.com --all
simstim policy deny paste.example.net --profile review
simstim policy allow api.example.com --in box/auth-fix --for 1h
```

Choose one layer when adding or removing a rule: `--all`, `--host box`,
`--repo .`, `--profile review`, or `--in box/auth-fix`. At a terminal, leaving
it out asks which layer to use. With input off, it fails with exit code 4.
`--repo .` uses the current repository's origin. The file stores the destination,
not an ID. Each read gives the same rule the same ID.

Use `--dry-run` with `allow` or `deny` to see the result without writing a file
or sending a mutation. Wider layers apply to a running sandbox at its next
start. The answer says so. A session's network grant applies at once.

```sh
simstim policy allow api.example.com --repo . --dry-run
simstim policy ls --repo . --profile review --host box
simstim policy check api.example.com --repo . --profile review --host box
simstim policy rm <rule-id> --repo .
```

`policy ls` names each rule's layer and end time, the values and their origins,
and every lock. `policy check` names the deciding rule and layer. Before the
unknown-site features arrive, unknown flows are refused even when the stored
unknown action is `ask`, `allow` or `classify`.

Only session allows take `--once` or `--for`. `--once` permits one flow; two
flows that arrive together cannot both claim it. A timed rule ends at its
expiry. A restart drops once rules and expired rules. Checks and dry runs do
not consume a live once rule.

## Profiles, lists and locks

Every layer has `sandbox`, `network` and `classifier` tables. Put a named
profile in `config.toml`, then use `new --profile review`. A broader layer may
select it with `sandbox.profile`. The profile comes after repository sections.
Use `new --set network.unknown=deny` for one session.

```toml
[sandbox]
secrets = ["API_TOKEN"]

[lock]
network.deny = ["paste.example.net"]
sandbox.paste = "media"

[repos."forge.example/team/*"]
sandbox.secrets = ["TEAM_TOKEN"]

[repos."forge.example/team/app"]
sandbox.profile = "review"
network.allow = ["api.example.com"]

[profiles.review]
sandbox.secrets = ["!API_TOKEN"]
network.unknown = "deny"
```

Lists add up. A leading `!` removes an inherited item. Environment variables
merge by name. Exact repository sections follow matching organisation sections.
A new session gets its sections from the origin of the folder on the host.
Later starts keep those section names, even if the folder's origin changes.
The trusted manifest keeps the layers and their rules across a restart.

Locks belong at the top level of `config.toml` or in a host's `[host.lock]`
table in `hosts.toml`. Repository files may narrow policy. Their allows require
a review; they cannot set locks. A host's tier still caps every grant. A LAN
address is valid syntax, and an allowance for it needs an `own` host.

A broken `config.toml` stops `new`, `resume` and policy mutations with exit
code 2. Run `simstim config check` to find the file and line. `ls` and `attach`
still run with one warning. They send no layers and arm no secrets.
