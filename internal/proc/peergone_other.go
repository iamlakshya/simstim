//go:build !linux && !darwin

package proc

import "io"

func dupPeer(int) (int, bool, bool)             { return -1, false, false }
func hungUp(int, bool) bool                     { return false }
func watchBlocking(int, bool, func()) io.Closer { return nil }
