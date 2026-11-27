# How a process sandbox freezes

A process sandbox has its own child cgroup. The supervisor stays outside it.
The kernel places the namespace launcher in that child before it runs. Every
guest process inherits the cgroup, including shell jobs with their own process
groups. The guest gets no cgroup descriptor or writable cgroup mount.

Fork writes `1` to that cgroup's `cgroup.freeze` file. It waits until the kernel
reports `frozen 1` in `cgroup.events` before copying. A freeze lasts at most five
seconds. A longer copy renews it before the lease ends. An expired lease makes
the copy fail, since freezing again cannot repair a gap in the copy.

The supervisor thaws the same cgroup after the copy, on cancellation and before
it stops the sandbox. A timer also thaws it when a lease ends. The helper keeps
the original descriptors after a failed thaw. It removes the cgroup only after
the kernel reports that it is empty.

The systemd user unit delegates its cgroup to the supervisor. A session started
without that delegation can still run, but cannot fork. The error gives the
reason. A stopped process group is never used in place of the cgroup freezer.
