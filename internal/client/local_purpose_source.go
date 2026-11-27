package client

func sameReceiptSource(a, b hostStateSourceWitness) bool {
	if a.validate() != nil || b.validate() != nil {
		return false
	}
	return sameHostTransportSource(hostStateSource{exists: a.Exists, generation: a.Generation, path: a.Path, root: a.Root},
		hostStateSource{exists: b.Exists, generation: b.Generation, path: b.Path, root: b.Root})
}
func sameReceiptScope(a, b sandboxReceiptScope) bool {
	return a.Host == b.Host && a.HostID == b.HostID && a.MemberID == b.MemberID && a.RootID == b.RootID && a.RunGeneration == b.RunGeneration &&
		a.BackendGeneration == b.BackendGeneration && sameReceiptSource(a.Source, b.Source)
}
