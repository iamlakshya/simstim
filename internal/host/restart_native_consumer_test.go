package host

import (
 "strings"
 "testing"
 "github.com/iamlakshya/simstim/internal/api"
)

func TestVMResumeEnvironmentUsesGuestBaseWithoutHostExecutableLookup(t *testing.T){
 m:=&Manifest{ID:"abcdefgh",Wall:&WallRecord{Level:"microvm",Driver:"firecracker"},Agent:AgentInfo{Kind:"custom",Argv:[]string{"/an-unavailable-host-program"}},Env:[]string{"PATH=/old/host/path","OLD_ONLY=old"},Term:TermInfo{Term:"xterm-256color"}}
 complete:=&api.SessionEnvironment{Version:1,Values:map[string]string{"LITERAL":"value"}}
 if err:=prepareResumeEnvironment(m,complete);err!=nil{t.Fatal("guest base attempted to locate a host command",err)}
 values:=environmentMap(m.Env)
 if values["PATH"]!=guestPath("/opt/agents/bin") || values["LITERAL"]!="value"{t.Fatal("fresh guest runtime environment differs",m.Env)}
 if _,found:=values["OLD_ONLY"];found || strings.Contains(values["PATH"],"/old/host"){t.Fatal("saved host environment was reused")}
}

func TestRestartRefusesUnsupportedRecordedWallBeforeAnyOwnerStop(t *testing.T){
 for _,record:=range []*WallRecord{{Level:"microvm",Driver:"nitro"},{Level:"process",Driver:"firecracker"},{Level:"gvisor",Driver:"process"}}{
  if err:=validateRestartWall(&Manifest{ID:"abcdefgh",Wall:record});err==nil{t.Fatal("unsupported or conflicting wall admitted",record)}
 }
 if err:=applyAdmittedRestartMemoryV1(nil,&Manifest{});err==nil{t.Fatal("prospective limits replaced an actual memory admission")}
}
