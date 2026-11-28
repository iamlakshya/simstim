//go:build !linux

package gvisor

func kernelRelease() string { return "" }
