package main

import "github.com/iamlakshya/simstim/internal/guest"

func init() {
	// Both the namespace launcher and the boot image invoke this private
	// command. Keep its versioned arguments with the actual guest parser.
	commands["wall-init"] = func(args []string) error { return guest.Run(args...) }
}
