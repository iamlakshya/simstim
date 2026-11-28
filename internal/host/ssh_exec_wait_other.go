//go:build !linux

package host

func waitBeforeReap(int) {}
