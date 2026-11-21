# Releasing the unwalled supervisor path

P6.3 depends on one release with walls enabled by default. The owner must merge and publish this change only after that release. This is a release order requirement. The private feature branch implements the real production path; a disabled runtime flag does not stand in for the prerequisite.

New `none` sessions use their original reserved operation and native supervisor startup. The guest init shares the host account and every namespace. It uses the ordinary holder protocol and link. It creates no wall and starts no egress process. The old ordinary holder entrypoint remains for holders that were already running.

Before publication, run the host runtime tests and attach fixtures on the new path. Compare every namespace of the guest init with the supervisor through `/proc/<pid>/ns/`. Confirm that no egress process starts. Run the key-to-PTY benchmark against the legacy path with 1,000 keys in each sample, and report its distribution against section 12 of the isolation spec.

The original native lifecycle sources remain required. A clean None stop records its exact boot, run, original directory and holder identity only after the actual init, terminals, streams and controller workers join. The record is canonical data. A same-boot managerless restart also needs the actual outside operation, the same rooted holder lease and current checks which refuse surviving target actors. A changed-boot restart uses its separate real kernel epoch. No None record becomes a walled stop, a VM source or an artefact grant.

Cancellation before any attempted native dispatch may restore only the same source's exact prior canonical bytes after checking the staged record. Any failed or uncertain dispatch keeps the old cleanup record invalid. Unknown old holders, missing native custody and uncertain cleanup remain preserved and refused. A stopped row or a saved PID never authorises a replacement process.

Check the full stop-to-resume path with another healthy session and an unrelated process whose executable cannot be read. The process scan must refuse only actors associated with the target's actual run or rooted members. A clean `max_run` end uses the same complete cleanup requirements as a manual stop. The host's original floor and explicit downgrade remain recorded; harmless selectors do not become applied sandbox policy.

The production host scope is Linux. A Mac client reaches a Linux host or a Lima Linux VM. Darwin host-test fixtures may explicitly retain the historical ordinary-holder test path through the existing test-driver seam. Those tests protect client compatibility; they provide no evidence for the new Linux runtime, namespaces, completion source or performance target.
