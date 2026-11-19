//go:build linux

package guest

import (
	"context"
	"os"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestVMFileCommitMetadataCannotConstructOriginalMountOwner(t *testing.T){
	root,err:=os.OpenRoot(t.TempDir());if err!=nil{t.Fatal(err)};defer root.Close()
	files:=newVMFiles(t.TempDir(),vmMountOps{})
	if err:=files.captureCopyMountV1("/work",root,".",".");err!=nil{t.Fatal(err)}
	if len(files.copyMounts)!=0{t.Fatal("metadata-only layout constructed an original native copy owner")}
	vm:=&vmRuntime{files:files,applied:true}
	if transaction,err:=vm.beginFileCommitV1(context.Background(),proto.FilePutHeaderV1{Version:1,Path:"/work/target"});err==nil || transaction!=nil{t.Fatal("saved metadata entered private file commit")}
}

func TestFileCommitRootRoleCannotBeSelectedByOrdinaryCommand(t *testing.T){
	g:=&Init{}
	run:=newOwnedCommand(t.Context(),proto.CtlExecArgs{Argv:[]string{"/arbitrary",VMFileCommitHelperMode},Cwd:"/"},nil)
	defer run.cancel()
	run.vmRole=vmFileCommitCommand
	if attr,err:=g.fileCommitProcess(run);err==nil || attr!=nil{t.Fatal("a command/role label became a root file publisher")}
	run.fileCommit=&fileCommitCommand{owner:g}
	if attr,err:=g.fileCommitProcess(run);err==nil || attr!=nil{t.Fatal("an empty descriptor record became a native VM owner")}
}

func TestPrivateFileCommitHelperRejectsArgumentsBeforeCredentialsOrFiles(t *testing.T){
	if err:=RunVMFileCommitHelper([]string{"unexpected"});err==nil{t.Fatal("private publication role accepted caller arguments")}
}
