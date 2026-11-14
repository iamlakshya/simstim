//go:build unix && !linux

package probetest

func prepareDumpBaseline() string { return "unsupported_outside_linux" }
