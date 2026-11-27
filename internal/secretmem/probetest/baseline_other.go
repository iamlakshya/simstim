//go:build !unix

package probetest

func prepareCoreBaseline() string { return "unsupported_platform" }
func prepareDumpBaseline() string { return "unsupported_outside_linux" }
