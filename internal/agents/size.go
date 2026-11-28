package agents

// DefaultSize returns the default CPU count and memory cap in bytes for a
// walled session running kind when neither config nor flags name a size
// (isolation spec 17.1, question 14).
func DefaultSize(kind string) (cpus int, memBytes int64) {
	cpus = 2
	memBytes = 4 << 30
	if a, ok := Find(kind); ok && a.Memory > 0 {
		// Round up to a whole GiB so agent GB requirements (e.g. 4 GB) stay >= 4 GiB.
		gib := (a.Memory + (1<<30 - 1)) / (1 << 30)
		if gib > 0 {
			memBytes = gib << 30
		}
	}
	return cpus, memBytes
}
