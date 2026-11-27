package client

// FailHostTierWritesForTest makes host tier writes fail until the returned
// function restores them. It is only compiled for this package's tests.
func FailHostTierWritesForTest(failure error) func() {
	old := writeHostTierFile
	writeHostTierFile = func(Tiers) error { return failure }
	return func() { writeHostTierFile = old }
}
