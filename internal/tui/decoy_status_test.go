package tui

import (
 "reflect"
 "strings"
 "testing"
 "github.com/iamlakshya/simstim/internal/api"
 "github.com/iamlakshya/simstim/internal/client"
)

func TestDecoyNoticeTintsRunningRowWithoutChangingStateOrAttachability(t *testing.T){
 m:=&Model{}
 s:=client.Session{ID:"abcdefgh",State:api.StateWorking,Reason:"decoy used",Alive:true}
 before:=s;glyph,label:=m.look(s)
 if label.s!="decoy used" || !reflect.DeepEqual(label.st,sDanger) || !reflect.DeepEqual(glyph.st,sDanger){t.Fatal("running security reason is not red")}
 if !reflect.DeepEqual(s,before) || ended(s){t.Fatal("display changed the running identity/state")}
 h:=&hostConn{host:client.Host{Name:"host"}}
 detail,_:=m.sessionNow(h,s)
 if !strings.Contains(detail,"decoy used"){t.Fatal("security reason missing from selected detail")}
 s.State=api.StateExited;_,label=m.look(s)
 if label.s=="decoy used"{t.Fatal("old reason hid the actual ended state")}
}
