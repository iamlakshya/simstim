//go:build !linux

package host

// The paired None constructors refuse this platform before creating an owner.
// Its empty representation has no manager or directory transaction to report;
// native Check, Resume and restart-writer refusals remain authoritative.
func (*NoneColdResumeV1) CloseDiagnosticsV4() error { return nil }
