//go:build linux

package guest

import (
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/filecopy"
	"golang.org/x/sys/unix"
)

func renameFileCommitV1(stage,destination *os.Root,leaf string,replace bool)(returned error){
	if err:=filecopy.Name(leaf);err!=nil{return err}
	if info,err:=destination.Lstat(leaf);err==nil{
		if !replace || info.IsDir(){return putError("the copy destination already exists")}
	}else if !errors.Is(err,os.ErrNotExist){return err}
	data,err:=stage.OpenFile("data",os.O_RDONLY|unix.O_NOFOLLOW,0)
	if err!=nil{return err}
	defer func(){returned=errors.Join(returned,data.Close())}()
	info,err:=data.Stat()
	if err!=nil || !info.Mode().IsRegular() || info.Mode().Perm()!=0644 || !inputOwner(info,AgentUID){return putError("the private validated stage changed before publication")}
	source,err:=stage.Open(".");if err!=nil{return err}
	defer func(){returned=errors.Join(returned,source.Close())}()
	target,err:=destination.Open(".");if err!=nil{return err}
	defer func(){returned=errors.Join(returned,target.Close())}()
	sourceRaw,err:=source.SyscallConn();if err!=nil{return err}
	targetRaw,err:=target.SyscallConn();if err!=nil{return err}
	var operationErr error
	err=sourceRaw.Control(func(sourceFD uintptr){
		if uint64(sourceFD)>uint64(^uint(0)>>1){operationErr=putError("the original stage directory is invalid");return}
		controlErr:=targetRaw.Control(func(targetFD uintptr){
			if uint64(targetFD)>uint64(^uint(0)>>1){operationErr=putError("the original destination directory is invalid");return}
			if replace{operationErr=unix.Renameat(int(sourceFD),"data",int(targetFD),leaf)}else{operationErr=unix.Renameat2(int(sourceFD),"data",int(targetFD),leaf,unix.RENAME_NOREPLACE)}
		})
		operationErr=errors.Join(operationErr,controlErr)
	})
	if err=errors.Join(err,operationErr);err!=nil{return err}
	return target.Sync()
}
