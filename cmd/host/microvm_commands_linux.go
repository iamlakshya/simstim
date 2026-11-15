//go:build linux

package main

import (
	"os"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall/microvm"
)

func init() {
	// These names remain shared by both VM backends. The inherited files
	// and closed versioned input carry the plan, never command arguments.
	commands["chv-disk"] = closedVMCommand(func() error { return microvm.RunDiskHelper(os.Stdin, os.Stdout) })
	commands["chv-run"] = closedVMCommand(func() error { return microvm.RunVMHelper(os.Stdin) })
	commands["chv-exec"] = closedVMCommand(func() error { return microvm.RunVMMExec(os.Stdin) })
}

func closedVMCommand(run func() error) func([]string) error {
	return func(args []string) error {
		if len(args) != 0 {
			return &proto.Error{Code: "wall_init", Message: "the private VM helper takes no path arguments",
				Hint: "Start the sandbox through its supervisor with the checked VM plan."}
		}
		return run()
	}
}
