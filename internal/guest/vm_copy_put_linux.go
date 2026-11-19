//go:build linux

package guest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/filecopy"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

// ownedVMFilePut takes the actual VM original mount producer path only. A
// process wall is handled by its outside factory, never by a fake guest cap.
func (g *Init) ownedVMFilePut(ctx context.Context,stream *link.Stream,header proto.FilePutHeaderV1)(handled bool,returned error){
	vm,ok:=g.vm.(*vmRuntime)
	if !ok || header.Path=="" || header.Directory{return false,nil}
	transaction,err:=vm.beginFileCommitV1(ctx,header)
	if err!=nil{return true,err}
	defer func(){returned=errors.Join(returned,transaction.close())}()
	if err:=filecopy.ReceivePreparedStage(ctx,transaction.file,header.Size,header.SHA256,stream);err!=nil{return true,err}
	if err:=vm.connection.check(g);err!=nil{return true,err}
	if err:=transaction.commit(ctx);err!=nil{return true,err}
	return true,nil
}

func privateFileCommitPairV1()(net.Conn,*os.File,error){
	fds,err:=unix.Socketpair(unix.AF_UNIX,unix.SOCK_STREAM|unix.SOCK_CLOEXEC,0)
	if err!=nil{return nil,nil,err}
	ours:=os.NewFile(uintptr(fds[0]),"private-file-commit-parent")
	theirs:=os.NewFile(uintptr(fds[1]),"private-file-commit-child")
	connection,err:=net.FileConn(ours)
	ours.Close()
	if err!=nil{return nil,nil,errors.Join(err,theirs.Close())}
	return connection,theirs,nil
}

func (t *vmFileCommitV1) commit(ctx context.Context)(returned error){
	if err:=t.owner.connection.check(t.owner.init);err!=nil{return err}
	connection,child,err:=privateFileCommitPairV1()
	if err!=nil{return err}
	defer connection.Close()
	files:=[]*os.File{child}
	defer func(){for _,file:=range files{returned=errors.Join(returned,file.Close())}}()
	for _,root:=range []*os.Root{t.stage,t.mount.root}{
		file,err:=root.Open(".")
		if err!=nil{return err}
		files=append(files,file)
	}
	visible,err:=os.Open("/")
	if err!=nil{return err}
	files=append(files,visible)
	executable,err:=t.owner.helperExecutable()
	if err!=nil{return err}
	run:=newOwnedCommand(ctx,proto.CtlExecArgs{Argv:[]string{executable,VMFileCommitHelperMode},Cwd:"/"},inputCommandEnv())
	run.vmRole=vmFileCommitCommand
	run.fileCommit=&fileCommitCommand{owner:t.owner.init,files:files}
	defer run.cancel()
	line,err:=host.NewStartupLineV1(connection)
	if err!=nil{return err}
	defer line.Close()
	stop:=context.AfterFunc(ctx,func(){_ = line.SetDeadline(time.Now());_ = line.Close()})
	defer stop()
	if deadline,ok:=ctx.Deadline();ok{_ = line.SetDeadline(deadline)}
	done:=make(chan error,1)
	go func(){
		args,err:=json.Marshal(proto.VMFileCommitV1{Version:1,Mount:t.mount.prefix,Header:t.header})
		if err==nil{err=line.WriteFrame(proto.TRequest,proto.Request{ID:1,Op:proto.VMFileCommitOperationV1,Args:args})}
		if err==nil{
			var frame proto.Frame
			frame,err=line.ReadFrame()
			if err==nil{err=line.RejectAncillary()}
			var response proto.Response
			if err==nil && (frame.Type!=proto.TResponse || json.Unmarshal(frame.Payload,&response)!=nil || response.ID!=1 || !response.OK){err=putError("the fixed account publisher did not acknowledge its actual rename")}
		}
		done<-err
	}()
	code:=t.owner.init.executeOwned(run,ownedCommandIO{Out:io.Discard,Err:io.Discard})
	// The actual command joined. No unread child can retain either private
	// descriptor direction. Cancel only this one line to release a failed read.
	if code!=0 || ctx.Err()!=nil{_ = line.Close()}
	controlErr:=<-done
	if code!=0 || ctx.Err()!=nil || controlErr!=nil{return errors.Join(putError("the original fixed account file publisher did not complete"),ctx.Err(),controlErr)}
	return t.owner.connection.check(t.owner.init)
}
