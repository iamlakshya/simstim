package host

// ownedMainPauseV2 belongs to the actual original main creator and is kept
// by that holder until restoration succeeds. Every field is under main.mu.
// It supplies no ordinary session, walled, storage or whole-cgroup Source.
type ownedMainPauseV2 struct {
	holder              *Holder
	main                *holderMainV1
	changed, wasStopped bool
	wasExited, closed   bool
	committed           bool
}
