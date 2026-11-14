package main

import (
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
)

func init() {
	commands[inputtransfer.HelperMode] = guest.RunInputHelper
	commands[inputtransfer.HelperApplyMode] = guest.RunInputApplyHelper
	commands[guest.PutHelperMode] = guest.RunPutHelper
	commands[guest.CopyHelperMode] = guest.RunCopyHelper
}
