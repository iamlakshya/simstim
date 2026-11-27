package host

// listens is only the existing bounded local socket reachability probe.
// It proves no process, boot, startup, stop, deletion or grant authority.
func listens(id string) bool {
	if !validID(id) {
		return false
	}
	return alive(id)
}
