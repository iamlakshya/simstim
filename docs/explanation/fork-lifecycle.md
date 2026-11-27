# Fork lifecycle

Fork copies files and starts a new sandbox. It does not copy memory or terminal output. Each child has its own output ring, runtime, memory admission, certificate authority and secret stand-ins.

The outside bridge keeps the source operation and its original native supervisor. It creates a new reservation and operation for each child. The supervisor borrows those exact operations for the copy. It freezes every source writer, copies the workspace and agent state, then thaws the source. A failed freeze renewal invalidates the whole batch. No child starts from that batch.

The supervisor releases every destination borrow before it acknowledges the copy. The bridge then admits and starts each child. A child that does not fit fails with `no_room`. Other children may start. The command reports each outcome.

The child keeps the parent's policy, grants and pull history. It starts with no armed secrets or SSH signer. An ordinary authenticated attach supplies fresh values. Fork never copies a private signer or treats public secret metadata as permission to fetch a value.

The first start renames the parent's branch prefix to the child's prefix before the agent starts. The agent table selects its fork command. An agent without one uses its continue command. The parent's conversation ID selects that command; the child's own ID comes from its new agent.

The native fork path needs a running sandbox on a supported Linux host. Selecting a shell or another member copies the shared files and starts that member's command. Its original member directory receipt and current controller are both checked. A stopped source, or a runtime without an original disk or file descriptor producer, is refused with a reason. The refusal preserves the source.
