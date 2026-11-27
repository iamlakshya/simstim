# Native manager resource pools

The original manager retains native close facts for every resource in its
pool. Its retirement check includes the whole pool, including partial
configuration locks and directory handles. A resource with unknown first
close facts keeps that same manager owner alive.

The existing manager diagnostics remain available separately. This check
adds no manager, unit, session or filesystem authority.
