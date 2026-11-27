# Declared sandbox memory

The protected primary manifest is the reservation. A walled primary records
its complete unit cap as `limits.declared`. Process and gVisor walls reserve
that cap even while idle. A VM reserves the canonical guest RAM plus its one
VMM reserve. It adds that reserve once. Members add nothing.

An unwalled session keeps decision 0024. Admission counts its actual native
`memory.current`, rather than its requested cap. Its legacy cap normalisation
still lowers an oversized request to the host budget.

The same placement transaction reads protected records and native canonical
cgroups, computes room, and publishes the new starting manifest through the
original retained session operation. Both the process branch and the signed
runtime branch use this transaction. No separate ledger or service holds room.
The starting timestamp is set at publication, after any slow seed preparation.

Only current-boot primaries count. A native unit counts while its cgroup exists,
even if its socket is dead or its metadata says stopped. A starting record
without a unit counts for exactly two minutes. Future or unknown starting
timestamps are counted conservatively. Unknown required native facts refuse
admission. A smaller declared value cannot understate the actual unit cap.

The actual native start rechecks that same reservation under its short
placement gate before dispatch. An expired reservation cannot start later
against room another process has acquired.

Restart admission captures the original requested size and policy inputs
before stopping anything. This prospective check leaves the old canonical
reservation intact. The final writer acquires the same kernel holder-lock
role, requires the old canonical cgroup to be absent, checks fresh room again,
and atomically publishes the replacement reservation before releasing the
placement lock. Native stop and boot authority remain separate requirements.

Automatic restore has its own private admission entry. Zero preserves the
walled cap. It skips only the specified drain precheck; native installation,
purge-root identity, accounting and publication checks still run. A VM size
change still needs a genuine new Need producer.

The size resolver uses explicit values, template and agent size data, then
2 CPUs and 4 GiB. `agents.Agent.Memory` stays the documentation's minimum
machine memory. It never supplies a per-sandbox default. A walled size is
refused when it does not fit; it is not silently made smaller.

Source models and the two-process publication regression do not establish
cgroup enforcement or VM hardware behaviour. The race regression starts no
agent, namespace, user manager or provider. Its two workers run the actual
placement lock, original operations and first writer against isolated files.
