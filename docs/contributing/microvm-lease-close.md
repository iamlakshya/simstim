# Retaining an unsettled runtime lease

The VM engine keeps its original agents and runtime lease until closing that
lease succeeds. A failed or pending close leaves the same object available for
another cleanup attempt. A separate close mutex joins concurrent attempts. The
engine's main mutex stays free while the concrete native owner settles.

The normal and separate-account Start paths each install this lease once. A
second Start is refused. Cleanup clears the pointer only after that original
lease closes. An error must never turn an owned pending resource into an
unreferenced object.

The runtime mode implementation must still settle its original native creation
before releasing any execution owner. These retention checks create no native
source or physical-exit proof. They do not qualify an incomplete mode callback
or an unsupported separate-account route.
