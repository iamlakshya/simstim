//go:build !linux

package egress

func hardenProcess() error { return nil }

func processDumpable() (int, error) { return 0, nil }
