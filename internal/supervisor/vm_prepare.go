package supervisor

import (
	"context"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/vmconfig"
)

// prepareVMGuest resolves existing applied policy/state once before seed. It
// cannot choose execution pins or promote a guest's pending capture itself.
func (s *Supervisor) prepareVMGuest(ctx context.Context, headless bool) error {
	if s.m.Wall == nil || s.m.Wall.Driver != "chv" && s.m.Wall.Driver != "firecracker" {
		return nil
	}
	if _,err:=s.protectedVMDriver();err!=nil{return err}
	if s.vmCorrelation==nil{return execRefusal("the VM prepare has no original same-connection correlation")}
	plan, err := effectiveVMPlan(s.m, headless)
	if err != nil {
		return err
	}
	correlation:=*s.vmCorrelation
	plan.Args.Correlation=&correlation
	plan,err=vmconfig.Validate(plan.Args)
	if err!=nil{return err}
	var result proto.CtlVMPrepareResult
	if err = s.ctl.request(ctx, proto.CtlVMPrepare, plan.Args, &result); err != nil {
		return err
	}
	if result.GuestABI != proto.GuestOwnedPTYV2 {
		return execRefusal("the VM has not applied its actual init-owned PTY contract")
	}
	if err:=s.confirmVMPrepare(ctx,result.Correlation);err!=nil{return err}
	if !headless {
		s.vmPlan = &plan
	}
	return nil
}

func effectiveVMPlan(m *host.Manifest, headless bool) (vmconfig.Plan, error) {
	config := proto.VMStartV1{Version: 1, System: "reset", StateDirs: []string{}}
	if m.Policy != nil && m.Policy.Sandbox.System != nil {
		config.System = *m.Policy.Sandbox.System
	}
	args := proto.CtlVMPrepareArgs{Session: m.ID, VM: config, StateFiles: []string{}}
	if !headless {
		if m.Policy != nil {
			if m.Policy.Sandbox.Sudo != nil {
				args.VM.Sudo = *m.Policy.Sandbox.Sudo
			}
			if m.Policy.Sandbox.Docker != nil {
				args.VM.Docker = *m.Policy.Sandbox.Docker
			}
		}
		state, err := host.SandboxStatePlan(m)
		if err != nil {
			return vmconfig.Plan{}, err
		}
		args.VM.StateDirs = state.Dirs
		args.StateFiles = state.Files
		args.VM.RestoreCaptureNonce = m.VMCaptureNonce
		if len(state.Files) > 0 {
			nonce, err := newRunToken()
			if err != nil {
				return vmconfig.Plan{}, err
			}
			args.CaptureNonce = nonce
		}
	}
	return vmconfig.Validate(args)
}

func (s *Supervisor) vmCaptureReadyLocked(nonce string) bool {
	return s.vmPlan != nil && len(s.vmPlan.Args.StateFiles) > 0 && s.vmPlan.Args.CaptureNonce != "" && nonce == s.vmPlan.Args.CaptureNonce
}

// acceptVMCapture records only a pending generation proven by this owned VMM
// exit, all current member ends and required cleanup. VM ready alone is not it.
func (s *Supervisor) acceptVMCapture(kind string, natural bool, cleanupErr error) error {
	if !s.vmCaptureEligible(kind, natural, cleanupErr) {
		return nil
	}
	s.mu.Lock()
	nonce := s.captureReady
	s.mu.Unlock()
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if s.closed {
		return pullChanged()
	}
	s.mu.Lock()
	previous := s.m.VMCaptureNonce
	s.m.VMCaptureNonce = nonce
	snapshot := s.snapshot()
	s.mu.Unlock()
	if err := s.saveLocked(&snapshot, false); err != nil {
		s.mu.Lock()
		s.m.VMCaptureNonce = previous
		s.mu.Unlock()
		s.audit.write("state_capture_failed", nil)
		return err
	}
	s.audit.write("state_saved", map[string]any{"kind": kind})
	return nil
}

func (s *Supervisor) vmCaptureEligible(kind string, natural bool, cleanupErr error) bool {
	s.mu.Lock()
	ready := s.vmCaptureReadyLocked(s.captureReady)
	s.mu.Unlock()
	// Actual v2 holders forward End even on explicit stop, before their
	// owned PTYs drain and init captures. Legacy process rules stay separate.
	return ready && s.captureEndEligible(kind, natural, cleanupErr) && s.correlatedEmpty()
}
