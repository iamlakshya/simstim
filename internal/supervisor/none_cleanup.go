package supervisor

import (
	"errors"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
)

// recordNoneCleanupV1 runs after finish has joined the actual runtime, stream,
// member, event and supervisor workers, while the original holder is retained.
// It writes canonical DATA, never an outside source or a whole-unit proof.
func (s *Supervisor) recordNoneCleanupV1(kind string, joined error) error {
	if !s.unwalledV1() || kind != host.EndStop || joined != nil {
		return joined
	}
	s.mu.Lock()
	protected := host.CloneManifest(*s.m)
	s.mu.Unlock()
	if !host.NoneSupervisorShapeV1(&protected) {
		return execRefusal("the original unwalled cleanup acquired another execution role")
	}
	runtime, ok := s.sb.(noneRuntime)
	if !ok || !runtime.cleanupConfirmedV1() || s.noneLive == nil || s.bootOwner == nil || !s.bootOwner.Valid(s.id) || !s.bootOwner.StartupCommittedV1() || s.originalStart == nil {
		return execRefusal("the original unwalled creator has no complete trusted cleanup")
	}
	pid, birth, err := s.noneLive.CleanupObservationV1()
	if err != nil {
		return err
	}
	selfBirth, err := noneSelfBirthV1()
	if err != nil {
		return err
	}
	directory, holder, err := s.bootOwner.NoneCleanupIdentitiesV1(s.lifetimeContext(), s.noneLive)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runtimeCleanupErr != nil || s.runtimeCleaned == nil || !channelClosed(s.runtimeCleaned) || s.m.Run.Generation != s.generation || s.m.Run.BootID != host.BootID() || s.m.Run.HolderPID != os.Getpid() || s.rec.phase != host.LifeStopped || s.rec.end == nil || s.rec.end.Kind != host.EndStop {
		return errors.Join(execRefusal("the unwalled cleanup differs from its original boot and run"), s.runtimeCleanupErr)
	}
	s.m.NoneCleanup = &host.NoneCleanupV1{Version: 1, Scope: "n5", Boot: s.m.Run.BootID, Generation: s.generation, SupervisorPID: os.Getpid(), InitPID: pid, SupervisorBirth: selfBirth, InitBirth: birth, At: time.Now().UTC(), Directory: directory, Holder: holder}
	return nil
}
