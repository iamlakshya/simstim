package host

func seedConnectionAlive() error {
	liveGitOpsMu.Lock()
	defer liveGitOpsMu.Unlock()
	if gitOpsStopped {
		return UserError("git_op", "the connection ended before the sandbox started", "Start the session again.")
	}
	return nil
}
