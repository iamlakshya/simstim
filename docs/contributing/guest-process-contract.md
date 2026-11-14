# Guest process and PTY ownership

The current process and gVisor contract is `agent-pty1000-v1`. Guest init starts
the holder with fixed UID and GID 1000. The holder starts its PTY child as that
same account. A trusted init running as root installs those credentials and
clears supplementary groups before `Start`. An init already running as the
agent checks its account and groups. Account text never chooses a credential.
Direct exec uses this fixed boundary for every command.

The separate `plain-test-v1` fixture has no wall. Only an explicit test binary
spawner installs its caller-account contract. An environment variable or
mapped root alone cannot enable it. Production process and gVisor supervisors
refuse that schema. The fixture preserves the earlier shutdown, event-drain
and owned-group assertions.

The microvm contract is `guest-owned-agent-pty-v2`. Its implementation belongs
to guest init. It must be present before a VM driver advertises a matching
ready capability. The current process holder does not implement that split.

For v2, init owns the UID 1000 child, its process group, the PTY master, resize,
signal and wait. The UID 900 holder owns terminal rendering, replay and its
session socket. It receives a dedicated inherited stream, chosen by init,
with a session ID and a fresh opaque run token. It receives no arbitrary host
PID, UID, command path or signing material.

The inherited stream uses the existing frame format. Init sends exact output
bytes and one terminal result after the final output. Holder sends input bytes,
resize and an allowed signal request for that session and run token. Init
applies requests only to its still-owned child. A new run invalidates the old
token. Input EOF, holder death, guest-link loss, cancellation and stop each end
the same owned group. Init sends its last group signal before reaping, then
joins the PTY reader before reporting the terminal result.

Only init resolves the fixed account and starts that child. The holder does
not switch credentials or inherit root. Guest init still treats holder events
as guest claims when forwarding them to the trusted supervisor. The trusted
supervisor derives sandbox lifetime from its owned runtime, not those claims.

Logical stream abort revokes every stream and queued frame. It does not close raw transport or wait for raw I/O. The same transport owner must still close it and join the reader and writer before releasing their permissions or buffers. A frame that finishes after abort cannot reopen a stream or acknowledge queued work.
