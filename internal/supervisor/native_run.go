package supervisor

import "context"

// runSupervisor builds the sandbox, serves the session socket and returns
// after the sandbox has ended and the real supervisor cleanup has joined.
func runSupervisor(owner context.Context, id string) error {
	return runSupervisorMode(owner, id, nil)
}
