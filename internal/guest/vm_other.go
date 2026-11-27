//go:build !linux

package guest

func runMicroVM() error { return vmError("microvm guest init requires the Linux guest kernel") }
