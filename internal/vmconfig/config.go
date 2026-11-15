// Package vmconfig validates resolved VM setup data from the canonical policy owner.
package vmconfig

import (
	"encoding/hex"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Plan is resolved existing protected data, never a second config source.
type Plan struct {
	Args  proto.CtlVMPrepareArgs
	State files.StatePlan
}

// Validate checks the one resolved VM setup schema before effects.
func Validate(a proto.CtlVMPrepareArgs) (Plan, error) {
	if !api.ValidSessionID(a.Session) || a.VM.Version != 1 || a.VM.System != "reset" && a.VM.System != "keep" {
		return Plan{}, configError("the resolved VM configuration or session is invalid")
	}
	if len(a.StateFiles) > 0 && !CaptureNonce(a.CaptureNonce) || a.CaptureNonce != "" && !CaptureNonce(a.CaptureNonce) || a.VM.RestoreCaptureNonce != "" && !CaptureNonce(a.VM.RestoreCaptureNonce) || a.CaptureNonce != "" && a.CaptureNonce == a.VM.RestoreCaptureNonce {
		return Plan{}, configError("the VM state nonce is invalid or reused")
	}
	plan := files.StatePlan{Dirs: append([]string(nil), a.VM.StateDirs...), Files: append([]string(nil), a.StateFiles...)}
	if err := plan.Validate(); err != nil {
		return Plan{}, configError("the VM state plan is invalid: " + err.Error())
	}
	if correlation:=a.Correlation;correlation!=nil {
		if correlation.Version!=1 || correlation.Session!=a.Session || !CaptureNonce(correlation.Generation) || !api.ValidPolicyHash(correlation.NeedSHA256) || !CaptureNonce(correlation.Nonce) {return Plan{},configError("the original VM prepare correlation is invalid")}
		copy:=*correlation;a.Correlation=&copy
	}
	a.VM.StateDirs = append([]string(nil), a.VM.StateDirs...)
	a.StateFiles = append([]string(nil), a.StateFiles...)
	return Plan{Args: a, State: plan}, nil
}

// CaptureNonce accepts only an opaque fixed-size lowercase nonce, never a path.
func CaptureNonce(nonce string) bool {
	if len(nonce) != 32 || nonce != strings.ToLower(nonce) {
		return false
	}
	_, err := hex.DecodeString(nonce)
	return err == nil
}

func configError(message string) error {
	return &proto.Error{Code: "wall_init", Message: message, Hint: "Check the protected VM setup and image, then resume the sandbox."}
}
