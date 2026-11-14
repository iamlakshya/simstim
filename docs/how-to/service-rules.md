# Service rules

A host allowance still meets the service's rules. Package registries and object stores accept GET and HEAD by default. Writes need a network rule that names their path and method.

GitHub also needs a path grant for these operations:

- Creating a repository or a gist.
- Changing a repository's visibility.
- Writing files under `.github`, including workflows.
- Making a runner registration token.
- Adding, changing or removing a collaborator.

A repository metadata update that does not name `private` or `visibility` passes the visibility check. The proxy checks the complete JSON body before it contacts the origin. It refuses ambiguous field names and keeps the validated body bytes unchanged.

Use the network policy command to grant the intended path at the intended layer. A broad host allowance and the `open` preset do not lift these barriers. A narrower deny or a lock still wins.

The request keeps its original permit. Removing its path grant ends the affected live origin, even when another rule still allows the host. A refused service operation enters the network audit and the existing `egress_blocked` alert. The dispatcher sends at most one such alert a minute for each session.
