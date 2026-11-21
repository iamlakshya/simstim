# Judge runtime ownership

The hot admission path calls the classifier runtime only after the current
policy and fixed checks leave a request open. The runtime repeats that
read-only check under the same policy gate. It uses the existing pending
target entry, engine and once ledger. Its own map holds bounded work only.

The resolved judge defaults to none. An enabled judge defaults to 20 attempts
per minute and a five-second timeout. An explicit rate of zero makes no call.
The rate counter has 61 one-second buckets. Each bucket remains charged until
60 seconds after its latest call. Quota may return less than one second late;
it never returns early. The configured count is not capped at the separate
active-job limit. Arithmetic overflow and a backwards elapsed clock refuse
new work.

Each flow keeps its original classifier deadline. A transparent flow waits
at most three seconds, while its existing job may continue to the configured
judge timeout. An early unsure answer can become an ask, but cannot start a
fresh 20-second wait. The original pending target and expiry remain available
for a later answer and retry. Concurrent callers for one target share work;
closing one caller does not cancel the others.

The source gate checks required host data and every optional string. Sensitive
optional data is omitted. Host data is never masked and treated as a new real
destination. The trusted task prefix is bounded to 4096 bytes and an inspected
path to 256 bytes. No query, body or guest explanation has an input field.
The private immutable source context is issued after this check, and checked
again before model IO and actual publication.

Facts remain evidence only. The registrable domain uses the existing pinned
Public Suffix List. A previously reached tag needs an accepted destination
witness; absence remains unknown. A configured popularity file supplies an
exact positive CSV `rank,domain` witness. Missing, conflicting or unreadable
rows remain unknown. No popularity list ships with the tool.

RDAP is off by default. When enabled by trusted settings, it uses the IANA DNS
bootstrap registry and the matching registry's HTTPS endpoint, following
[RFC 9224](https://www.rfc-editor.org/rfc/rfc9224). Domain registration events
follow [RFC 9083](https://www.rfc-editor.org/rfc/rfc9083#section-4.5). A failed,
missing, unrelated or future registration is unknown, never old. Answers are
cached for a day. Lookups use bounded responses, no redirects or environment
proxy, checked public addresses and the same owned physical IO retirement.
Repository files cannot select a host popularity file or enable RDAP contact.

Audit records of a judge call and answer are observations, not allowed flows.
An audit failure stops publication. Only the canonical grant callback can
install an allow through the current source, target, expiry, policy and scope
checks. Denies count as current refusals, without a second denial ledger.
The runtime elects one delivered generic unavailable notification per session.
That bounded notice cannot renew the original flow's deadline.

Closing the runtime cancels admitted jobs and joins their physical adapters.
Every policy-gate acquisition in a job uses its cancellable context, and
cleanup does not acquire that gate. The source mutation hooks can therefore
cancel and join calls while keeping ownership of the original registry.

The unit source covers quota boundaries and arithmetic, structured source
input, shared work, independent waiter cancellation, actual late once grants,
timeouts, shutdown and unavailable event election. Program cancellation,
native IO retirement and model quality require their respective validator
runs. Source and compilation receipts alone establish none of those results.

Configured command and model adapters require the original source sanitiser
before they perform I/O. The sanitiser receives the complete decoded reason.
It uses the same Egress credential scrubber before removing controls or
limiting the public reason to 200 characters. Missing, null, malformed and
truncated answers publish no partial reason.

All admitted judge jobs register their original source and physical completion
in the existing flow tracker. A job holds one shared outbound slot across its
optional facts and adapter work. Cancellation retains that slot until the
physical adapter has joined. Replacing the source joins commands as well as
model and fact connections before publishing the replacement.
