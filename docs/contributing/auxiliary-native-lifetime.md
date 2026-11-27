# Auxiliary stopped readers

A stopped pull can use a separate, closed helper and a fresh microvm. This operation retains the original primary operation lock. It does not start an agent, deliver secrets, install an egress service, or change the saved ordinary run or lifecycle.

The first implementation accepts a protected saved run from a different native boot epoch. It requires the original holder lock to be vacant and both the ordinary and auxiliary units and cgroups to be absent. A current-boot unit name, saved PID, stopped flag or pulled-commit list cannot establish that every old process ended. Current-boot and unmanaged stopped readers still require a genuine stop owner. Missing, subordinate-owned or unrecognised native disk layouts are preserved and refused by this first path.

Before activation, the outside owner captures the actual runtime, image, agent disk and existing session disk descriptors. It publishes a dedicated auxiliary reservation through the same protected operation and placement gate. A separate operation kind fences older helpers. Fresh accounting admission precedes the native activation.

The new fixed auxiliary unit has the complete declared memory limit and zero swap. The original authenticated manager, its first creation job, actual native process handle, executable, literal arguments and cgroup membership are checked before lending any private descriptors. The loan contains the same original operation and holder descriptions and the original saved disk descriptor. Correlation fields cannot create a loan.

The closed helper opens the existing microvm layout. Its actual engine owns the native VMM connection and performs the headless prepare handshake. Only bounded copy reads and the fixed pull exporter can cross its carrier. Other control operations, network requests and guest-opened streams are refused.

Cleanup retains the original operation, source descriptors and reservation until the helper creation handle signals exit and both native units and cgroups are absent. A control acknowledgement, EOF or cancellation cannot release them. Lost native ownership remains unresolved and retained. The auxiliary leaf is cleared only after physical settlement. Local review, pull completion and any later removal retain their separate checks.

This source path still needs native cancellation, process death, disk identity, manager-loss and race coverage. It does not supply current-boot safe-prune or whole-host purge authority. Copy RPC routing to stopped readers and other wall backends remain separate work.
