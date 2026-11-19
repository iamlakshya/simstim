//go:build !linux

package guest

import "syscall"

func (g *Init) fileCommitProcess(run *execRun)(*syscall.SysProcAttr,error){return nil,putError("the private VM file publisher requires Linux")}

// RunVMFileCommitHelper refuses the private VM role off Linux.
func RunVMFileCommitHelper([]string)error{return putError("the private VM file publisher requires Linux")}
