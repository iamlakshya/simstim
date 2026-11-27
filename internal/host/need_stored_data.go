package host

import (
	"encoding/json"
	"errors"
	"runtime"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/sandbox"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

// Stored Need decoding/start validation are DATA-only. They cannot issue an
// original boot receipt, startup grant or a raw Manifest execution context.
const vmNeedField = "vm_need_v1"

func validateStoredVMNeed(m *Manifest, raw []byte) error {
	need, err := vmneed.DecodeV1(raw, runtime.GOARCH)
	var future *vmneed.UnsupportedVersionError
	if errors.As(err, &future) {
		m.VMNeedV1 = append(json.RawMessage(nil), future.Raw...)
		return nil
	}
	if err != nil {
		return vmNeedError("the recorded VM Need is malformed")
	}
	if err = need.ValidateWall(runtime.GOARCH, wallLevel(m), wallDriver(m), m.Limits.MemoryMax); err != nil {
		return vmNeedError("the recorded VM Need conflicts with its wall or unit cap")
	}
	m.VMNeedV1 = append(json.RawMessage(nil), raw...)
	return nil
}

func wallLevel(m *Manifest) string {
	if m.Wall == nil {
		return ""
	}
	return m.Wall.Level
}

func wallDriver(m *Manifest) string {
	if m.Wall == nil {
		return ""
	}
	return m.Wall.Driver
}

func vmNeedError(message string) error {
	return &proto.Error{Code: "wall_unavailable", Message: message, Hint: "Use a supported helper and freshly authenticated client Need; check the admitted wall and size before resuming."}
}

func ValidateManifestStart(m *Manifest) error {
	if len(m.AutomaticRestore) > 0 {
		if _, err := DecodeAutomaticRestore(m.AutomaticRestore); err != nil {
			return err
		}
	}
	var docker, sudo bool
	if m.Policy != nil {
		docker = m.Policy.Sandbox.Docker != nil && *m.Policy.Sandbox.Docker
		sudo = m.Policy.Sandbox.Sudo != nil && *m.Policy.Sandbox.Sudo
	}
	if err := sandbox.ValidateVMOptions(wallLevel(m), docker, sudo); err != nil {
		return err
	}
	if len(m.VMNeedV1) == 0 {
		return nil
	}
	need, err := vmneed.DecodeV1(m.VMNeedV1, runtime.GOARCH)
	if err != nil {
		return vmNeedError("the recorded VM Need cannot start with this helper")
	}
	return need.ValidateWall(runtime.GOARCH, wallLevel(m), wallDriver(m), m.Limits.MemoryMax)
}
