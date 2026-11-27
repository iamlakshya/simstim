# Trusted request lifetimes

Each accepted supervisor connection owns its actual serve stack. Holder relay
and forward workers belong to that same stack. Shutdown closes admission,
cancels connection work and closes the actual lines before backend teardown.
File work is cancelled before teardown and joined afterwards.

The original backend cleanup channel closes only after real backend, guest,
file and observer joins. N5 borrowers can then release their actual incoming
operation loans. Shutdown joins every accepted serve and stream stack before
returning. A durable None cleanup marker must be written after that return.
Closing a socket alone does not establish either of those joins.

These lifetimes supply no native stop, VM, allocation or future resume source.
They do not change attached terminal bytes or the existing queued-input drain.
