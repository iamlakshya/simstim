# The original removal process and its scope

The original outside helper starts the copied removal process itself. It keeps the same command, atomic child pidfd and wait result. A manager-spawned replacement does not own that creation. Creating a new session with `Setsid` does not move a process out of its login cgroup.

Before the real start, the helper captures the native user manager and creates a reaping gate. A constructor which cannot finish its own cleanup returns the same unready scope and its original native handles. The starter retains it before handling the error. No replacement scope is created. The only `Cmd.Wait` reader stays behind that gate. The original child remains unreaped while the manager may still attach its PID. Request cancellation, a client disconnect and an observed child exit do not open the gate.

The helper makes one fresh transient scope outside the sessions' slice. Managers with the supported PIDFDs interface receive the actual original creation handle. Older managers receive the original child's PID while the same unreaped reservation remains held. The tagged systemd implementations still attach through a numeric write to `cgroup.procs`. PIDFDs is a wire format, not proof of an atomic migration. An uncertain call is never retried using the PID.

Admission requires the original manager reply and job, unit invocation, native cgroup description and actual child membership. A completed job without live native membership is insufficient. Only then can the original creator lend its private code directory and locked file description. The child still derives its own domain and operations.

A fifth inherited eventfd delays bootstrap exit while adoption is unresolved. The child waits before checking its original parent's lifetime or responding to cancellation and EOF. The eventfd announces settled lifetime bookkeeping only. It grants no domain, operation or code admission. A lost manager reply retains the original parent stack, child and reaping hold. The bridge cannot finish that outstanding constructor.

Cleanup first joins the same original child and its actual wait result. It checks vacancy through the retained original cgroup and refuses a replaced group or unit. If necessary, it stops only that verified empty scope, and retains the original stop job through completion. It releases the manager, cgroup and code loans after the scope is gone. A failed close keeps its original closure objects for observation; an unknown native manager close remains a refusal.

No manager means no scope-backed lifetime guarantee. The scope preflight refuses before starting the copied child. No session operation or destructive request has been admitted at that boundary.

The bootstrap gate covers cooperative cancellation, disconnect and parent death. Simultaneous external termination or OOM of both parent and child remains a separate native acceptance gate. It is not certified by retaining a pidfd or by source compilation.

The public upstream evidence is the [v255 scope parser](https://github.com/systemd/systemd/blob/v255/src/core/dbus-scope.c), [v257 attachment path](https://github.com/systemd/systemd/blob/v257/src/core/cgroup.c) and [v257 cgroup write](https://github.com/systemd/systemd/blob/v257/src/shared/cgroup-setup.c).
