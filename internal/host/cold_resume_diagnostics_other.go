//go:build !linux

package host

// There is no native Cold producer or owned native manager on this platform.
func (*ColdResumeV1) CloseDiagnosticsV4() error { return nil }
