# Trusted judge metadata

Judge metadata describes the selected seed. It grants no permission to start,
read a host path, reach a destination or publish a policy rule.

The host reads ecosystem tags through the existing jailed seed Git operation.
The laptop reads them through the same owned Git source and selected staging
commit as its bundle. Both use `ls-tree -z --name-only` with the fixed literal
root manifest names in `internal/seedmeta`. Neither reads manifest contents,
loose working-tree files or the guest's repository. A dirty seed uses its
selected prospective commit, just as the bundle does.

The laptop's tags form part of the authenticated `InputRepository` declaration
and its existing digest. The exact decoder checks their field. The data uses
the canonical owned repository clone. The protected host `Seed` retains the
same tags, and every file-plan clone owns its copy.

The supervisor captures the remote, ecosystems, agent and task from the
original protected manifest under the existing start lock. It takes the
profile from that start's resolved policy. Remote sanitisation drops URL
credentials, queries and fragments. Local paths and ambiguous forms supply no
remote fact. No guest reply refreshes these fields.

Egress must check every string against its current source secrets before a
foreign model call. A returned reason needs that check too. A valid host or
path may itself contain a known value. The adapter's JSON quoting and bounds
do not replace this source check. The fixed network and credential controls
still decide whether an allowed destination can carry the actual request.

Unit checks cover the exact selected-commit query, changes to its argument
vector, closed tag validation, owned clones, digest binding, remote credential
removal and the original start projection. They measure no model accuracy.
