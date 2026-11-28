package host

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// handlePortGrant forwards "port.grant" and "port.revoke" requests from the
// client to a walled session's supervisor over its session socket (spec 7.4).
func (b *bridge) handlePortGrant(req proto.Request) {
	var a api.PortGrantArgs
	if err := json.Unmarshal(req.Args, &a); err != nil {
		b.reply(req.ID, nil, UserError("bad_request", "the "+req.Op+" request is not valid JSON", "Update the client."))
		return
	}
	if !validID(a.ID) {
		b.reply(req.ID, nil, UserError("not_found", "no session "+a.ID, "List sessions with ls."))
		return
	}
	if a.Port < 1 || a.Port > 65535 {
		b.reply(req.ID, nil, UserError("bad_request", fmt.Sprintf("port %d is out of range (1–65535)", a.Port), "Pass a TCP port number between 1 and 65535."))
		return
	}
	m, err := LoadManifest(a.ID)
	if err != nil {
		b.reply(req.ID, nil, UserError("not_found", "no session "+a.ID, "List sessions with ls."))
		return
	}
	if !isWalled(m) {
		b.reply(req.ID, nil, UserError("bad_request", "port grants apply only to walled sessions", "Forward an unwalled session's TCP port directly."))
		return
	}
	res, err := HolderRequest(a.ID, req.Op, map[string]int{"port": a.Port}, 5*time.Second)
	if err != nil {
		b.reply(req.ID, nil, err)
		return
	}
	b.reply(req.ID, res, nil)
}
