package guest

import (
	"encoding/json"
	"github.com/iamlakshya/simstim/internal/api"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/vmconfig"
)

func (g *Init) decodeMember(data []byte) (proto.CtlStartArgs, error) {
	var args proto.CtlStartArgs
	var err error
	if g.vm != nil {
		args, err = vmconfig.DecodeMember(data)
	} else {
		err = json.Unmarshal(data, &args)
	}
	if err != nil && memberSecretModesPresent(data) {
		return args, guestSecretModeError()
	}
	if err == nil && args.PreparedClaim != "" && !api.ExactJSONFields(data, proto.CtlStartArgs{}) {
		return args, execError("unsupported prepared member fields")
	}
	if err == nil {
		err = validateMemberSecretModeWire(data, args)
	}
	if err == nil { err = validatePackageManagerWire(data, args) }
	return args, err
}
