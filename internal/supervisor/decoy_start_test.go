package supervisor

import (
 "bytes"
 "context"
 "crypto/rand"
 "encoding/json"
 "errors"
 "testing"
 "github.com/iamlakshya/simstim/internal/api"
 "github.com/iamlakshya/simstim/internal/egress"
 "github.com/iamlakshya/simstim/internal/host"
 "github.com/iamlakshya/simstim/internal/proto"
)

func TestBootDecoysResolveLocksBeforeEntropyFilesAndRegistration(t *testing.T){
 enabled,disabled:=true,false
 for _,pair:=range [][2]*bool{{&enabled,&disabled},{&disabled,&enabled}}{
  entropy:=&decoyEntropyProbe{err:errors.New("entropy must not be touched")}
  layers:=[]api.Layer{{Kind:api.LayerAll,Lock:&api.Lock{LayerValues:api.LayerValues{Sandbox:api.SandboxValues{Decoys:pair[0]}}}},{Kind:api.LayerSession,LayerValues:api.LayerValues{Sandbox:api.SandboxValues{Decoys:pair[1]}}}}
  files,tripwires,err:=bootDecoyPlan(context.Background(),layers,entropy)
  if err==nil || files!=nil || len(tripwires)!=0 || entropy.reads!=0{t.Fatal("locked refusal produced entropy, files or egress registration",err,entropy.reads)}
 }
}

func TestBootDecoysFalseDominatesValidLaterTrueWithoutEffects(t *testing.T){
 enabled,disabled:=true,false
 entropy:=&decoyEntropyProbe{err:errors.New("entropy must not be touched")}
 layers:=[]api.Layer{{Kind:api.LayerAll,LayerValues:api.LayerValues{Sandbox:api.SandboxValues{Decoys:&disabled}}},{Kind:api.LayerSession,LayerValues:api.LayerValues{Sandbox:api.SandboxValues{Decoys:&enabled}}}}
 files,tripwires,err:=bootDecoyPlan(context.Background(),layers,entropy)
 if err!=nil || files!=nil || len(tripwires)!=0 || entropy.reads!=0{t.Fatal("disabled resolved start produced effects",err,entropy.reads)}
}

func TestBootDecoyClonesOwnGenerationAndDoNotPublishValues(t *testing.T){
 files,tripwires,err:=bootDecoyPlan(t.Context(),nil,rand.Reader)
 if err!=nil{t.Fatal(err)}
 if files==nil || len(tripwires)==0{t.Fatal("default real wall lacks a generation")}
 s:=&Supervisor{bootDecoyFiles:files};var args proto.CtlStartArgs;s.bootDecoyArgs(&args)
 original:=args.Decoys.Files[0].Content;files.Files[0].Content="changed"
 if args.Decoys.Files[0].Content!=original{t.Fatal("caller changed the private member generation")}
 control:=&egressCtl{};control.setPublicInitial(egress.InitArgs{SessionID:"abcdefgh",Decoys:tripwires})
 public,err:=control.publicInitial(host.Manifest{});if err!=nil{t.Fatal(err)}
 if len(public.Decoys)!=0{t.Fatal("private generation entered public egress metadata")}
 encoded,err:=json.Marshal(public);if err!=nil{t.Fatal(err)}
 for _,tripwire:=range tripwires{for _,value:=range tripwire.Values{if bytes.Contains(encoded,[]byte(value)){t.Fatal("private generated value entered public metadata")}}}
}
