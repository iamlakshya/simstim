//go:build !linux

package host

// The paired constructor creates no native main or retained resources here.
func (*ManagerCustodyMainV1) CloseDiagnosticsV4() error { return nil }
