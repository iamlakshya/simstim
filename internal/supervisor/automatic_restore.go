package supervisor

import (
	"context"
	"encoding/json"
	"os"
	"slices"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/secretmem"
)

// RunAutomaticRestore is the closed versioned controller boot entrypoint. It
// owns ONE canonical boot lease after the launcher's explicit CAS handoff.
func RunAutomaticRestore(id string) error {
	if err := secretmem.Protect(); err != nil {
		return err
	}
	// The same real outside parent supplies its automatic operation loan on
	// the native startup line. This child cannot self-issue one from saved data.
	return runSupervisorMode(context.Background(), id, nil)
}

func (s *Supervisor) checkControllerPrepare(ctx context.Context) error {
	if s.automatic == nil || s.automatic.PrimaryWanted {
		return nil
	}
	var raw json.RawMessage
	if err := s.ctl.request(ctx, proto.CtlGuestCapabilities, struct{}{}, &raw); err != nil {
		return err
	}
	fields, err := api.JSONObjectFields(raw, "version", "features")
	if err != nil || len(fields) != 2 || len(fields["version"]) == 0 || len(fields["features"]) == 0 {
		return execRefusal("the actual guest has no controller preparation capability")
	}
	var result proto.CtlGuestFeatures
	if json.Unmarshal(raw, &result) != nil || result.Version != 1 || !slices.Contains(result.Features, proto.GuestSessionPrepareV1) {
		return execRefusal("the actual guest cannot restore children without restarting their ended primary")
	}
	return nil
}

func (s *Supervisor) prepareController(ctx context.Context, args proto.CtlStartArgs) error {
	args.Argv, args.Hooks, args.Secrets, args.Missing = nil, nil, nil, nil
	args.SecretModes = nil
	args.Agent, args.RunToken = "", ""
	args.DefaultShell, args.Shell = false, ""
	var raw json.RawMessage
	if err := s.ctl.request(ctx, proto.CtlPrepare, proto.CtlPrepareArgs{Version: 1, Start: args}, &raw); err != nil {
		return err
	}
	fields, err := api.JSONObjectFields(raw, "version", "guest_abi")
	if err != nil || len(fields) != 2 || len(fields["version"]) == 0 || len(fields["guest_abi"]) == 0 {
		return execRefusal("the guest controller preparation receipt is malformed")
	}
	var result proto.CtlPrepareResult
	if json.Unmarshal(raw, &result) != nil || result.Version != 1 || !s.guestABI(result.GuestABI) {
		return execRefusal("the guest controller preparation schema does not match this runtime")
	}
	if err := s.markPackageSettingsInstalled(); err != nil {
		return err
	}
	if err := s.restoreSavedMembers(ctx, s.memberValues); err != nil {
		return err
	}
	if !s.guestABI(s.terminalABI) {
		return execRefusal("no selected child proved its actual terminal ownership schema")
	}
	if err := s.certifyGuestBoot(ctx, s.terminalABI); err != nil {
		return err
	}
	return s.automaticReady()
}

func (s *Supervisor) automaticReady() error {
	if s.automatic == nil {
		return nil
	}
	s.mu.Lock()
	if !s.automatic.PrimaryWanted {
		s.m.Status = s.automatic.PrimaryStatus
		s.m.Run.HolderPID, s.m.Run.AgentPID = os.Getpid(), 0
		s.m.Run.Generation = s.generation
		s.m.Run.StartedAt = now().UTC()
		s.m.Armed = s.eg != nil && !s.eg.dead.Load() && s.eg.ctl != nil && !channelClosed(s.eg.ctl.done)
		s.rec.started = s.m.Run.StartedAt
		s.m.Run.StartEnvelope = false
		s.lastBeat = now()
		s.setPhase(host.LifeRunning)
	}
	s.m.AutomaticRestore = nil
	snapshot := s.snapshot()
	s.mu.Unlock()
	if err := s.save(&snapshot, false); err != nil {
		return err
	}
	if err := s.startMaxRun(); err != nil {
		return err
	}
	if err := s.commitOriginalReady(); err != nil {
		return err
	}
	s.raise(s.alertEvent(alerts.SandboxReady))
	s.replay()
	s.audit.write("controller_restored", nil)
	return nil
}
