package main

import (
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
)

func init() {
	commands[guest.VMFileCommitHelperMode] = guest.RunVMFileCommitHelper
	commands[guest.PackageManagerHelperMode] = guest.RunPackageManagerHelper
	commands[guest.DecoyHelperMode] = guest.RunDecoyHelper
	commands[inputtransfer.HelperMode] = guest.RunInputHelper
	commands[inputtransfer.HelperApplyMode] = guest.RunInputApplyHelper
	commands[guest.PutHelperMode] = guest.RunPutHelper
	commands[guest.CopyHelperMode] = guest.RunCopyHelper
}
