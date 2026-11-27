# Native manager setup

A failed native manager setup can return its original partial cleanup owner.
Callers retain it before handling the error. It cannot read manager properties,
stop a unit or create a source. A partial manager is not managerless absence,
even when its setup error includes a missing path.

The original caller closes its partial native objects. Unknown cleanup keeps
that same owner and the first diagnostic. It never retries an old descriptor
number or reopens a path to manufacture retirement.

Physical progress uses the same manager's native retirement check. A known
retirement with an I/O diagnostic does not keep an operation parked. The
original constructor still reports setup failure. Owned close paths retain
and report the diagnostic separately; an unknown native result keeps the
original resource and charge. Request cancellation changes the operation's
outcome, not its already proven native retirement.
