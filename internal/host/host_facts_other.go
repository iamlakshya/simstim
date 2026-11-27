//go:build !linux

package host

func hostFactsDiskV1(string) (int64, bool)       { return 0, false }
func hostFactsPressureV1(string) (float64, bool) { return 0, false }
func hostFactsBatteryV1() (bool, bool)           { return false, false }
