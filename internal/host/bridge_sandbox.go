package host

import (
 "encoding/json"
 "strings"

 "github.com/iamlakshya/simstim/internal/api"
 "github.com/iamlakshya/simstim/internal/proto"
)

func init(){Features=append(Features,"sandbox")}

// handleSandbox serves the real read-only diagnostic probe. Native boot
// admission consumes its original producers independently of these facts.
func (b *bridge) handleSandbox(req proto.Request)bool {
 if req.Op!="sandbox.probe" {return false}
 if err:=CheckCaller();err!=nil {b.reply(req.ID,nil,err);return true}
 var args struct{ReleaseURL string `json:"release_url"`}
 raw:=req.Args
 if len(raw)==0 {raw=json.RawMessage(`{}`)}
 if !strings.HasPrefix(strings.TrimSpace(string(raw)),"{") || !api.ExactJSONFields(raw,args) || json.Unmarshal(raw,&args)!=nil {
  b.reply(req.ID,nil,UserError("bad_request","the sandbox probe arguments are not an exact object","Update the client and run the host doctor again."));return true
 }
 result,err:=CapabilityProbe(b.ctx,args.ReleaseURL)
 if err!=nil {err=UserError("sandbox_probe","could not probe sandbox capabilities: "+err.Error(),"Check the host's permissions and run the host doctor again.")}
 b.reply(req.ID,result,err);return true
}
