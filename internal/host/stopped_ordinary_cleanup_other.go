//go:build !linux

package host

func (*purgeOwnerV1) destroyOrdinarySessionResourcesV1(*SessionOperation, *StoppedOrdinarySessionV1) error {
	return sessionLeaseError("the actual ordinary native storage consumer requires Linux")
}

func (*purgeOwnerV1) finishOrdinarySessionMetadataV1(*SessionOperation, *StoppedOrdinarySessionV1) error {
	return sessionLeaseError("the actual ordinary native terminal cleanup requires Linux")
}

func (*purgeOwnerV1) removeOrdinaryWorktreeV1(*SessionOperation, *StoppedOrdinarySessionV1, *Manifest) error {
	return sessionLeaseError("the actual ordinary native worktree cleanup requires Linux")
}
