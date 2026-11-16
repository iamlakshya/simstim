package guest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"

	"github.com/iamlakshya/simstim/internal/proto"
)

// applyBootDecoys performs no root filesystem write. One existing owned
// UID1000 helper installs the complete generation after fresh-home restore.
func (g *Init) applyBootDecoys(ctx context.Context,files *proto.DecoyFilesV1)error{
	if err:=ValidateDecoyFilesV1(files);err!=nil{return err}
	if ctx==nil {if files==nil{return nil};return execError("the private decoy writer has no guest lifetime")}
	g.mu.Lock()
	if g.decoysApplied {
		equal:=reflect.DeepEqual(g.decoyGeneration,files)
		g.mu.Unlock()
		if !equal{return execError("the prepared private decoy generation changed")}
		return nil
	}
	g.mu.Unlock()
	if files!=nil {
		executable,err:=g.helperExecutable();if err!=nil{return err}
		payload,err:=json.Marshal(files);if err!=nil{return err}
		run:=newOwnedCommand(ctx,proto.CtlExecArgs{Argv:[]string{executable,DecoyHelperMode},Cwd:"/"},inputCommandEnv())
		run.vmRole=vmSeedCommand
		defer run.cancel()
		code:=g.executeOwned(run,ownedCommandIO{Out:io.Discard,Err:io.Discard,AttachInput:func(input io.WriteCloser)func(){
			done:=make(chan struct{})
			go func(){defer close(done);defer input.Close();_,_=io.Copy(input,bytes.NewReader(payload))}()
			return func(){_ = input.Close();<-done}
		}})
		if code!=0 || ctx.Err()!=nil{return execError("the fixed-account decoy file helper did not complete")}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ended || ctx.Err()!=nil{return execError("the guest ended before the private decoy generation completed")}
	g.decoyGeneration=proto.CloneDecoyFilesV1(files)
	g.decoysApplied=true
	return nil
}

func (g *Init) checkBootDecoys(files *proto.DecoyFilesV1)error{
	g.mu.Lock();defer g.mu.Unlock()
	if !g.decoysApplied && files==nil && g.decoyGeneration==nil{return nil} // Legacy/none only; supported hosts require the actual feature.
	if !g.decoysApplied || !reflect.DeepEqual(g.decoyGeneration,files){return execError("the member changed its actual private decoy generation")}
	return nil
}
