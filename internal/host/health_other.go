//go:build !linux

package host

func hostHealthDiskTotalV1(string) int64               { return 0 }
func hostHealthNativeV1() (reboot, linger, known bool) { return false, false, false }
