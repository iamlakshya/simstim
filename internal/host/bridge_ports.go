package host

import (
	"encoding/json"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (b *bridge) handlePorts(request proto.Request) bool {
	if request.Op != "port.grant" {
		return false
	}
	if err := CheckCaller(); err != nil {
		b.reply(request.ID, nil, err)
		return true
	}
	var args api.PortArgsV1
	if !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args.Version != 1 || !api.ValidSessionID(args.ID) || args.Port < 1 || args.Port > 65535 {
		b.reply(request.ID, nil, UserError("bad_port", "invalid exact sandbox port grant", "Name a running sandbox and a port from 1 to 65535."))
		return true
	}
	manifest, err := LoadManifest(args.ID)
	if err != nil || manifest.ID != args.ID || manifest.Wall == nil {
		b.reply(request.ID, nil, UserError("not_walled", "the selected session has no available sandbox", "Start or resume a sandbox before forwarding its ports."))
		return true
	}
	result, err := HolderRequestWithFeatures(args.ID, request.Op, args, 15*time.Second, api.FeatureSandboxPortsV1)
	b.reply(request.ID, json.RawMessage(result), err)
	return true
}
