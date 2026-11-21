# Native helper identity fixtures

A positive helper identity fixture must let the actual protected child create its connected Unix endpoint. The parent accepts that connection and enables credential reception before the first hello can be queued. The current kernel sender and the accepted socket's peer credentials must identify the same actual child.

A socketpair made before fork keeps its creator's peer credentials. Passing one end to the child does not make those credentials describe the child. Such a fixture cannot pass the production native identity check. Keep that check strict when changing tests.

The fixture still binds the native hello to the original atomic creation pidfd, exact helper code and command vector, and refuses reuse after the original child exits. It creates no session, boot receipt, operation, guest or namespace authority.
