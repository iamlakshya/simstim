# Native publication accounting

A native preparation producer can inspect the actual control record inside the existing publication gate. The same gate retains its caller, cancellation, purge and exact active-record checks. It then supplies the already-locked record to the accounting kernel, avoiding a second lock acquisition.

The internal callback creates no operation, publisher, native stopped proof, reservation or capacity permission. Preparation still requires its original native producer and exact current active token and record. Existing publication callers use the same gate and order.
