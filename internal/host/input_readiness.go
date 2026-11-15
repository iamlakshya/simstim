package host

import (
 "context"
 "errors"
 "os"
 "time"
 "github.com/iamlakshya/simstim/internal/inputtransfer"
 "github.com/iamlakshya/simstim/internal/proto"
 "github.com/iamlakshya/simstim/internal/version"
)

func initialInputManifest(m *Manifest,begin inputtransfer.BeginArgs)error {
 if m==nil || m.ID!=begin.Session || m.SandboxOf!="" || m.Wall==nil || m.Inputs==nil || m.Inputs.Nonce!=begin.Nonce || m.Inputs.Digest!=begin.Digest {return UserError("input_protocol","the initial input scope differs from its protected start declaration","Keep the original input receipt and check that exact session before retrying.")}
 if m.Status.State!=StateStarting && (m.Life==nil || m.Life.Phase!=LifeBooting) {return UserError("input_protocol","the original input start is no longer pending","Use the original operation receipt to describe or finish an uncertain input transfer.")}
 return nil
}

// waitInitialInputHolder waits only BEFORE any Begin frame is sent. A hello
// challenge marks the native loan reader, which cannot accept input yet. No
// EOF or uncertain Begin response is ever retried after a physical request.
func waitInitialInputHolder(ctx context.Context,begin inputtransfer.BeginArgs)(*Manifest,*holderLine,proto.Hello,error){
 if ctx==nil{return nil,nil,proto.Hello{},UserError("input_protocol","the initial input has no owner lifetime","Start it through its original client request.")}
 waiting,cancel:=context.WithTimeout(ctx,secretsWait);defer cancel()
 for {
  if err:=waiting.Err();err!=nil{return nil,nil,proto.Hello{},UserError("input_protocol","the original input start did not become ready within its handshake budget","Keep the original receipt and check the session before retrying.")}
  m,err:=LoadManifest(begin.Session)
  if err==nil {
   if err=initialInputManifest(m,begin);err!=nil{return nil,nil,proto.Hello{},err}
   line,hello,dialErr:=dialSessionHolder(begin.Session,time.Second)
   if dialErr==nil {
    if hello.Check(version.Protocol)!=nil || hello.Role!="supervisor"{_ = line.c.Close();return nil,nil,hello,UserError("protocol","the initial input socket is not its compatible supervisor","Update the helper and keep the original operation receipt.")}
    if hello.StartupNonce=="" {return m,line,hello,nil}
    _ = line.c.Close() // No request was sent to this native preacceptance reader.
   } else if !noHolderSocket(dialErr) {
    // A bounded hello can race the transition between the loan and input
    // readers. Still no Begin was written, so waiting is unambiguous.
    if waiting.Err()!=nil {continue}
   }
  } else if !errors.Is(err,os.ErrNotExist){return nil,nil,proto.Hello{},err}
  timer:=time.NewTimer(20*time.Millisecond)
  select {case <-waiting.Done():timer.Stop();case <-timer.C:}
 }
}
