package supervisor

import (
 "context"
 "encoding/json"
 "testing"
 "github.com/iamlakshya/simstim/internal/inputtransfer"
 "github.com/iamlakshya/simstim/internal/host"
)

func TestInitialInputReceiptSupportsSameRunDescribeAndFinish(t *testing.T){
 s,input:=inputFinishFixture(t);s.startupInput,s.lateInput=input,nil
 old:=saveManifest;t.Cleanup(func(){saveManifest=old});writes:=0
 saveManifest=func(*host.Manifest)error{writes++;return nil}
 raw,err:=json.Marshal(inputtransfer.OperationArgs{Scope:input.scope,RunGeneration:input.runGeneration});if err!=nil{t.Fatal(err)}
 result,err:=s.describeInputOperation(raw);if err!=nil || !result.Summary.Equal(input.summary){t.Fatal("initial receipt could not describe its same operation",err)}
 if err:=s.recoverInputFinish(context.Background(),finishBegin(input));err!=nil{t.Fatal(err)}
 if err:=s.recoverInputFinish(context.Background(),finishBegin(input));err!=nil || writes!=1{t.Fatal("initial lost ACK repeated completion",err,writes)}
}

func TestInitialAndLateInputCannotDonateOrAmbiguouslySelectReceipt(t *testing.T){
 s,input:=inputFinishFixture(t);s.startupInput=input
 duplicate:=*input;s.lateInput=&duplicate
 if got:=s.retainedInputLocked(input.scope,input.runGeneration);got!=nil{t.Fatal("ambiguous retained owners selected")}
 s.lateInput=nil
 foreign:=input.scope;foreign.Digest="other"
 if got:=s.retainedInputLocked(foreign,input.runGeneration);got!=nil{t.Fatal("foreign scope selected an initial receipt")}
 if got:=s.retainedInputLocked(input.scope,"other-run");got!=nil{t.Fatal("foreign generation selected an initial receipt")}
}
