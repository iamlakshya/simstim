# Physical probe resource custody

The display-only userspace-kernel driver reports construction facts. Its
physical entry also returns the actual sandbox resource. The original private
host probe owner adopts that resource before interpreting the result, including
errors before a command exists.

The same private purpose fences duplicate probes, joins the producing call on
close and cancels its original context. Cleanup invokes the actual resource's
Destroy and retains it on error. A closed flag is published only after native
and metadata cleanup complete. Repeated Close cannot hide a failed descriptor
close or drop the selected runtime publication lock.

The registered host driver continues to use that physical entry. Probe.OK
still requires the real fixed smoke process, positive original physical exit,
actual cleanup and the final same-purpose checks. Source and compilation do
not establish native availability.
