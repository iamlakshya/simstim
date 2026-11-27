//go:build linux

package hosttest

import (
	"fmt"
	"os"

	"github.com/iamlakshya/simstim/internal/wall/chv"
)

func runVMHelper(args []string) bool {
	var run func() error
	switch args[0] {
	case "chv-disk":
		run = func() error { return chv.RunDiskHelper(os.Stdin, os.Stdout) }
	case "chv-run":
		run = func() error { return chv.RunVMHelper(os.Stdin) }
	case "chv-exec":
		run = func() error { return chv.RunVMMExec(os.Stdin) }
	default:
		return false
	}
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "the closed microvm helper takes no path arguments")
		os.Exit(1)
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return true
}
