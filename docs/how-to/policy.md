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

The live replacement protocol keeps network and classifier changes separate
from settings that need another start. The applied level, image, mounts,
secrets and SSH keys stay in the trusted running record. A pending view names
those settings separately. A replacement must name the hash it replaces;
a conflicting write reports the current hash without applying the old one.

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
its existing refusal of unnamed IP literals. The first accepted
`allow-unknown` audit for a host raises `egress_unknown` once in the running
egress actor. Existing alert destinations and noise limits still apply. A
live policy change keeps this history; a cold start begins a new one.

This bookkeeping shares the existing bounded destination records. It does
not allocate a synthetic address or send a DNS lookup for a real-DNS or
direct-IP flow. If those records are full, only a new untrackable unknown
allowance is refused, with a resource hint. An existing named grant still
uses its normal policy path.

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

## Public address restrictions

A structured network allowance may set `public_only = true`. It still needs a
matching allow rule, and all locks, denies, method limits and service checks
still apply. The field restricts its physical destination after DNS resolution,
including retries and later policy changes. An older numeric grant cannot open
a refused range or the host itself for a permit that carries this restriction.

The field survives saved layers, cloning and cold starts. Its absence preserves
ordinary rules. A helper must support `network-public-only-v1` before it receives
a restricted rule. The field alone does not complete the held-answer workflow,
and it never permits a secret-bearing target in a saved file.

## Pending network data

Unknown `ask` flows wait before CONNECT succeeds or an absolute request reaches
its origin. A flow timeout or disconnect leaves the question open for its
original `network.ask_open` deadline. A later ordinary policy allowance is
checked again before the original flow can proceed.

Current pending reads use the live egress owner. They include opaque IDs,
remaining waiters and the original expiry. Zero waiters do not close a question.
Refusal uses its exact current ID; a descriptor supplies only descriptive class
and revision data. Displayed hosts are redacted data and grant no permission.
The status reason stays generic because the supervisor has no current secret
redactor; policy reads obtain fresh display rows from egress. Opaque positive
answers and wider publication remain under implementation.

A target containing a known credential cannot be saved as an ordinary policy
rule. Its private engine path supports memory-only once or session scope and
uses the existing matcher and once ledger. A cold actor starts without those
private rules. All numeric held-answer outputs retain `public_only = true`,
including answers made on an own host; an answer never creates a private-address
exception. An independent existing numeric rule still works before unknown
handling. Public positive-action routing and ordinary session publication
remain separate work while this engine path is being integrated.

`policy ls --in <host>/<session>` also lists live held network requests when
its helper supports them. They come before ordinary rules. Each row keeps its
opaque ID, waiting flow count and original deadline, including when no flow is
left waiting. Two private targets can have the same masked display and remain
different requests. Their display text is never a target for an allowance.
With `--json`, `pending_network` contains the complete versioned snapshot. An
older helper omits the field; a stopped sandbox reports that its requests are
unavailable.

## Held network requests in the launcher

Press `p` on a walled session to open its policy. Supported helpers list held
network requests before ordinary rules. Their yellow rows retain separate
opaque IDs even when two private targets have the same masked label. A request
with no waiting flow stays visible until its original deadline.

Select a held row and press `x` to refuse that request, or `r` to refresh the
complete list. Delete removes an ordinary session rule, never a held request.
Closing the sheet or changing its connection cancels queued work. A stopped
sandbox or failed read shows that its live requests are unavailable.

## Apply, edit and learn

A hand edit waits until the next start, or until you apply it:

```sh
simstim policy apply --dry-run
simstim policy apply -y
```

`apply` shows each sandbox's changed layers, then asks before sending them.
With input off, pass `-y` or `--dry-run`. The client sends to saved hosts in
parallel, with a five-second limit for each host. A host that cannot be reached
stays in local pending state. The next contact retries only recorded hosts.
`allow`, `deny`, `rm` and `apply` list missed hosts and still exit successfully.
They do not grant permission to a different host identity.

`policy edit --profile review` opens the selected table in `$EDITOR`, runs the
configuration checks, then shows a dry run. Use `--all`, `--host`, `--repo` or
`--profile` to choose its file. Session policy has no laptop file to edit.

To propose rules from a session's recorded network allowances:

```sh
simstim policy learn box/auth-fix --repo . --dry-run
simstim policy learn box/auth-fix --profile review -y
```

The list gives each destination and its flow count. Without `-y`, pick each rule
at a terminal. `-y` selects every proposal. A masked destination cannot become
a rule. Learning writes only selected rules, then uses the same live apply
path. A proposal or a returned policy value never supplies a permission.

Learning uses the recorded source of each decision. Some older records have
no provenance and do not become proposals.

Push allowances keep their separate repository visibility and public-push
checks. Add one with `policy allow push`; a policy-file edit cannot supply
those answers. Secrets, SSH keys, mounts, images, sizes, paste and isolation
levels always need another start.

A `~` marker shows that policy changes are pending. `show` lists pending
values and the pending isolation level separately from the applied run.

## Pause session allowances

Use `policy lockdown` to apply the locked preset and refuse unknown destinations
on saved hosts. Limit it to one host with `--host`:

```sh
simstim policy lockdown
simstim policy lockdown --host box
```

Session allowances pause, including once and timed rules. Session denies and
broader rules remain. Refused open flows close. The host keeps this restriction
for later starts, including starts made from another laptop.

Lift only that restriction with:

```sh
simstim policy lockdown --off --confirm=lockdown
```

At a terminal, `--off` asks for the word `lockdown` when `--confirm` is omitted.
It leaves local policy edits unapplied and fetches no credentials. A host that
cannot be reached remains pending, and the command fails. The recorded change
is retried at the next contact.

All agents in a sandbox share its model usage totals and token budget. The optional model judge counts towards that budget too. The budget keeps token counts that a provider has already reported when an answer ends early. The displayed total marks estimates made from the request and answer sizes.

HTTP estimates count body prefixes written by the original request and answer connections. Headers, chunk framing and HTTP/2 padding do not count as body bytes. Origin read-ahead and bytes still waiting in the server do not count.

When a policy stops one HTTP/2 request, other allowed requests on that connection can continue. The stopped request keeps its original physical writes until they finish or fail. The existing TLS cipher checks still apply.

WebSocket request estimates count only payload prefixes written to the original model connection. Frame headers, masking keys and control messages do not count. A logical turn keeps its original message writer until that write finishes or fails. An early error in one lane lets the other admitted answers finish.

Responses WebSocket events use their echoed `stream_id` to select an ordered lane. Different lanes can interleave. A provider error without an exact request or lane ends that socket. Each admitted answer then counts as an estimate.

When a WebSocket reaches its token budget, every answer already admitted on that socket can finish. A later request waits for those answers, then the socket closes with code 1008.


## Answer a held network request

Read `policy ls --in box/abc234de` to find the request's opaque ID. Use that
unchanged ID with the exact session ID. A masked destination is display text.
It cannot stand in for a request ID.

```sh
simstim policy allow --request 00112233445566778899aabbccddeeff --in box/abc234de --once
simstim policy refuse 00112233445566778899aabbccddeeff --in box/abc234de
```

Once releases the flows waiting on that request. When none waits, it permits
the next flow. The next flow after those asks again. An ordinary held request
also supports a session answer, or an explicit `--repo forge.example/team/app`
or `--profile review`. A repository or profile answer saves a rule and uses
the normal policy apply path. A target containing a known credential stays
in protected memory; it cannot become a file rule.

In the policy sheet, `o` answers once, Enter answers for the session, `r`
selects an original exact repository section, and `f` selects the original
profile. An organisation pattern does not become a repository answer; use an
explicit `--repo` choice when this laptop has no exact section. `x` refuses.
New sessions with an original local primary retain an empty exact repository
section, after organisation sections and before the profile. The empty section
grants nothing. A later repository answer writes there and follows the normal
policy apply path. Host-reported origins and legacy records cannot create that
original local reference.

Ctrl+R refreshes. The original request, host and connection remain
fixed through questions. A changed source, an expired request or an unsupported
response stops the action. An uncertain answer is not sent again.

## Set an upload budget

Each sandbox has a separate upload budget for each destination host. The
default is 16 MiB per hour. The first counted upload starts the hour. The
counter measures bytes written to the upstream TCP connection, including HTTP
and TLS framing. Model APIs and independently approved push remotes keep their
existing exclusions. A guest label cannot claim one.

At the limit, the upload waits and appears as an **upload budget** request.
Choose once to continue its waiting flows, or the next flow when none waits.
Choose refuse to stop it. The policy sheet offers these two answers. A normal
network allow does not remove the byte limit. Session, repository and profile
answers cannot create a lasting budget exception.

For a higher limit, use the normal selected policy writer:

```sh
simstim policy edit --profile review
```

Set `network.upload_budget` to a size such as `"32M"` in that layer, then
apply the policy. `new --set network.upload_budget=32M` selects it for a new
session. `"0M"` is a zero-byte budget, not an unlimited setting.

Changing policy keeps bytes already spent in the current hour. Raising the
limit can release a waiting upload after the current policy and budget are
checked together. Lowering it does not reset the counter. Once does not refill
the budget; it continues only the flows selected by that original answer.
