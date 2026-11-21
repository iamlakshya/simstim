# Managed ordinary launch roots

A managed ordinary holder uses the existing native launch environment builder.
Its home and XDG path settings come from the actual creating helper. Empty
values clear the manager's ambient settings. The manager cannot select a
different session directory through its own environment. The launch also
removes any stale inherited holder-lock descriptor marker.

The command remains the fixed existing holder command. Environment closure
grants no native lifetime or stop authority. The current code, sender,
invocation, original descriptors and actual process joins are still required.
An admitted managed test must observe the real canonical unit and original
isolated roots. It must fail when those native inputs differ.
