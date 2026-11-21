# Judge evaluation corpus

The opt-in `TestJudgeEvaluation` reads reviewed JSON lines from
`JUDGE_EVAL_CORPUS`. Keep observations and reports in the repository's ignored
`private/` folder. Do not publish destinations from real sessions as fixtures.

Each line holds `id`, `input`, `want`, `label`, `origin`, `source` and `basis`.
`input` is the version 1 judge request. `want` is `allow` or `deny`. A case
expected to allow needs an independently reviewed `benign` label and origin
`observed-traffic`. A case expected to deny needs label `incident`, origin
`incident` or `lookalike`, and a stable `incident` identifier. `source` and
`basis` identify the evidence and independent reason for the label. A recorded
allow is only a candidate observation; it does not establish benign ground
truth. Generated plausible destinations do not qualify as observed traffic.

A run requires at least 200 independently labelled benign observed cases and
200 incident-derived or lookalike cases. Repeated host/port destinations cannot
inflate that minimum through changed paths or tasks. Case and incident
identifiers are normalised to prevent case, space and separator aliases from
inflating the report. The report gives the number of distinct incidents too.
`JUDGE_EVAL_COMMAND` names an absolute host executable that
implements the JSON judge protocol. `JUDGE_EVAL_MODEL` records the actual model
and version. Run the same corpus once with a local model and once with an API
model. The executable may read its own approved configuration; its environment
does not receive session secrets.

Set `JUDGE_EVAL_REPORT` to a new absolute path inside a `private/` directory to
save a JSON report with mode 0600. Existing files are never overwritten.
Sources must be regular private files. Input is bounded to 16 MiB and 10,000
unique cases. Invalid labels, missing evidence or too few cases stop before
the first judge call.

For an existing private net audit log, `JUDGE_EVAL_INGEST=1` enables the separate
`TestJudgeObservationImport` intake. `JUDGE_EVAL_AUDIT` selects its regular
private source and `JUDGE_EVAL_CANDIDATES` selects a new private output path.
The output preserves the source hash and line references. It holds unlabelled
candidates only. It cannot be used directly as the reviewed JSON-lines corpus.
Attach original trusted task and seed context and supply independent labels
and their basis during review. The importer makes no judge or network call.

The report counts wrong allows over denied cases, wrong denies over benign
cases, and unsure answers separately. It also reports the fixed steps alone.
Timeouts and malformed answers remain unsure. Publish no quality rate until
both runs have measured it. The harness contains no fabricated observations
and makes no claim that an evaluation has run. Synthetic `.invalid` unit cases
test the runner's counters only; they are never empirical corpus evidence.
