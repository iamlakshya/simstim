# Publishing a stopped record

The existing client stop command uses the host's `session.stop` feature. Its
bridge takes the original primary operation and calls a concrete native stop
producer. Ordinary, N5 and manager custody producers keep separate checks.
A stopped row and a transport acknowledgement supply none of those checks.

Only a completed native producer may publish the stopped record. The writer
retains primary and member roots, their receipts and the original manifest
inode through its compare and replacement. Earlier life, status and OOM
information remains in `stop_diagnostics_v1`. Original transport and producer
errors remain errors even after independently proved physical closure.

The projection and exact-file helpers handle data only. They neither certify
clean exit nor modify accepted capture state. They preserve a genuine None
cleanup field and cannot manufacture it from a stop request.
