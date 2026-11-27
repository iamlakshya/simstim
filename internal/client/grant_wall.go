package client

// CheckRequestedWall validates a request's level. Capability and floor checks
// use the accepted connection in ResolveNewWall before values are fetched.
func CheckRequestedWall(level string) error {
	return ValidateWallRequest(level, false)
}
