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
and every lock. `policy check` names the deciding rule and layer. An actually
configured unknown action decides only a flow which no rule covers, including
an otherwise open preset fallback. Named rules, locks, agent hosts and matched
preset entries still decide first. A legacy policy with no unknown setting
keeps its old preset behaviour; the merged built-in `ask` is a real setting.

An explicit `network.unknown = "allow"` opens uncovered DNS hosts and public IP literals.
Its audit decision is `allow-unknown`. It does not open a refused range or the
host's own address, and it does not become a numeric exception for private or
shared addresses. The `open` preset without that explicit unknown action keeps
its existing refusal of unnamed IP literals. First-host alert delivery is
still being completed.

With `network.unknown = "classify"`, uncovered flows first meet the fixed
destination checks. They refuse unnamed IP addresses, the listed exfiltration
domains and random-looking labels of at least 40 bytes. The starting randomness
threshold is 3.5 bits per byte; it has not been measured against real traffic.
Canonical A-labels are decoded for confusable comparison with currently
allowed hosts. This uses the complete Unicode 15.0.0 data and UTS #39 revision
26, matching the normalisation tables active under the pinned toolchain. A
comparison never consumes a lease or overrides a deny, method limit or expiry.
It is not the newer Unicode algorithm; changing versions needs a matching
data and normalisation update.
Other uncovered flows still refuse while the hold and judge paths are being
completed. This fixed stage is not a completed `ask` or judge implementation.

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

## Choose model capabilities for one session

Use `policy allow` with `--in` and `--tools` to allow named capabilities at an exact model host. The names are `web_fetch`, `web_search`, `code_execution`, `mcp`, `url_content` and `files`. Repeat the flag or separate names with commas. An empty list is an error.

```sh
simstim policy allow api.openai.com --in box/auth-fix --tools web_search,files --dry-run
```

Tools apply only to session allowances. They cannot appear on a deny or a wider file layer. `--once` and `--for` keep their existing lifetime rules. An older helper refuses a tools allowance, including its dry run. Ordinary allowances keep their existing IDs.

## Local input preparation

A new session keeps one local settings snapshot through policy questions,
input preparation and secret fetching. Changing that file ends the preparation.
A laptop seed uses the policy from its selected commit. A dirty seed keeps one
private prospective commit for both the policy check and the bundle capture.
Only the primary repository selects the sandbox policy.

Local copy expressions and their original working directory stay on the client.
They are saved with the original local policy selection, rather than sent as
policy paths to the host.

The client retains only the current input operation receipt after an uncertain
transfer. A completion retry checks that original receipt against the same
supervisor run. It can finish the acknowledged operation without reading
laptop files or sending the payload again. An incomplete or changed operation
keeps its receipt and reports the uncertainty.

A laptop primary seed requires a helper and guest that verify the policy from
its exact selected commit, including when that commit has no policy file.
Terminal defaults, environment values and copy expressions use the same retained
local settings operation. A configured secret name cannot enter through ordinary
terminal metadata or environment lookup.

Setup and folder questions keep the original settings descriptor between
network attempts. Each attempt keeps its existing deadline. Ending the logical
request blocks late answers and new work, including while resource cleanup is
still finishing. A retry cannot renew a changed settings source.

## Service write boundaries

A broad GitHub host allowance keeps protected service writes closed. File-content writes need an explicit branch under `<name>/<session-id>/`; a missing branch selects the repository's default and stays closed. Git reference writes use `refs/heads/<name>/<session-id>/`. Own-prefix API ref deletion follows the usual network policy. Merges, pull-request merges, asynchronous merges, upstream sync and branch renames need a path grant for their actual endpoint.

An explicit path allowance can open a protected API write outside that prefix. Its normal method restriction still applies. An omitted method restriction keeps its existing unrestricted meaning. Locks and denies still win. Invalid or unsupported request shapes are refused before a lease or origin connection, even with a path grant. Workflow, visibility and other service barriers compose with the ref boundary.

Only the required endpoint lease is acquired first, so a broad host grant cannot hide it or spend another once grant for the same request. Removing or expiring that endpoint allowance closes its protected request without falling back to a broad host permission.

API permissions can authorise a commit to a public repository. The local public-repository acknowledgement applies to repository push grants. Treat an API write-path grant as authority for the operation it names. The ordinary TLS splice boundary still limits which requests the proxy can observe.

GitHub GraphQL requests use their selected operation, resolved variables and defaults, aliases, fragments and conditional selections. The seven specified ref-moving mutations follow the same namespace and explicit endpoint rules as REST. Opaque ref or pull-request IDs do not prove a session branch; those writes need an endpoint grant. A malformed or unresolvable guarded mutation is refused before a lease or origin, including invalid string escapes. Other mutation names keep their ordinary policy rules.

Repeated fragments are classified once within the selected request and its variables. They share no permission across requests. Unicode scalar, fixed-pair, UTF-8 and block-string input forms use the same branch check. Unsupported transport or request envelopes stay closed even with a path allowance.

Listed token-creation and rotation endpoints on GitHub, GitLab and OpenAI need
an explicit endpoint allowance when the exact host is bound to a secret. This
also covers anonymous requests to that bound host. A broad host allowance does
not open these endpoints. The existing model path checks, method restrictions,
locks and denies still apply. Retired bindings retain the same protection.

Discord webhook paths and Telegram bot paths need an explicit endpoint
allowance, including reads and custom methods. Writes to Pastebin need one too;
its `GET` and `HEAD` requests keep ordinary network policy. These hosts are
inspected even after a broad host allowance. The endpoint's normal method
restriction applies, and an omitted method restriction retains its unrestricted
meaning. The same original permit handles both endpoint and network authority.
