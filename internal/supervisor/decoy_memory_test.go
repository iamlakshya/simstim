package supervisor

import (
 "testing"
 "github.com/iamlakshya/simstim/internal/api"
 "github.com/iamlakshya/simstim/internal/host"
)

func TestDecoyUseKeepsNativeMemoryWarningAndRunningState(t *testing.T){
 for _,source:=range []string{host.SrcMemory,"notify"}{
  s:=&Supervisor{rec:record{phase:host.LifeRunning},bootDecoyTripwires:[]api.DecoyTripwire{{Name:"known"}},m:&host.Manifest{ID:"abcdefgh",Status:host.Status{State:host.StateNeedsInput,Source:source,Reason:"memory warning",Claimed:true}}}
  s.noteDecoyUse(api.NetAuditEntry{Rule:"decoy",Secret:"known",Decision:"deny",Host:"example.invalid"})
  if !s.decoyUsed || !s.dirty || s.m.Status.State!=host.StateNeedsInput || !s.m.Status.Claimed{t.Fatal("trusted audit changed the running state/provenance")}
  expected:="decoy used";if source==host.SrcMemory{expected="memory warning"}
  if s.m.Status.Reason!=expected{t.Fatal("native memory priority changed",source,s.m.Status.Reason)}
 }
}
